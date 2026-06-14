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

As a defense-in-depth layer, every L2-minted install into L1 is **version-gated**
(`store.ConditionalStore.SetIfNewer`): it will not overwrite a live L1 entry that
already holds an equal-or-greater version. This closes a narrow L1-stomp window —
between a fill's `SetCAS` and its L1 install, a concurrent `Set` or a
bus-delivered eviction can land a newer entry, which an unconditional install
would clobber with the fill's older value. It relies on the version being
globally monotonic per key (see the clock-seeding in the version protocol below).
The L1 stays a best-effort accelerator: a Store that does not implement
`ConditionalStore` falls back to an unconditional install, and bus eviction stays
unconditional (dropping is always safe).

## Version protocol

There is a **single monotonic version per key**, minted only inside L2 Lua
scripts. The client never invents a version; it receives one. The version is
monotonic across the key's **entire history**, including after the live entry or
its tombstone expires out of Redis: while a hash is present the script mints
`current + 1`, but when none exists it seeds from the server clock as
`(unixMillis << 10) | seq` rather than restarting at 1. Wall-clock time only
advances across an expiry gap, so a re-mint after expiry is always strictly
greater than any version the key carried before — closing the narrow window
where a slow fill holding a pre-expiry expected version could win a CAS against a
key that had since expired and been re-minted from zero. This needs no second
key and no hash tag (`TIME` takes no keys, so it never forces a cross-slot
script).

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
  honest best you can do under request-only CPU. Fan-out is **capped**
  (`MaxConcurrentRefresh`, default 16 per instance): a synchronized stale wave —
  a deploy, or a cold autoscaled fleet expiring many keys at once — would
  otherwise spawn one goroutine and one loader call per distinct key, a
  thundering herd on the origin. On saturation a refresh is dropped (the entry
  stays stale and re-triggers later); stale-serve keeps serving meanwhile, so the
  cap only slows how fast the stale set drains, never failing a read. Jitter on
  the fresh TTL is the first line of defence against such waves; the cap is the
  backstop. The cap is per-instance, so the fleet-wide bound is instances × cap.
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

### Transports

The bus is an interface (`invalidation.Bus`), so the transport is pluggable:

| Transport | Package | Delivery | When |
|---|---|---|---|
| In-process | `invalidation.Mem` | fan-out within one process | unit tests, single-instance |
| GCP Pub/Sub (pull) | `invalidation/gcppubsub` `New` | true fan-out | always-on CPU |
| GCP Pub/Sub (push) | `invalidation/gcppubsub` `NewPush` | load-balanced | request-only-CPU Cloud Run |
| Redis Pub/Sub | `invalidation/redispubsub` | true fan-out | Redis/Memorystore already in use |

The Redis transport is the lowest-friction one: it reuses the very client the
Redis L2 already holds, so it adds no new infrastructure and no GCP dependency —
fitting for serverless platforms beyond Cloud Run. Redis Pub/Sub is
fire-and-forget (no per-instance subscription, no expiration policy to manage):
an instance not connected at publish time misses the event and, as always,
converges on its next L2 read.

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

### Degraded mode when L2 is unreachable

L2 is the source of truth, but it must not be a hard availability dependency for
reads. When a fill hits a **non-conflict** L2 error (a `Load`, `SetCAS`, or
`CompareAndDelete` that fails because Redis/Memorystore is unreachable — distinct
from `ErrVersionConflict`, which is a normal CAS loss), the read path **fails open
to the origin**:

- `GetOrLoad` returns the loader's result — the value, or `ErrNotFound` for a
  not-found loader. The origin is authoritative and already produced it, so a
  request is never failed merely because the coordination tier is down.
- The result is **not written to L1**: with no L2-minted version, an install would
  be uncoordinated and could be served stale after recovery (it would not be
  superseded until its TTL, since a fresh L1 hit short-circuits the next L2 read).
  Declining to cache keeps the fill invariant intact — nothing un-versioned ever
  enters L1 — at the cost of origin-load amplification during the outage, bounded
  by singleflight collapsing concurrent misses. That is a capacity problem, never
  a correctness one. The `Stats.L2Errors` counter makes the degradation visible.
- A genuine **loader** error still propagates — degraded mode tolerates only a
  failed L2 coordination of a *successful* load, never a real upstream failure.
- Read-only paths that never touch L2 — a fresh or stale L1 hit and a `Get` peek —
  are unaffected and keep serving while L2 is down.
- The **write path** (`Set`, `Invalidate`, `InvalidateTag`) still hard-fails on an
  L2 error: the write to the source of truth did not land, and the caller must
  observe that. Silently evicting L1 and broadcasting would advertise an
  invalidation for a write that never happened.

## Testing strategy and its blind spots

- **Unit** (`-race`): the L1, the cache logic, stampede collapse (200 concurrent
  misses → 1 load), SWR with an injectable clock, and cross-instance eviction
  via an in-process fan-out bus (`invalidation.Mem`) — N cache instances in one
  process, each with its own L1 and subscription.
- **Integration** (`test/integration`, testcontainers): the `redisstore`
  protocol against real Redis (versioning, CAS conflict, the fill invariant,
  two-instance L2 sharing), an end-to-end cross-instance test, cross-instance
  invalidation over the real Pub/Sub emulator, and the **L2-outage degraded-mode
  contract** — a toxiproxy in front of a dedicated Redis is cut and healed to
  prove reads fail open to the origin while L2 is down and resume normal
  versioned fills on recovery.

What the harness deliberately does **not** reproduce, and why you should not read
too much into a green run: Cloud Run CPU throttling, cold-start and
subscription-creation latency, *partial* partitions and redelivery, and push
load-balancer distribution. The Pub/Sub emulator also does not enforce
subscription expiration or push authentication. Those are validated only on a
real deployment.

## Dependency hygiene

The core module `github.com/ant-caor/nimbus` depends on `rueidis` and
`golang.org/x/sync` only. Everything provider- or OTel-specific lives in its own
module, so it reaches a dependent only if that dependent imports it:

- `github.com/ant-caor/nimbus/invalidation/gcppubsub` — the GCP Pub/Sub bus
  (pulls `cloud.google.com/go/pubsub/v2`, gRPC, protobuf).
- `github.com/ant-caor/nimbus/metrics` — the OpenTelemetry metrics adapter
  (pulls `go.opentelemetry.io/otel/...`).
- `test/integration/` — the testcontainers / Pub/Sub-emulator tree.

Import paths are unchanged from a single-module layout; importing `gcppubsub` or
`metrics` simply adds a `require` for that module rather than for the core. The
result: a consumer of just the core (in-process L1 + Redis L2 + the Redis Pub/Sub
bus, `invalidation/redispubsub`) gets a coherence stack with **no GCP and no OTel
in its module graph** — fully cloud-agnostic, running on any cloud or on-prem
wherever Redis is reachable. `go list -m all` on the core resolves to roughly a
dozen modules instead of two hundred.

Each module is independently buildable through its committed `replace` directives
(pointing at the in-tree core), so CI validates each in isolation with
`GOWORK=off` — exactly as a real `go get` consumer would pull it. For convenience,
a repo-root `go.work` (untracked) can wire all modules together when editing
across them locally; it is optional and never required to build.

The Go floor is 1.25, set by `rueidis`'s transitive requirements rather than by
choice; it is a reasonable minimum for 2026.

## Performance

Hot paths allocate zero times per operation for `string` keys (and any key
whose `KeyString` renders without allocating). Integer keys use an
allocation-light `strconv` path — zero for small magnitudes, at most one
allocation for the rendered key string otherwise; other key types fall back to
`fmt` and should supply `KeyString` for a zero-allocation hot path. See the
performance table in the [README](README.md) and reproduce with `make bench`.
