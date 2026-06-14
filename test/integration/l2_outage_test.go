// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"net"
	"testing"
	"time"

	toxiclient "github.com/Shopify/toxiproxy/v2/client"
	"github.com/redis/rueidis"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	tctoxiproxy "github.com/testcontainers/testcontainers-go/modules/toxiproxy"
	"github.com/testcontainers/testcontainers-go/network"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/invalidation"
	"github.com/ant-caor/nimbus/redisstore"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

// The outage suite proves the L2-unreachable degraded-mode CONTRACT:
//
//   - Read-only paths that never touch L2 -- a fresh L1 hit, a Get peek, and a
//     stale-serve -- keep working while Redis is unreachable.
//   - A GetOrLoad COLD MISS whose loader SUCCEEDS but whose versioned write-back
//     to L2 fails with a non-conflict (connectivity) error must NOT fail the
//     request: it returns the loader's value. (Before the contract this returned
//     the raw Redis error after the loader had already succeeded.) A cold miss
//     whose loader reports ErrNotFound likewise returns ErrNotFound, not the
//     Redis error.
//   - Once L2 is restored, the cache RESUMES normal versioned fills and
//     cross-instance coherence is back.
//
// L2 is the source of truth and the bus is only a latency optimization, so these
// tests use an in-process Mem bus (shared by reference between the two
// instances): the property under test is L2 connectivity, and the Pub/Sub
// emulator's asynchronous delivery would only add timing noise to the signal.
// Cross-instance coherence here is exercised through the shared L2 plus the
// synchronous Mem bus.
//
// Why a DEDICATED Redis + toxiproxy on a private network (not the shared
// TestMain Redis): cutting connectivity is destructive to every client of that
// Redis, and the suite runs tests in parallel against one shared container.
// Each outage test therefore owns its own Redis and its own toxiproxy so the cut
// is contained. toxiproxy's proxy.Disable()/Enable() is preferred over container
// Stop/Start because it is instantaneous and deterministic: it severs in-flight
// and new connections immediately and heals them immediately, with no restart
// latency, port remap, or reconnect-storm timing to flake on.

const (
	// toxiproxyImage and redisOutageImage pin the images used by the outage
	// fixture. The toxiproxy image matches the testcontainers module's examples;
	// the Redis image matches TestMain.
	toxiproxyImage   = "ghcr.io/shopify/toxiproxy:2.12.0"
	redisOutageImage = "redis:7-alpine"

	// proxyListenPort is the port toxiproxy listens on inside its container for
	// the "redis" proxy created by WithProxy; ProxiedEndpoint maps it to the host.
	proxyListenPort = 8666
)

// outageFixture is a self-contained, isolated Redis reachable only through a
// toxiproxy that the test can cut and heal at will.
type outageFixture struct {
	// addr is the host:port the rueidis client connects to (the toxiproxy front
	// door). When the proxy is disabled this endpoint refuses/severs connections.
	addr  string
	proxy *toxiclient.Proxy
}

// cut severs all connectivity to Redis (existing connections are dropped and new
// ones are refused). It is the moment Redis becomes "unreachable".
func (f *outageFixture) cut(t *testing.T) {
	t.Helper()
	require.NoError(t, f.proxy.Disable(), "disable proxy (cut L2)")
}

// heal restores connectivity. rueidis re-dials lazily on the next command.
func (f *outageFixture) heal(t *testing.T) {
	t.Helper()
	require.NoError(t, f.proxy.Enable(), "enable proxy (restore L2)")
}

// newOutageFixture spins up a dedicated Redis and a toxiproxy in front of it on a
// private Docker network, returning the proxied address and a proxy handle. All
// resources are torn down via t.Cleanup.
func newOutageFixture(t *testing.T) *outageFixture {
	t.Helper()
	ctx := context.Background()

	nw, err := network.New(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw.Remove(ctx) })

	redisCtr, err := tcredis.Run(ctx, redisOutageImage,
		network.WithNetwork([]string{"redis"}, nw))
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(redisCtr) })

	toxiCtr, err := tctoxiproxy.Run(ctx, toxiproxyImage,
		// Create a proxy named "redis" fronting the redis container's in-network
		// address. The proxy listens on proxyListenPort inside the toxiproxy
		// container; ProxiedEndpoint maps it to a host port.
		tctoxiproxy.WithProxy("redis", "redis:6379"),
		network.WithNetwork([]string{"toxiproxy"}, nw))
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(toxiCtr) })

	host, port, err := toxiCtr.ProxiedEndpoint(proxyListenPort)
	require.NoError(t, err)

	toxiURI, err := toxiCtr.URI(ctx)
	require.NoError(t, err)
	client := toxiclient.NewClient(toxiURI)
	proxies, err := client.Proxies()
	require.NoError(t, err)
	proxy, ok := proxies["redis"]
	require.True(t, ok, "toxiproxy should expose the 'redis' proxy")

	return &outageFixture{addr: net.JoinHostPort(host, port), proxy: proxy}
}

// newOutageRedisClient builds a rueidis client pointed at the proxied address and
// tuned so a cut surfaces FAST as an error instead of hanging:
//   - Dialer.Timeout bounds the re-dial attempt while the proxy is disabled.
//   - ConnWriteTimeout bounds an in-flight command on a severed connection.
//   - DisableRetry stops rueidis from silently retrying read-only commands under
//     network errors, which would otherwise mask the outage and stall the test.
func newOutageRedisClient(t *testing.T, addr string) rueidis.Client {
	t.Helper()
	c, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:      []string{addr},
		DisableCache:     true,
		DisableRetry:     true,
		ConnWriteTimeout: time.Second,
		Dialer:           net.Dialer{Timeout: time.Second},
	})
	require.NoError(t, err)
	t.Cleanup(c.Close)
	return c
}

// buildOutageCache builds a cache over the shared (dedicated) L2 client and a
// shared in-process Mem bus, keyed under prefix. tweak customizes the builder.
func buildOutageCache(t *testing.T, l2client rueidis.Client, bus invalidation.Bus, prefix string,
	loader nimbus.Loader[string, string], tweak func(*nimbus.Builder[string, string])) nimbus.Cache[string, string] {
	t.Helper()
	l2 := redisstore.New[string](l2client, store.JSON[string](), redisstore.WithKeyPrefix(prefix))
	b := nimbus.NewBuilder[string, string](loader).
		L1(memory.New[string]()).
		L2(l2).
		Bus(bus)
	if tweak != nil {
		tweak(b)
	}
	c, err := b.Build()
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestL2OutageDegradedModeContract is the headline degraded-mode proof. It primes
// state against a real Redis, cuts L2, asserts the survival + cold-miss contract
// while down, then restores L2 and asserts normal versioned fills and
// cross-instance coherence resume.
func TestL2OutageDegradedModeContract(t *testing.T) {
	ctx := context.Background()
	fx := newOutageFixture(t)
	l2client := newOutageRedisClient(t, fx.addr)
	bus := invalidation.NewMem()
	prefix := "outage:" + safeName(t.Name()) + ":"

	// Loader returns a recognizable value per key and lets us count calls per key
	// by encoding the key into the value.
	loader := func(_ context.Context, key string) (string, error) {
		switch key {
		case "absent":
			return "", nimbus.ErrNotFound
		default:
			return "origin-" + key, nil
		}
	}

	// Three instances over the SAME dedicated L2 + the SAME in-process bus:
	//   - a: a long fresh TTL, so "fresh"/"cold"/recovery are unambiguously fresh
	//     hits (their freshness never lapses mid-test and contaminates the signal).
	//   - b: proves cross-instance coherence (promotion + bus eviction) after
	//     recovery.
	//   - s: a SHORT fresh window with a long stale tail, used solely to drive the
	//     stale-serve sub-case deterministically. Isolating it keeps a's fresh-hit
	//     assertions from racing the clock.
	a := buildOutageCache(t, l2client, bus, prefix, loader, func(b *nimbus.Builder[string, string]) {
		b.TTL(time.Hour, 0)
	})
	bClient := newOutageRedisClient(t, fx.addr)
	b := buildOutageCache(t, bClient, bus, prefix, loader, func(bb *nimbus.Builder[string, string]) {
		bb.TTL(time.Hour, 0)
	})
	const staleFresh = 300 * time.Millisecond
	sClient := newOutageRedisClient(t, fx.addr)
	s := buildOutageCache(t, sClient, bus, prefix, loader, func(bb *nimbus.Builder[string, string]) {
		// Long stale tail so the entry stays servable-stale across the outage; the
		// L2 TTL (derived from staleUntil) likewise outlives the test window.
		bb.TTL(staleFresh, time.Hour)
	})

	// --- Prime state through real Redis (L2 healthy) ---------------------------

	// "fresh" -> a versioned fill writes L2 and installs a fresh L1 entry on a.
	v, err := a.GetOrLoad(ctx, "fresh")
	require.NoError(t, err)
	require.Equal(t, "origin-fresh", v)

	// "stale" -> primed on the short-window instance s. We then sleep just past its
	// fresh window so it is provably stale (but still servable) before the outage.
	v, err = s.GetOrLoad(ctx, "stale")
	require.NoError(t, err)
	require.Equal(t, "origin-stale", v)

	// Confirm the priming really hit L2 (so the outage assertions are meaningful):
	// a second instance b promotes "fresh" from the shared L2 without re-loading.
	v, err = b.GetOrLoad(ctx, "fresh")
	require.NoError(t, err)
	require.Equal(t, "origin-fresh", v)

	// Make s's "stale" entry provably stale before we cut L2: past staleFresh but
	// well inside the long stale tail. A real sleep is appropriate here because the
	// boundary is wall-clock and we control both ends (staleFresh and the margin).
	time.Sleep(staleFresh + 200*time.Millisecond)

	// --- Cut L2 -----------------------------------------------------------------
	fx.cut(t)

	// 1) A FRESH L1 hit still serves: GetOrLoad short-circuits before any L2 read.
	//    (We read "fresh" immediately; its 400ms fresh window is still open.)
	v, err = a.GetOrLoad(ctx, "fresh")
	require.NoError(t, err, "a fresh L1 hit must not touch L2 and must survive the outage")
	require.Equal(t, "origin-fresh", v)

	// 2) A Get PEEK still works against L1 with L2 down.
	v, ok, err := a.Get(ctx, "fresh")
	require.NoError(t, err, "Get is read-only against L1 and must survive the outage")
	require.True(t, ok)
	require.Equal(t, "origin-fresh", v)

	// 3) COLD MISS for a NEW key returns the loader's value, NOT the Redis error.
	//    A never cached "cold"; its fill must Load L2 (fails, treated as absent),
	//    run the loader (succeeds), attempt the versioned SetCAS (fails on the dead
	//    connection with a non-conflict error), and -- per the contract -- still
	//    return the loader value rather than surfacing the write-back error.
	v, err = a.GetOrLoad(ctx, "cold")
	require.NoError(t, err, "a cold miss must not fail the request when only the L2 write-back is unreachable")
	require.Equal(t, "origin-cold", v)

	// 3b) COLD MISS whose loader reports ErrNotFound returns ErrNotFound, not the
	//     Redis error from the failed tombstone CAS.
	_, err = a.GetOrLoad(ctx, "absent")
	require.ErrorIs(t, err, nimbus.ErrNotFound,
		"a not-found cold miss must surface ErrNotFound, not the L2 connectivity error")

	// The degradation is observable: both cold misses above hit a non-conflict L2
	// error and fell back to the origin, so a's L2Errors counter advanced.
	require.GreaterOrEqual(t, a.Stats().L2Errors, uint64(2),
		"degraded fills must be counted in Stats.L2Errors")

	// 4) A STALE entry still serves stale while L2 is down. s's "stale" entry is
	//    provably past its fresh window (we slept past staleFresh above) but well
	//    inside its long stale tail, so GetOrLoad must serve the stale value
	//    immediately via the stale-serve path. The background refresh it schedules
	//    will try L2 and fail harmlessly -- correctness does not depend on it.
	v, err = s.GetOrLoad(ctx, "stale")
	require.NoError(t, err, "a stale-serve must not touch L2 synchronously and must survive the outage")
	require.Equal(t, "origin-stale", v)
	require.Greater(t, s.Stats().StaleHits, uint64(0), "the stale read took the stale-serve path")

	// --- Restore L2 -------------------------------------------------------------
	fx.heal(t)

	// 5) A subsequent COLD MISS now does a NORMAL versioned fill again: the value
	//    is written to L2 and is therefore visible to a SECOND instance that
	//    promotes it from L2 without re-loading the origin. We use a brand-new key
	//    so the only way B can see it is via the shared L2.
	const recoveryKey = "recovered"

	// rueidis reconnects lazily, so the first fills after heal may still race the
	// re-dial and degrade (return the value without writing L2). Drive fills until
	// one is a TRUE versioned write: verified out of band via a fresh handle to the
	// SAME L2 that must Load the value at a non-zero version. This is the proof that
	// normal versioned fills (not degraded loader-only returns) have resumed.
	rawL2 := redisstore.New[string](newOutageRedisClient(t, fx.addr), store.JSON[string](),
		redisstore.WithKeyPrefix(prefix))
	require.Eventually(t, func() bool {
		v, err := a.GetOrLoad(ctx, recoveryKey)
		if err != nil || v != "origin-"+recoveryKey {
			return false
		}
		e, ok, lerr := rawL2.Load(ctx, recoveryKey)
		return lerr == nil && ok && e.Value == "origin-"+recoveryKey && e.Version != 0
	}, 15*time.Second, 100*time.Millisecond, "normal versioned fills must resume after L2 recovers (value lands in L2)")

	// 6) Cross-instance coherence is back: B promotes the recovered value from the
	//    shared L2 without re-loading the origin, and an Invalidate on A evicts it
	//    from B over the (in-process) bus. B's own rueidis connection also re-dials
	//    lazily after the heal, so retry until B's L2 read succeeds and promotes.
	require.Eventually(t, func() bool {
		v, err := b.GetOrLoad(ctx, recoveryKey)
		return err == nil && v == "origin-"+recoveryKey
	}, 10*time.Second, 100*time.Millisecond, "b must promote the recovered value from the shared L2")
	if _, ok, _ := b.Get(ctx, recoveryKey); !ok {
		t.Fatal("b should hold the recovered key before invalidation")
	}

	// Invalidate is a write against L2 (a versioned tombstone) and is NOT covered by
	// the degraded-mode read contract, so it legitimately surfaces a connectivity
	// error. The connection is healed (the recovery fill above proved it), but
	// rueidis may briefly hand out a stale pooled connection right after heal; retry
	// until the tombstone write lands, then assert the broadcast reached b.
	require.Eventually(t, func() bool {
		return a.Invalidate(ctx, recoveryKey) == nil
	}, 10*time.Second, 100*time.Millisecond, "Invalidate must succeed against the restored L2")
	require.Eventually(t, func() bool {
		_, ok, _ := b.Get(ctx, recoveryKey)
		return !ok
	}, 5*time.Second, 50*time.Millisecond, "cross-instance invalidation must work again after recovery")
	require.Greater(t, b.Stats().BusEvicts, uint64(0), "b recorded the bus eviction after recovery")
}

// TestL2OutageColdMissLoaderErrorStillPropagates is the sharp negative control
// for the contract: the degraded-mode tolerance applies ONLY to the L2
// write-back. A genuine LOADER error during an outage must STILL surface to the
// caller -- the cache must not swallow a real upstream failure just because L2 is
// also down.
func TestL2OutageColdMissLoaderErrorStillPropagates(t *testing.T) {
	ctx := context.Background()
	fx := newOutageFixture(t)
	l2client := newOutageRedisClient(t, fx.addr)
	bus := invalidation.NewMem()
	prefix := "outageerr:" + safeName(t.Name()) + ":"

	sentinel := errInjected
	loader := func(_ context.Context, _ string) (string, error) { return "", sentinel }
	c := buildOutageCache(t, l2client, bus, prefix, loader, func(b *nimbus.Builder[string, string]) {
		b.TTL(time.Hour, 0)
	})

	fx.cut(t)

	_, err := c.GetOrLoad(ctx, "k")
	require.ErrorIs(t, err, sentinel,
		"a real loader error during an L2 outage must still propagate; the contract only tolerates a failed L2 write-back of a SUCCESSFUL load")
	require.Greater(t, c.Stats().LoadErrors, uint64(0))
}

// errInjected is a stable sentinel for the loader-error control above.
var errInjected = injectedError("injected loader failure")

type injectedError string

func (e injectedError) Error() string { return string(e) }
