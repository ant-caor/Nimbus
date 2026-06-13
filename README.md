# Nimbus

**Cloud Run-first caching for Go.** A fast in-process L1, a shared versioned L2 (the source of truth), and a Pub/Sub invalidation bus that keeps per-instance caches coherent across ephemeral, autoscaling instances.

[![CI](https://github.com/ant-caor/nimbus/actions/workflows/ci.yml/badge.svg)](https://github.com/ant-caor/nimbus/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/ant-caor/nimbus.svg)](https://pkg.go.dev/github.com/ant-caor/nimbus)
[![Go Report Card](https://goreportcard.com/badge/github.com/ant-caor/nimbus)](https://goreportcard.com/report/github.com/ant-caor/nimbus)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/ant-caor/nimbus/badge)](https://scorecard.dev/viewer/?uri=github.com/ant-caor/nimbus)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.25+-00ADD8.svg)](go.mod)

> **Status: pre-v0.1.** L1, L2 (Redis), stampede protection, stale-while-revalidate, the **cross-instance Pub/Sub bus**, OpenTelemetry metrics, and a deployable Cloud Run example (Dockerfile + Terraform) are all implemented and tested (integration against real Redis and the Pub/Sub emulator via testcontainers). APIs may still change before v0.1.

---

## Why caching on Cloud Run is hard

Naive caching breaks on Cloud Run for three reasons:

1. **Instances are ephemeral and scale to zero.** In-memory caches vanish on scale-down. When traffic spikes from 0 to N, *every* new instance starts cold and stampedes the database at the worst moment.
2. **There is no shared memory between instances.** A request lands on any instance, so in-process caches diverge, and because instances are ephemeral you cannot address them individually to invalidate.
3. **Cost vs. latency is a real tradeoff.** An external cache (Memorystore) gives coherence but adds cost, latency, and a VPC connector; in-memory is free but volatile.

Nimbus handles these tradeoffs so you do not re-implement them in every service.

---

## How it works

Three tiers, one rule: **L2 is the source of truth; L1 is a best-effort per-instance accelerator; the bus is a latency optimization, not the sole coherence mechanism.**

```mermaid
flowchart LR
    R[Request] --> L1[L1 in-process LRU+TTL]
    L1 -- miss --> L2[(L2 Redis / Memorystore<br/>versioned, source of truth)]
    L2 -- miss --> SF[singleflight] --> O[(origin loader)]
    W[Set / Invalidate] --> BUS{{Pub/Sub bus}}
    BUS -.evict L1.-> I2[instance B]
    BUS -.evict L1.-> I3[instance N]
```

The correctness backbone is the **fill invariant**: no value enters L1 except stamped with a version minted by L2, decided atomically against concurrent invalidations (a CAS on write). A value loaded by an in-flight fill is discarded, not cached, if the key was invalidated while the loader ran. An instance that misses a bus broadcast still converges on its next L2 read, because L2 versions are authoritative.

Keys are string-keyed internally (`Store[V]`), with the user's key type `K` living only on the public `Cache[K, V]`, so eviction-by-key is consistent across L1, L2, and the bus.

---

## Quickstart

```go
cache, err := nimbus.NewBuilder[string, User](loadUser).
    L1(memory.New[User]()).               // in-process accelerator
    L2(redisstore.New[User](rdb, store.JSON[User]())). // shared source of truth
    TTL(30*time.Second, 5*time.Minute).   // fresh window + stale-while-revalidate window
    Jitter(0.1).                          // desync expiries
    NegativeTTL(2 * time.Second).         // cache known-absent keys
    Build()
if err != nil {
    log.Fatal(err)
}
defer cache.Close()

// Read-through with stampede protection: concurrent misses collapse to one load.
v, err := cache.GetOrLoad(ctx, "user:42")
switch {
case errors.Is(err, nimbus.ErrNotFound): // known-absent (negative hit)
case err != nil:                           // transient failure (not cached)
default:                                    // use v
}
```

The loader signals a missing key by returning `nimbus.ErrNotFound`, which triggers negative caching. The builder names the types once; every option is a plain method.

---

## Caching techniques

| Technique | Solves |
|---|---|
| Multi-tier L1/L2 | speed of in-process + coherence of a shared store |
| Singleflight | cold-start thundering herd on the origin |
| Stale-while-revalidate | tail latency and backend resilience |
| TTL + jitter | cache avalanche from synchronized expiry |
| Negative caching | DB pressure from missing-key lookups |
| Versioned fill invariant | the fill-after-invalidate stale-read race |
| Cross-instance invalidation (Pub/Sub) | coherence across ephemeral replicas |

---

## Performance

Benchmarks (`go test -bench=. -benchmem`). Hot paths allocate **zero** times per operation for `string` keys (and any key type whose `KeyString` renders without allocating). Integer keys take an allocation-light `strconv` path — zero allocations for small magnitudes, at most one for the rendered key string otherwise — while other key types fall back to `fmt`; supply `KeyString` to keep them zero-alloc. Numbers below are from an Apple-class CPU running the `darwin/amd64` toolchain under emulation, so treat them as indicative and relative, not absolute.

| Operation | Latency | Allocations |
|---|---:|---:|
| L1 `Get` (hit) | ~95 ns/op | 0 B, 0 allocs |
| L1 `Set` | ~47 ns/op | 0 B, 0 allocs |
| L1 `Get` (parallel) | ~115 ns/op | 0 B, 0 allocs |
| `GetOrLoad` (L1 hit) | ~181 ns/op | 0 B, 0 allocs |
| `GetOrLoad` (L1 hit, parallel) | ~307 ns/op | 0 B, 0 allocs |
| `Get` (L1 hit) | ~179 ns/op | 0 B, 0 allocs |
| Invalidation dedupe (`Seen`) | ~22 ns/op | 0 B, 0 allocs |
| In-process bus publish (`Mem`) | ~185 ns/op | 0 B, 0 allocs |

The ~85 ns gap between a raw L1 `Get` and `GetOrLoad` is the key-string mapping, freshness check, and stats accounting on the hot path. L2 (Redis) reads and the `gcppubsub` bus are dominated by network and Redis/Pub/Sub round-trips, not CPU, so they are exercised in the integration suite rather than micro-benchmarked.

Reproduce with:

```sh
go test -run='^$' -bench=. -benchmem ./...
```

---

## Observability

The `metrics` adapter exports cache statistics as OpenTelemetry metrics. It ships
as a **separate module** so the core carries no OpenTelemetry dependency — add it
only if you want OTel:

```sh
go get github.com/ant-caor/nimbus/metrics
```

It observes `Stats()` through asynchronous instruments, so it adds nothing to the
hot path:

```go
reg, err := metrics.Register(meter, cache) // meter is an otel metric.Meter
defer reg.Unregister()
```

It reports `nimbus.hits`, `.stale_hits`, `.misses`, `.loads`, `.load_errors`,
`.negative_hits`, `.refreshes`, `.bus_evicts`, `.evictions`, and `.l1.entries`.

## Try it locally

`demo/local/` brings up two cache instances + Redis in Docker so you can watch
L1/L2 sharing on your laptop (see `demo/local/README.md`). For the full
cross-instance bus over Pub/Sub deployed to Cloud Run via Terraform, see
`examples/cloudrun/` (Cloud Run + Memorystore + Pub/Sub + Direct VPC Egress).

---

## Invalidation transports

The bus is an interface (`invalidation.Bus`), so the transport is pluggable. The
in-process and Redis transports live in the core module; the GCP transport is a
**separate module** (`go get github.com/ant-caor/nimbus/invalidation/gcppubsub`)
so the core never pulls the GCP/gRPC tree:

| Transport | Package | Module | Notes |
|---|---|---|---|
| GCP Pub/Sub (pull) | `invalidation/gcppubsub` | separate | true fan-out; needs always-on CPU |
| GCP Pub/Sub (push) | `invalidation/gcppubsub` | separate | load-balanced; throttle-safe for request-only-CPU Cloud Run |
| Redis Pub/Sub | `invalidation/redispubsub` | core | reuses the Redis client your L2 already holds — no extra infra, no GCP dependency |
| In-process | `invalidation.Mem` | core | single-process fan-out for tests |

The Redis Pub/Sub bus + Redis L2 is a fully **GCP-free, cloud-agnostic** coherence
stack: it runs on any cloud or on-prem wherever Redis is reachable.

```go
bus := redispubsub.New(rdb, "myapp:invalidations") // rdb is your rueidis.Client
cache, err := nimbus.NewBuilder[string, User](loadUser).
    L2(redisstore.New[User](rdb, store.JSON[User]())).
    Bus(bus).
    Build()
```

---

## When *not* to use this

- A single long-lived instance with plenty of memory: a plain in-process cache (Ristretto, Otter) is simpler.
- Reads that must be strongly consistent: Nimbus is eventually consistent across instances by design.
- Near-100% write workloads: the machinery is not worth it.

Nimbus is for read-heavy, high-traffic services on autoscaling Cloud Run where cold starts and cross-instance coherence actually bite.

---

## Design notes

The in-memory cache space in Go is well served by [Ristretto](https://github.com/dgraph-io/ristretto) and [Otter](https://github.com/maypok86/otter). Nimbus does not try to out-compete them on raw eviction; its value is the **serverless orchestration layer**: tiering, the versioned fill invariant, cross-instance coherence, and a deployable reference. The L1 is hand-written behind the `store.Store` interface, so a faster engine can be dropped in later.

The core module `github.com/ant-caor/nimbus` depends on only `rueidis` and `golang.org/x/sync`. Provider- and OTel-specific code is isolated into its own modules — `invalidation/gcppubsub` (GCP), `metrics` (OpenTelemetry), and the testcontainers tree under `test/integration/` — so a dependent never pulls those graphs unless it imports them. `go list -m all` on the core resolves to roughly a dozen modules rather than two hundred.

A deeper write-up of the coherence protocol lives in [`DESIGN.md`](DESIGN.md).

---

## Versioning and compatibility

Nimbus follows [Semantic Versioning](https://semver.org/). **While the project is
pre-1.0, minor releases (`0.x`) may contain breaking changes; patch releases
never do.** Breaking changes are called out in [`CHANGELOG.md`](CHANGELOG.md) with
a migration note. After 1.0, the public API of the root package follows the
standard Go compatibility promise within a major version.

- **Supported Go:** the latest 1.25 patch. The floor (Go 1.25) is set by a
  transitive `rueidis` requirement, not by choice.
- **Module path:** `github.com/ant-caor/nimbus`. A future v2+ would use the
  `/v2` import-path suffix per [Go module versioning](https://go.dev/ref/mod#major-version-suffixes).
- **Scope of the promise:** exported symbols of the root package and documented
  subpackages. Anything under `internal/` is not part of the public API.

The release process is documented in [`RELEASING.md`](RELEASING.md), and security
reporting in [`SECURITY.md`](SECURITY.md).

---

## License

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
