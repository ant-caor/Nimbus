// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package invalidation

import (
	"encoding/json"
	"testing"
	"unicode/utf8"
)

// FuzzEventRoundTrip asserts the bus wire format survives a JSON round-trip with
// the eviction-driving fields intact. Bus events are invalidation-only and
// delivery is at-least-once and out-of-order, so correctness rests on each event
// carrying exactly the keys (and id/version/origin) it was published with — a
// codec that dropped or reordered Keys would evict the wrong entries or none.
func FuzzEventRoundTrip(f *testing.F) {
	f.Add("id-1", "key", "user:42", "tag-a", uint64(7), "origin-1")
	f.Add("", "tag", "", "", uint64(0), "")
	f.Add("💥\x00", "key", "rc:k:weird\nkey", "t", ^uint64(0), "o")
	f.Fuzz(func(t *testing.T, id, kind, key, tag string, version uint64, origin string) {
		for _, s := range []string{id, kind, key, tag, origin} {
			if !utf8.ValidString(s) {
				// JSON normalizes invalid UTF-8 to U+FFFD; such fields are not
				// exactly round-trippable. The wire format only ever carries
				// valid-UTF-8 string keys/ids in practice.
				t.Skip()
			}
		}
		ev := Event{
			ID:       id,
			Kind:     Kind(kind),
			Keys:     []string{key},
			Tag:      tag,
			Version:  version,
			OriginID: origin,
		}
		b, err := json.Marshal(ev)
		if err != nil {
			t.Skip()
		}
		var got Event
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal of self-marshaled event failed: %v", err)
		}
		if got.ID != ev.ID || got.Version != ev.Version || got.OriginID != ev.OriginID || got.Kind != ev.Kind {
			t.Fatalf("scalar field mismatch: %+v vs %+v", ev, got)
		}
		if len(got.Keys) != 1 || got.Keys[0] != key {
			t.Fatalf("Keys not preserved: %v vs [%q]", got.Keys, key)
		}
	})
}

// FuzzEventDecode feeds arbitrary bytes to the decoder. Events arrive off an
// untrusted transport (Redis/Pub/Sub); a malformed or hostile message must be
// dropped via an error, never panic the subscriber loop.
func FuzzEventDecode(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"id":"x","kind":"key","keys":["k"],"version":3,"origin":"o"}`),
		[]byte(`{"keys":null}`), []byte(`{"keys":[]}`), []byte(`{}`),
		[]byte(`null`), []byte(`not json`), []byte(``), {0xff, 0x00},
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var ev Event
		_ = json.Unmarshal(b, &ev) // must not panic on any input
	})
}
