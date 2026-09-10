---
title: Install
description: 'Install the rowshape CLI — a single static binary with no runtime — via Homebrew, go install, npx, a direct download, or Docker in CI.'
sidebar:
  order: 1
---

rowshape is a single static Go binary. There is no runtime to install and no
services to run. Pick whichever channel fits your environment — they all deliver
the same binary.

## Homebrew (macOS, Linux)

```sh
brew install rowshape/tap/rowshape
rowshape --help
```

## go install

```sh
go install github.com/rowshape/rowshape@latest
```

Installs into `$(go env GOPATH)/bin`; make sure that is on your `PATH`.

## npm wrapper (npx)

The npm package is a thin wrapper: on install it downloads the matching native
binary from the GitHub Release, so `npx` runs the real Go binary — not a
reimplementation.

```sh
npx rowshape --help
# or add it to a project:
npm install --save-dev rowshape
```

## Direct download (GitHub Releases)

Every release publishes binaries for macOS, Linux, and Windows on both amd64 and
arm64. Download the archive for your platform, verify it, and drop the binary on
your `PATH`:

```sh
curl -sSL -o rowshape.tar.gz \
  https://github.com/rowshape/rowshape/releases/latest/download/rowshape_<version>_<os>_<arch>.tar.gz
tar -xzf rowshape.tar.gz
./rowshape --help
```

## Docker (CI)

A `FROM scratch` image ships for use in CI pipelines:

```sh
docker run --rm ghcr.io/rowshape/rowshape:latest --help
```

The `rowshape/rowshape` GitHub Action wraps this for you — see the
[GitHub Action guide](../agent/) and the finding catalog for what it reports.

## Supply chain

Every release ships an [SBOM](https://en.wikipedia.org/wiki/Software_supply_chain)
(SPDX, one per archive), a `checksums.txt`, and a cosign keyless signature over
that checksums file.

**You do not have to do this by hand.** The GitHub Action and the npm installer
both verify the archive against `checksums.txt` automatically and refuse to run
a binary that does not match. See the
[GitHub Action guide](https://github.com/rowshape/rowshape/blob/main/docs/action.md)
for the `verify` and `verify-signature` inputs.

To verify a manual download, check the signature over `checksums.txt` first —
it proves who produced the file — then check your archive against it:

```sh
# 1. The signature is keyless, so the certificate identity must be pinned.
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature   checksums.txt.sig \
  --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
  --certificate-identity-regexp "^https://github.com/rowshape/rowshape/\.github/workflows/.+@refs/tags/" \
  checksums.txt

# 2. Now that checksums.txt is trusted, check the archive against it.
sha256sum --ignore-missing -c checksums.txt
```

Order matters: verifying the archive against an *unverified* `checksums.txt`
proves only that the two files agree, which an attacker who replaced both can
arrange. The identity flags are required — without them cosign will not verify a
keyless signature at all.

The binary is a single static executable with a deliberately small dependency
set — half the reason rowshape is written in Go.
