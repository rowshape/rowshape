# Security Policy

## Reporting a vulnerability

**Please do not open a public issue.** Use GitHub's private reporting:
[Report a vulnerability](https://github.com/rowshape/rowshape/security/advisories/new).

Include what you did, what happened, and what you expected. A fixture, a
migration, or a connection shape that reproduces it is worth more than a
description — and a fixture is safe to attach, which is rather the point of the
format.

You will get an acknowledgement within a few days. If a report is valid, you will
be credited in the advisory unless you would rather not be.

## What counts as a vulnerability here

rowshape connects to databases, so the interesting boundaries are narrower than
"it crashed". These are in scope:

- **Anything that writes to a database rowshape promised not to write to.**
  `pull` is read-only through catalog views. `validate` and `hydrate` refuse to
  touch the host a fixture was pulled from. A way around either refusal is the
  most serious class of bug this project has.
- **A fixture that leaks more than the privacy level admits.** The claim is
  exact: a fixture contains no rows, only statistics computed from them; at
  `--privacy standard` some reveal the extremes of numeric and date columns; at
  `--privacy strict` none do. A field carrying a real value that
  `rowshape inspect --leaks` does not report is a vulnerability, not a bug.
- **A credential reaching a log, an error message, or a fixture.** Connection
  strings are never printed, and errors are sanitized to rowshape's own
  classification rather than the driver's text.
- **SQL injection through a fixture or a migration path**, including identifiers
  taken from a fixture and interpolated into DDL.
- **Supply chain:** a released artifact that does not match its checksum, its
  SBOM, or its cosign signature.

## What does not count

- **A wrong verdict is a bug, not a vulnerability** — file it as an issue. That
  includes a `PASS` that should have been a `FAIL`. It matters a great deal, and
  the corpus exists to catch it, but it is not a security boundary.
- Anything requiring an attacker who already has the credentials you handed
  rowshape. It has exactly the access you gave it.
- Findings against a database you do not have authorization to test.

## Verifying a release

Every release publishes an SBOM per archive and a
[cosign](https://github.com/sigstore/cosign) signature over the checksums file,
signed keyless through CI's OIDC identity — from the first release, not
retrofitted later.

```sh
cosign verify-blob \
  --certificate      checksums.txt.pem \
  --signature        checksums.txt.sig \
  --certificate-identity-regexp   'https://github.com/rowshape/rowshape/.*' \
  --certificate-oidc-issuer       'https://token.actions.githubusercontent.com' \
  checksums.txt

sha256sum --check checksums.txt --ignore-missing
```

The npm wrapper verifies the checksum of the archive it downloads and **fails
closed**: a missing entry in `checksums.txt` is a refusal, not a warning.

## Supported versions

Fixes land on the latest release. Until v1.0.0, that is the only supported
version.

rowshape supports PostgreSQL 10 through the current release, and the full corpus
runs against every one of them on every push — a rule that is right on one major
can be wrong on another.
