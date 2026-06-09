// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"testing"
	"time"

	"github.com/ant-caor/nimbus/store"
)

// TestZeroAllocStore gates invariant #4 for the L1 store engine: a Get and an
// overwriting Set on an existing key must allocate zero times per op. These are
// the "L1 Get"/"L1 Set" rows of the README perf table; making them a test (not
// just a benchmark) fails the build on any allocation regression.
func TestZeroAllocStore(t *testing.T) {
	m := New[int](WithCapacity(1 << 10))
	ctx := context.Background()
	now := time.Now()
	e := store.Entry[int]{Value: 1, FreshUntil: now.Add(time.Hour), StaleUntil: now.Add(time.Hour)}
	if err := m.Set(ctx, "k", e); err != nil { // prime the key so Set overwrites in place
		t.Fatal(err)
	}

	cases := []struct {
		name string
		fn   func()
	}{
		{"Get", func() { _, _, _ = m.Get(ctx, "k") }},
		{"Set", func() { _ = m.Set(ctx, "k", e) }}, // overwrite existing key: no map insert
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := testing.AllocsPerRun(1000, tc.fn); got != 0 {
				t.Errorf("L1 %s allocated %v alloc(s)/op; invariant #4 requires 0", tc.name, got)
			}
		})
	}
}
