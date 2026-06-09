// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

// Package clock provides an injectable time source so TTL behavior is
// deterministic in tests and clock skew can be simulated per instance.
package clock

import (
	"sync"
	"time"
)

// Clock is a source of the current time.
type Clock interface {
	Now() time.Time
}

// System is a Clock backed by the wall clock.
type System struct{}

// Now returns the current wall-clock time.
func (System) Now() time.Time { return time.Now() }

// Mock is a manually-advanced Clock for tests. It is safe for concurrent use.
type Mock struct {
	mu  sync.Mutex
	now time.Time
}

// NewMock returns a Mock starting at t.
func NewMock(t time.Time) *Mock { return &Mock{now: t} }

// Now returns the mock's current time.
func (m *Mock) Now() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

// Advance moves the mock clock forward by d.
func (m *Mock) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = m.now.Add(d)
}
