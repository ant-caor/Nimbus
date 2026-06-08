package store

import "time"

// Entry is the versioned envelope stored in every cache tier.
//
// Version is minted exclusively by the authoritative L2 store (see
// VersionedStore). Callers and L1 stores must never invent a Version: doing so
// breaks the fill invariant that nimbus relies on for cross-instance
// coherence.
type Entry[V any] struct {
	Value      V
	Version    uint64
	StoredAt   time.Time
	FreshUntil time.Time // before this instant the entry is fresh
	StaleUntil time.Time // between FreshUntil and StaleUntil the entry is stale-servable
	Negative   bool      // true => key is known-absent (negative cache)
}

// Fresh reports whether the entry can be served without revalidation.
func (e Entry[V]) Fresh(now time.Time) bool { return now.Before(e.FreshUntil) }

// Stale reports whether the entry is past its fresh window but may still be
// served while a revalidation runs (stale-while-revalidate).
func (e Entry[V]) Stale(now time.Time) bool {
	return !e.Fresh(now) && now.Before(e.StaleUntil)
}

// Expired reports whether the entry must not be served and requires a reload.
func (e Entry[V]) Expired(now time.Time) bool { return !now.Before(e.StaleUntil) }
