// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"testing"
	"unicode/utf8"
)

// fuzzVal is a representative cached value: a struct the JSON codec must
// round-trip faithfully, since it is exactly what enters L1 and L2.
type fuzzVal struct {
	Name string   `json:"name"`
	N    int64    `json:"n"`
	Tags []string `json:"tags"`
}

// FuzzValueCodecRoundTrip asserts that whatever the JSON codec encodes it can
// decode back unchanged. The fill invariant trusts that a value stamped into L2
// and promoted into L1 is the same bytes the loader produced; a codec that
// silently mangled a value would corrupt the cache without tripping any version
// check.
func FuzzValueCodecRoundTrip(f *testing.F) {
	f.Add("user", int64(42), "a,b")
	f.Add("", int64(0), "")
	f.Add("💥\x00", int64(-9223372036854775808), "x")
	c := JSON[fuzzVal]()
	f.Fuzz(func(t *testing.T, name string, n int64, tags string) {
		if !utf8.ValidString(name) || !utf8.ValidString(tags) {
			// JSON (the codec) replaces invalid UTF-8 with U+FFFD, so such a
			// value is not exactly round-trippable — a documented encoding/json
			// property, out of scope for this round-trip assertion.
			t.Skip()
		}
		v := fuzzVal{Name: name, N: n, Tags: []string{tags}}
		b, err := c.Encode(v)
		if err != nil {
			t.Skip() // un-encodable inputs are out of scope
		}
		got, err := c.Decode(b)
		if err != nil {
			t.Fatalf("decode of self-encoded value failed: %v (value %+v)", err, v)
		}
		if got.Name != v.Name || got.N != v.N || len(got.Tags) != 1 || got.Tags[0] != v.Tags[0] {
			t.Fatalf("round-trip mismatch: encoded %+v, decoded %+v", v, got)
		}
	})
}

// FuzzValueCodecDecode feeds arbitrary bytes to the decoder. Values arrive from
// L2 (Redis), which a misbehaving peer or a corrupted entry could make
// arbitrary; Decode must return an error, never panic.
func FuzzValueCodecDecode(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{"name":"x","n":5,"tags":["t"]}`),
		[]byte(`{}`), []byte(`null`), []byte(`[]`), []byte(`garbage`),
		[]byte(``), {0x00, 0xff}, []byte(`{"n":"not-a-number"}`),
	} {
		f.Add(seed)
	}
	c := JSON[fuzzVal]()
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = c.Decode(b) // must not panic on any input
	})
}
