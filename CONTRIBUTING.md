# Contributing to Nimbus

Thanks for your interest. Nimbus is young (pre-1.0); issues and PRs are welcome.

## Code of Conduct

This project is governed by the [Code of Conduct](CODE_OF_CONDUCT.md). By
participating, you are expected to uphold it. Report unacceptable behavior to
contact@antoniocabezas.com.

## Before you start

- **Bugs and features:** open an issue using the
  [templates](.github/ISSUE_TEMPLATE). For notable changes (public API, new
  packages, breaking changes) please open an issue or discussion first so the
  design is visible before code is written — see [GOVERNANCE.md](GOVERNANCE.md).
- **Security issues:** do not open a public issue; follow [SECURITY.md](SECURITY.md).

## Layout

- The library lives in the root module (`github.com/ant-caor/nimbus`). Its only
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

Public API changes must also update the docs (godoc / README) in the same PR.

## Changelog

This project keeps a changelog in [`CHANGELOG.md`](CHANGELOG.md) following
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), paired with
[Semantic Versioning](https://semver.org/). This has been the convention since the
first release (`v0.1.0`).

- Add an entry for every **user-facing** change under the top
  **`## [Unreleased]`** section, grouped by type — **Added**, **Changed**,
  **Deprecated**, **Removed**, **Fixed**, **Security**. Only include the
  subsections that actually have entries.
- Write **for humans**: one entry per notable change, describing the impact (not a
  raw commit dump), and name the relevant exported symbols where it helps.
- **Internal-only** changes (tests, CI, refactors with no observable effect) do
  not need an entry. CI enforces this: a PR that changes Go source or a `go.mod`
  must touch `CHANGELOG.md`, or carry the **`skip-changelog`** label for an
  internal-only change.
- On release, the `[Unreleased]` entries move under a dated
  `## [X.Y.Z] - YYYY-MM-DD` heading and the compare links at the bottom are
  updated — see [RELEASING.md](RELEASING.md).

## Developer Certificate of Origin (sign-off)

Contributions are accepted under the [Developer Certificate of Origin](https://developercertificate.org/):
by signing off, you certify you wrote the change or otherwise have the right to
submit it under the project's license. Add a sign-off line to every commit:

```sh
git commit -s -m "your message"
```

This appends `Signed-off-by: Your Name <your@email>` using your `git config`
`user.name` and `user.email`. PRs whose commits are not signed off may be asked
to amend.

