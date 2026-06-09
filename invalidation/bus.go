// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package invalidation

import "context"

// Broadcaster publishes invalidation events so every instance can evict.
type Broadcaster interface {
	Publish(ctx context.Context, ev Event) error
}

// Subscriber delivers invalidation events to handler until ctx is cancelled.
// Delivery is at-least-once, so handler must be idempotent.
type Subscriber interface {
	Subscribe(ctx context.Context, handler func(Event)) error
	Close() error
}

// Bus is a combined Broadcaster and Subscriber.
type Bus interface {
	Broadcaster
	Subscriber
}
