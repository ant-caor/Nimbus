package memory

import "github.com/ant-caor/nimbus/internal/clock"

type config struct {
	capacity int // max entries across all shards; 0 = unbounded
	shards   int // rounded up to a power of two
	clock    clock.Clock
}

func defaultConfig() config {
	return config{capacity: 10_000, shards: 16, clock: clock.System{}}
}

// Option configures a memory store.
type Option func(*config)

// WithCapacity sets the maximum number of entries kept across all shards.
// When exceeded, the least-recently-used entry in the affected shard is
// evicted. A value <= 0 means unbounded.
func WithCapacity(n int) Option { return func(c *config) { c.capacity = n } }

// WithShards sets the shard count (rounded up to a power of two). More shards
// reduce lock contention under concurrency.
func WithShards(n int) Option { return func(c *config) { c.shards = n } }

// WithClock injects a time source so TTL behavior is deterministic in tests.
func WithClock(cl clock.Clock) Option { return func(c *config) { c.clock = cl } }

func roundUpPow2(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
