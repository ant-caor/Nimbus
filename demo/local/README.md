# runcache local demo

Two cache instances (`svc-a`, `svc-b`) sharing one Redis, the way multiple Cloud
Run instances share one Memorystore. Use it to see the L1/L2 behavior on your
laptop.

## Run

```sh
cd demo/local
docker compose up --build
```

- `svc-a` -> http://localhost:8081
- `svc-b` -> http://localhost:8082

## What to try

```sh
# First read on A is slow (~200ms): the origin loader runs, result lands in L2.
curl "http://localhost:8081/get?key=hello"

# Read the same key on B: fast, and the value was loaded by A, proving B
# promoted it from the shared L2 instead of re-hitting the origin.
curl "http://localhost:8082/get?key=hello"

# Write a new value on A (lands in L2, the source of truth)...
curl -X POST "http://localhost:8081/set?key=hello&value=world"

# ...a cold read (new key, or after L1 expiry) sees the latest from L2.
curl "http://localhost:8082/get?key=hello"

# Per-instance counters (hits, misses, loads, refreshes, evictions):
curl "http://localhost:8081/stats"
```

When the Pub/Sub invalidation bus lands, this compose file will also start the
Pub/Sub emulator, and `POST /invalidate` on one instance will evict the entry on
the other within milliseconds. Until then, a non-receiving instance converges on
its next L2 read (bounded by its L1 fresh TTL).

## Why docker compose and not Terraform here

This is a local development loop, so `docker compose` is the right tool: it
stands up disposable containers on your machine in one command. Terraform is for
provisioning real cloud infrastructure and lives with the deployable Cloud Run
example under `examples/cloudrun/` (Cloud Run + Memorystore + Pub/Sub), not here.
