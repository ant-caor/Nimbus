// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package store

import (
	"reflect"
	"testing"
)

// The JSON codec is the (de)serializer for every value that crosses into L2.
// The fuzz tests (codec_fuzz_test.go) prove it never panics and self-round-trips
// arbitrary inputs of one struct shape; these example-based tests pin the
// round-trip for the concrete value types nimbus is generic over in practice:
// scalars, slices, maps, and nested structs.

func TestJSONCodecRoundTripString(t *testing.T) {
	c := JSON[string]()
	b, err := c.Encode("hello, 世界")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got != "hello, 世界" {
		t.Errorf("round-trip = %q, want %q", got, "hello, 世界")
	}
}

func TestJSONCodecRoundTripInt(t *testing.T) {
	c := JSON[int64]()
	for _, want := range []int64{0, 1, -1, 9223372036854775807, -9223372036854775808} {
		b, err := c.Encode(want)
		if err != nil {
			t.Fatalf("Encode(%d): %v", want, err)
		}
		got, err := c.Decode(b)
		if err != nil {
			t.Fatalf("Decode(%d): %v", want, err)
		}
		if got != want {
			t.Errorf("round-trip = %d, want %d", got, want)
		}
	}
}

func TestJSONCodecRoundTripSlice(t *testing.T) {
	c := JSON[[]string]()
	want := []string{"a", "b", "", "c"}
	b, err := c.Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip = %#v, want %#v", got, want)
	}
}

func TestJSONCodecRoundTripMap(t *testing.T) {
	c := JSON[map[string]int]()
	want := map[string]int{"x": 1, "y": -2, "z": 0}
	b, err := c.Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip = %#v, want %#v", got, want)
	}
}

type codecUser struct {
	Name    string         `json:"name"`
	Age     int            `json:"age"`
	Roles   []string       `json:"roles"`
	Profile map[string]any `json:"profile"`
}

func TestJSONCodecRoundTripStruct(t *testing.T) {
	c := JSON[codecUser]()
	want := codecUser{
		Name:    "Ada",
		Age:     36,
		Roles:   []string{"admin", "dev"},
		Profile: map[string]any{"active": true, "score": float64(9.5)},
	}
	b, err := c.Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip = %#v, want %#v", got, want)
	}
}

// TestJSONCodecRoundTripPointer covers a pointer value type, the shape used when
// a nil result must be distinguishable from a zero value.
func TestJSONCodecRoundTripPointer(t *testing.T) {
	c := JSON[*codecUser]()
	want := &codecUser{Name: "Grace", Age: 45}
	b, err := c.Encode(want)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := c.Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got == nil || !reflect.DeepEqual(*got, *want) {
		t.Errorf("round-trip = %#v, want %#v", got, want)
	}
}

// TestJSONCodecDecodeErrorPropagates confirms a malformed payload surfaces as an
// error from Decode rather than a silent zero value, so the fill path can reject
// a corrupted L2 entry.
func TestJSONCodecDecodeErrorPropagates(t *testing.T) {
	c := JSON[codecUser]()
	if _, err := c.Decode([]byte(`{"age": "not-a-number"}`)); err == nil {
		t.Error("Decode of malformed JSON returned nil error, want an error")
	}
	if _, err := c.Decode([]byte(`not json`)); err == nil {
		t.Error("Decode of garbage returned nil error, want an error")
	}
}

// TestJSONCodecEncodeErrorPropagates confirms an un-encodable value surfaces as
// an error from Encode (a channel cannot be JSON-marshaled).
func TestJSONCodecEncodeErrorPropagates(t *testing.T) {
	c := JSON[chan int]()
	if _, err := c.Encode(make(chan int)); err == nil {
		t.Error("Encode of a channel returned nil error, want an error")
	}
}
