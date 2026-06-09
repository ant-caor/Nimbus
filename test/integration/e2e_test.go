// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/redisstore"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

// TestEndToEndCrossInstanceViaL2 is the end-to-end proof for the current
// (pre-bus) stack: multiple cache instances backed by one shared Redis L2.
// It shows the origin loader is shielded, L2 is the source of truth, and a
// fresh instance reads the latest value without re-hitting the origin.
func TestEndToEndCrossInstanceViaL2(t *testing.T) {
	ctx := context.Background()
	var origin atomic.Int64
	loader := func(_ context.Context, _ string) (string, error) {
		origin.Add(1)
		return "v1", nil
	}
	prefix := "e2e:" + t.Name() + ":"
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

	// Instance A loads from the origin.
	v, err := a.GetOrLoad(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "v1", v)
	require.Equal(t, int64(1), origin.Load())

	// Instance B promotes the value from the shared L2 without hitting origin.
	v, err = b.GetOrLoad(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "v1", v)
	require.Equal(t, int64(1), origin.Load(), "B should promote from L2, not call the origin")

	// A writes a new value; it lands in L2 (the source of truth).
	require.NoError(t, a.Set(ctx, "k", "v2"))

	// A brand-new instance with a cold L1 reads the latest from L2, no origin call.
	c := mk()
	v, err = c.GetOrLoad(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "v2", v)
	require.Equal(t, int64(1), origin.Load(), "a fresh instance should read latest from L2, not the origin")
}
