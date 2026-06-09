// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package invalidation

import (
	"context"
	"strconv"
	"testing"
	"time"
)

func BenchmarkDedupeSeen(b *testing.B) {
	d := NewDedupe(4096)
	ids := make([]string, 1024)
	for i := range ids {
		ids[i] = strconv.Itoa(i)
	}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		d.Seen(ids[i&1023])
		i++
	}
}

func BenchmarkMemPublish(b *testing.B) {
	m := NewMem()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = m.Subscribe(ctx, func(Event) {}) }()
	time.Sleep(20 * time.Millisecond) // let the subscriber register

	ev := Event{ID: "x", Kind: KindKey, Keys: []string{"k"}, Version: 1}
	b.ReportAllocs()
	for b.Loop() {
		_ = m.Publish(ctx, ev)
	}
}
