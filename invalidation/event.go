// Package invalidation defines the cross-instance eviction bus contracts.
//
// Events are invalidation-only and never carry cached values. That invariant
// is what makes at-least-once, out-of-order delivery safe: applying an
// eviction is idempotent and version-gated, so a duplicate or reordered event
// can only ever drop an entry that is already gone or older.
package invalidation

import "time"

// Kind distinguishes key-level from tag-level invalidation.
type Kind string

// Kind values for an invalidation event.
const (
	KindKey Kind = "key" // one or more specific keys were invalidated
	KindTag Kind = "tag" // every key carrying a tag was invalidated
)

// Event is a single invalidation broadcast. Keys are pre-resolved string keys
// (the cache maps its K to a string via a key codec), so receivers can evict
// without consulting any local tag index or the L2 store.
type Event struct {
	ID        string    `json:"id"`             // unique; idempotency key
	Kind      Kind      `json:"kind"`           // key or tag
	Keys      []string  `json:"keys,omitempty"` // resolved keys to evict
	Tag       string    `json:"tag,omitempty"`  // present for observability on tag events
	Version   uint64    `json:"version"`        // version that triggered the eviction
	OriginID  string    `json:"origin"`         // publishing instance id; receivers skip self
	EmittedAt time.Time `json:"emitted_at"`
}
