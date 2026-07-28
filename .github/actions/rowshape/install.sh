#!/usr/bin/env bash
# Download the released rowshape binary for this runner and expose it on PATH so
# the run step can invoke `rowshape validate`.
#
# RELEASE-GATED (P0-T4): this cannot be exercised end-to-end until a tagged
# release publishes assets. To keep the release-gated part from being wholly
# unverified, the archive-name computation is factored into rowshape_asset_name()
# and mirrors .goreleaser.yaml archives.name_template and npm/install.js EXACTLY
# — raw lowercase GOOS/GOARCH, windows -> .zip — and is unit-tested for all six
# published platform/arch combos in test/action (TestInstallAssetNaming), the
# same way npm/naming.test.js guards install.js.
#
# The downloaded archive is verified before it is ever executed (CR2-T1): its
# SHA-256 must match the release checksums.txt, and when cosign is available the
# keyless signature over checksums.txt is checked first. The verification
# helpers are factored out the same way the naming is, and are unit-tested
# against a tampered archive in TestInstallChecksumVerification.
#
# Sourcing with ROWSHAPE_INSTALL_SOURCE_ONLY=1 defines the helpers without
# performing any network I/O, so the naming can be checked in isolation.
set -u

rowshape_os() { # uname -s -> goreleaser GOOS
  case "$1" in
    Linux) echo linux ;;
    Darwin) echo darwin ;;
    MINGW* | MSYS* | CYGWIN* | Windows_NT) echo windows ;;
    *) echo "" ;;
  esac
}

rowshape_arch() { # uname -m -> goreleaser GOARCH
  case "$1" in
    x86_64 | amd64) echo amd64 ;;
    arm64 | aarch64) echo arm64 ;;
    *) echo "" ;;
  esac
}

rowshape_asset_name() { # version os arch -> archive filename
  local v="$1" os="$2" arch="$3" ext=tar.gz
  [ "$os" = windows ] && ext=zip
  printf 'rowshape_%s_%s_%s.%s' "$v" "$os" "$arch" "$ext"
}

# rowshape_sha256 <file> -> lowercase hex digest, or empty if no tool is available.
# Runner images differ: Linux has sha256sum, macOS ships shasum, and Git for
# Windows provides sha256sum. openssl is the last resort.
rowshape_sha256() {
  local f="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$f" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$f" | awk '{print $1}'
  elif command -v openssl >/dev/null 2>&1; then
    openssl dgst -sha256 "$f" | awk '{print $NF}'
  else
    echo ""
  fi
}

# rowshape_expected_sum <checksums-file> <asset-name> -> the recorded digest.
#
# checksums.txt is `<hex>  <filename>` per line. The filename is matched as a
# whole field rather than with a substring test, so `rowshape_1.0.0_linux_amd64
# .tar.gz` cannot be satisfied by a line for a different asset that merely
# contains it as a prefix.
rowshape_expected_sum() {
  local file="$1" asset="$2"
  awk -v want="$asset" '$2 == want || $2 == "*" want {print $1; exit}' "$file"
}

# rowshape_verify_checksum <archive> <checksums-file> <asset> -> 0 ok, 1 not ok.
# Prints the reason on failure. Fails CLOSED: a missing tool, a missing entry,
# and a mismatch are all refusals, never a warning-and-continue.
rowshape_verify_checksum() {
  local archive="$1" sums="$2" asset="$3"
  local want got
  want=$(rowshape_expected_sum "$sums" "$asset")
  if [ -z "$want" ]; then
    echo "rowshape: ${asset} has no entry in checksums.txt" >&2
    return 1
  fi
  got=$(rowshape_sha256 "$archive")
  if [ -z "$got" ]; then
    echo "rowshape: no SHA-256 tool available (need sha256sum, shasum, or openssl)" >&2
    return 1
  fi
  if [ "$want" != "$got" ]; then
    echo "rowshape: checksum mismatch for ${asset}" >&2
    echo "rowshape:   expected ${want}" >&2
    echo "rowshape:   actual   ${got}" >&2
    return 1
  fi
  return 0
}

if [ "${ROWSHAPE_INSTALL_SOURCE_ONLY:-}" = "1" ]; then
  return 0 2>/dev/null || exit 0
fi

REPO="${INPUT_REPO:-rowshape/rowshape}"
OS=$(rowshape_os "$(uname -s)")
ARCH=$(rowshape_arch "$(uname -m)")
if [ -z "$OS" ] || [ -z "$ARCH" ]; then
  echo "rowshape: unsupported runner $(uname -s)/$(uname -m)" >&2
  echo "rowshape: install a binary from https://github.com/${REPO}/releases and set the 'binary' input" >&2
  exit 1
fi
# windows/arm64 used to be excluded from the release and refused here; it is now
# built (.goreleaser.yaml), so every platform this script resolves has an asset.

raw="${INPUT_VERSION:-latest}"
if [ "$raw" = latest ] || [ -z "$raw" ]; then
  # Resolve the latest tag from the redirect target of /releases/latest.
  tag=$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/${REPO}/releases/latest" | sed 's#.*/tag/##')
  if [ -z "$tag" ]; then
    echo "rowshape: could not resolve the latest release tag for ${REPO}" >&2
    exit 1
  fi
else
  tag="$raw"
fi
# The tag carries a leading v (v1.2.3); goreleaser's {{ .Version }} strips it.
case "$tag" in
  v*) version="${tag#v}" ;;
  *) version="$tag"; tag="v${tag}" ;;
esac

asset=$(rowshape_asset_name "$version" "$OS" "$ARCH")
url="https://github.com/${REPO}/releases/download/${tag}/${asset}"

workdir=$(mktemp -d)
echo "rowshape: downloading ${url}" >&2
if ! curl -fsSL -o "${workdir}/${asset}" "$url"; then
  echo "rowshape: download failed for ${url}" >&2
  exit 1
fi

# --- Verify before executing (CR2-T1) -------------------------------------
# The release publishes checksums.txt plus a cosign keyless signature over it,
# and the docs tell users to verify. This installer used to download an archive
# and execute it with no verification at all — on the CI path, which runs with
# repository credentials in scope. Verification is mandatory and fails closed:
# `verify: false` is the only way to skip it, and it is not the default.
base="https://github.com/${REPO}/releases/download/${tag}"
if [ "${INPUT_VERIFY:-true}" = "false" ]; then
  echo "rowshape: WARNING - signature and checksum verification disabled by input" >&2
else
  if ! curl -fsSL -o "${workdir}/checksums.txt" "${base}/checksums.txt"; then
    echo "rowshape: could not download checksums.txt for ${tag}" >&2
    echo "rowshape: set verify:false only if you accept an unverified binary" >&2
    exit 1
  fi

  # cosign proves WHO produced checksums.txt; the checksum proves the archive
  # matches it. Verify the signature first so a forged checksums.txt cannot
  # validate a forged archive. Keyless: the certificate identity is the release
  # workflow, bound to the GitHub Actions OIDC issuer.
  want_cosign="${INPUT_VERIFY_SIGNATURE:-auto}"
  if [ "$want_cosign" != "false" ]; then
    if command -v cosign >/dev/null 2>&1; then
      if curl -fsSL -o "${workdir}/checksums.txt.sig" "${base}/checksums.txt.sig" &&
        curl -fsSL -o "${workdir}/checksums.txt.pem" "${base}/checksums.txt.pem"; then
        if cosign verify-blob \
          --certificate "${workdir}/checksums.txt.pem" \
          --signature "${workdir}/checksums.txt.sig" \
          --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
          --certificate-identity-regexp "^https://github.com/${REPO}/\.github/workflows/.+@refs/tags/" \
          "${workdir}/checksums.txt" >/dev/null 2>&1; then
          echo "rowshape: cosign signature verified for checksums.txt" >&2
        else
          echo "rowshape: cosign verification FAILED for checksums.txt" >&2
          exit 1
        fi
      elif [ "$want_cosign" = "true" ]; then
        echo "rowshape: signature assets missing and verify-signature:true was requested" >&2
        exit 1
      else
        echo "rowshape: no signature assets published for ${tag}; continuing with checksum only" >&2
      fi
    elif [ "$want_cosign" = "true" ]; then
      echo "rowshape: cosign not on PATH and verify-signature:true was requested" >&2
      echo "rowshape: add sigstore/cosign-installer before this step" >&2
      exit 1
    else
      echo "rowshape: cosign not on PATH; continuing with checksum only" >&2
    fi
  fi

  if ! rowshape_verify_checksum "${workdir}/${asset}" "${workdir}/checksums.txt" "$asset"; then
    echo "rowshape: refusing to execute an unverified binary" >&2
    exit 1
  fi
  echo "rowshape: checksum verified for ${asset}" >&2
fi

# bsdtar handles zip as well as tar, and ships with Windows 10+ and the runner
# images. `unzip` does NOT ship with Git for Windows' bash, and the zip branch is
# reached only when OS=windows (see the ext= assignment above) — so using unzip
# here broke extraction on the one platform that takes this branch. npm/install.js
# already extracts both formats with tar for exactly this reason; keep the two
# installers in step.
case "$asset" in
  *.zip) tar -xf "${workdir}/${asset}" -C "$workdir" ;;
  *.tar.gz) tar -xzf "${workdir}/${asset}" -C "$workdir" ;;
esac

bin="${workdir}/rowshape"
[ "$OS" = windows ] && bin="${workdir}/rowshape.exe"
if [ ! -f "$bin" ]; then
  echo "rowshape: binary not found in ${asset} after extraction" >&2
  exit 1
fi
[ "$OS" != windows ] && chmod +x "$bin"

# Expose the binary to the run step: on PATH, and pinned via ROWSHAPE_BIN so the
# exact downloaded artifact is used even if another rowshape is on PATH.
if [ -n "${GITHUB_PATH:-}" ]; then echo "$workdir" >>"$GITHUB_PATH"; fi
if [ -n "${GITHUB_ENV:-}" ]; then echo "ROWSHAPE_BIN=$bin" >>"$GITHUB_ENV"; fi
echo "rowshape: installed ${tag} at ${bin}" >&2
