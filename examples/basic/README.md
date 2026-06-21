# examples/basic

The smallest Nimbus example: an **L1-only** cache with stampede protection. No
Redis, no Pub/Sub, no Docker — it runs with nothing but Go, so it is the quickest
way to see the read-through API.

It builds a cache over a slow loader and calls `GetOrLoad` for the same key
three times: the first call invokes the loader, the rest are served from the
in-process L1. The final `Stats()` line shows one miss/load and two hits.

This example is part of the core module (no external dependencies), so run it
from this directory:

```sh
cd examples/basic
go run .
```

Expected output (the value three times, then the counters):

```
value-for-user:42
value-for-user:42
value-for-user:42
stats: {Hits:2 ... Misses:1 Loads:1 ...}
```

From here:

- Add a shared, authoritative tier and cross-instance coherence with a Redis L2
  and a Redis Pub/Sub bus — see [`examples/redisbus`](../redisbus) (GCP-free) or
  [`demo/local`](../../demo/local) (docker compose).
- Deploy on Cloud Run with Memorystore + Pub/Sub — see
  [`examples/cloudrun`](../cloudrun).
