package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/redisstore"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

// TestInvalidateTagOverL2 exercises the L2 tag index end to end: two keys share
// a tag, InvalidateTag tombstones both in Redis and evicts them locally, and a
// subsequent read reloads from the origin (proving L2 was invalidated, not just
// the local L1).
func TestInvalidateTagOverL2(t *testing.T) {
	ctx := context.Background()
	var origin int
	loader := func(_ context.Context, _ string) (string, error) {
		origin++
		return "from-origin", nil
	}
	l2 := redisstore.New[string](
		newRedisClient(t),
		store.JSON[string](),
		redisstore.WithKeyPrefix("tag:"+t.Name()+":"),
		redisstore.WithTagPrefix("tagidx:"+t.Name()+":"),
	)
	c, err := nimbus.NewBuilder[string, string](loader).
		L1(memory.New[string]()).
		L2(l2).
		TTL(time.Hour, 0).
		Build()
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	require.NoError(t, c.Set(ctx, "a", "v1", nimbus.WithTags("grp")))
	require.NoError(t, c.Set(ctx, "b", "v2", nimbus.WithTags("grp")))
	if _, ok, _ := c.Get(ctx, "a"); !ok {
		t.Fatal("a should be present before invalidation")
	}

	require.NoError(t, c.InvalidateTag(ctx, "grp"))

	if _, ok, _ := c.Get(ctx, "a"); ok {
		t.Fatal("a should be evicted from L1 after tag invalidation")
	}
	// L2 was tombstoned too, so GetOrLoad must miss L2 and reload from the origin.
	v, err := c.GetOrLoad(ctx, "a")
	require.NoError(t, err)
	require.Equal(t, "from-origin", v)
	require.Equal(t, 1, origin, "exactly one origin load after tag invalidation")
}
