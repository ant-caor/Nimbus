# Releasing

Nimbus is a Go library: a release **is** a semver git tag. There is no binary to
build, so the [`release.yml`](.github/workflows/release.yml) workflow only runs
the test suite as a gate and publishes a GitHub Release with generated notes; the
module proxy (`proxy.golang.org`) and checksum database (`sum.golang.org`) pick
up the version on the first `go get`.

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

5. The `Release` workflow runs `go test -race ./...` and, on success, creates the
   GitHub Release. Verify the module is fetchable:

   ```sh
   go install github.com/ant-caor/nimbus@v0.1.0   # populates the proxy/sum DB
   ```

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
