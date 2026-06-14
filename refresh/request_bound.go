// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package refresh

import (
	"context"
	"sync"
	"time"
)

// defaultMaxConcurrentRefresh bounds concurrent request-bound revalidations when
// the caller does not set one. It is a flat, fleet-predictable constant rather
// than a CPU multiple: the cap exists to protect the origin behind a refresh
// (the loader and L2), whose capacity is unrelated to this instance's core
// count. Note it is a PER-INSTANCE cap — under a synchronized wave across an
// autoscaled fleet the origin can see up to (instances * cap) concurrent
// refreshes — so tune MaxConcurrentRefresh down for a fragile origin. Because
// stale-while-revalidate keeps serving the stale value, a modest default is
// origin-friendly and never fails a read.
const defaultMaxConcurrentRefresh = 16

// RequestBound revalidates stale entries on an ad-hoc detached goroutine; it
// keeps no long-lived worker pool.
//
// On Cloud Run with request-only CPU allocation this is the safe default, but
// note the honest semantics: the refresh goroutine is detached (it does not
// share the triggering request's context), and Cloud Run allocates CPU per
// instance only while that instance is processing requests. So the refresh
// makes progress whenever the instance is handling any request and may stall
// while the instance is idle; the next request re-triggers a still-stale entry.
// It is best-effort, bounded by RefreshTimeout, not a guarantee of completion.
//
// Fan-out is capped: at most maxConcurrent revalidations run at once. Without a
// cap, a synchronized stale wave (a deploy or a cold autoscaled fleet expiring
// many keys at once) would spawn one goroutine and one loader call per distinct
// key — a thundering herd on the origin precisely when it is most fragile. On
// saturation a Schedule is dropped (the entry stays stale and re-triggers on the
// next request), mirroring Background's queue-full drop. Stale-while-revalidate
// keeps serving the stale value meanwhile, so a conservative cap only slows how
// fast the stale set drains; it never fails a read.
type RequestBound struct {
	timeout  time.Duration
	sem      chan struct{} // counting semaphore bounding concurrent refreshes
	mu       sync.Mutex
	inflight map[string]struct{}
	closed   bool
	wg       sync.WaitGroup
}

// NewRequestBound returns a request-bound refresher. timeout bounds each
// revalidation (<= 0 means no timeout). maxConcurrent caps concurrent
// revalidations; <= 0 selects defaultMaxConcurrentRefresh.
func NewRequestBound(timeout time.Duration, maxConcurrent int) *RequestBound {
	if maxConcurrent < 1 {
		maxConcurrent = defaultMaxConcurrentRefresh
	}
	return &RequestBound{
		timeout:  timeout,
		sem:      make(chan struct{}, maxConcurrent),
		inflight: make(map[string]struct{}),
	}
}

// Schedule launches run for key unless a refresh for key is already in flight,
// the refresher is closed, or the concurrency cap is saturated. It returns true
// only when it actually launches.
func (r *RequestBound) Schedule(key string, run func(ctx context.Context) error) bool {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return false
	}
	if _, ok := r.inflight[key]; ok {
		r.mu.Unlock()
		return false
	}
	// Acquire a concurrency token without blocking; on saturation drop the
	// refresh rather than pile on another goroutine and loader call.
	select {
	case r.sem <- struct{}{}:
	default:
		r.mu.Unlock()
		return false
	}
	r.inflight[key] = struct{}{}
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.inflight, key)
			r.mu.Unlock()
			<-r.sem // release the concurrency token
		}()
		ctx, cancel := r.context()
		defer cancel()
		_ = run(ctx)
	}()
	return true
}

func (r *RequestBound) context() (context.Context, context.CancelFunc) {
	if r.timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), r.timeout)
}

// Close prevents new refreshes and waits for in-flight ones to finish. Unlike
// Background.Close it needs no idempotency guard: it closes no channel, so a
// repeat call merely re-sets closed and re-Waits on an already-drained group.
func (r *RequestBound) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}

var _ Refresher = (*RequestBound)(nil)
