// Does the npm wrapper refuse a binary that does not match the release?
//
// It did not. install.js downloaded an archive from the releases page and
// executed it having verified nothing at all — no checksum, no signature —
// while .goreleaser.yaml publishes checksums.txt plus a cosign keyless
// signature over it, and the docs site walks users through `cosign verify-blob`.
// The supply-chain artifacts existed and nothing consumed them.
//
// These checks cover the verification helpers directly, so they run with no
// network and no release:
//
//	node npm/verify.test.js
"use strict";

const assert = require("assert");
const crypto = require("crypto");
const fs = require("fs");
const os = require("os");
const path = require("path");

const { expectedSum, verifyChecksum } = require("./install.js");

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

console.log("npm installer verification");

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), "rowshape-verify-"));
const asset = "rowshape_1.2.3_linux_amd64.tar.gz";
const archive = path.join(tmp, asset);
const body = Buffer.from("this stands in for the release archive");
fs.writeFileSync(archive, body);
const good = crypto.createHash("sha256").update(body).digest("hex");

const checksums = [
  `${crypto.createHash("sha256").update("other").digest("hex")}  rowshape_1.2.3_darwin_arm64.tar.gz`,
  `${good}  ${asset}`,
  `${crypto.createHash("sha256").update("more").digest("hex")}  rowshape_1.2.3_windows_amd64.zip`,
].join("\n");

check("a matching archive verifies", () => {
  verifyChecksum(archive, checksums, asset);
});

check("a tampered archive is refused", () => {
  const tampered = path.join(tmp, "tampered.tar.gz");
  fs.writeFileSync(tampered, Buffer.concat([body, Buffer.from("x")]));
  assert.throws(() => verifyChecksum(tampered, checksums, asset), /checksum mismatch/);
});

check("an asset absent from checksums.txt is refused, not skipped", () => {
  assert.throws(
    () => verifyChecksum(archive, checksums, "rowshape_9.9.9_linux_arm64.tar.gz"),
    /no entry in checksums.txt/
  );
});

check("an empty checksums.txt is refused", () => {
  assert.throws(() => verifyChecksum(archive, "", asset), /no entry in checksums.txt/);
});

check("the right line is selected out of many", () => {
  assert.strictEqual(expectedSum(checksums, asset), good);
});

// A substring match would let an entry for a LONGER filename satisfy a shorter
// one, so the asset name is compared as a whole field.
check("a filename that merely contains the asset name does not satisfy it", () => {
  const sneaky = `${crypto.createHash("sha256").update("evil").digest("hex")}  prefix_${asset}_suffix.tar.gz`;
  assert.strictEqual(expectedSum(sneaky, asset), null);
});

check("a binary-mode '*' prefix is tolerated", () => {
  assert.strictEqual(expectedSum(`${good} *${asset}`, asset), good);
});

check("digests compare case-insensitively", () => {
  assert.strictEqual(expectedSum(`${good.toUpperCase()}  ${asset}`, asset), good);
});

fs.rmSync(tmp, { recursive: true, force: true });

if (failures > 0) {
  console.error(`\n${failures} check(s) failed`);
  process.exit(1);
}
console.log("\nnpm installer refuses anything it cannot verify");
