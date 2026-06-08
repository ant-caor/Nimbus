# CLAUDE.md

Guidance for Claude Code when working in this repository.

## Project

**Nimbus** is a Cloud Run-first cache for Go. It combines a fast in-process **L1**,
a shared versioned **L2** (Redis, the source of truth), and a **Pub/Sub invalidation
bus** that keeps per-instance caches coherent across ephemeral, autoscaling Cloud Run
instances.

- Module: `github.com/ant-caor/nimbus` (Go 1.25)
- Root package: `nimbus`
- Status: pre-v0.1; APIs may still change.

The correctness backbone is the **fill invariant**: no value enters L1 except stamped
with a version minted by L2, decided atomically against concurrent invalidations. The
bus is a latency optimization, not the sole coherence mechanism — an instance that
misses a broadcast still converges on its next L2 read. See `DESIGN.md` for the full
coherence protocol.

## Layout

| Path | What |
|---|---|
| `*.go` (root) | the `nimbus` package: `builder.go`, `cache.go`, `loader.go`, `errors.go`, `doc.go` |
| `store/`, `store/memory/` | the `Store[V]` interface and the hand-written sharded LRU+TTL L1 |
| `redisstore/` | the versioned Redis L2 (over rueidis) |
| `invalidation/`, `invalidation/gcppubsub/` | the invalidation bus (in-process `Mem` + GCP Pub/Sub pull/push) |
| `refresh/` | stale-while-revalidate refresh scheduling |
| `metrics/` | OpenTelemetry metrics (async; core stays OTel-free) |
| `internal/clock`, `internal/singleflight` | injectable clock; stampede collapse |
| `examples/basic`, `examples/cloudrun` | runnable examples (cloudrun has Dockerfile + Terraform) |
| `demo/local` | two instances + Redis via docker compose |
| `test/integration/` | **separate module** — testcontainers (Redis + Pub/Sub emulator) |

## Commands

```sh
make race         # unit tests with the race detector (primary check)
make test         # unit tests
make bench        # benchmarks with allocation stats
make lint         # golangci-lint
make fmt          # gofmt -w .
make tidy         # go mod tidy for both modules
make integration  # Redis + Pub/Sub emulator via testcontainers (needs Docker)
```

The integration suite lives in its own module, so `go test ./...` at the root runs
only unit tests; integration runs from `test/integration/`.

## Conventions

- **Brand vs identifier:** write the brand as **Nimbus** in prose/titles, but keep
  lowercase `nimbus` for the package name, import paths, identifiers, and metric names
  (e.g. `nimbus.hits`). Package doc comments follow godoc form (`Package nimbus ...`).
- **Dependency hygiene:** the library module depends only on `rueidis` and
  `golang.org/x/sync`; the GCP Pub/Sub client is pulled only via
  `invalidation/gcppubsub`. Keep the testcontainers/testify tree confined to
  `test/integration/` so it never reaches the library's dependents.
- **String-keyed internals:** everything below the public API is string-keyed; the
  user's key type `K` lives only on `Cache[K, V]` and is mapped to a string at the
  boundary, so eviction-by-key is consistent across L1, L2, and the bus.
- **Bar for changes:** keep the build green under `go test -race ./...`, the
  integration module, `gofmt`, and `golangci-lint`. New behavior gets unit tests; new
  backends/transports get an integration test; hot-path changes update a benchmark.
- Hot paths allocate zero times per operation — preserve that.
