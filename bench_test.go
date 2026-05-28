package runcache

import (
	"context"
	"testing"
	"time"
)

func BenchmarkGetOrLoadHit(b *testing.B) {
	loader := func(_ context.Context, _ string) (int, error) { return 42, nil }
	c, err := NewBuilder[string, int](loader).TTL(time.Hour, 0).Build()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	_, _ = c.GetOrLoad(ctx, "k") // prime L1
	b.ReportAllocs()
	for b.Loop() {
		_, _ = c.GetOrLoad(ctx, "k")
	}
}

func BenchmarkGetOrLoadHitParallel(b *testing.B) {
	loader := func(_ context.Context, _ string) (int, error) { return 42, nil }
	c, err := NewBuilder[string, int](loader).TTL(time.Hour, 0).Build()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	_, _ = c.GetOrLoad(ctx, "k")
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = c.GetOrLoad(ctx, "k")
		}
	})
}

func BenchmarkGet(b *testing.B) {
	loader := func(_ context.Context, _ string) (int, error) { return 42, nil }
	c, err := NewBuilder[string, int](loader).TTL(time.Hour, 0).Build()
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	_ = c.Set(ctx, "k", 42)
	b.ReportAllocs()
	for b.Loop() {
		_, _, _ = c.Get(ctx, "k")
	}
}
