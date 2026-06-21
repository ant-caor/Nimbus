// Copyright 2026 Antonio Cabezas Ordóñez
// SPDX-License-Identifier: Apache-2.0

// Command redisbus is a cloud-agnostic demonstration of Nimbus's full coherence
// stack with no GCP and no emulator: an in-process L1, a versioned Redis L2
// (redisstore), and a Redis Pub/Sub invalidation bus (redispubsub). All three
// pieces live in the core module, so this example needs only a replace to ../..
//
// It builds two cache instances in one process, both pointed at the same Redis
// and the same Pub/Sub channel, then shows that a Set on instance A evicts
// instance B's L1 — cross-instance coherence over plain Redis. Point REDIS_ADDR
// at any reachable Redis; the default is localhost:6379.
//
//	docker run --rm -p 6379:6379 redis
//	go run .
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/redis/rueidis"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/invalidation/redispubsub"
	"github.com/ant-caor/nimbus/redisstore"
	"github.com/ant-caor/nimbus/store"
	"github.com/ant-caor/nimbus/store/memory"
)

const (
	channel = "nimbus:redisbus:invalidations"
	key     = "user:42"
)

func main() {
	redisAddr := envOr("REDIS_ADDR", "localhost:6379")
	ctx := context.Background()

	// The origin loader counts how often it is hit, so we can prove that an L1
	// hit (no load) differs from a converged read after an eviction. In a real
	// service this is your database or upstream API.
	var loads int
	loader := func(_ context.Context, k string) (string, error) {
		loads++
		return fmt.Sprintf("value-for-%s-rev%d", k, loads), nil
	}

	// Two independent cache instances, as if they were two Cloud Run replicas.
	// Each owns its own rueidis.Client and its own L1, but they share one Redis
	// (the L2 source of truth) and one Pub/Sub channel (the invalidation bus).
	a, closeA := newInstance(redisAddr, loader)
	defer closeA()
	b, closeB := newInstance(redisAddr, loader)
	defer closeB()

	// Give each instance's bus subscriber a moment to connect before we publish.
	time.Sleep(200 * time.Millisecond)

	// 1. Warm both instances' L1 from the same origin value via the shared L2.
	va, err := a.GetOrLoad(ctx, key)
	if err != nil {
		log.Fatalf("A GetOrLoad: %v", err)
	}
	vb, err := b.GetOrLoad(ctx, key)
	if err != nil {
		log.Fatalf("B GetOrLoad: %v", err)
	}
	log.Printf("warm:  A=%q B=%q (origin loads so far: %d)", va, vb, loads)

	// 2. Write a new value on instance A. Set publishes an invalidation on the
	//    Redis Pub/Sub channel; instance B receives it and drops its stale L1
	//    entry. L2 is updated authoritatively in the same call.
	if err := a.Set(ctx, key, "value-set-by-A"); err != nil {
		log.Fatalf("A Set: %v", err)
	}

	// 3. Wait for the broadcast to reach B's subscriber (fire-and-forget Pub/Sub;
	//    in production B would otherwise converge on its next L2 read regardless,
	//    because L2 is the source of truth and the bus only shrinks that window).
	if err := waitForEvict(b, 2*time.Second); err != nil {
		log.Fatalf("B never saw the invalidation: %v", err)
	}

	// 4. Read again on B. Its L1 was evicted, so it re-reads the shared L2 and
	//    sees A's write — without B ever calling the origin loader for it.
	vb, err = b.GetOrLoad(ctx, key)
	if err != nil {
		log.Fatalf("B GetOrLoad after evict: %v", err)
	}
	log.Printf("after: B=%q (origin loads so far: %d)", vb, loads)

	if vb != "value-set-by-A" {
		log.Fatalf("cross-instance coherence FAILED: B still sees %q", vb)
	}
	log.Printf("OK: instance B converged on A's write via the Redis Pub/Sub bus")
}

// newInstance builds one cache instance (its own client + L1) sharing the given
// Redis as L2 and the package channel as the invalidation bus. It returns the
// cache and a close func that tears down the cache and the client.
func newInstance(redisAddr string, loader nimbus.Loader[string, string]) (nimbus.Cache[string, string], func()) {
	rdb, err := rueidis.NewClient(rueidis.ClientOption{
		InitAddress:  []string{redisAddr},
		DisableCache: true, // nimbus owns the in-process cache layer
	})
	if err != nil {
		log.Fatalf("connect redis (%s): %v", redisAddr, err)
	}

	cache, err := nimbus.NewBuilder[string, string](loader).
		L1(memory.New[string]()).
		L2(redisstore.New[string](rdb, store.JSON[string]())).
		Bus(redispubsub.New(rdb, channel)). // same channel => fan-out across instances
		TTL(30*time.Second, 5*time.Minute).
		Build()
	if err != nil {
		rdb.Close()
		log.Fatalf("build cache: %v", err)
	}

	return cache, func() {
		_ = cache.Close() // stops the bus subscription
		rdb.Close()
	}
}

// waitForEvict polls until key is no longer present in c's L1 (the bus eviction
// landed) or the timeout elapses. A read-only Get never loads or revalidates, so
// it observes the L1 state without disturbing it.
func waitForEvict(c nimbus.Cache[string, string], timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok, err := c.Get(context.Background(), key); err != nil {
			return err
		} else if !ok {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("L1 entry still present after %s", timeout)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
