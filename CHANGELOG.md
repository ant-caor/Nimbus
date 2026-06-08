# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
While the project is **pre-1.0, minor releases may contain breaking changes**;
patch releases never do.

## [Unreleased]

### Added

- Core cache: a generic `Cache[K, V]` with a fluent `Builder`, read-through
  `GetOrLoad`, `Get`, `Set`, `Invalidate`, `InvalidateTag`, `Stats`, and `Close`.
- Multi-tier storage: an in-process L1 (`store/memory`, a sharded LRU+TTL) behind
  the `store.Store` interface, and a versioned Redis L2 (`redisstore`) as the
  authoritative source of truth.
- Stampede protection via singleflight: concurrent misses collapse to one load.
- Stale-while-revalidate with request-bound (default) and background refresh
  modes, TTL jitter, `MaxTTL`, and negative caching.
- The versioned **fill invariant** (CAS on write) that closes the
  fill-after-invalidate stale-read race.
- Cross-instance invalidation bus (`invalidation.Bus`) with transports:
  in-process (`invalidation.Mem`), GCP Pub/Sub pull and push
  (`invalidation/gcppubsub`), and Redis Pub/Sub (`invalidation/redispubsub`).
- Tag-based invalidation (`WithTags`, `InvalidateTag`).
- OpenTelemetry metrics subpackage (`metrics`) using asynchronous instruments,
  keeping the core OTel-free.
- Examples: an L1-only `examples/basic`, a deployable `examples/cloudrun`
  (distroless Dockerfile + Terraform + OIDC push), and a local `demo/local`
  (docker compose with Redis and the Pub/Sub emulator).
- Documentation: `README.md`, a design write-up in `DESIGN.md`, and `CLAUDE.md`.
- Integration test suite (separate module) running against real Redis and the
  Pub/Sub emulator via testcontainers.

[Unreleased]: https://github.com/ant-caor/nimbus/commits/main
