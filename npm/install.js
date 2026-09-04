// postinstall: download the platform-appropriate rowshape binary from the
// matching GitHub Release into ./bin so `npx rowshape` runs the native binary.
// This is the answer to the "why not pure npm" objection (PRD §7): npm is a
// delivery channel for the single static Go binary, not a reimplementation.
"use strict";

const fs = require("fs");
const path = require("path");
const https = require("https");
const zlib = require("zlib");
const crypto = require("crypto");
const { execSync } = require("child_process");

const REPO = "rowshape/rowshape";
const VERSION = require("./package.json").version;

// Map Node's platform/arch onto goreleaser's archive naming.
//
// These MUST match .goreleaser.yaml's archives.name_template, which is
// `{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}` — raw lowercase GOOS
// and GOARCH. This file previously used the older goreleaser convention
// (title-cased OS, amd64 rewritten to x86_64) and asked for
// `rowshape_1.0.0_Darwin_x86_64.tar.gz` where the release actually publishes
// `rowshape_1.0.0_darwin_amd64.tar.gz`. Every install would have 404'd, and
// nothing could catch it before the first real release.
const PLATFORM = { darwin: "darwin", linux: "linux", win32: "windows" }[process.platform];
const ARCH = { x64: "amd64", arm64: "arm64" }[process.arch];

function fail(msg) {
  console.error(`rowshape: ${msg}`);
  console.error(
    "Install a binary directly from https://github.com/rowshape/rowshape/releases " +
      "or `go install github.com/rowshape/rowshape@latest`."
  );
  process.exit(1);
}

if (!PLATFORM || !ARCH) {
  fail(`unsupported platform ${process.platform}/${process.arch}`);
}
// The release builds 6 combos: darwin/linux/windows on amd64+arm64.
// windows/arm64 used to be excluded and refused here; it is now built
// (.goreleaser.yaml), so every platform this wrapper supports has an asset.

// assetName mirrors .goreleaser.yaml archives.name_template exactly:
//   {{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}
// with the windows format_override to zip.
function assetName(version, platform, arch) {
  const ext = platform === "windows" ? "zip" : "tar.gz";
  return `rowshape_${version}_${platform}_${arch}.${ext}`;
}

const ext = process.platform === "win32" ? "zip" : "tar.gz";
const asset = assetName(VERSION, PLATFORM, ARCH);
const url = `https://github.com/${REPO}/releases/download/v${VERSION}/${asset}`;
const binName = process.platform === "win32" ? "rowshape.exe" : "rowshape";
const binDir = path.join(__dirname, "bin");

function get(u, cb) {
  https
    .get(u, { headers: { "User-Agent": "rowshape-npm-installer" } }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        return get(res.headers.location, cb);
      }
      if (res.statusCode !== 200) {
        return fail(`download failed (${res.statusCode}) for ${u}`);
      }
      cb(res);
    })
    .on("error", (e) => fail(`network error: ${e.message}`));
}

// expectedSum pulls one asset's digest out of a goreleaser checksums.txt, whose
// lines are `<hex>  <filename>`. The filename is compared as a whole field, not
// with a substring test, so an entry for a different asset that happens to
// contain this name cannot satisfy it.
function expectedSum(checksums, asset) {
  for (const line of checksums.split("\n")) {
    const parts = line.trim().split(/\s+/);
    if (parts.length < 2) continue;
    const name = parts[1].replace(/^\*/, ""); // some tools prefix binary mode
    if (name === asset) return parts[0].toLowerCase();
  }
  return null;
}

// verifyChecksum throws unless the archive matches its recorded digest. It fails
// CLOSED: a missing checksums entry is a refusal, not a warning. The release
// publishes checksums.txt and the docs tell users to verify it — this installer
// used to download an archive and execute it having verified nothing at all.
function verifyChecksum(archivePath, checksums, asset) {
  const want = expectedSum(checksums, asset);
  if (!want) throw new Error(`${asset} has no entry in checksums.txt`);
  const got = crypto.createHash("sha256").update(fs.readFileSync(archivePath)).digest("hex");
  if (got !== want) {
    throw new Error(`checksum mismatch for ${asset}\n  expected ${want}\n  actual   ${got}`);
  }
}

// Exported so the naming and verification can be checked against what goreleaser
// actually publishes (npm/naming.test.js). Requiring this file must not download
// anything — the postinstall hook runs it directly.
module.exports = { assetName, expectedSum, verifyChecksum, PLATFORM, ARCH };
if (require.main !== module) return;

// A PLACEHOLDER version has no release behind it, and saying so is better than
// letting the download fail.
//
// The release workflow stamps this version from the git tag; the version checked
// into the repo is 0.0.0. That is what gets published to claim the package name
// before the first real release exists — and npm gives a package's FIRST publish
// the `latest` tag no matter what `--tag` asked for, so `npx rowshape` resolves
// to it until a real version supersedes it.
//
// Without this guard the install reaches GitHub for
// releases/download/v0.0.0/... , 404s, and reports a download error — which
// reads as a broken installer rather than as "this is not released yet". The
// URL is fully determined here, so the outcome is knowable without the request.
if (VERSION === "0.0.0") {
  console.error(
    "rowshape: this is a placeholder package published to reserve the name — " +
      "there is no v0.0.0 release to download."
  );
  fail("no release has been published yet");
}

fs.mkdirSync(binDir, { recursive: true });
const archivePath = path.join(binDir, asset);
const releaseBase = `https://github.com/${REPO}/releases/download/v${VERSION}`;

// Collect a text asset (checksums.txt and the cosign material). `optional`
// resolves to null on 404 instead of aborting, so a release without signature
// assets still installs under checksum verification.
function getText(u, optional, cb) {
  https
    .get(u, { headers: { "User-Agent": "rowshape-npm-installer" } }, (res) => {
      if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
        return getText(res.headers.location, optional, cb);
      }
      if (res.statusCode !== 200) {
        if (optional) return cb(null);
        return fail(`could not download ${u} (${res.statusCode})`);
      }
      let body = "";
      res.setEncoding("utf8");
      res.on("data", (c) => (body += c));
      res.on("end", () => cb(body));
    })
    .on("error", (e) => (optional ? cb(null) : fail(`network error: ${e.message}`)));
}

// Best-effort cosign verification of checksums.txt, mirroring install.sh.
// ROWSHAPE_VERIFY_SIGNATURE=true turns "cosign missing" into a hard failure.
function verifySignature(checksumsPath, done) {
  const mode = process.env.ROWSHAPE_VERIFY_SIGNATURE || "auto";
  if (mode === "false") return done();
  let haveCosign = true;
  try {
    execSync("cosign version", { stdio: "ignore" });
  } catch {
    haveCosign = false;
  }
  if (!haveCosign) {
    if (mode === "true") fail("cosign not on PATH and ROWSHAPE_VERIFY_SIGNATURE=true");
    return done();
  }
  getText(`${releaseBase}/checksums.txt.sig`, true, (sig) => {
    getText(`${releaseBase}/checksums.txt.pem`, true, (pem) => {
      if (!sig || !pem) {
        if (mode === "true") fail("signature assets missing and ROWSHAPE_VERIFY_SIGNATURE=true");
        return done();
      }
      const sigPath = path.join(binDir, "checksums.txt.sig");
      const pemPath = path.join(binDir, "checksums.txt.pem");
      fs.writeFileSync(sigPath, sig);
      fs.writeFileSync(pemPath, pem);
      try {
        execSync(
          `cosign verify-blob --certificate "${pemPath}" --signature "${sigPath}" ` +
            `--certificate-oidc-issuer "https://token.actions.githubusercontent.com" ` +
            `--certificate-identity-regexp "^https://github.com/${REPO}/\\.github/workflows/.+@refs/tags/" ` +
            `"${checksumsPath}"`,
          { stdio: "ignore" }
        );
        console.error("rowshape: cosign signature verified for checksums.txt");
      } catch {
        fail("cosign verification FAILED for checksums.txt");
      } finally {
        fs.rmSync(sigPath, { force: true });
        fs.rmSync(pemPath, { force: true });
      }
      done();
    });
  });
}

// Verification runs before the archive is ever extracted or made executable.
// ROWSHAPE_VERIFY=false is the only bypass and is not the default.
function withVerification(next) {
  if (process.env.ROWSHAPE_VERIFY === "false") {
    console.error("rowshape: WARNING - checksum verification disabled by ROWSHAPE_VERIFY=false");
    return next();
  }
  getText(`${releaseBase}/checksums.txt`, false, (checksums) => {
    const checksumsPath = path.join(binDir, "checksums.txt");
    fs.writeFileSync(checksumsPath, checksums);
    // Signature first: it proves who produced checksums.txt, so a forged
    // checksums.txt cannot go on to validate a forged archive.
    verifySignature(checksumsPath, () => {
      try {
        verifyChecksum(archivePath, checksums, asset);
        console.error(`rowshape: checksum verified for ${asset}`);
      } catch (e) {
        fs.rmSync(archivePath, { force: true });
        fail(`${e.message}\nrefusing to install an unverified binary`);
      } finally {
        fs.rmSync(checksumsPath, { force: true });
      }
      next();
    });
  });
}

get(url, (res) => {
  const out = fs.createWriteStream(archivePath);
  res.pipe(out);
  out.on("finish", () => {
    out.close(() => {
      withVerification(() => {
        try {
          if (ext === "zip") {
            // Rely on the system unzip / tar (tar handles zip on modern Windows).
            execSync(`tar -xf "${archivePath}" -C "${binDir}"`);
          } else {
            const tar = fs.readFileSync(archivePath);
            const tarballPath = path.join(binDir, "rowshape.tar");
            fs.writeFileSync(tarballPath, zlib.gunzipSync(tar));
            execSync(`tar -xf "${tarballPath}" -C "${binDir}"`);
            fs.unlinkSync(tarballPath);
          }
          fs.unlinkSync(archivePath);
          const bin = path.join(binDir, binName);
          if (!fs.existsSync(bin)) fail("binary not found after extraction");
          if (process.platform !== "win32") fs.chmodSync(bin, 0o755);
        } catch (e) {
          fail(`extraction failed: ${e.message}`);
        }
      });
    });
  });
});
