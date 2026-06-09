// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package memory

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/ant-caor/nimbus/store"
)

func benchEntry(v int) store.Entry[int] {
	now := time.Now()
	return store.Entry[int]{Value: v, FreshUntil: now.Add(time.Hour), StaleUntil: now.Add(time.Hour)}
}

func benchKeys(n int) []string {
	ks := make([]string, n)
	for i := range ks {
		ks[i] = "key:" + strconv.Itoa(i)
	}
	return ks
}

func BenchmarkGet(b *testing.B) {
	m := New[int](WithCapacity(1 << 17))
	ctx := context.Background()
	_ = m.Set(ctx, "key:0", benchEntry(1))
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = m.Get(ctx, "key:0")
	}
}

func BenchmarkSet(b *testing.B) {
	m := New[int](WithCapacity(1 << 17))
	ctx := context.Background()
	keys := benchKeys(4096)
	e := benchEntry(1)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		_ = m.Set(ctx, keys[i&4095], e)
		i++
	}
}

func BenchmarkGetParallel(b *testing.B) {
	m := New[int](WithCapacity(1 << 17))
	ctx := context.Background()
	keys := benchKeys(4096)
	for i, k := range keys {
		_ = m.Set(ctx, k, benchEntry(i))
	}
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _, _ = m.Get(ctx, keys[i&4095])
			i++
		}
	})
}
