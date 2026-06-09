// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package nimbus

import "testing"

// FuzzKeyString guards invariant #5 at its only enforcement point: the K→string
// boundary. For string keys the mapping must be the identity, because eviction
// by key has to stay consistent across L1, L2, and the bus — a bus event names a
// key by its string form, and the original K is unrecoverable there. Any
// transformation of a string key (truncation, escaping, normalization) would let
// two distinct keys collide or an eviction miss its target.
//
// The seed corpus runs on every `go test`; `-fuzz=FuzzKeyString` explores more.
func FuzzKeyString(f *testing.F) {
	for _, s := range []string{
		"", "k", "user:42", "a b", "💥", "k\x00null", "rc:k:prefixish",
		"  ", "\n\t", "ünïcödé", "very" + string(make([]byte, 4096)),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got := defaultKeyString[string](s); got != s {
			t.Fatalf("defaultKeyString[string](%q) = %q; must be the identity", s, got)
		}
	})
}
