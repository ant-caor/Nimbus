package invalidation

import (
	"context"
	"sync"
)

// Mem is an in-process fan-out bus: each published event is delivered to every
// active subscriber. It is the bus used in unit tests and is also a valid
// choice for a single-process, multi-instance setup. For real cross-process
// Cloud Run coherence, use the gcppubsub bus.
type Mem struct {
	mu     sync.Mutex
	subs   map[int]chan Event
	nextID int
	closed bool
}

// NewMem returns an in-process fan-out bus.
func NewMem() *Mem {
	return &Mem{subs: make(map[int]chan Event)}
}

// Publish delivers ev to every active subscriber. Delivery is non-blocking: a
// subscriber whose buffer is full drops the event (and will converge on its
// next L2 read), matching the "bus is an optimization" contract.
func (m *Mem) Publish(_ context.Context, ev Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ch := range m.subs {
		select {
		case ch <- ev:
		default:
		}
	}
	return nil
}

// Subscribe delivers events to handler until ctx is cancelled.
func (m *Mem) Subscribe(ctx context.Context, handler func(Event)) error {
	ch := make(chan Event, 256)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	id := m.nextID
	m.nextID++
	m.subs[id] = ch
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.subs, id)
		m.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-ch:
			handler(ev)
		}
	}
}

// Close stops accepting new subscriptions.
func (m *Mem) Close() error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil
}

var _ Bus = (*Mem)(nil)
