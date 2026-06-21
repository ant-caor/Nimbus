# Releasing

Nimbus is a Go library: a release **is** a semver git tag. There is no binary to
build, so the [`release.yml`](.github/workflows/release.yml) workflow only runs
the test suite as a gate and publishes a GitHub Release with generated notes; the
module proxy (`proxy.golang.org`) and checksum database (`sum.golang.org`) pick
up the version on the first `go get`.

## Modules in this repo

This repo hosts several modules. Each releases independently and its tag is
**prefixed with the module's directory** (Go's requirement for a module not at
the repo root):

| Module | Tag format | Published? |
|---|---|---|
| `github.com/ant-caor/nimbus` (core) | `vX.Y.Z` | yes |
| `github.com/ant-caor/nimbus/metrics` | `metrics/vX.Y.Z` | yes |
| `github.com/ant-caor/nimbus/invalidation/gcppubsub` | `invalidation/gcppubsub/vX.Y.Z` | yes |
| `examples/cloudrun`, `examples/redisbus`, `demo/local` | — | no (package main) |
| `test/integration` | — | no (test infra) |

The sub-modules **import the core**, so always **release the core first**: a
sub-module pinned to `github.com/ant-caor/nimbus vX.Y.Z` cannot resolve until
that root tag is on the proxy. Their `0.x` lines are independent and may drift
from the core's — that is expected for a multi-module repo.

Before tagging a sub-module, replace its dev `require github.com/ant-caor/nimbus
v0.0.0` + local `replace` with a `require` on the just-released core version
(`go mod edit -dropreplace=github.com/ant-caor/nimbus -require=github.com/ant-caor/nimbus@vX.Y.Z`,
then `GOWORK=off go mod tidy`). The `examples/*` and `demo/local` modules are
never published, so their `replace ../..` directives stay.

## Cutting a release

1. Ensure `main` is green and `CHANGELOG.md` has the changes under `[Unreleased]`.
2. Move the `[Unreleased]` entries under a new `## [X.Y.Z] - YYYY-MM-DD` heading
   and update the comparison links at the bottom.
3. Pick the version per [SemVer](https://semver.org/). **Pre-1.0, a minor bump
   may break the API; a patch bump never does.** Commit the changelog.
4. Tag and push. Signing the tag is recommended:

   ```sh
   git tag -s v0.1.0 -m "v0.1.0"   # -s signs the tag; use -a if you have no key
   git push origin v0.1.0
   ```

5. The `Release` workflow detects the module from the tag, runs `go test -race
   ./...` in it and, on success, creates the GitHub Release. Verify the module is
   fetchable:

   ```sh
   go install github.com/ant-caor/nimbus@v0.1.0   # populates the proxy/sum DB
   ```

### Releasing a sub-module (`metrics`, `invalidation/gcppubsub`)

Do this **after** the core release it depends on is published.

1. Point the sub-module at the published core version and drop the dev replace:

   ```sh
   cd metrics   # or invalidation/gcppubsub
   go mod edit -dropreplace=github.com/ant-caor/nimbus \
               -require=github.com/ant-caor/nimbus@v0.1.0
   GOWORK=off go mod tidy && GOWORK=off go test ./...
   ```

2. Commit, then tag with the **directory-prefixed** version and push:

   ```sh
   git tag -s metrics/v0.1.0 -m "metrics/v0.1.0"
   git push origin metrics/v0.1.0
   ```

3. Verify: `go install github.com/ant-caor/nimbus/metrics@v0.1.0`.

The `examples/*` and `demo/local` modules are `package main` and are never tagged;
they keep their `replace ../..` directives so a fresh checkout builds them.

## Supply-chain integrity

- **Module integrity** is provided by the Go checksum database (`sum.golang.org`)
  and recorded in consumers' `go.sum` — the standard mechanism for Go libraries.
- **Signed tags** (`git tag -s`) let consumers verify authorship of a release.
- **SLSA provenance / artifact signing** (cosign, etc.) applies to *binary*
  release artifacts. Nimbus ships none — the source at a verified tag is the
  artifact — so there is nothing to attest beyond the checksum DB and the tag.

## Optional badges / registrations (maintainer, one-time)

- **OpenSSF Scorecard** runs automatically (see `scorecard.yml`); the badge
  populates after the first run on `main`.
- **OpenSSF Best Practices**: register the project at
  <https://www.bestpractices.dev/> to earn that badge.
- **Codecov**: add the repo on <https://codecov.io/> and set the `CODECOV_TOKEN`
  repository secret so CI can upload coverage.
- **CodeQL**: enable code scanning (Settings → Code security → CodeQL analysis →
  Default setup) to add static analysis with no workflow file to maintain.
