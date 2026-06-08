// Package redispubsub implements the nimbus invalidation bus over Redis Pub/Sub
// (via rueidis).
//
// Every instance subscribes to one shared channel, so a published eviction fans
// out to all instances — the same broadcast semantics as the pull-based
// gcppubsub bus, but reusing the Redis you already run for the L2 tier: no extra
// infrastructure and no GCP dependency. This is the simplest way to get
// cross-instance coherence when Redis (or Memorystore) is already in the
// picture.
//
// Redis Pub/Sub is fire-and-forget: an instance that is not connected when an
// event is published simply misses it. That is safe because L2 remains the
// source of truth — a missed broadcast only means the instance converges on its
// next L2 read instead of immediately. Events are invalidation-only, so
// at-least-once, out-of-order delivery is fine; the cache de-dupes and
// version-gates.
//
// The rueidis.Client is owned by the caller and is not closed by Bus.Close; it
// may be the very same client used for the Redis L2.
package redispubsub

import (
	"context"
	"encoding/json"

	"github.com/redis/rueidis"

	"github.com/ant-caor/nimbus/invalidation"
)

// Bus is a Redis Pub/Sub-backed invalidation bus.
type Bus struct {
	client  rueidis.Client
	channel string
}

// New returns a bus that publishes to and subscribes on the given channel. All
// instances sharing a cache must use the same channel for the broadcast to fan
// out across them.
func New(client rueidis.Client, channel string) *Bus {
	return &Bus{client: client, channel: channel}
}

// Publish broadcasts an invalidation event to the channel.
func (b *Bus) Publish(ctx context.Context, ev invalidation.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return b.client.Do(ctx, b.client.B().Publish().Channel(b.channel).Message(string(data)).Build()).Error()
}

// Subscribe delivers events to handler until ctx is cancelled, then returns.
// rueidis transparently re-subscribes across reconnects, so a transient Redis
// blip does not silently end the subscription.
func (b *Bus) Subscribe(ctx context.Context, handler func(invalidation.Event)) error {
	return b.client.Receive(ctx, b.client.B().Subscribe().Channel(b.channel).Build(), func(msg rueidis.PubSubMessage) {
		var ev invalidation.Event
		if err := json.Unmarshal([]byte(msg.Message), &ev); err != nil {
			return // poison message: drop rather than tear down the subscription
		}
		handler(ev)
	})
}

// Close is a no-op; the caller owns the rueidis.Client.
func (b *Bus) Close() error { return nil }

var _ invalidation.Bus = (*Bus)(nil)
