// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"fmt"
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

// TestDeleteByTagLargeTagScans drives a tag with many more members than one
// SSCAN page / one pipeline batch (256), proving the cursor iteration and the
// batched pipeline tombstone every member, drain the set, and that a repeat call
// is an idempotent no-op.
func TestDeleteByTagLargeTagScans(t *testing.T) {
	ctx := context.Background()
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	until := time.Now().Add(time.Hour)

	const n = 1000
	for i := 0; i < n; i++ {
		_, err := l2.SetCAS(ctx, fmt.Sprintf("k%d", i), "v", store.ForceVersion, until, until, []string{"grp"})
		require.NoError(t, err)
	}

	affected, err := l2.DeleteByTag(ctx, "grp")
	require.NoError(t, err)
	distinct := make(map[string]struct{}, len(affected))
	for _, k := range affected {
		distinct[k] = struct{}{}
	}
	require.Len(t, distinct, n, "every member must be tombstoned across SSCAN pages and pipeline batches")

	// Every member is now a tombstone (Load reports not found, version carried).
	for i := 0; i < n; i++ {
		_, ok, lerr := l2.Load(ctx, fmt.Sprintf("k%d", i))
		require.NoError(t, lerr)
		require.False(t, ok, "k%d must be tombstoned after tag invalidation", i)
	}

	// The set was drained, so a repeat invalidation is an idempotent no-op.
	again, err := l2.DeleteByTag(ctx, "grp")
	require.NoError(t, err)
	require.Empty(t, again, "the tag set must be drained; a repeat InvalidateTag finds nothing")
}

// TestDeleteByTagMintsMonotonicTombstones proves the pipelined batch path still
// mints a strictly greater version per key — so a slow in-flight fill holding the
// pre-invalidation version loses its CAS. This is why the tombstone stays a
// per-key script rather than a single multi-key one.
func TestDeleteByTagMintsMonotonicTombstones(t *testing.T) {
	ctx := context.Background()
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	until := time.Now().Add(time.Hour)

	e, err := l2.SetCAS(ctx, "k", "v", store.ForceVersion, until, until, []string{"grp"})
	require.NoError(t, err)
	require.NotZero(t, e.Version)

	_, err = l2.DeleteByTag(ctx, "grp")
	require.NoError(t, err)

	got, ok, err := l2.Load(ctx, "k")
	require.NoError(t, err)
	require.False(t, ok, "k must be tombstoned")
	require.Greater(t, got.Version, e.Version, "the tombstone version must advance past the live version")
}

// TestDeleteByTagDoesNotNukeNewMembers locks down the SSCAN "don't blind-delete
// the whole set" safety: a member added after an invalidation survives (it is not
// tombstoned) and is caught by the next invalidation.
func TestDeleteByTagDoesNotNukeNewMembers(t *testing.T) {
	ctx := context.Background()
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	until := time.Now().Add(time.Hour)

	_, err := l2.SetCAS(ctx, "old", "v", store.ForceVersion, until, until, []string{"grp"})
	require.NoError(t, err)
	_, err = l2.DeleteByTag(ctx, "grp")
	require.NoError(t, err)

	// A fresh member tagged after the invalidation must survive.
	_, err = l2.SetCAS(ctx, "new", "v2", store.ForceVersion, until, until, []string{"grp"})
	require.NoError(t, err)
	got, ok, err := l2.Load(ctx, "new")
	require.NoError(t, err)
	require.True(t, ok, "a member added after invalidation must not have been deleted")
	require.Equal(t, "v2", got.Value)

	// ...and the next invalidation catches it.
	affected, err := l2.DeleteByTag(ctx, "grp")
	require.NoError(t, err)
	require.Contains(t, affected, "new", "the next InvalidateTag must catch the new member")
}
