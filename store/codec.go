// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package store

import "encoding/json"

// Codec encodes and decodes values for the shared L2 tier, where entries cross
// the wire and outlive a single process. The in-process L1 holds values
// directly and does not use a Codec.
type Codec[V any] interface {
	Encode(v V) ([]byte, error)
	Decode(b []byte) (V, error)
}

// JSON returns a Codec that encodes values as JSON. It is the default L2 codec.
func JSON[V any]() Codec[V] { return jsonCodec[V]{} }

type jsonCodec[V any] struct{}

func (jsonCodec[V]) Encode(v V) ([]byte, error) { return json.Marshal(v) }

func (jsonCodec[V]) Decode(b []byte) (V, error) {
	var v V
	err := json.Unmarshal(b, &v)
	return v, err
}
