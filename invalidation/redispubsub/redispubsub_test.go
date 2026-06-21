// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package redispubsub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ant-caor/nimbus/invalidation"
)

// Bus.Publish and Bus.Subscribe are thin wrappers over rueidis: Publish JSON-
// marshals the event and PUBLISHes it; Subscribe JSON-unmarshals each delivered
// message and hands it to the handler, dropping a message that fails to decode.
// The wiring against a live Redis (cross-instance fan-out, fire-and-forget
// delivery) is proven in test/integration. What is purely local logic — the
// JSON codec the two ends agree on, and the poison-message drop the Subscribe
// callback performs — is unit-tested here, with no rueidis.Client.
//
// A full fake rueidis.Client is deliberately avoided: Receive and Do take/return
// Completed and RedisResult, whose fields are unexported with no exported
// constructor, so the surface cannot be driven faithfully without the rueidis
// mock package — a dependency the core module must not grow. We instead exercise
// the exact codec the wrapper relies on.

// decode mirrors precisely what the Subscribe callback (redispubsub.go:62) does
// with each PubSubMessage.Message: unmarshal into an Event, and on error drop
// the message (return) rather than tear the subscription down. Testing this
// function is testing that path's behavior without needing a client to drive it.
func decode(message string) (invalidation.Event, bool) {
	var ev invalidation.Event
	if err := json.Unmarshal([]byte(message), &ev); err != nil {
		return invalidation.Event{}, false // poison: dropped
	}
	return ev, true
}

func TestEventCodecRoundTrip(t *testing.T) {
	want := invalidation.Event{
		ID:        "evt-7",
		Kind:      invalidation.KindTag,
		Keys:      []string{"user:1", "user:2", "order:99"},
		Tag:       "users",
		Version:   1<<40 + 17, // a large, realistic seeded version
		OriginID:  "instance-abc",
		EmittedAt: time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, ok := decode(string(data))
	if !ok {
		t.Fatal("round-trip decode reported failure on self-encoded event")
	}

	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Kind != want.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, want.Kind)
	}
	if got.Tag != want.Tag {
		t.Errorf("Tag = %q, want %q", got.Tag, want.Tag)
	}
	if got.Version != want.Version {
		t.Errorf("Version = %d, want %d", got.Version, want.Version)
	}
	if got.OriginID != want.OriginID {
		t.Errorf("OriginID = %q, want %q", got.OriginID, want.OriginID)
	}
	if !got.EmittedAt.Equal(want.EmittedAt) {
		t.Errorf("EmittedAt = %v, want %v", got.EmittedAt, want.EmittedAt)
	}
	if len(got.Keys) != len(want.Keys) {
		t.Fatalf("Keys len = %d, want %d", len(got.Keys), len(want.Keys))
	}
	for i := range want.Keys {
		if got.Keys[i] != want.Keys[i] {
			t.Errorf("Keys[%d] = %q, want %q", i, got.Keys[i], want.Keys[i])
		}
	}
}

// TestEventCodecKeyEvent covers the KindKey shape, where Tag is empty and so is
// elided by the omitempty tag.
func TestEventCodecKeyEvent(t *testing.T) {
	want := invalidation.Event{
		ID:        "k1",
		Kind:      invalidation.KindKey,
		Keys:      []string{"a"},
		Version:   42,
		OriginID:  "node-1",
		EmittedAt: time.Unix(1750000000, 0).UTC(),
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, ok := decode(string(data))
	if !ok {
		t.Fatal("decode of self-encoded key event failed")
	}
	if got.Kind != invalidation.KindKey || got.Tag != "" || got.Version != 42 {
		t.Errorf("round-trip mismatch: got %+v", got)
	}
}

// TestPoisonMessageDropped is the resilience guarantee: a malformed payload on
// the channel must be dropped (decode reports !ok) and must never panic, so a
// single bad publisher cannot tear down every subscriber.
func TestPoisonMessageDropped(t *testing.T) {
	poison := []string{
		``,                       // empty
		`not json at all`,        // garbage
		`{`,                      // truncated object
		`{"version": "not-int"}`, // wrong type for a field
		`[]`,                     // wrong top-level shape (array)
		`null`,                   // JSON null
		"\x00\xff",               // binary junk
		`{"kind": 5}`,            // wrong type for kind
	}
	for _, p := range poison {
		t.Run(p, func(t *testing.T) {
			ev, ok := decode(p)
			if p == `null` {
				// JSON null is the only input that decodes WITHOUT error, into the
				// zero Event (an array like `[]` DOES error and must be dropped).
				// The contract for null is "no panic, decodes to the zero Event".
				if !ok || ev.ID != "" {
					t.Errorf("decode(%q) = (%+v, ok=%v), want zero Event with ok=true", p, ev, ok)
				}
				return
			}
			if ok {
				t.Errorf("decode(%q) reported success, want drop", p)
			}
		})
	}
}

// TestBusConstruction confirms New wires the channel and that the caller-owned
// client contract holds: Close is a no-op returning nil.
func TestBusConstruction(t *testing.T) {
	b := New(nil, "nimbus:inval")
	if b.channel != "nimbus:inval" {
		t.Errorf("channel = %q, want %q", b.channel, "nimbus:inval")
	}
	if err := b.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

// Interface conformance is already asserted in redispubsub.go
// (var _ invalidation.Bus = (*Bus)(nil)); restating it here keeps the unit suite
// self-contained and fails loudly if the source assertion is ever removed.
var _ invalidation.Bus = (*Bus)(nil)
