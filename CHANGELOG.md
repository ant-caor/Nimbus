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
  in-process (`invalidation.Mem`) and Redis Pub/Sub (`invalidation/redispubsub`)
  in the core module, plus GCP Pub/Sub pull and push in a **separate module**
  (`github.com/ant-caor/nimbus/invalidation/gcppubsub`).
- Tag-based invalidation (`WithTags`, `InvalidateTag`).
- OpenTelemetry metrics in a **separate module**
  (`github.com/ant-caor/nimbus/metrics`) using asynchronous instruments, so the
  core module carries no OpenTelemetry dependency.
- **Cloud-agnostic dependency layout.** The core module
  `github.com/ant-caor/nimbus` requires only `rueidis` and `golang.org/x/sync`;
  GCP (`invalidation/gcppubsub`) and OpenTelemetry (`metrics`) are opt-in sibling
  modules, so a dependent never pulls the GCP/gRPC/protobuf or OTel trees unless
  it imports them. Import paths are unchanged; using GCP or OTel now adds a
  `go get` of the respective module. The Redis Pub/Sub bus + Redis L2 give a
  fully GCP-free coherence path that runs on any cloud or on-prem.
- Examples: an L1-only `examples/basic` (in the core module), a deployable
  `examples/cloudrun` (distroless Dockerfile + Terraform + OIDC push), and a
  local `demo/local` (docker compose with Redis and the Pub/Sub emulator); the
  two GCP-using examples are their own modules and are never published.
- Documentation: `README.md` and a design write-up in `DESIGN.md`.
- Integration test suite (separate module) running against real Redis and the
  Pub/Sub emulator via testcontainers.
- `store.TombstoneTTLer`, an optional interface a `VersionedStore` may implement
  to report its tombstone lifetime (implemented by `redisstore`). `Build` uses it
  to reject a configuration whose L2 tombstone TTL does not exceed the refresh
  timeout, which would reopen the fill-after-invalidate race.
- `store.ConditionalStore`, an optional interface a `Store` may implement for a
  version-gated install (`SetIfNewer`), implemented by `store/memory`.

### Changed

- The default key renderer now maps integer keys via `strconv` instead of
  `fmt.Sprint`, cutting a large-integer key on the read hot path from two
  allocations to at most one (small magnitudes stay zero-alloc). The
  zero-allocation guarantee is now documented as unconditional for `string` keys
  (and allocation-free `KeyString` codecs); non-string, non-integer keys should
  supply `KeyString` for a zero-allocation hot path.
- L2 per-key versions are now monotonic across **TTL expiry**. Previously the
  version was derived from the live Redis hash and restarted at 1 once the entry
  or its tombstone expired, so the "single monotonic version counter per key"
  guarantee held only within a key's lifetime. The Lua scripts now seed an
  absent key's version from the server clock (`(unixMillis << 10) | seq`) instead
  of zero, so a re-mint after expiry always exceeds any prior version — without a
  second key, a hash tag, or a cross-slot script. **Version values are now large,
  opaque, clock-seeded numbers rather than a 1-based count**; callers must already
  treat `Entry.Version` as opaque-and-monotonic, never assuming it starts at 1.
  Covered by `TestVersionFloorMonotonicAcrossExpiry` and
  `TestVersionFloorMonotonicAcrossTombstoneExpiry`.

### Fixed

- Close a narrow **L1-stomp** window: every L2-minted install into L1 is now
  version-gated (`SetIfNewer`), so a slow fill cannot overwrite a newer entry that
  a concurrent `Set` or a bus-delivered eviction placed in L1 between the fill's
  `SetCAS` and its install. The L1 stays best-effort — a `Store` without
  `ConditionalStore` falls back to an unconditional install, and bus eviction
  stays unconditional. Covered by `TestSetIfNewer` and `TestFillVersionGatesL1Install`.
- Enforce the fill invariant for **negative** entries: a known-absent result is
  now cached only through a version-gated CAS (a tombstone written iff L2 is still
  at the version read before the loader ran). Previously the negative install
  bypassed the CAS, so a value written while a not-found loader was in flight
  could be masked by a stale negative for the whole `NegativeTTL`. Covered by
  `TestNegativeFillInvariantUnderWrite`.

[Unreleased]: https://github.com/ant-caor/nimbus/commits/main
