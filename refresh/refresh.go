// Package refresh defines how stale-while-revalidate revalidation is scheduled.
//
// Two strategies exist because of Cloud Run's CPU model: request-bound refresh
// runs while a request holds CPU (safe under request-only allocation), while
// background refresh runs on a worker pool and requires always-on CPU.
//
// Keys are strings, matching the rest of runcache's internal key space.
package refresh

import "context"

// Refresher schedules revalidation of cache keys. Implementations dedupe per
// key and Schedule is non-blocking.
type Refresher interface {
	// Schedule requests a revalidation of key, performed by run. The strategy
	// decides where run executes (within request CPU or on a worker pool).
	Schedule(key string, run func(ctx context.Context) error)
	// Close stops any in-flight or pending refreshes and releases resources.
	Close() error
}
