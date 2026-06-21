# Nimbus over plain Redis (cloud-agnostic L2 + bus)

The GCP-free coherence path. This example wires Nimbus's full stack using only
Redis: an in-process **L1**, a versioned Redis **L2** (`redisstore`, the source
of truth), and a **Redis Pub/Sub** invalidation bus (`invalidation/redispubsub`).
All three packages live in the core module, so there is no GCP dependency, no
Pub/Sub emulator, and no separate transport module — just a `replace` to `../..`.

## What it shows

It builds **two** cache instances in one process (as if two Cloud Run replicas),
each with its own L1 but sharing one Redis and one Pub/Sub channel. It then:

1. warms both instances' L1 with the same value,
2. does a `Set` on instance **A**, which publishes an invalidation on the Redis
   Pub/Sub channel,
3. waits for instance **B** to drop its now-stale L1 entry, and
4. re-reads on **B**, which converges on A's write from the shared L2 — without
   B ever calling the origin loader.

This is the same broadcast eviction the Cloud Run example gets from GCP Pub/Sub,
but reusing the Redis you already run for the L2 tier. The bus only shrinks the
convergence window; even if B missed the broadcast, it would converge on its next
L2 read, because **L2 is the source of truth**.

## Run it

```sh
docker run --rm -p 6379:6379 redis   # any reachable Redis works
go run .                             # or REDIS_ADDR=host:port go run .
```

Expected output (origin loaded once; B never re-loads after the eviction):

```
warm:  A="value-for-user:42-rev1" B="value-for-user:42-rev1" (origin loads so far: 1)
after: B="value-set-by-A" (origin loads so far: 1)
OK: instance B converged on A's write via the Redis Pub/Sub bus
```

This example is its own unpublished module (`package main`); it is never tagged
or released.
