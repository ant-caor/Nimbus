package runcache

import (
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	mrand "math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ant-caor/runcache/internal/clock"
	"github.com/ant-caor/runcache/internal/singleflight"
	"github.com/ant-caor/runcache/invalidation"
	"github.com/ant-caor/runcache/refresh"
	"github.com/ant-caor/runcache/store"
)

// noExpiry is the fresh window used when no TTL is configured: the entry stays
// fresh until evicted by L1 capacity pressure.
const noExpiry = 100 * 365 * 24 * time.Hour

// Stats is a snapshot of cache counters. The counters are monotonic and read
// independently, so a snapshot is eventually-consistent rather than a single
// consistent instant (Hits and Misses may be sampled a few operations apart).
type Stats struct {
	Hits         uint64
	StaleHits    uint64 // served stale while a revalidation was scheduled
	Misses       uint64
	Loads        uint64
	LoadErrors   uint64
	NegativeHits uint64
	Refreshes    uint64 // stale-while-revalidate refreshes scheduled
	BusEvicts    uint64 // L1 entries evicted by cross-instance invalidation events
	Evictions    uint64
	L1Len        int
}

// Cache is a Cloud Run-first cache keyed by a user type K with values of type V.
type Cache[K comparable, V any] interface {
	// Get returns a cached value if present and servable (fresh or stale). It is
	// a read-only peek: it never invokes the loader, never schedules a
	// revalidation, and does not update Stats. A negative entry reports false.
	Get(ctx context.Context, key K) (V, bool, error)
	// GetOrLoad returns the value, loading it through the loader on a miss with
	// stampede protection. It returns ErrNotFound on a negative hit.
	GetOrLoad(ctx context.Context, key K) (V, error)
	// Set writes a value and broadcasts an invalidation so other instances drop
	// any stale or negative entry for the key.
	Set(ctx context.Context, key K, val V, opts ...EntryOption) error
	// Invalidate evicts a key locally and broadcasts the eviction.
	Invalidate(ctx context.Context, key K) error
	// InvalidateTag evicts every key carrying tag and broadcasts the eviction.
	InvalidateTag(ctx context.Context, tag string) error
	// Stats returns a counter snapshot.
	Stats() Stats
	// Close stops background work and the bus subscription. It does not close
	// stores or clients passed in by the caller.
	Close() error
}

// EntryOption customizes a single Set.
type EntryOption func(*entryMeta)

type entryMeta struct {
	tags []string
}

// WithTags associates the written key with one or more tags so it can later be
// invalidated via InvalidateTag.
func WithTags(tags ...string) EntryOption {
	return func(m *entryMeta) { m.tags = append(m.tags, tags...) }
}

type cache[K comparable, V any] struct {
	loader    Loader[K, V]
	l1        store.Store[V]
	l2        store.VersionedStore[V] // nil when no shared L2 tier is configured
	bus       invalidation.Bus
	dedupe    *invalidation.Dedupe
	sf        singleflight.Group[V]
	refresher refresh.Refresher
	clk       clock.Clock
	keyString func(K) string
	originID  string

	busCancel context.CancelFunc
	busWG     sync.WaitGroup

	fresh       time.Duration
	staleWindow time.Duration
	negTTL      time.Duration
	maxTTL      time.Duration
	jitter      float64
	refresh     RefreshMode

	tagMu sync.Mutex
	tags  map[string]map[string]struct{} // local, non-authoritative tag index

	closed atomic.Bool

	stats struct {
		hits, staleHits, misses, loads, loadErrors, negHits, refreshes, busEvicts uint64
	}
}

// onEvent applies an invalidation broadcast to the local L1. Dropping an L1
// entry is always safe (the next read repopulates from L2), so eviction is
// unconditional; the version on the event is a hint, not a correctness gate.
func (c *cache[K, V]) onEvent(ev invalidation.Event) {
	if ev.OriginID == c.originID {
		return // our own broadcast; we already evicted locally
	}
	if c.dedupe != nil && c.dedupe.Seen(ev.ID) {
		return
	}
	for _, ks := range ev.Keys {
		_ = c.l1.Delete(context.Background(), ks)
		atomic.AddUint64(&c.stats.busEvicts, 1)
	}
}

var _ Cache[string, int] = (*cache[string, int])(nil)

func (c *cache[K, V]) Get(ctx context.Context, key K) (V, bool, error) {
	var zero V
	if c.closed.Load() {
		return zero, false, ErrClosed
	}
	e, ok, err := c.l1.Get(ctx, c.keyString(key))
	if err != nil || !ok {
		return zero, false, err
	}
	now := c.clk.Now()
	if e.Expired(now) || e.Negative {
		return zero, false, nil
	}
	return e.Value, true, nil
}

func (c *cache[K, V]) GetOrLoad(ctx context.Context, key K) (V, error) {
	var zero V
	if c.closed.Load() {
		return zero, ErrClosed
	}
	ks := c.keyString(key)
	now := c.clk.Now()
	e, ok, _ := c.l1.Get(ctx, ks)
	if ok && e.Fresh(now) {
		atomic.AddUint64(&c.stats.hits, 1)
		if e.Negative {
			atomic.AddUint64(&c.stats.negHits, 1)
			return zero, ErrNotFound
		}
		return e.Value, nil
	}
	if ok && !e.Negative && e.Stale(now) {
		// Stale-while-revalidate: serve the stale value immediately and refresh
		// out of band so the request does not pay the loader latency.
		atomic.AddUint64(&c.stats.staleHits, 1)
		c.scheduleRefresh(key, ks)
		return e.Value, nil
	}
	atomic.AddUint64(&c.stats.misses, 1)
	// Stampede protection: concurrent misses for ks collapse into one load.
	v, _, err := c.sf.Do(ks, func() (V, error) {
		return c.fill(ctx, key, ks)
	})
	return v, err
}

// fill resolves key on an L1 miss and installs the result, enforcing the fill
// invariant: any value entering L1 carries an L2-minted version, decided
// atomically against concurrent invalidations. With no L2 it falls back to a
// direct loader call (version 0).
func (c *cache[K, V]) fill(ctx context.Context, key K, ks string) (V, error) {
	var zero V

	if c.l2 == nil {
		atomic.AddUint64(&c.stats.loads, 1)
		val, err := c.loader(ctx, key)
		now := c.clk.Now()
		if errors.Is(err, ErrNotFound) {
			_ = c.l1.Set(ctx, ks, c.negativeEntry(now, 0))
			return zero, ErrNotFound
		}
		if err != nil {
			atomic.AddUint64(&c.stats.loadErrors, 1)
			return zero, err
		}
		_ = c.l1.Set(ctx, ks, c.valueEntry(val, now))
		return val, nil
	}

	now := c.clk.Now()
	cur, ok, _ := c.l2.Load(ctx, ks)
	if ok && cur.Fresh(now) {
		// L2 already holds a fresh value (e.g. another instance loaded it):
		// promote to L1 without hitting the origin.
		_ = c.l1.Set(ctx, ks, cur)
		return cur.Value, nil
	}
	expect := cur.Version // current authoritative version (0 if absent/tombstone)

	atomic.AddUint64(&c.stats.loads, 1)
	val, err := c.loader(ctx, key)
	now = c.clk.Now()
	if errors.Is(err, ErrNotFound) {
		_ = c.l1.Set(ctx, ks, c.negativeEntry(now, expect))
		return zero, ErrNotFound
	}
	if err != nil {
		atomic.AddUint64(&c.stats.loadErrors, 1)
		return zero, err
	}
	freshUntil, staleUntil := c.window(now)
	stored, serr := c.l2.SetCAS(ctx, ks, val, expect, freshUntil, staleUntil, nil)
	if errors.Is(serr, store.ErrVersionConflict) {
		// A concurrent writer changed L2 between our Load and SetCAS. Do not
		// install our now-stale value; serve the winner from L2.
		if e2, ok2, _ := c.l2.Load(ctx, ks); ok2 {
			_ = c.l1.Set(ctx, ks, e2)
			return e2.Value, nil
		}
		return zero, ErrNotFound
	}
	if serr != nil {
		return zero, serr
	}
	_ = c.l1.Set(ctx, ks, stored)
	return val, nil
}

func (c *cache[K, V]) scheduleRefresh(key K, ks string) {
	launched := c.refresher.Schedule(ks, func(ctx context.Context) error {
		// fill reads L2's version first, so a refresh reconciles against the
		// source of truth instead of blindly trusting the loader.
		if _, err := c.fill(ctx, key, ks); err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		return nil
	})
	if launched { // count real refreshes, not suppressed duplicates
		atomic.AddUint64(&c.stats.refreshes, 1)
	}
}

func (c *cache[K, V]) Set(ctx context.Context, key K, val V, opts ...EntryOption) error {
	if c.closed.Load() {
		return ErrClosed
	}
	var em entryMeta
	for _, o := range opts {
		o(&em)
	}
	ks := c.keyString(key)
	now := c.clk.Now()
	freshUntil, staleUntil := c.window(now)

	if c.l2 != nil {
		stored, err := c.l2.SetCAS(ctx, ks, val, store.ForceVersion, freshUntil, staleUntil, em.tags)
		if err != nil {
			return err
		}
		_ = c.l1.Set(ctx, ks, stored)
		c.publish(ctx, invalidation.Event{
			ID:        newID(),
			Kind:      invalidation.KindKey,
			Keys:      []string{ks},
			Version:   stored.Version,
			OriginID:  c.originID,
			EmittedAt: now,
		})
		return nil
	}

	e := store.Entry[V]{Value: val, StoredAt: now, FreshUntil: freshUntil, StaleUntil: staleUntil}
	if err := c.l1.Set(ctx, ks, e); err != nil {
		return err
	}
	c.indexTags(ks, em.tags)
	c.publish(ctx, invalidation.Event{
		ID:        newID(),
		Kind:      invalidation.KindKey,
		Keys:      []string{ks},
		Version:   e.Version,
		OriginID:  c.originID,
		EmittedAt: now,
	})
	return nil
}

func (c *cache[K, V]) Invalidate(ctx context.Context, key K) error {
	if c.closed.Load() {
		return ErrClosed
	}
	ks := c.keyString(key)
	var version uint64
	if c.l2 != nil {
		nv, _, err := c.l2.CompareAndDelete(ctx, ks, store.ForceVersion)
		if err != nil {
			return err
		}
		version = nv
	}
	_ = c.l1.Delete(ctx, ks)
	c.publish(ctx, invalidation.Event{
		ID:        newID(),
		Kind:      invalidation.KindKey,
		Keys:      []string{ks},
		Version:   version,
		OriginID:  c.originID,
		EmittedAt: c.clk.Now(),
	})
	return nil
}

func (c *cache[K, V]) InvalidateTag(ctx context.Context, tag string) error {
	if c.closed.Load() {
		return ErrClosed
	}
	var keys []string
	var resErr error
	if c.l2 != nil {
		// Authoritative resolution from L2's tag index. On a partial failure it
		// still returns the keys it managed to tombstone, so we evict and
		// broadcast those before surfacing the error.
		keys, resErr = c.l2.DeleteByTag(ctx, tag)
	} else {
		// L1-only: the local, non-authoritative tag index (single instance).
		c.tagMu.Lock()
		set := c.tags[tag]
		keys = make([]string, 0, len(set))
		for k := range set {
			keys = append(keys, k)
		}
		delete(c.tags, tag)
		c.tagMu.Unlock()
	}

	for _, ks := range keys {
		_ = c.l1.Delete(ctx, ks)
	}
	if len(keys) > 0 {
		c.publish(ctx, invalidation.Event{
			ID:        newID(),
			Kind:      invalidation.KindTag,
			Tag:       tag,
			Keys:      keys,
			OriginID:  c.originID,
			EmittedAt: c.clk.Now(),
		})
	}
	return resErr
}

func (c *cache[K, V]) Stats() Stats {
	s := Stats{
		Hits:         atomic.LoadUint64(&c.stats.hits),
		StaleHits:    atomic.LoadUint64(&c.stats.staleHits),
		Misses:       atomic.LoadUint64(&c.stats.misses),
		Loads:        atomic.LoadUint64(&c.stats.loads),
		LoadErrors:   atomic.LoadUint64(&c.stats.loadErrors),
		NegativeHits: atomic.LoadUint64(&c.stats.negHits),
		Refreshes:    atomic.LoadUint64(&c.stats.refreshes),
		BusEvicts:    atomic.LoadUint64(&c.stats.busEvicts),
	}
	if st, ok := c.l1.(interface {
		Evictions() uint64
		Len() int
	}); ok {
		s.Evictions = st.Evictions()
		s.L1Len = st.Len()
	}
	return s
}

func (c *cache[K, V]) Close() error {
	c.closed.Store(true)
	if c.busCancel != nil {
		c.busCancel() // stop the subscriber goroutine
	}
	c.busWG.Wait()
	if c.refresher != nil {
		_ = c.refresher.Close()
	}
	// We deliberately do not close c.l1/c.l2 or the bus: the caller owns any
	// store, client, or bus it passed in.
	return nil
}

func (c *cache[K, V]) publish(ctx context.Context, ev invalidation.Event) {
	if c.bus == nil {
		return
	}
	// Best-effort: a failed broadcast is tolerated because L2 is the source of
	// truth and instances converge on their next L2 read.
	_ = c.bus.Publish(ctx, ev)
}

func (c *cache[K, V]) indexTags(ks string, tags []string) {
	if len(tags) == 0 {
		return
	}
	c.tagMu.Lock()
	defer c.tagMu.Unlock()
	for _, t := range tags {
		set := c.tags[t]
		if set == nil {
			set = make(map[string]struct{})
			c.tags[t] = set
		}
		set[ks] = struct{}{}
	}
}

func (c *cache[K, V]) valueEntry(val V, now time.Time) store.Entry[V] {
	fresh, stale := c.window(now)
	return store.Entry[V]{Value: val, StoredAt: now, FreshUntil: fresh, StaleUntil: stale}
}

func (c *cache[K, V]) negativeEntry(now time.Time, version uint64) store.Entry[V] {
	d := c.negTTL
	if d <= 0 {
		d = c.fresh
	}
	if d <= 0 {
		d = noExpiry
	}
	until := now.Add(d)
	return store.Entry[V]{Negative: true, Version: version, StoredAt: now, FreshUntil: until, StaleUntil: until}
}

func (c *cache[K, V]) window(now time.Time) (freshUntil, staleUntil time.Time) {
	f := c.fresh
	if f <= 0 {
		f = noExpiry
	} else if c.jitter > 0 {
		f = applyJitter(f, c.jitter)
	}
	freshUntil = now.Add(f)
	staleUntil = freshUntil.Add(c.staleWindow)
	if c.maxTTL > 0 {
		hardCap := now.Add(c.maxTTL)
		if freshUntil.After(hardCap) {
			freshUntil = hardCap
		}
		if staleUntil.After(hardCap) {
			staleUntil = hardCap
		}
	}
	return freshUntil, staleUntil
}

func applyJitter(d time.Duration, frac float64) time.Duration {
	// delta in [-frac, +frac]
	delta := (mrand.Float64()*2 - 1) * frac
	return time.Duration(float64(d) * (1 + delta))
}

func newID() string {
	var b [16]byte
	_, _ = crand.Read(b[:])
	return hex.EncodeToString(b[:])
}
