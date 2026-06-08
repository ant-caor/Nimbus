package integration

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/invalidation/redispubsub"
	"github.com/ant-caor/nimbus/store/memory"
)

// TestCrossInstanceInvalidationViaRedisPubSub mirrors the headline Pub/Sub
// coherence proof, but over the Redis Pub/Sub bus: an Invalidate on one instance
// evicts the entry from another instance's L1, fanned out across a shared Redis
// channel (no GCP, reusing the Redis the L2 already runs on).
func TestCrossInstanceInvalidationViaRedisPubSub(t *testing.T) {
	ctx := context.Background()
	channel := "inv:" + safeName(t.Name())
	var loads atomic.Int64
	loader := func(_ context.Context, _ string) (int, error) { return int(loads.Add(1)), nil }

	mk := func() nimbus.Cache[string, int] {
		bus := redispubsub.New(newRedisClient(t), channel)
		c, err := nimbus.NewBuilder[string, int](loader).
			L1(memory.New[int]()).
			Bus(bus).
			TTL(time.Hour, 0).
			Build()
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	a := mk()
	b := mk()

	// Redis Pub/Sub only delivers to currently-subscribed clients, so let both
	// Receive loops establish their subscriptions before we publish.
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
	}, 15*time.Second, 100*time.Millisecond, "b's L1 should be evicted via Redis Pub/Sub")

	require.Greater(t, b.Stats().BusEvicts, uint64(0), "b should record a bus eviction")
}
