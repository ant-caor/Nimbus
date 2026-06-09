// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

// Package gcppubsub implements the nimbus invalidation bus over Google Cloud
// Pub/Sub (cloud.google.com/go/pubsub/v2).
//
// Each instance publishes evictions to a shared topic and subscribes with its
// own auto-expiring subscription, so a broadcast fans out to every instance.
// The subscription is deleted when Subscribe returns (e.g. on Close, which a
// Cloud Run service should wire to SIGTERM); the expiration policy is the
// backstop for instances that are hard-killed.
//
// This is the pull-based bus. For request-only-CPU Cloud Run services, see
// PushHandler, which lets a push subscription deliver invalidations inside a
// request (so CPU is allocated). Either way, L2 remains the source of truth:
// an instance that misses a broadcast still converges on its next L2 read.
package gcppubsub

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	pubsub "cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/ant-caor/nimbus/invalidation"
)

type config struct {
	subscriptionID  string
	ackDeadline     time.Duration
	subscriptionTTL time.Duration
}

// Option configures the bus.
type Option func(*config)

// WithSubscriptionID sets a fixed per-instance subscription ID instead of a
// random one. Each instance must use a distinct ID for broadcast fan-out.
func WithSubscriptionID(id string) Option { return func(c *config) { c.subscriptionID = id } }

// WithAckDeadline sets the subscription ack deadline (default 10s).
func WithAckDeadline(d time.Duration) Option { return func(c *config) { c.ackDeadline = d } }

// WithSubscriptionTTL sets the subscription expiration policy, the backstop that
// reclaims subscriptions from instances that never ran teardown. Real Pub/Sub
// enforces a 1 day minimum; 0 disables the policy (default 24h).
func WithSubscriptionTTL(d time.Duration) Option { return func(c *config) { c.subscriptionTTL = d } }

// Bus is a Pub/Sub-backed invalidation bus. The *pubsub.Client is owned by the
// caller and is not closed by Bus.Close.
type Bus struct {
	client    *pubsub.Client
	topicID   string
	topicName string
	publisher *pubsub.Publisher
	cfg       config
}

// New ensures the topic exists and returns a bus that publishes to it. topicID
// is the short topic name (not the full resource path).
func New(ctx context.Context, client *pubsub.Client, topicID string, opts ...Option) (*Bus, error) {
	cfg := config{ackDeadline: 10 * time.Second, subscriptionTTL: 24 * time.Hour}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.subscriptionID == "" {
		cfg.subscriptionID = topicID + "-" + randHex()
	}
	topicName := fmt.Sprintf("projects/%s/topics/%s", client.Project(), topicID)
	if err := ensureTopic(ctx, client, topicName); err != nil {
		return nil, err
	}
	return &Bus{
		client:    client,
		topicID:   topicID,
		topicName: topicName,
		publisher: client.Publisher(topicID),
		cfg:       cfg,
	}, nil
}

// Publish broadcasts an invalidation event to the topic.
func (b *Bus) Publish(ctx context.Context, ev invalidation.Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	res := b.publisher.Publish(ctx, &pubsub.Message{Data: data})
	_, err = res.Get(ctx)
	return err
}

// Subscribe creates this instance's subscription and delivers events to handler
// until ctx is cancelled, then deletes the subscription.
func (b *Bus) Subscribe(ctx context.Context, handler func(invalidation.Event)) error {
	subName := fmt.Sprintf("projects/%s/subscriptions/%s", b.client.Project(), b.cfg.subscriptionID)
	req := &pubsubpb.Subscription{
		Name:               subName,
		Topic:              b.topicName,
		AckDeadlineSeconds: clampAckSeconds(b.cfg.ackDeadline),
	}
	if b.cfg.subscriptionTTL > 0 {
		req.ExpirationPolicy = &pubsubpb.ExpirationPolicy{Ttl: durationpb.New(b.cfg.subscriptionTTL)}
	}
	created := true
	if _, err := b.client.SubscriptionAdminClient.CreateSubscription(ctx, req); err != nil {
		if status.Code(err) != codes.AlreadyExists {
			return fmt.Errorf("gcppubsub: create subscription: %w", err)
		}
		created = false // another holder owns this subscription; don't tear it down
	}
	if created {
		defer func() {
			// Best-effort teardown with a fresh context, since ctx is cancelled.
			dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = b.client.SubscriptionAdminClient.DeleteSubscription(dctx, &pubsubpb.DeleteSubscriptionRequest{Subscription: subName})
		}()
	}

	sub := b.client.Subscriber(b.cfg.subscriptionID)
	return sub.Receive(ctx, func(_ context.Context, m *pubsub.Message) {
		var ev invalidation.Event
		if err := json.Unmarshal(m.Data, &ev); err != nil {
			m.Ack() // poison message: drop rather than redeliver forever
			return
		}
		handler(ev)
		m.Ack()
	})
}

// Close is a no-op; the caller owns the *pubsub.Client.
func (b *Bus) Close() error { return nil }

func randHex() string {
	var b [8]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// clampAckSeconds rounds the ack deadline up to whole seconds and clamps it to
// the Pub/Sub-valid range of [10s, 600s].
func clampAckSeconds(d time.Duration) int32 {
	secs := int32((d + time.Second - 1) / time.Second)
	if secs < 10 {
		secs = 10
	}
	if secs > 600 {
		secs = 600
	}
	return secs
}

var _ invalidation.Bus = (*Bus)(nil)

// PushBus is the push-delivery variant: it publishes to the topic like Bus, but
// receives invalidations via an HTTP push subscription instead of a streaming
// pull. Mount Handler() on your service. This is throttle-safe under Cloud Run
// request-only CPU allocation, because the inbound push request allocates CPU
// (a streaming pull would stall between requests). Delivery is load-balanced
// across instances, so an instance that does not receive a given push converges
// on its next L2 read.
type PushBus struct {
	publisher *pubsub.Publisher
	topicName string

	mu      sync.Mutex
	handler func(invalidation.Event)
}

// NewPush ensures the topic exists and returns a push bus.
func NewPush(ctx context.Context, client *pubsub.Client, topicID string) (*PushBus, error) {
	topicName := fmt.Sprintf("projects/%s/topics/%s", client.Project(), topicID)
	if err := ensureTopic(ctx, client, topicName); err != nil {
		return nil, err
	}
	return &PushBus{publisher: client.Publisher(topicID), topicName: topicName}, nil
}

// ensureTopic best-effort creates the topic. AlreadyExists means another
// instance won the race; PermissionDenied means the topic is provisioned
// out-of-band (e.g. by Terraform) and this identity only has publish rights, so
// we assume it exists.
func ensureTopic(ctx context.Context, client *pubsub.Client, topicName string) error {
	_, err := client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
	switch status.Code(err) {
	case codes.OK, codes.AlreadyExists, codes.PermissionDenied:
		return nil
	default:
		return fmt.Errorf("gcppubsub: create topic: %w", err)
	}
}

// Publish broadcasts an invalidation event to the topic.
func (p *PushBus) Publish(ctx context.Context, ev invalidation.Event) error {
	if p.publisher == nil {
		return nil // zero-value (receive-only) PushBus: nothing to publish to
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	res := p.publisher.Publish(ctx, &pubsub.Message{Data: data})
	_, err = res.Get(ctx)
	return err
}

// Subscribe registers handler; push deliveries to Handler invoke it. It blocks
// until ctx is cancelled, matching the Subscriber contract the cache expects.
func (p *PushBus) Subscribe(ctx context.Context, handler func(invalidation.Event)) error {
	p.mu.Lock()
	p.handler = handler
	p.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// Close is a no-op; the caller owns the *pubsub.Client.
func (p *PushBus) Close() error { return nil }

// Handler returns the http.Handler to mount for the push subscription endpoint.
// Put it behind OIDC push authentication (see examples/cloudrun).
func (p *PushBus) Handler() http.Handler {
	return PushHandler(func(ev invalidation.Event) {
		p.mu.Lock()
		h := p.handler
		p.mu.Unlock()
		if h != nil {
			h(ev)
		}
	})
}

var _ invalidation.Bus = (*PushBus)(nil)
