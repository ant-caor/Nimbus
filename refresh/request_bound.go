// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package refresh

import (
	"context"
	"sync"
	"time"
)

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
type RequestBound struct {
	timeout  time.Duration
	mu       sync.Mutex
	inflight map[string]struct{}
	closed   bool
	wg       sync.WaitGroup
}

// NewRequestBound returns a request-bound refresher. timeout bounds each
// revalidation; <= 0 means no timeout.
func NewRequestBound(timeout time.Duration) *RequestBound {
	return &RequestBound{timeout: timeout, inflight: make(map[string]struct{})}
}

// Schedule launches run for key unless a refresh for key is already in flight
// or the refresher is closed. It returns true only when it actually launches.
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
	r.inflight[key] = struct{}{}
	r.wg.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.wg.Done()
		defer func() {
			r.mu.Lock()
			delete(r.inflight, key)
			r.mu.Unlock()
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

// Close prevents new refreshes and waits for in-flight ones to finish.
func (r *RequestBound) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}

var _ Refresher = (*RequestBound)(nil)
