// Package metrics exports nimbus statistics as OpenTelemetry metrics.
//
// It observes the cache's Stats() snapshot through asynchronous instruments, so
// it adds no overhead to the cache hot path, and the core nimbus package
// keeps no OpenTelemetry dependency. Import this package only if you want OTel
// metrics.
package metrics

import (
	"context"

	"go.opentelemetry.io/otel/metric"

	"github.com/ant-caor/nimbus"
)

// StatsProvider is satisfied by any nimbus.Cache.
type StatsProvider interface {
	Stats() nimbus.Stats
}

type config struct {
	prefix string
}

// Option configures Register.
type Option func(*config)

// WithPrefix sets the instrument name prefix (default "nimbus").
func WithPrefix(p string) Option { return func(c *config) { c.prefix = p } }

// Register wires OpenTelemetry instruments that report c's stats on each metric
// collection. Call it once per cache; Unregister the returned registration when
// the cache is closed.
func Register(meter metric.Meter, c StatsProvider, opts ...Option) (metric.Registration, error) {
	cfg := config{prefix: "nimbus"}
	for _, o := range opts {
		o(&cfg)
	}

	specs := []struct {
		name string
		desc string
		get  func(nimbus.Stats) int64
	}{
		{"hits", "L1 fresh hits", func(s nimbus.Stats) int64 { return int64(s.Hits) }},
		{"stale_hits", "stale-while-revalidate hits", func(s nimbus.Stats) int64 { return int64(s.StaleHits) }},
		{"misses", "misses", func(s nimbus.Stats) int64 { return int64(s.Misses) }},
		{"loads", "origin loads", func(s nimbus.Stats) int64 { return int64(s.Loads) }},
		{"load_errors", "origin load errors", func(s nimbus.Stats) int64 { return int64(s.LoadErrors) }},
		{"negative_hits", "negative-cache hits", func(s nimbus.Stats) int64 { return int64(s.NegativeHits) }},
		{"refreshes", "SWR refreshes scheduled", func(s nimbus.Stats) int64 { return int64(s.Refreshes) }},
		{"bus_evicts", "cross-instance bus evictions", func(s nimbus.Stats) int64 { return int64(s.BusEvicts) }},
		{"evictions", "L1 capacity evictions", func(s nimbus.Stats) int64 { return int64(s.Evictions) }},
	}

	getters := make(map[metric.Int64ObservableCounter]func(nimbus.Stats) int64, len(specs))
	observables := make([]metric.Observable, 0, len(specs)+1)
	for _, sp := range specs {
		inst, err := meter.Int64ObservableCounter(cfg.prefix+"."+sp.name, metric.WithDescription(sp.desc))
		if err != nil {
			return nil, err
		}
		getters[inst] = sp.get
		observables = append(observables, inst)
	}

	l1len, err := meter.Int64ObservableGauge(cfg.prefix+".l1.entries", metric.WithDescription("current L1 entry count"))
	if err != nil {
		return nil, err
	}
	observables = append(observables, l1len)

	return meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		s := c.Stats()
		for inst, get := range getters {
			o.ObserveInt64(inst, get(s))
		}
		o.ObserveInt64(l1len, int64(s.L1Len))
		return nil
	}, observables...)
}
