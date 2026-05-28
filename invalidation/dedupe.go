package invalidation

import "sync"

// Dedupe is a bounded, ring-buffered set of recently-seen event IDs. It makes
// handling at-least-once delivery idempotent at the bookkeeping level (eviction
// itself is already idempotent, so a missed dedupe only costs a redundant
// evict, never correctness).
type Dedupe struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
	idx   int
}

// NewDedupe returns a Dedupe remembering the last capacity IDs.
func NewDedupe(capacity int) *Dedupe {
	if capacity < 1 {
		capacity = 1
	}
	return &Dedupe{
		seen:  make(map[string]struct{}, capacity),
		order: make([]string, capacity),
	}
}

// Seen reports whether id was seen before. If not, it records id (evicting the
// oldest remembered ID once at capacity) and returns false.
func (d *Dedupe) Seen(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[id]; ok {
		return true
	}
	if old := d.order[d.idx]; old != "" {
		delete(d.seen, old)
	}
	d.order[d.idx] = id
	d.idx = (d.idx + 1) % len(d.order)
	d.seen[id] = struct{}{}
	return false
}
