// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package refresh

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestBoundDedups(t *testing.T) {
	t.Parallel()
	r := NewRequestBound(time.Second)
	defer func() { _ = r.Close() }()

	var runs atomic.Int64
	release := make(chan struct{})
	for i := 0; i < 10; i++ {
		r.Schedule("k", func(_ context.Context) error {
			runs.Add(1)
			<-release // hold the slot so duplicates are suppressed
			return nil
		})
	}
	time.Sleep(50 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("concurrent schedules for one key should run once, got %d", got)
	}
	close(release)
}

func TestBackgroundRunsAndCloses(t *testing.T) {
	t.Parallel()
	b := NewBackground(2, 16, time.Second)

	var runs atomic.Int64
	var wg sync.WaitGroup
	wg.Add(5)
	for i := 0; i < 5; i++ {
		b.Schedule(fmt.Sprintf("k%d", i), func(_ context.Context) error {
			runs.Add(1)
			wg.Done()
			return nil
		})
	}
	wg.Wait()
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := runs.Load(); got != 5 {
		t.Fatalf("runs = %d, want 5", got)
	}
	// Schedule after Close is a no-op.
	b.Schedule("x", func(_ context.Context) error { runs.Add(1); return nil })
	if got := runs.Load(); got != 5 {
		t.Fatalf("schedule after close should not run, runs = %d", got)
	}
}
