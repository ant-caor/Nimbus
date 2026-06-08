// Package nimbus is a Cloud Run-first cache for Go.
//
// It combines a fast in-process L1, a shared versioned L2 (the source of
// truth), and a Pub/Sub invalidation bus that keeps per-instance caches
// coherent across ephemeral, autoscaling Cloud Run instances.
//
// The correctness backbone is the fill invariant: no value enters L1 except
// stamped with a version minted by the authoritative L2 store, decided
// atomically against concurrent invalidations. The bus is a latency
// optimization, not the sole coherence mechanism; an instance that misses a
// broadcast still converges on its next L2 read. See DESIGN.md for details.
package nimbus
