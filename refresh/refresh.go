// Package refresh defines how stale-while-revalidate revalidation is scheduled.
//
// Two strategies exist because of Cloud Run's CPU model. Request-bound refresh
// uses a short-lived detached goroutine and no worker pool: it makes progress
// whenever the instance has CPU (i.e. while it is processing requests), which
// is the best you can do under request-only CPU allocation. Background refresh
// uses a long-lived worker pool and therefore requires always-on CPU. Neither
// guarantees completion while the instance is idle; the next request re-triggers
// a still-stale entry.
//
// Keys are strings, matching the rest of runcache's internal key space.
package refresh

import "context"

// Refresher schedules revalidation of cache keys. Implementations dedupe per
// key and Schedule is non-blocking.
type Refresher interface {
	// Schedule requests a revalidation of key, performed by run. It returns true
	// if it actually launched a refresh, and false if it was suppressed (a
	// refresh for key was already in flight, the refresher is closed, or the
	// queue was full), so callers can count real launches.
	Schedule(key string, run func(ctx context.Context) error) bool
	// Close stops any in-flight or pending refreshes and releases resources.
	Close() error
}
