# Nimbus design

How Nimbus stays coherent across ephemeral, autoscaling Cloud Run instances,
and the tradeoffs behind it.

## The problem

Cloud Run runs your container as many short-lived instances that autoscale, can
scale to zero, and share nothing. That breaks the usual caching assumptions:

- **In-memory caches are volatile.** Instances are created and destroyed
  constantly; an in-process cache is lost on every scale-down. A scale-from-zero
  burst spins up many cold instances that all stampede the database at once.
- **There is no shared memory and no stable addressing.** A request lands on an
  arbitrary instance, so per-instance caches diverge, and because instances are
  ephemeral you cannot address one to invalidate it.
- **External caches cost latency and money.** Memorystore gives you coherence,
  but adds a network hop, a VPC connector or Direct VPC Egress, and a standing
  bill. In-memory is free but volatile.

Nimbus is the orchestration layer over these tradeoffs.

## Architecture

Three tiers and one rule:

> **L2 is the source of truth. L1 is a best-effort, per-instance accelerator.
> The bus is a latency optimization, not the sole coherence mechanism.**

```
                    request
                       │
                 ┌─────▼─────┐  hit
                 │    L1     │────────► value
                 │ in-proc   │
                 │ LRU + TTL │
                 └─────┬─────┘  miss
                       │
                 ┌─────▼─────────────┐  fresh
                 │        L2         │────────► value (promote to L1)
                 │ Redis, versioned, │
                 │ source of truth   │
                 └─────┬─────────────┘  miss / stale
                       │  singleflight
                 ┌─────▼─────┐
                 │  origin   │
                 │  loader   │
                 └───────────┘

   write / invalidate ──► Pub/Sub topic ──► every instance evicts its L1
```

- **L1** (`store/memory`): a hand-written sharded LRU with TTL. Hashing keys to
  shards keeps lock contention low under Cloud Run's per-instance request
  concurrency. It is deliberately behind the `store.Store` interface so a faster
  engine (Ristretto, Otter) can replace it without touching the cache.
- **L2** (`redisstore`, over rueidis): the shared, versioned source of truth.
  Client-side caching is disabled; Nimbus owns the in-process layer.
- **Bus** (`invalidation` + `invalidation/gcppubsub`): broadcasts evictions so
  L1 caches converge in milliseconds rather than at L1 TTL.

Everything below the public API is **string-keyed**. The user's key type `K`
lives only on `Cache[K, V]`, which maps `K` to a string once at its boundary.
That uniformity is what lets an instance evict an entry named by a key it
received over the bus, where the original `K` is unrecoverable.

## The fill invariant (the core correctness idea)

Version-gating an *eviction* is only half a protocol. The subtle, dangerous bug
is the **fill-after-invalidate race**:

1. Instance B misses and calls the loader for key `k`; the loader reads the
   pre-write value `v_old` and has not returned yet.
2. Instance A writes a new value and invalidates `k`. The invalidation cannot
   evict anything on B, because B has nothing cached for `k` yet.
3. B's loader returns `v_old` and B caches it. No future event will ever evict
   it: the only invalidation that would have caught it already fired and found
   nothing. B serves stale until its TTL expires.

Nimbus closes this with the **fill invariant**:

> No value (positive, negative, or an explicit `Set`) enters L1 except stamped
> with a version minted by L2, decided atomically against concurrent
> invalidations. A fill whose version is not the latest at install time is
> discarded, not cached.

Concretely, a fill reads L2's current version `expect`, runs the loader, then
writes with `SetCAS(key, val, expect)`. If anything bumped the version in
between (an invalidation, another writer), the CAS fails and the loaded value is
thrown away; Nimbus re-reads L2 and serves the winner. The race is closed at
the only place a value can enter the cache.

This is proven by `TestFillInvariantUnderInvalidate`: it blocks a loader
mid-fill, invalidates the key, releases the loader, and asserts the stale value
is never observable.

## Version protocol

There is a **single monotonic version counter per key**, minted only inside L2
Lua scripts. The client never invents a version; it receives one.

- `SetCAS(key, val, expect, …)` — increments and writes only if the current
  version equals `expect` (or `expect` is `ForceVersion`, for last-writer-wins
  writes like `Set`). Returns the new version.
- `CompareAndDelete(key, version)` — increments and writes a **tombstone** if
  `version >= current`. The tombstone carries the new version and outlives the
  longest plausible loader, so it gates slow in-flight fills (a fill with an
  older `expect` will CAS-fail against it).
- `Load(key)` — returns the current entry, and crucially returns the current
  **version even when the key is absent or tombstoned** (so a fill can use it as
  `expect`).

Because the bus carries **eviction events only, never values**, delivery order
does not matter and at-least-once delivery is safe: applying an eviction is
idempotent, and a duplicate or reordered event can only drop an entry that is
already gone. A bounded ring-buffer (`Dedupe`) suppresses redundant work but is
not required for correctness.

## Stale-while-revalidate and the Cloud Run CPU model

An entry has a fresh window and an additional stale window. Within the stale
window, `GetOrLoad` serves the stale value immediately and schedules a
revalidation, which reads L2's version first (so a refresh reconciles against
the truth instead of blindly trusting the loader). A `MaxTTL` caps absolute
lifetime so SWR cannot renew an entry forever without reconciling. Jitter on the
fresh TTL desynchronizes expiries to avoid an avalanche.

How the revalidation runs matters on Cloud Run:

- **Request-bound (default)** runs the refresh on a short-lived detached
  goroutine, with no worker pool. Cloud Run allocates CPU per instance only while
  that instance is processing requests, so this refresh makes progress whenever
  the instance is handling any request and may stall while the instance is idle.
  It is best-effort (bounded by `RefreshTimeout`), not a guarantee of completion;
  if it stalls, the next request re-triggers a still-stale entry. This is the
  honest best you can do under request-only CPU.
- **Background** runs on a worker pool off the request path. This **requires
  always-on CPU** (instance-based billing); otherwise the workers stall between
  requests. It is opt-in and documented as such.

## The bus: pull vs push

| | Pull (per-instance subscription) | Push (HTTP to the service) |
|---|---|---|
| Fan-out | true: every instance gets every event | load-balanced: one instance per message |
| CPU model | needs always-on CPU (streaming pull stalls when throttled) | throttle-safe: the inbound request allocates CPU |
| Setup | each instance creates/deletes its own subscription | one push subscription, no per-instance admin calls |
| Best for | services already running always-on | request-only-CPU Cloud Run (the common case) |

With push, only the receiving instance evicts immediately; the others converge
on their next L2 read, because **L2 is the source of truth**. That is the whole
point of the golden rule: the bus shrinks the convergence window from "L1 TTL"
to "milliseconds" for instances that receive a broadcast, but correctness never
depends on any instance receiving it.

Eviction on receipt is **unconditional** (drop the L1 entry). Dropping is always
safe — the next read repopulates from L2 — so the version on a key event is a
hint to avoid a redundant drop, not a correctness gate. Tag events carry the
resolved key list (resolved authoritatively from L2's tag index by the
publisher), so a receiver evicts the right keys without a local tag index.

Subscriptions are torn down when the subscriber stops (wire `Close` to SIGTERM
on Cloud Run); the subscription expiration policy is the backstop for instances
that are hard-killed. Note Pub/Sub's minimum expiration is one day, not one hour.

## Consistency guarantees and non-goals

- **Eventually consistent across L1.** After a write/invalidation, instances
  converge within the bus latency (push: the receiving instance immediately,
  others on next L2 read; no bus: within the L1 fresh TTL).
- **Bounded staleness**, capped by the fresh TTL (and `MaxTTL`).
- **Negative entries converge differently.** A negative (known-absent) entry is
  L1-only, and a fresh negative hit short-circuits before any L2 read, so the
  "converge on next L2 read" property does NOT apply to it. A negative entry
  converges only via a bus eviction or its negative TTL. With push delivery
  (one instance per message), an instance that misses the broadcast can keep
  reporting "not found" for a newly-created key until its `NegativeTTL` elapses,
  so keep `NegativeTTL` modest. Pull delivery (fan-out) evicts every instance.
- **Not strongly consistent on read.** If you need read-your-writes across all
  instances synchronously, Nimbus is the wrong tool.
- **Bus events are invalidation-only.** Putting values on the bus would make
  ordering significant and break the order-independence guarantee; it is a
  deliberate invariant.

## Testing strategy and its blind spots

- **Unit** (`-race`): the L1, the cache logic, stampede collapse (200 concurrent
  misses → 1 load), SWR with an injectable clock, and cross-instance eviction
  via an in-process fan-out bus (`invalidation.Mem`) — N cache instances in one
  process, each with its own L1 and subscription.
- **Integration** (`test/integration`, testcontainers): the `redisstore`
  protocol against real Redis (versioning, CAS conflict, the fill invariant,
  two-instance L2 sharing), an end-to-end cross-instance test, and
  cross-instance invalidation over the real Pub/Sub emulator.

What the harness deliberately does **not** reproduce, and why you should not read
too much into a green run: Cloud Run CPU throttling, cold-start and
subscription-creation latency, network partitions and redelivery, and push
load-balancer distribution. The Pub/Sub emulator also does not enforce
subscription expiration or push authentication. Those are validated only on a
real deployment.

## Dependency hygiene

The library module depends on `rueidis` and `golang.org/x/sync` only; the GCP
Pub/Sub client is pulled only when you import `invalidation/gcppubsub`. The
testcontainers / Pub/Sub-emulator dependency tree lives in a **separate module**
under `test/integration/`, so it never reaches the library's dependents.

The Go floor is 1.25, set by `rueidis`'s transitive requirements rather than by
choice; it is a reasonable minimum for 2026.

## Performance

Hot paths allocate zero times per operation. See the performance table in the
[README](README.md) and reproduce with `make bench`.
