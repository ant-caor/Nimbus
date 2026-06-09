package refresh

import (
	"context"
	"sync"
	"time"
)

// Background revalidates stale entries on a fixed worker pool fed by a bounded
// queue.
//
// WARNING: on Cloud Run this REQUIRES always-on CPU (instance-based billing /
// --no-cpu-throttling). With the default request-only allocation, worker
// goroutines are throttled to near-zero between requests and refreshes stall.
// Prefer RequestBound unless your service already runs always-on CPU.
type Background struct {
	jobs    chan job
	timeout time.Duration

	mu       sync.Mutex
	inflight map[string]struct{}
	closing  bool

	wg sync.WaitGroup
}

type job struct {
	key string
	run func(ctx context.Context) error
}

// NewBackground starts a pool of workers draining a queue of depth queueSize.
// timeout bounds each revalidation; <= 0 means no timeout.
func NewBackground(workers, queueSize int, timeout time.Duration) *Background {
	if workers < 1 {
		workers = 1
	}
	if queueSize < 1 {
		queueSize = 1
	}
	b := &Background{
		jobs:     make(chan job, queueSize),
		timeout:  timeout,
		inflight: make(map[string]struct{}),
	}
	for i := 0; i < workers; i++ {
		b.wg.Add(1)
		go b.worker()
	}
	return b
}

func (b *Background) worker() {
	defer b.wg.Done()
	for j := range b.jobs {
		ctx, cancel := b.context()
		_ = j.run(ctx)
		cancel()
		b.mu.Lock()
		delete(b.inflight, j.key)
		b.mu.Unlock()
	}
}

func (b *Background) context() (context.Context, context.CancelFunc) {
	if b.timeout <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), b.timeout)
}

// Schedule enqueues run for key unless one is in flight or the queue is full
// (in which case the refresh is dropped and the entry stays stale until the
// next trigger).
func (b *Background) Schedule(key string, run func(ctx context.Context) error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closing {
		return
	}
	if _, ok := b.inflight[key]; ok {
		return
	}
	select {
	case b.jobs <- job{key: key, run: run}:
		b.inflight[key] = struct{}{}
	default:
		// queue full: drop
	}
}

// Close stops accepting work, drains in-flight jobs, and waits for workers.
func (b *Background) Close() error {
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return nil
	}
	b.closing = true
	close(b.jobs)
	b.mu.Unlock()
	b.wg.Wait()
	return nil
}

var _ Refresher = (*Background)(nil)
