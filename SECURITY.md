# Security Policy

## Supported versions

Nimbus is pre-1.0. Fixes are released against the latest published minor
version. Once 1.0 ships, this section will list the supported version range.

| Version | Supported |
|---|---|
| latest minor (0.x) | ✅ |
| older | ❌ |

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues,
discussions, or pull requests.**

Instead, use one of these private channels:

1. **GitHub Security Advisories (preferred):** open a private report via the
   repository's **Security → Report a vulnerability** tab
   ([Privately reporting a security vulnerability](https://docs.github.com/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)).
2. **Email:** contact@antoniocabezas.com.

Please include:

- the affected version or commit,
- a description of the issue and its impact,
- steps to reproduce or a proof of concept, if possible.

## What to expect

- **Acknowledgement** within a few business days.
- An assessment of severity and affected versions.
- Coordinated disclosure: we will agree on a timeline, prepare a fix, publish a
  patched release and a GitHub Security Advisory (with a CVE where appropriate),
  and credit the reporter unless they prefer to remain anonymous.

## Scope

This policy covers the Nimbus library modules in this repository. Vulnerabilities
in third-party dependencies should be reported upstream; if a dependency issue
affects Nimbus users, we will bump the dependency and note it in the changelog.

The dependency graph is scanned with [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck)
in CI against the [Go vulnerability database](https://pkg.go.dev/vuln/).
