package nimbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ant-caor/nimbus/internal/clock"
	"github.com/ant-caor/nimbus/invalidation"
	"github.com/ant-caor/nimbus/refresh"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

// Builder constructs a Cache. The user names the key and value types once, on
// NewBuilder; every configuration method is a plain (non-generic) method, so
// there is no per-option type-annotation tax.
type Builder[K comparable, V any] struct {
	cfg config[K, V]
}

type config[K comparable, V any] struct {
	loader         Loader[K, V]
	l1             store.Store[V]
	l2             store.VersionedStore[V]
	bus            invalidation.Bus
	clk            clock.Clock
	keyString      func(K) string
	fresh          time.Duration
	staleWindow    time.Duration
	negTTL         time.Duration
	maxTTL         time.Duration
	jitter         float64
	refresh        RefreshMode
	refreshWorkers int
	refreshTimeout time.Duration
}

// NewBuilder starts building a Cache around loader. Return ErrNotFound from the
// loader to request negative-caching of a key.
func NewBuilder[K comparable, V any](loader Loader[K, V]) *Builder[K, V] {
	return &Builder[K, V]{cfg: config[K, V]{
		loader:  loader,
		clk:     clock.System{},
		refresh: RefreshRequestBound,
	}}
}

// L1 sets the in-process accelerator tier. Defaults to an in-memory LRU.
func (b *Builder[K, V]) L1(s store.Store[V]) *Builder[K, V] { b.cfg.l1 = s; return b }

// L2 sets the shared, authoritative tier (the source of truth).
func (b *Builder[K, V]) L2(s store.VersionedStore[V]) *Builder[K, V] { b.cfg.l2 = s; return b }

// Bus sets the cross-instance invalidation bus. Defaults to a no-op bus.
func (b *Builder[K, V]) Bus(bus invalidation.Bus) *Builder[K, V] { b.cfg.bus = bus; return b }

// TTL sets the fresh duration and the additional stale-while-revalidate window.
func (b *Builder[K, V]) TTL(fresh, staleWindow time.Duration) *Builder[K, V] {
	b.cfg.fresh, b.cfg.staleWindow = fresh, staleWindow
	return b
}

// Jitter applies +/- frac randomization to the fresh TTL to avoid synchronized
// expiry. frac must be in [0, 1].
func (b *Builder[K, V]) Jitter(frac float64) *Builder[K, V] { b.cfg.jitter = frac; return b }

// NegativeTTL sets how long a known-absent key is negatively cached. If unset,
// it falls back to the fresh TTL; if that is also unset, negative entries
// persist until evicted. Negative entries are L1-only and converge across
// instances via the bus or this TTL (not via an L2 read), so keep it modest.
func (b *Builder[K, V]) NegativeTTL(d time.Duration) *Builder[K, V] { b.cfg.negTTL = d; return b }

// MaxTTL caps the absolute lifetime of an entry so stale-while-revalidate
// cannot renew it indefinitely without reconciling against L2.
func (b *Builder[K, V]) MaxTTL(d time.Duration) *Builder[K, V] { b.cfg.maxTTL = d; return b }

// RefreshMode selects request-bound (default) or background revalidation.
func (b *Builder[K, V]) RefreshMode(m RefreshMode) *Builder[K, V] { b.cfg.refresh = m; return b }

// BackgroundRefresh selects background revalidation with the given worker count.
// This requires Cloud Run always-on CPU; see RefreshBackground.
func (b *Builder[K, V]) BackgroundRefresh(workers int) *Builder[K, V] {
	b.cfg.refresh = RefreshBackground
	b.cfg.refreshWorkers = workers
	return b
}

// RefreshTimeout bounds how long a single stale-while-revalidate refresh may
// run. Defaults to 5s.
func (b *Builder[K, V]) RefreshTimeout(d time.Duration) *Builder[K, V] {
	b.cfg.refreshTimeout = d
	return b
}

// Clock injects a time source for deterministic tests.
func (b *Builder[K, V]) Clock(c clock.Clock) *Builder[K, V] { b.cfg.clk = c; return b }

// KeyString overrides how a key K is rendered to the string used by L1, L2, and
// the bus. The default handles string keys directly and uses fmt for the rest.
func (b *Builder[K, V]) KeyString(fn func(K) string) *Builder[K, V] { b.cfg.keyString = fn; return b }

// Build validates the configuration and returns a ready Cache.
func (b *Builder[K, V]) Build() (Cache[K, V], error) {
	cfg := b.cfg
	if cfg.loader == nil {
		return nil, errors.New("nimbus: a loader is required")
	}
	if cfg.jitter < 0 || cfg.jitter > 1 {
		return nil, fmt.Errorf("nimbus: jitter must be in [0,1], got %v", cfg.jitter)
	}
	if cfg.fresh < 0 || cfg.staleWindow < 0 || cfg.negTTL < 0 || cfg.maxTTL < 0 {
		return nil, errors.New("nimbus: durations must not be negative")
	}
	if cfg.clk == nil {
		cfg.clk = clock.System{}
	}
	if cfg.l1 == nil {
		// Share the cache's clock so TTL behavior is consistent (and testable).
		cfg.l1 = memory.New[V](memory.WithClock(cfg.clk))
	}
	if cfg.bus == nil {
		cfg.bus = invalidation.Nop{}
	}
	if cfg.keyString == nil {
		cfg.keyString = defaultKeyString[K]
	}

	refreshTimeout := cfg.refreshTimeout
	if refreshTimeout <= 0 {
		refreshTimeout = 5 * time.Second
	}
	var refresher refresh.Refresher
	switch cfg.refresh {
	case RefreshBackground:
		workers := cfg.refreshWorkers
		if workers <= 0 {
			workers = 4
		}
		refresher = refresh.NewBackground(workers, 1024, refreshTimeout)
	default:
		refresher = refresh.NewRequestBound(refreshTimeout)
	}

	c := &cache[K, V]{
		loader:      cfg.loader,
		l1:          cfg.l1,
		l2:          cfg.l2,
		bus:         cfg.bus,
		dedupe:      invalidation.NewDedupe(4096),
		refresher:   refresher,
		clk:         cfg.clk,
		keyString:   cfg.keyString,
		originID:    newID(),
		fresh:       cfg.fresh,
		staleWindow: cfg.staleWindow,
		negTTL:      cfg.negTTL,
		maxTTL:      cfg.maxTTL,
		jitter:      cfg.jitter,
		refresh:     cfg.refresh,
		tags:        make(map[string]map[string]struct{}),
	}

	// Start the cross-instance invalidation subscriber, unless the bus is the
	// no-op bus: it has nothing to deliver, so there is no point spinning a
	// goroutine that would only block until Close.
	if _, isNop := cfg.bus.(invalidation.Nop); !isNop {
		subCtx, cancel := context.WithCancel(context.Background())
		c.busCancel = cancel
		c.busWG.Add(1)
		go func() {
			defer c.busWG.Done()
			_ = c.bus.Subscribe(subCtx, c.onEvent)
		}()
	}

	return c, nil
}

func defaultKeyString[K comparable](k K) string {
	if s, ok := any(k).(string); ok {
		return s
	}
	return fmt.Sprint(k)
}
