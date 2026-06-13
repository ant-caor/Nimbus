// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pubsub "cloud.google.com/go/pubsub/v2"
	"github.com/stretchr/testify/require"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/invalidation/gcppubsub"
	"github.com/ant-caor/nimbus/redisstore"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

// busSettleDelay is how long we wait after building an instance for its
// per-instance Pub/Sub subscription and Receive loop to come up. Pub/Sub only
// delivers to live subscribers, so a publish before this window would be lost.
// The emulator creates subscriptions quickly; one second is generous.
const busSettleDelay = time.Second

// uniqueSuffix returns a fresh random token used to make each test run's Redis
// prefixes and Pub/Sub topics distinct. These tests assert on exact starting
// versions and on absence, so -- unlike the idempotent t.Name()-keyed tests in
// the rest of the suite -- they must not reuse keys across repeated runs
// (e.g. -count=N), which share one long-lived Redis.
func uniqueSuffix() string {
	var b [8]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// newPubSubClient returns a Pub/Sub client wired to the emulator (via the
// PUBSUB_EMULATOR_HOST env var set in TestMain), auto-closed at test end.
func newPubSubClient(t *testing.T) *pubsub.Client {
	t.Helper()
	c, err := pubsub.NewClient(context.Background(), pubsubProjectID)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestFillAfterInvalidateRaceTwoInstances is the real-Redis, two-instance
// version of the fill-after-invalidate race (the unit-level proof is
// redisstore_test.go's TestFillInvariantUnderInvalidate). It guards the FILL
// INVARIANT: no value enters L1 except stamped with an L2-minted version decided
// atomically against concurrent invalidations.
//
// Timeline, forced deterministically with channels (no sleeps):
//
//  1. Instance B misses k and enters its loader, which has already read L2's
//     version as `expect`, then BLOCKS mid-fill.
//  2. Instance A writes a new value for k, bumping L2's version past B's
//     `expect`. B has nothing cached yet, so a broadcast would evict nothing on
//     B -- the dangerous case the fill invariant exists to close.
//  3. B's loader unblocks and returns the now-stale value. Its SetCAS(expect)
//     must CONFLICT against the bumped version, so B discards the stale value and
//     serves the winner from L2 instead.
//
// The assertion is that B never caches nor serves the stale value: it converges
// on A's winner. Correctness here comes from the versioned CAS, not the bus.
func TestFillAfterInvalidateRaceTwoInstances(t *testing.T) {
	ctx := context.Background()
	prefix := "race:" + safeName(t.Name()) + ":" + uniqueSuffix() + ":"

	// Instance A's loader is never expected to run (A writes via Set), but if it
	// did we would catch it.
	var aLoads atomic.Int64
	aLoader := func(_ context.Context, _ string) (string, error) {
		aLoads.Add(1)
		return "should-not-load", nil
	}
	a := buildL2Cache(t, prefix, aLoader, nil)

	// Instance B's loader reads the pre-write value, signals it has entered, then
	// blocks on a gate so we can interleave A's write before it returns.
	loaderEntered := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	var bLoads atomic.Int64
	bLoader := func(_ context.Context, _ string) (string, error) {
		bLoads.Add(1)
		once.Do(func() { close(loaderEntered) })
		<-gate
		return "stale-from-B", nil // the value B read before A's write
	}
	b := buildL2Cache(t, prefix, bLoader, nil)

	// Seed k so both instances observe a defined starting version (L2 holds
	// version 1 of "v-old").
	require.NoError(t, a.Set(ctx, "k", "v-old"))

	// Invalidate so L2 is a tombstone: B's next read finds no fresh value and runs
	// its loader against expect = the tombstone's version.
	require.NoError(t, a.Invalidate(ctx, "k"))

	type result struct {
		v   string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		v, err := b.GetOrLoad(ctx, "k")
		resCh <- result{v, err}
	}()

	<-loaderEntered // B has read L2's version as `expect` and is blocked in its loader.

	// A writes the winning value, bumping L2's version past B's `expect`. B has
	// nothing cached, so this cannot help B via the bus -- only the CAS will.
	require.NoError(t, a.Set(ctx, "k", "v-new"))

	close(gate) // B's loader returns the stale value; its SetCAS(expect) must conflict.

	r := <-resCh
	require.NoError(t, r.err, "B must converge, not error")
	require.Equal(t, "v-new", r.v, "B must serve A's winner from L2, never its own stale value")

	// And B must not be holding the stale value afterward.
	v, ok, err := b.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "v-new", v, "B's L1 must hold the winner, not the discarded stale value")
	require.Equal(t, int64(1), bLoads.Load(), "B's loader ran exactly once")
	require.Equal(t, int64(0), aLoads.Load(), "A never loaded; it only wrote")
}

// TestNegativeCacheConvergesViaBusNotL2 guards the NEGATIVE-CONVERGENCE
// invariant: a negative (known-absent) entry is L1-only, and a fresh negative
// hit short-circuits before any L2 read, so it converges ONLY via a bus eviction
// or its NegativeTTL -- NOT via the next L2 read.
//
// Scenario:
//  1. Instance A caches a negative for k (its loader reports ErrNotFound).
//  2. A fresh negative hit on A must short-circuit before any L2 read.
//  3. Instance B creates k via Set, which writes L2 and broadcasts an eviction.
//  4. After the broadcast (pull = fan-out) A evicts the negative and a fresh
//     GetOrLoad promotes the real value from L2.
//
// This uses real Pub/Sub (pull fan-out) so every instance is evicted.
func TestNegativeCacheConvergesViaBusNotL2(t *testing.T) {
	ctx := context.Background()
	suffix := uniqueSuffix()
	prefix := "neg:" + safeName(t.Name()) + ":" + suffix + ":"
	topicID := "neg-" + safeName(t.Name()) + "-" + suffix

	// A's loader always reports absent, so A caches a negative on miss.
	var aLoads atomic.Int64
	aLoader := func(_ context.Context, _ string) (string, error) {
		aLoads.Add(1)
		return "", nimbus.ErrNotFound
	}
	a := buildL2BusCache(t, prefix, topicID, aLoader, func(b *nimbus.Builder[string, string]) {
		// A modest NegativeTTL is the backstop; we assert convergence via the bus
		// well within this window, so the TTL must not be the thing that converges.
		b.NegativeTTL(time.Hour)
	})

	// B's loader is irrelevant; B writes via Set.
	bLoader := func(_ context.Context, _ string) (string, error) { return "unused", nil }
	b := buildL2BusCache(t, prefix, topicID, bLoader, nil)

	// Let both subscriptions establish before any publish.
	time.Sleep(busSettleDelay)

	// A caches a negative for k.
	_, err := a.GetOrLoad(ctx, "k")
	require.ErrorIs(t, err, nimbus.ErrNotFound)
	require.Equal(t, int64(1), aLoads.Load())

	// A fresh negative hit must short-circuit before any L2 read.
	_, err = a.GetOrLoad(ctx, "k")
	require.ErrorIs(t, err, nimbus.ErrNotFound)
	require.Equal(t, int64(1), aLoads.Load(), "fresh negative hit must not re-run the loader")
	require.Greater(t, a.Stats().NegativeHits, uint64(0), "the second read was a negative hit")

	// B creates k. This writes L2 (so L2 now holds a real value) AND broadcasts an
	// eviction for k.
	require.NoError(t, b.Set(ctx, "k", "real"))

	// A converges only once the eviction is delivered; pull is fan-out, so A is
	// evicted and the next GetOrLoad promotes "real" from L2.
	require.Eventually(t, func() bool {
		v, err := a.GetOrLoad(ctx, "k")
		return err == nil && v == "real"
	}, 15*time.Second, 100*time.Millisecond, "A's negative must converge to the real value via the bus")

	require.Greater(t, a.Stats().BusEvicts, uint64(0), "A recorded the bus eviction that cleared its negative")
}

// TestNegativeCacheNotConvergedByL2Read is the sharper half of the negative
// invariant: it proves that an L2 write WITHOUT a delivered bus event does not
// converge a fresh negative. A has NO bus at all, and we write the key directly
// to the shared L2 out of band; A must still report not-found within the window.
//
// This isolates the "negative does not consult L2" property from any timing race
// with the bus: there is no bus path between the writer and A.
func TestNegativeCacheNotConvergedByL2Read(t *testing.T) {
	ctx := context.Background()
	prefix := "negl2:" + safeName(t.Name()) + ":" + uniqueSuffix() + ":"

	var aLoads atomic.Int64
	aLoader := func(_ context.Context, _ string) (string, error) {
		aLoads.Add(1)
		return "", nimbus.ErrNotFound
	}
	// No bus on A: the only ways a negative could converge are a (forbidden) L2
	// read or its NegativeTTL. We keep NegativeTTL long so it cannot be the cause
	// within the test window.
	a := buildL2Cache(t, prefix, aLoader, func(b *nimbus.Builder[string, string]) {
		b.NegativeTTL(time.Hour)
	})

	// A direct handle to the same L2 to write k out of band, exactly as a peer
	// instance would, but with no eviction reaching A.
	rawL2 := redisstore.New[string](newRedisClient(t), store.JSON[string](),
		redisstore.WithKeyPrefix(prefix))

	// A caches a negative for k.
	_, err := a.GetOrLoad(ctx, "k")
	require.ErrorIs(t, err, nimbus.ErrNotFound)

	// Create k in L2 out of band.
	until := time.Now().Add(time.Hour)
	_, err = rawL2.SetCAS(ctx, "k", "real", store.ForceVersion, until, until, nil)
	require.NoError(t, err)

	// A must STILL report not-found: a fresh negative short-circuits before L2,
	// so the new L2 value is invisible until a bus eviction or the NegativeTTL.
	// If it were (wrongly) consulting L2, it would flip to "real" here.
	require.Never(t, func() bool {
		v, err := a.GetOrLoad(ctx, "k")
		return err == nil && v == "real"
	}, 2*time.Second, 100*time.Millisecond,
		"a fresh negative must NOT converge via an L2 read; only the bus or NegativeTTL converge it")

	// Get (the read-only peek) also reports absent for the negative.
	_, ok, err := a.Get(ctx, "k")
	require.NoError(t, err)
	require.False(t, ok)
}

// TestTagInvalidationFanOutCrossInstance guards the TAG FAN-OUT invariant:
// InvalidateTag resolves keys authoritatively from L2's tag index and broadcasts
// the RESOLVED KEY LIST; a receiving instance evicts exactly those keys without
// keeping any local tag index of its own.
//
// Scenario: A and B both hold a, b, c in L1 (a and b carry tag "grp", c does
// not). A calls InvalidateTag("grp"). Over the real bus, B must evict a and b
// but keep c, even though B never indexed any tags locally.
func TestTagInvalidationFanOutCrossInstance(t *testing.T) {
	ctx := context.Background()
	suffix := uniqueSuffix()
	prefix := "tagfan:" + safeName(t.Name()) + ":" + suffix + ":"
	tagPrefix := "tagfanidx:" + safeName(t.Name()) + ":" + suffix + ":"
	topicID := "tagfan-" + safeName(t.Name()) + "-" + suffix

	loader := func(_ context.Context, key string) (string, error) { return "origin-" + key, nil }

	mkTagged := func() nimbus.Cache[string, string] {
		l2 := redisstore.New[string](newRedisClient(t), store.JSON[string](),
			redisstore.WithKeyPrefix(prefix), redisstore.WithTagPrefix(tagPrefix))
		client := newPubSubClient(t)
		bus, err := gcppubsub.New(ctx, client, topicID, gcppubsub.WithSubscriptionTTL(0))
		require.NoError(t, err)
		c, err := nimbus.NewBuilder[string, string](loader).
			L1(memory.New[string]()).
			L2(l2).
			Bus(bus).
			TTL(time.Hour, 0).
			Build()
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	a := mkTagged()
	b := mkTagged()

	time.Sleep(busSettleDelay)

	// A writes a, b carrying tag grp. Each Set broadcasts a per-key eviction, so
	// we seed the tagged keys first and let those broadcasts drain before B caches
	// anything (otherwise a late Set broadcast could evict B's freshly-promoted
	// copy and muddy the tag-fan-out signal we are actually testing).
	require.NoError(t, a.Set(ctx, "a", "va", nimbus.WithTags("grp")))
	require.NoError(t, a.Set(ctx, "b", "vb", nimbus.WithTags("grp")))

	// The untagged control key c is written directly to the shared L2 with NO bus
	// broadcast, so the only invalidation events in flight are the tag's. This
	// isolates the property under test: a receiver evicts exactly the tag's
	// resolved keys and nothing else.
	rawL2 := redisstore.New[string](newRedisClient(t), store.JSON[string](),
		redisstore.WithKeyPrefix(prefix), redisstore.WithTagPrefix(tagPrefix))
	until := time.Now().Add(time.Hour)
	_, err := rawL2.SetCAS(ctx, "c", "vc", store.ForceVersion, until, until, nil)
	require.NoError(t, err)

	// Let A's two Set broadcasts drain so they cannot race B's promotion below.
	time.Sleep(busSettleDelay)

	// B promotes all three into its own L1 from the shared L2. B never indexes
	// tags locally -- it only ever sees keys.
	for _, k := range []string{"a", "b", "c"} {
		_, err := b.GetOrLoad(ctx, k)
		require.NoError(t, err)
		if _, ok, _ := b.Get(ctx, k); !ok {
			t.Fatalf("b should hold %q before tag invalidation", k)
		}
	}

	// A invalidates the tag: it resolves {a,b} from L2's tag index, tombstones
	// them in L2, and broadcasts the resolved key list.
	require.NoError(t, a.InvalidateTag(ctx, "grp"))

	// B must evict exactly a and b via the broadcast, without any local tag index.
	require.Eventually(t, func() bool {
		_, okA, _ := b.Get(ctx, "a")
		_, okB, _ := b.Get(ctx, "b")
		return !okA && !okB
	}, 15*time.Second, 100*time.Millisecond, "b must evict the tagged keys received over the bus")

	// c was not in the tag, so it must survive on B.
	v, ok, err := b.Get(ctx, "c")
	require.NoError(t, err)
	require.True(t, ok, "the untagged key c must NOT be evicted by the tag fan-out")
	require.Equal(t, "vc", v)

	require.GreaterOrEqual(t, b.Stats().BusEvicts, uint64(2), "b evicted the two tagged keys via the bus")
}

// TestConcurrentSetCASExactlyOneWinner guards the VERSION-CAS invariant under
// concurrency: when two instances race a versioned fill against the same key
// (each having read the same `expect` version), exactly one SetCAS wins and the
// loser is rejected with ErrVersionConflict -- L2 is not corrupted and the
// monotonic version advances by exactly one.
//
// We drive the store directly (two independent redisstore handles sharing one
// Redis) because that is the layer where the CAS conflict is decided; the cache
// fill builds on this exact guarantee.
func TestConcurrentSetCASExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	prefix := "cas:" + safeName(t.Name()) + ":" + uniqueSuffix() + ":"

	l2a := redisstore.New[string](newRedisClient(t), store.JSON[string](), redisstore.WithKeyPrefix(prefix))
	l2b := redisstore.New[string](newRedisClient(t), store.JSON[string](), redisstore.WithKeyPrefix(prefix))

	// Seed an entry so both racers read the same non-zero `expect`. The version
	// is opaque (clock-seeded), so we capture it rather than assert a literal.
	until := time.Now().Add(time.Hour)
	seed, err := l2a.SetCAS(ctx, "k", "seed", store.ForceVersion, until, until, nil)
	require.NoError(t, err)
	require.NotZero(t, seed.Version)

	// Both instances read the same authoritative version before writing.
	curA, _, err := l2a.Load(ctx, "k")
	require.NoError(t, err)
	curB, _, err := l2b.Load(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, seed.Version, curA.Version)
	require.Equal(t, seed.Version, curB.Version)

	// Race two CAS writes from the same expected version. Exactly one must win.
	type casResult struct {
		entry store.Entry[string]
		err   error
	}
	resCh := make(chan casResult, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		e, err := l2a.SetCAS(ctx, "k", "from-A", curA.Version, until, until, nil)
		resCh <- casResult{e, err}
	}()
	go func() {
		defer wg.Done()
		<-start
		e, err := l2b.SetCAS(ctx, "k", "from-B", curB.Version, until, until, nil)
		resCh <- casResult{e, err}
	}()
	close(start)
	wg.Wait()
	close(resCh)

	var winners, losers int
	var winningEntry store.Entry[string]
	for r := range resCh {
		if r.err == nil {
			winners++
			winningEntry = r.entry
			continue
		}
		require.ErrorIs(t, r.err, store.ErrVersionConflict, "the losing CAS must fail with a version conflict")
		losers++
	}
	require.Equal(t, 1, winners, "exactly one CAS must win")
	require.Equal(t, 1, losers, "exactly one CAS must lose with a version conflict")

	// The version advanced by exactly one: no double-bump, no corruption.
	require.Equal(t, seed.Version+1, winningEntry.Version, "the winner minted exactly seed+1")

	// L2 holds the winner's value at the winning version, consistently from either handle.
	got, ok, err := l2b.Load(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, seed.Version+1, got.Version, "L2 settled at exactly seed+1")
	require.Equal(t, winningEntry.Value, got.Value, "L2 holds the winner's value, uncorrupted")

	// The loser's next CAS must now use the new version; using the stale expect
	// still conflicts (the version moved on).
	_, err = l2a.SetCAS(ctx, "k", "late", curA.Version, until, until, nil)
	require.ErrorIs(t, err, store.ErrVersionConflict, "a stale expect after the race must still conflict")
}

// buildL2Cache builds a cache instance backed by a shared Redis L2 (no bus),
// keyed under prefix. tweak, if non-nil, customizes the builder.
func buildL2Cache(t *testing.T, prefix string, loader nimbus.Loader[string, string],
	tweak func(*nimbus.Builder[string, string])) nimbus.Cache[string, string] {
	t.Helper()
	l2 := redisstore.New[string](newRedisClient(t), store.JSON[string](), redisstore.WithKeyPrefix(prefix))
	b := nimbus.NewBuilder[string, string](loader).
		L1(memory.New[string]()).
		L2(l2).
		TTL(time.Hour, 0)
	if tweak != nil {
		tweak(b)
	}
	c, err := b.Build()
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// buildL2BusCache builds a cache instance backed by a shared Redis L2 AND a real
// (emulated) Pub/Sub bus on topicID, keyed under prefix.
func buildL2BusCache(t *testing.T, prefix, topicID string, loader nimbus.Loader[string, string],
	tweak func(*nimbus.Builder[string, string])) nimbus.Cache[string, string] {
	t.Helper()
	l2 := redisstore.New[string](newRedisClient(t), store.JSON[string](), redisstore.WithKeyPrefix(prefix))
	client := newPubSubClient(t)
	bus, err := gcppubsub.New(context.Background(), client, topicID, gcppubsub.WithSubscriptionTTL(0))
	require.NoError(t, err)
	b := nimbus.NewBuilder[string, string](loader).
		L1(memory.New[string]()).
		L2(l2).
		Bus(bus).
		TTL(time.Hour, 0)
	if tweak != nil {
		tweak(b)
	}
	c, err := b.Build()
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}
