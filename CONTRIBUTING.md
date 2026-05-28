# Contributing to runcache

Thanks for your interest. runcache is in early development; issues and PRs are welcome.

## Layout

- The library lives in the root module (`github.com/ant-caor/runcache`). Its only
  runtime dependencies are `rueidis` (for the Redis L2) and `golang.org/x/sync`.
- Integration tests live in a **separate module** under `test/integration/` so the
  testcontainers / Pub/Sub-emulator dependency tree never reaches the library's
  dependents.

## Development

```sh
make fmt        # gofmt
make race       # unit tests with the race detector
make lint       # golangci-lint
make bench      # benchmarks with allocation stats
make integration  # Redis + Pub/Sub emulator via testcontainers (needs Docker)
```

## Bar for changes

Every change keeps the build green under:

- `go test -race ./...` (unit),
- `go test ./...` in `test/integration/` (integration + end-to-end, Docker required),
- `gofmt` and `golangci-lint`.

New behavior comes with unit tests; new backends or transports come with an
integration test. Hot-path changes should include or update a benchmark.
