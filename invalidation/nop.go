// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package invalidation

import "context"

// Nop is a no-op bus for single-instance use and tests. Publishes are dropped;
// Subscribe blocks until ctx is cancelled and then returns ctx.Err().
type Nop struct{}

// Publish discards the event.
func (Nop) Publish(context.Context, Event) error { return nil }

// Subscribe blocks until ctx is cancelled; it never delivers events.
func (Nop) Subscribe(ctx context.Context, _ func(Event)) error {
	<-ctx.Done()
	return ctx.Err()
}

// Close is a no-op.
func (Nop) Close() error { return nil }

// compile-time check that Nop satisfies Bus.
var _ Bus = Nop{}
