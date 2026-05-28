package refresh

import (
	"context"
	"sync"
	"time"
)

// RequestBound revalidates stale entries on an ad-hoc goroutine launched from
// the request that observed the staleness. It keeps no long-lived worker pool.
//
// On Cloud Run with request-only CPU allocation this is the safe default: the
// refresh makes progress while a request holds CPU. It does not promise to run
// between requests, which is acceptable because the next request re-triggers it.
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

// Schedule launches run for key unless a refresh for key is already in flight.
func (r *RequestBound) Schedule(key string, run func(ctx context.Context) error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	if _, ok := r.inflight[key]; ok {
		r.mu.Unlock()
		return
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
