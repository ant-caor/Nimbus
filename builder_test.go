// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

package nimbus

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ant-caor/nimbus/store"
)

// fakeL2 is a no-op VersionedStore used to exercise Build-time validation. Build
// only type-asserts the L2 and, when ttl > 0, reads TombstoneTTL; the data-plane
// methods are never called here, so they return zero values.
type fakeL2 struct {
	ttl time.Duration
}

func (f fakeL2) Get(context.Context, string) (store.Entry[int], bool, error) {
	return store.Entry[int]{}, false, nil
}
func (f fakeL2) Set(context.Context, string, store.Entry[int]) error { return nil }
func (f fakeL2) Delete(context.Context, string) error                { return nil }
func (f fakeL2) Close() error                                        { return nil }
func (f fakeL2) Load(context.Context, string) (store.Entry[int], bool, error) {
	return store.Entry[int]{}, false, nil
}
func (f fakeL2) SetCAS(context.Context, string, int, uint64, time.Time, time.Time, []string) (store.Entry[int], error) {
	return store.Entry[int]{}, nil
}
func (f fakeL2) CompareAndDelete(context.Context, string, uint64) (uint64, bool, error) {
	return 0, false, nil
}
func (f fakeL2) DeleteByTag(context.Context, string) ([]string, error) { return nil, nil }

// l2WithTTL exposes TombstoneTTLer only when the fake opts in, so we can prove
// Build silently skips the check for stores that do not implement it.
type l2WithTTL struct{ fakeL2 }

func (f l2WithTTL) TombstoneTTL() time.Duration { return f.ttl }

func newLoader() Loader[int, int] {
	return func(context.Context, int) (int, error) { return 0, nil }
}

func TestBuildRejectsTombstoneTTLNotExceedingRefreshTimeout(t *testing.T) {
	cases := []struct {
		name           string
		tombstone      time.Duration
		refreshTimeout time.Duration
	}{
		{"tombstone below refresh", 3 * time.Second, 5 * time.Second},
		{"tombstone equal to refresh", 5 * time.Second, 5 * time.Second},
		{"tombstone below default refresh", 2 * time.Second, 0 /* default 5s */},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewBuilder[int, int](newLoader()).
				L2(l2WithTTL{fakeL2{ttl: tc.tombstone}})
			if tc.refreshTimeout > 0 {
				b = b.RefreshTimeout(tc.refreshTimeout)
			}
			_, err := b.Build()
			if err == nil {
				t.Fatalf("Build accepted tombstone TTL %s with refresh timeout %s; want error", tc.tombstone, tc.refreshTimeout)
			}
			if !strings.Contains(err.Error(), "tombstone") {
				t.Fatalf("error %q does not mention the tombstone TTL", err)
			}
		})
	}
}

func TestBuildAcceptsTombstoneTTLExceedingRefreshTimeout(t *testing.T) {
	c, err := NewBuilder[int, int](newLoader()).
		L2(l2WithTTL{fakeL2{ttl: 60 * time.Second}}).
		RefreshTimeout(5 * time.Second).
		Build()
	if err != nil {
		t.Fatalf("Build rejected a tombstone TTL that exceeds the refresh timeout: %v", err)
	}
	_ = c.Close()
}

func TestBuildSkipsTombstoneValidationWhenUnavailable(t *testing.T) {
	cases := []struct {
		name string
		l2   store.VersionedStore[int]
	}{
		// A store that does not implement TombstoneTTLer is never validated.
		{"no TombstoneTTLer", fakeL2{}},
		// ttl == 0 means "unbounded / not applicable"; skip rather than reject.
		{"zero tombstone TTL", l2WithTTL{fakeL2{ttl: 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := NewBuilder[int, int](newLoader()).
				L2(tc.l2).
				RefreshTimeout(time.Hour).
				Build()
			if err != nil {
				t.Fatalf("Build rejected config with unvalidatable tombstone TTL: %v", err)
			}
			_ = c.Close()
		})
	}
}
