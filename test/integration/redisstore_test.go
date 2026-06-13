// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/redisstore"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

func newL2(t *testing.T) *redisstore.Store[string] {
	t.Helper()
	return redisstore.New[string](
		newRedisClient(t),
		store.JSON[string](),
		redisstore.WithKeyPrefix("k:"+t.Name()+":"),
		redisstore.WithTagPrefix("t:"+t.Name()+":"),
	)
}

func TestRedisStoreVersioning(t *testing.T) {
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	ctx := context.Background()
	until := time.Now().Add(time.Minute)

	// The first version for an absent key is minted with expect=0. Its value is
	// opaque (clock-seeded, not literally 1) but must be non-zero and monotonic.
	e1, err := l2.SetCAS(ctx, "k", "v1", 0, until, until, nil)
	require.NoError(t, err)
	require.NotZero(t, e1.Version)

	got, ok, err := l2.Load(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "v1", got.Value)
	require.Equal(t, e1.Version, got.Version)

	e2, err := l2.SetCAS(ctx, "k", "v2", e1.Version, until, until, nil)
	require.NoError(t, err)
	require.Equal(t, e1.Version+1, e2.Version, "an in-lifetime re-mint is exactly +1")
}

func TestSetCASConflict(t *testing.T) {
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	ctx := context.Background()
	until := time.Now().Add(time.Minute)

	first, err := l2.SetCAS(ctx, "k", "first", 0, until, until, nil)
	require.NoError(t, err)

	// A second writer that still expects version 0 must lose.
	_, err = l2.SetCAS(ctx, "k", "stale", 0, until, until, nil)
	require.ErrorIs(t, err, store.ErrVersionConflict)

	// Writing with the correct expected version succeeds.
	e, err := l2.SetCAS(ctx, "k", "second", first.Version, until, until, nil)
	require.NoError(t, err)
	require.Equal(t, first.Version+1, e.Version)
}

// TestVersionFloorMonotonicAcrossExpiry proves the per-key version never resets
// to a smaller value after the live entry TTLs out of Redis. Before the
// clock-seed fix the version was derived from HGET ver and restarted at 1 once
// the hash expired, which could let a slow in-flight fill holding a pre-expiry
// expected version win a CAS it should have lost.
func TestVersionFloorMonotonicAcrossExpiry(t *testing.T) {
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	ctx := context.Background()

	// redisTTL floors a sub-second stale window to 1000ms, so the entry lives
	// ~1s in Redis regardless of how close staleUntil is.
	shortUntil := time.Now().Add(900 * time.Millisecond)
	e1, err := l2.SetCAS(ctx, "k", "v1", store.ForceVersion, shortUntil, shortUntil, nil)
	require.NoError(t, err)
	require.NotZero(t, e1.Version)

	// Wait until the hash is genuinely gone from Redis (not merely logically stale).
	require.Eventually(t, func() bool {
		_, ok, rerr := l2.Load(ctx, "k")
		return rerr == nil && !ok
	}, 5*time.Second, 100*time.Millisecond, "entry should TTL out of Redis")

	// Re-mint after expiry: pre-fix this returned version 1 (< e1.Version).
	until := time.Now().Add(time.Minute)
	e2, err := l2.SetCAS(ctx, "k", "v2", store.ForceVersion, until, until, nil)
	require.NoError(t, err)
	require.Greater(t, e2.Version, e1.Version, "version must not reset after the hash expired")

	// In-lifetime re-mint is still exactly +1, not another reseed.
	e3, err := l2.SetCAS(ctx, "k", "v3", e2.Version, until, until, nil)
	require.NoError(t, err)
	require.Equal(t, e2.Version+1, e3.Version)
}

// TestVersionFloorMonotonicAcrossTombstoneExpiry proves the same property across
// the expiry of an invalidation tombstone — the exact gap that motivated the
// fix, since the tombstone is what gates a fill-after-invalidate.
func TestVersionFloorMonotonicAcrossTombstoneExpiry(t *testing.T) {
	l2 := redisstore.New[string](
		newRedisClient(t), store.JSON[string](),
		redisstore.WithKeyPrefix("k:"+t.Name()+":"),
		redisstore.WithTombstoneTTL(time.Second), // expire fast for the test
	)
	defer func() { _ = l2.Close() }()
	ctx := context.Background()
	until := time.Now().Add(time.Minute)

	e1, err := l2.SetCAS(ctx, "k", "v1", store.ForceVersion, until, until, nil)
	require.NoError(t, err)

	tombVer, ok, err := l2.CompareAndDelete(ctx, "k", store.ForceVersion)
	require.NoError(t, err)
	require.True(t, ok)
	require.Greater(t, tombVer, e1.Version)

	// Wait for the tombstone to TTL out: read() then reports version 0.
	require.Eventually(t, func() bool {
		got, _, rerr := l2.Load(ctx, "k")
		return rerr == nil && got.Version == 0
	}, 5*time.Second, 100*time.Millisecond, "tombstone should TTL out")

	e2, err := l2.SetCAS(ctx, "k", "v2", store.ForceVersion, until, until, nil)
	require.NoError(t, err)
	require.Greater(t, e2.Version, tombVer, "version must exceed the expired tombstone's version")
}

// TestFillInvariantUnderInvalidate is the proof for the critical fill-after-
// invalidate race: a value loaded by an in-flight fill must NOT be installed if
// the key was invalidated while the loader ran.
func TestFillInvariantUnderInvalidate(t *testing.T) {
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	ctx := context.Background()

	gate := make(chan struct{})
	loaderEntered := make(chan struct{})
	var once sync.Once
	loader := func(_ context.Context, _ string) (string, error) {
		once.Do(func() { close(loaderEntered) })
		<-gate // hold the fill open
		return "stale-value", nil
	}
	c, err := nimbus.NewBuilder[string, string](loader).
		L1(memory.New[string]()).
		L2(l2).
		TTL(time.Minute, 0).
		Build()
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	type result struct {
		v   string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		v, err := c.GetOrLoad(ctx, "k")
		resCh <- result{v, err}
	}()

	<-loaderEntered // fill has read L2 (absent, expect=0) and is inside the loader

	// Invalidate while the loader is blocked: L2 gets a tombstone at version 1.
	_, _, err = l2.CompareAndDelete(ctx, "k", store.ForceVersion)
	require.NoError(t, err)

	close(gate) // let the loader return; its SetCAS(expect=0) must now conflict
	r := <-resCh
	require.ErrorIs(t, r.err, nimbus.ErrNotFound, "stale value must not be installed after invalidation")

	// The stale value must not be observable afterward either.
	_, ok, err := c.Get(ctx, "k")
	require.NoError(t, err)
	require.False(t, ok)
}

// TestNegativeFillInvariantUnderWrite proves the fill invariant also holds for
// NEGATIVE entries: if a real value is written while a not-found loader is in
// flight, the instance must not cache the stale "not found" — its gated negative
// install must conflict and serve the winner instead. (Regression: the negative
// branch used to install into L1 without a CAS.)
func TestNegativeFillInvariantUnderWrite(t *testing.T) {
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	ctx := context.Background()

	gate := make(chan struct{})
	loaderEntered := make(chan struct{})
	var once sync.Once
	loader := func(_ context.Context, _ string) (string, error) {
		once.Do(func() { close(loaderEntered) })
		<-gate                        // hold the fill open
		return "", nimbus.ErrNotFound // origin says: absent
	}
	c, err := nimbus.NewBuilder[string, string](loader).
		L1(memory.New[string]()).
		L2(l2).
		TTL(time.Minute, 0).
		NegativeTTL(time.Minute).
		Build()
	require.NoError(t, err)
	defer func() { _ = c.Close() }()

	type result struct {
		v   string
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		v, err := c.GetOrLoad(ctx, "k")
		resCh <- result{v, err}
	}()

	<-loaderEntered // fill has read L2 (absent, expect=0) and is inside the loader

	// A real value lands in L2 while the not-found loader is blocked: version -> 1.
	until := time.Now().Add(time.Minute)
	_, err = l2.SetCAS(ctx, "k", "real", store.ForceVersion, until, until, nil)
	require.NoError(t, err)

	close(gate) // loader returns ErrNotFound; the gated negative install must conflict

	r := <-resCh
	require.NoError(t, r.err, "must not cache/return a stale negative after a concurrent write")
	require.Equal(t, "real", r.v, "must serve the winning value, not 'not found'")

	// And the negative must not have been cached over the real value.
	v, ok, err := c.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok, "negative must not be cached after losing the race")
	require.Equal(t, "real", v)
}

// TestTwoInstancesShareL2 proves that a second instance promotes the first
// instance's L2 value instead of re-hitting the origin.
func TestTwoInstancesShareL2(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int64
	loader := func(_ context.Context, _ string) (string, error) {
		calls.Add(1)
		return "shared", nil
	}
	prefix := "share:" + t.Name() + ":"
	mk := func() nimbus.Cache[string, string] {
		l2 := redisstore.New[string](newRedisClient(t), store.JSON[string](), redisstore.WithKeyPrefix(prefix))
		c, err := nimbus.NewBuilder[string, string](loader).
			L1(memory.New[string]()).
			L2(l2).
			TTL(time.Minute, 0).
			Build()
		require.NoError(t, err)
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	a, b := mk(), mk()

	v, err := a.GetOrLoad(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "shared", v)

	v, err = b.GetOrLoad(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "shared", v)

	require.Equal(t, int64(1), calls.Load(), "second instance should promote L2, not re-load origin")
}
