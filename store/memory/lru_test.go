// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ant-caor/nimbus/internal/clock"
	"github.com/ant-caor/nimbus/store"
)

func entry[V any](v V, ttl time.Duration) store.Entry[V] {
	now := time.Now()
	return store.Entry[V]{Value: v, StoredAt: now, FreshUntil: now.Add(ttl), StaleUntil: now.Add(ttl)}
}

func TestLRUEviction(t *testing.T) {
	t.Parallel()
	m := New[int](WithCapacity(2), WithShards(1)) // single shard => exact capacity
	ctx := context.Background()

	_ = m.Set(ctx, "a", entry(1, time.Hour))
	_ = m.Set(ctx, "b", entry(2, time.Hour))
	if _, ok, _ := m.Get(ctx, "a"); !ok { // touch a so b is the LRU
		t.Fatal("a should be present")
	}
	_ = m.Set(ctx, "c", entry(3, time.Hour)) // over capacity -> evict LRU (b)

	if _, ok, _ := m.Get(ctx, "b"); ok {
		t.Fatal("b should have been evicted")
	}
	if _, ok, _ := m.Get(ctx, "a"); !ok {
		t.Fatal("a should remain")
	}
	if _, ok, _ := m.Get(ctx, "c"); !ok {
		t.Fatal("c should remain")
	}
	if got := m.Evictions(); got != 1 {
		t.Fatalf("evictions = %d, want 1", got)
	}
}

func TestExpiryPurgesOnGet(t *testing.T) {
	t.Parallel()
	mc := clock.NewMock(time.Now())
	m := New[int](WithClock(mc))
	ctx := context.Background()

	now := mc.Now()
	_ = m.Set(ctx, "k", store.Entry[int]{Value: 1, FreshUntil: now.Add(time.Minute), StaleUntil: now.Add(time.Minute)})
	if _, ok, _ := m.Get(ctx, "k"); !ok {
		t.Fatal("k should be present before expiry")
	}
	mc.Advance(2 * time.Minute)
	if _, ok, _ := m.Get(ctx, "k"); ok {
		t.Fatal("k should be expired and purged")
	}
	if m.Len() != 0 {
		t.Fatalf("len = %d, want 0 after purge", m.Len())
	}
}

func TestDelete(t *testing.T) {
	t.Parallel()
	m := New[int]()
	ctx := context.Background()
	_ = m.Set(ctx, "k", entry(1, time.Hour))
	_ = m.Delete(ctx, "k")
	if _, ok, _ := m.Get(ctx, "k"); ok {
		t.Fatal("k should be gone after delete")
	}
}

func TestSetIfNewer(t *testing.T) {
	t.Parallel()
	mc := clock.NewMock(time.Now())
	m := New[string](WithClock(mc))
	ctx := context.Background()

	ver := func(v string, version uint64, ttl time.Duration) store.Entry[string] {
		now := mc.Now()
		return store.Entry[string]{Value: v, Version: version, StoredAt: now, FreshUntil: now.Add(ttl), StaleUntil: now.Add(ttl)}
	}
	current := func() (string, uint64) {
		e, ok, _ := m.Get(ctx, "k")
		if !ok {
			t.Fatal("k unexpectedly absent")
		}
		return e.Value, e.Version
	}

	// Install over an absent key.
	if installed, _ := m.SetIfNewer(ctx, "k", ver("v5", 5, time.Hour)); !installed {
		t.Fatal("SetIfNewer over an absent key must install")
	}
	if v, n := current(); v != "v5" || n != 5 {
		t.Fatalf("after absent install = (%q,%d), want (v5,5)", v, n)
	}

	// A strictly newer version replaces.
	if installed, _ := m.SetIfNewer(ctx, "k", ver("v7", 7, time.Hour)); !installed {
		t.Fatal("a newer version must install")
	}
	if v, n := current(); v != "v7" || n != 7 {
		t.Fatalf("after newer install = (%q,%d), want (v7,7)", v, n)
	}

	// An older version is rejected and does not stomp the live entry.
	if installed, _ := m.SetIfNewer(ctx, "k", ver("v3", 3, time.Hour)); installed {
		t.Fatal("an older version must be rejected")
	}
	if v, n := current(); v != "v7" || n != 7 {
		t.Fatalf("older install stomped the entry = (%q,%d), want (v7,7)", v, n)
	}

	// An equal version is skipped: same version denotes the same write.
	if installed, _ := m.SetIfNewer(ctx, "k", ver("v7-dup", 7, time.Hour)); installed {
		t.Fatal("an equal version must be skipped")
	}
	if v, _ := current(); v != "v7" {
		t.Fatalf("equal-version install changed the value to %q, want v7", v)
	}

	// Once the live entry expires it is dead, so even an older version installs.
	mc.Advance(2 * time.Hour)
	if installed, _ := m.SetIfNewer(ctx, "k", ver("v2", 2, time.Hour)); !installed {
		t.Fatal("install over an expired entry must succeed regardless of version")
	}
	if v, n := current(); v != "v2" || n != 2 {
		t.Fatalf("after expired-replace = (%q,%d), want (v2,2)", v, n)
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()
	m := New[int](WithCapacity(1000))
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				k := fmt.Sprintf("k%d", (i*7+j)%200)
				_ = m.Set(ctx, k, entry(j, time.Hour))
				_, _, _ = m.Get(ctx, k)
				if j%7 == 0 {
					_ = m.Delete(ctx, k)
				}
			}
		}(i)
	}
	wg.Wait()
}
