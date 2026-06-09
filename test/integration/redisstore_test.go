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

	e1, err := l2.SetCAS(ctx, "k", "v1", 0, until, until, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(1), e1.Version)

	got, ok, err := l2.Load(ctx, "k")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "v1", got.Value)
	require.Equal(t, uint64(1), got.Version)

	e2, err := l2.SetCAS(ctx, "k", "v2", 1, until, until, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), e2.Version)
}

func TestSetCASConflict(t *testing.T) {
	l2 := newL2(t)
	defer func() { _ = l2.Close() }()
	ctx := context.Background()
	until := time.Now().Add(time.Minute)

	_, err := l2.SetCAS(ctx, "k", "first", 0, until, until, nil)
	require.NoError(t, err) // version 1

	// A second writer that still expects version 0 must lose.
	_, err = l2.SetCAS(ctx, "k", "stale", 0, until, until, nil)
	require.ErrorIs(t, err, store.ErrVersionConflict)

	// Writing with the correct expected version succeeds.
	e, err := l2.SetCAS(ctx, "k", "second", 1, until, until, nil)
	require.NoError(t, err)
	require.Equal(t, uint64(2), e.Version)
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
