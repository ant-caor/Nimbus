package integration

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pubsub "cloud.google.com/go/pubsub/v2"
	"github.com/stretchr/testify/require"

	"github.com/ant-caor/runcache"
	"github.com/ant-caor/runcache/invalidation/gcppubsub"
	"github.com/ant-caor/runcache/store/memory"
)

// TestCrossInstanceInvalidationViaPubSub is the headline coherence proof: an
// Invalidate on one instance evicts the entry from another instance's L1, over
// a real (emulated) Pub/Sub topic with per-instance subscriptions.
func TestCrossInstanceInvalidationViaPubSub(t *testing.T) {
	ctx := context.Background()
	topicID := "inv-" + safeName(t.Name())
	var loads atomic.Int64
	loader := func(_ context.Context, _ string) (int, error) { return int(loads.Add(1)), nil }

	mk := func() runcache.Cache[string, int] {
		client, err := pubsub.NewClient(ctx, pubsubProjectID)
		require.NoError(t, err)
		// TTL 0 disables the expiration policy, which the emulator does not support.
		bus, err := gcppubsub.New(ctx, client, topicID, gcppubsub.WithSubscriptionTTL(0))
		require.NoError(t, err)
		c, err := runcache.NewBuilder[string, int](loader).
			L1(memory.New[int]()).
			Bus(bus).
			TTL(time.Hour, 0).
			Build()
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close(); _ = client.Close() })
		return c
	}

	a := mk()
	b := mk()

	// Let both per-instance subscriptions be created and their Receive loops
	// start before we publish, so neither misses the broadcast.
	time.Sleep(time.Second)

	_, err := a.GetOrLoad(ctx, "k")
	require.NoError(t, err)
	_, err = b.GetOrLoad(ctx, "k")
	require.NoError(t, err)
	if _, ok, _ := b.Get(ctx, "k"); !ok {
		t.Fatal("b should hold k before invalidation")
	}

	require.NoError(t, a.Invalidate(ctx, "k"))

	require.Eventually(t, func() bool {
		_, ok, _ := b.Get(ctx, "k")
		return !ok
	}, 15*time.Second, 100*time.Millisecond, "b's L1 should be evicted via Pub/Sub")

	require.Greater(t, b.Stats().BusEvicts, uint64(0), "b should record a bus eviction")
}

func safeName(name string) string {
	return strings.NewReplacer("/", "-", " ", "-").Replace(name)
}
