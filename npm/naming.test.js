// Does the npm wrapper ask for the file goreleaser actually publishes?
//
// It did not. install.js used the older goreleaser convention — title-cased OS,
// amd64 rewritten to x86_64 — and asked for
//
//	rowshape_1.0.0_Darwin_x86_64.tar.gz
//
// while .goreleaser.yaml's name_template ({{ .Os }}_{{ .Arch }}, raw lowercase
// GOOS/GOARCH) publishes
//
//	rowshape_1.0.0_darwin_amd64.tar.gz
//
// Every `npx rowshape` would have 404'd. Two files, in different languages, that
// have to agree on a string, and nothing compared them: the mismatch could only
// surface at the first real release, in a postinstall hook, on a user's machine.
//
// Run against a real build to compare against reality rather than a belief:
//
//	goreleaser release --snapshot --clean --skip=publish,docker,sign,sbom
//	node npm/naming.test.js
//
// With no dist/ present it still checks the naming convention itself, so it is
// useful without goreleaser installed.

"use strict";

const fs = require("fs");
const path = require("path");
const assert = require("assert");

const { assetName } = require("./install.js");

let failures = 0;
function check(name, fn) {
  try {
    fn();
    console.log(`  ok   ${name}`);
  } catch (e) {
    failures++;
    console.error(`  FAIL ${name}\n       ${e.message}`);
  }
}

// The 5 combos .goreleaser.yaml builds: darwin/linux on amd64+arm64, windows on
// amd64 (windows/arm64 is explicitly ignored).
const COMBOS = [
  ["darwin", "amd64", "tar.gz"],
  ["darwin", "arm64", "tar.gz"],
  ["linux", "amd64", "tar.gz"],
  ["linux", "arm64", "tar.gz"],
  ["windows", "amd64", "zip"],
  ["windows", "arm64", "zip"],
];

check("asset names follow goreleaser's name_template", () => {
  for (const [os, arch, ext] of COMBOS) {
    const got = assetName("1.2.3", os, arch);
    assert.strictEqual(
      got,
      `rowshape_1.2.3_${os}_${arch}.${ext}`,
      `name_template is {{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }} — lowercase GOOS/GOARCH, not Darwin/x86_64. got ${got}`
    );
  }
});

check("windows archives are zip, everything else tar.gz", () => {
  assert.ok(assetName("1.0.0", "windows", "amd64").endsWith(".zip"), "windows must be zip (format_overrides)");
  assert.ok(assetName("1.0.0", "linux", "amd64").endsWith(".tar.gz"), "linux must be tar.gz");
});

// The real check: compare against what a build actually produced.
const dist = path.join(__dirname, "..", "dist");
if (fs.existsSync(dist)) {
  const built = fs.readdirSync(dist).filter((f) => f.endsWith(".tar.gz") || f.endsWith(".zip"));
  check(`every one of the ${COMBOS.length} built archives is reachable by install.js`, () => {
    assert.ok(built.length > 0, "dist/ has no archives — did the snapshot build run?");
    // Recover the version goreleaser stamped, from any archive.
    const m = built[0].match(/^rowshape_(.+?)_(darwin|linux|windows)_(amd64|arm64)\.(tar\.gz|zip)$/);
    assert.ok(m, `dist archive ${built[0]} does not match the expected pattern — the template changed`);
    const version = m[1];
    for (const [os, arch] of COMBOS) {
      const want = assetName(version, os, arch);
      assert.ok(
        built.includes(want),
        `install.js would fetch ${want}, which the release does not publish. Built: ${built.join(", ")}`
      );
    }
  });
} else {
  console.log("  skip dist/ comparison (no snapshot build present)");
}

if (failures > 0) {
  console.error(`\n${failures} check(s) failed`);
  process.exit(1);
}
console.log("\nnpm wrapper naming matches the release");
