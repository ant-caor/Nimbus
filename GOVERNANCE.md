# Governance

Nimbus is currently maintained by a single primary maintainer (see
[MAINTAINERS.md](MAINTAINERS.md)). This document describes how the project is
run so the process is transparent as it grows.

## Decision making

- **Day-to-day changes** (bug fixes, docs, tests, dependency bumps) are decided
  by maintainer review on a pull request.
- **Notable changes** (public API, new packages, breaking changes, removing
  features) should start as a GitHub issue or discussion so the design and
  trade-offs are visible before code is written.
- The maintainers aim for consensus. When consensus cannot be reached, the
  primary maintainer makes the final call, documenting the rationale.

## Compatibility and releases

- Versioning follows [Semantic Versioning](https://semver.org/). While the
  project is pre-1.0, **minor releases may contain breaking changes**; patch
  releases never do. The compatibility contract is documented in the
  [README](README.md) and tracked in [CHANGELOG.md](CHANGELOG.md).
- A change to the public API requires a CHANGELOG entry and, if breaking before
  1.0, a clear migration note.

## Code of Conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md). The
maintainers are responsible for enforcing it.

## Security

Security issues follow the process in [SECURITY.md](SECURITY.md); they are
handled privately until a fix is released.
