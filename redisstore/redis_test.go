// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package redisstore

import (
	"testing"
	"time"

	"github.com/ant-caor/nimbus/store"
)

// These are pure-logic unit tests for redisstore. The CAS/versioning behavior,
// the version floor across expiry and tombstones, the fill invariant, and tag
// invalidation all run in test/integration against a real Redis (the Lua mints
// versions server-side, so only a real interpreter can prove them). Here we
// cover the deterministic Go helpers that need neither a client nor Docker.
//
// parseUnixNano and parseVerFlag are intentionally NOT unit-tested: both take a
// rueidis.RedisMessage / []rueidis.RedisMessage, whose fields (typ, bytes,
// intlen) are unexported and have no exported constructor. A meaningful value
// can only be built by the rueidis mock package, and adding it would pollute the
// core module's dependency graph. A zero-value RedisMessage carries typ==0,
// which is no valid RESP type, so it cannot stand in for a Lua reply. Their
// behavior is exercised end-to-end in test/integration instead.

func TestBoolArg(t *testing.T) {
	if got := boolArg(true); got != "1" {
		t.Errorf("boolArg(true) = %q, want %q", got, "1")
	}
	if got := boolArg(false); got != "0" {
		t.Errorf("boolArg(false) = %q, want %q", got, "0")
	}
}

func TestRedisTTL(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	s := &Store[int]{}

	tests := []struct {
		name       string
		staleUntil time.Time
		want       int64
	}{
		{"well in the future", now.Add(10 * time.Second), 10_000},
		{"just above the floor", now.Add(1500 * time.Millisecond), 1500},
		{"exactly at the floor", now.Add(1000 * time.Millisecond), 1000},
		{"just below the floor", now.Add(999 * time.Millisecond), 1000},
		{"equal to now floors up", now, 1000},
		{"in the past floors up", now.Add(-5 * time.Second), 1000},
		{"far in the future", now.Add(48 * time.Hour), 48 * 3600 * 1000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.redisTTL(tt.staleUntil, now); got != tt.want {
				t.Errorf("redisTTL(%v, %v) = %d, want %d", tt.staleUntil, now, got, tt.want)
			}
		})
	}
}

// TestRedisTTLNeverNonPositive guards the invariant the floor exists to protect:
// a PEXPIRE with a non-positive TTL would either error or immediately expire a
// still-servable entry. No input must ever produce one.
func TestRedisTTLNeverNonPositive(t *testing.T) {
	now := time.Now()
	s := &Store[int]{}
	for _, d := range []time.Duration{-time.Hour, -time.Nanosecond, 0, time.Nanosecond, time.Millisecond, time.Hour} {
		if got := s.redisTTL(now.Add(d), now); got < 1000 {
			t.Errorf("redisTTL with offset %v = %d, want >= 1000", d, got)
		}
	}
}

func TestKeyTagEncodingDefaults(t *testing.T) {
	s := New[int](nil, nil)
	if got := s.k("user:1"); got != "rc:k:user:1" {
		t.Errorf("k(%q) = %q, want %q", "user:1", got, "rc:k:user:1")
	}
	if got := s.t("group"); got != "rc:t:group" {
		t.Errorf("t(%q) = %q, want %q", "group", got, "rc:t:group")
	}
}

func TestWithKeyPrefixAndTagPrefix(t *testing.T) {
	s := New[int](nil, nil, WithKeyPrefix("app:keys:"), WithTagPrefix("app:tags:"))
	if got := s.k("abc"); got != "app:keys:abc" {
		t.Errorf("k(%q) = %q, want %q", "abc", got, "app:keys:abc")
	}
	if got := s.t("xyz"); got != "app:tags:xyz" {
		t.Errorf("t(%q) = %q, want %q", "xyz", got, "app:tags:xyz")
	}
}

// TestKeyTagEncodingEmpty pins the degenerate case: an empty key/tag yields just
// the prefix, never a panic or a stray separator.
func TestKeyTagEncodingEmpty(t *testing.T) {
	s := New[int](nil, nil)
	if got := s.k(""); got != "rc:k:" {
		t.Errorf("k(%q) = %q, want %q", "", got, "rc:k:")
	}
	if got := s.t(""); got != "rc:t:" {
		t.Errorf("t(%q) = %q, want %q", "", got, "rc:t:")
	}
}

func TestTombstoneTTLDefault(t *testing.T) {
	s := New[int](nil, nil)
	if got := s.TombstoneTTL(); got != 60*time.Second {
		t.Errorf("default TombstoneTTL() = %v, want %v", got, 60*time.Second)
	}
}

func TestWithTombstoneTTL(t *testing.T) {
	s := New[int](nil, nil, WithTombstoneTTL(5*time.Minute))
	if got := s.TombstoneTTL(); got != 5*time.Minute {
		t.Errorf("TombstoneTTL() = %v, want %v", got, 5*time.Minute)
	}
}

func TestWithTagTTL(t *testing.T) {
	s := New[int](nil, nil, WithTagTTL(2*time.Hour))
	if got := s.cfg.tagTTL; got != 2*time.Hour {
		t.Errorf("cfg.tagTTL = %v, want %v", got, 2*time.Hour)
	}
}

// TestNewDefaults documents the out-of-the-box configuration that the rest of
// nimbus and the integration suite assume.
func TestNewDefaults(t *testing.T) {
	s := New[int](nil, nil)
	if s.cfg.keyPrefix != "rc:k:" {
		t.Errorf("default keyPrefix = %q, want %q", s.cfg.keyPrefix, "rc:k:")
	}
	if s.cfg.tagPrefix != "rc:t:" {
		t.Errorf("default tagPrefix = %q, want %q", s.cfg.tagPrefix, "rc:t:")
	}
	if s.cfg.tombstoneTTL != 60*time.Second {
		t.Errorf("default tombstoneTTL = %v, want %v", s.cfg.tombstoneTTL, 60*time.Second)
	}
	if s.cfg.tagTTL != 24*time.Hour {
		t.Errorf("default tagTTL = %v, want %v", s.cfg.tagTTL, 24*time.Hour)
	}
}

// TestNewDefaultsCodec confirms New installs the JSON codec when nil is passed,
// the same default the fill path relies on to (de)serialize values.
func TestNewDefaultsCodec(t *testing.T) {
	s := New[int](nil, nil)
	if s.codec == nil {
		t.Fatal("New(nil codec) left codec nil, want default JSON codec")
	}
}

// TestNewKeepsExplicitCodec confirms an explicit codec is not overridden.
func TestNewKeepsExplicitCodec(t *testing.T) {
	c := store.JSON[int]()
	s := New[int](nil, c)
	if s.codec == nil {
		t.Fatal("explicit codec was dropped")
	}
}

// TestCloseIsNoop pins the caller-owned-client contract: Close never touches the
// rueidis client and always returns nil.
func TestCloseIsNoop(t *testing.T) {
	s := New[int](nil, nil)
	if err := s.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}
