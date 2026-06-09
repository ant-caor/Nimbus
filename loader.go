package runcache

import "context"

// Loader fetches the authoritative value for key on a cold miss or on
// revalidation. Return ErrNotFound to request negative-caching of key; any
// other error is treated as transient and is not cached.
type Loader[K comparable, V any] func(ctx context.Context, key K) (V, error)

// RefreshMode selects how stale-while-revalidate revalidation is executed.
//
// The distinction matters on Cloud Run: with request-only CPU allocation,
// background goroutines are throttled to near-zero between requests, so a
// detached refresh can stall. See DESIGN.md.
type RefreshMode int

const (
	// RefreshRequestBound runs revalidation within the lifecycle of the request
	// that observed the stale entry, so CPU is allocated while that request is
	// in flight. It is the default and is safe under Cloud Run request-only CPU.
	RefreshRequestBound RefreshMode = iota

	// RefreshBackground runs revalidation on a long-lived worker pool off the
	// request path. It REQUIRES Cloud Run always-on CPU (instance-based
	// billing); otherwise refreshes stall between requests.
	RefreshBackground
)

// String implements fmt.Stringer.
func (m RefreshMode) String() string {
	switch m {
	case RefreshRequestBound:
		return "request-bound"
	case RefreshBackground:
		return "background"
	default:
		return "unknown"
	}
}
