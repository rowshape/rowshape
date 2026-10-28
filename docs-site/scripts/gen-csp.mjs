// Writes the Content-Security-Policy into dist/_headers after `astro build`.
//
// Why generated, and why after the build: the policy allows inline scripts by
// hash, and the hashes are of the bytes Astro and Starlight actually emit. Any
// upgrade, any new component with an inline script, any change to how a theme
// bootstrap is minified changes them. Written by hand the policy would be
// correct on the day it was written and break the site (silently, in the
// visitor's console) on the next dependency bump. Derived from dist/ it is
// correct by construction, and check-build re-derives it independently.
//
// Why a header and not <meta http-equiv>: a meta CSP cannot carry
// frame-ancestors, and it only applies from the point it is parsed.
//
// Run: node scripts/gen-csp.mjs   (part of `npm run build`)

import { readFile, readdir, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { buildPolicy, executableInlineScripts, inlineHandlers, scriptHash } from './csp.mjs';

const DIST = 'dist';
const HEADERS = join(DIST, '_headers');
/** Cloudflare drops a _headers line longer than this. */
const MAX_LINE = 2000;

async function walk(dir, out = []) {
	for (const entry of await readdir(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) await walk(path, out);
		else if (path.endsWith('.html')) out.push(path);
	}
	return out;
}

if (!existsSync(HEADERS)) {
	console.error(`gen-csp: no ${HEADERS} — public/_headers must exist for the policy to go into`);
	process.exit(1);
}

const hashes = new Set();
const handlers = [];
for (const page of await walk(DIST)) {
	const html = await readFile(page, 'utf8');
	for (const body of executableInlineScripts(html)) hashes.add(scriptHash(body));
	for (const h of inlineHandlers(html)) handlers.push(`${page}: ${h}`);
}
if (handlers.length) {
	console.error("gen-csp: inline handlers need 'unsafe-hashes', which the policy does not grant:");
	for (const h of handlers) console.error(`  ${h}`);
	process.exit(1);
}

const line = `  Content-Security-Policy: ${buildPolicy(hashes)}`;
if (line.length > MAX_LINE) {
	console.error(`gen-csp: the policy is ${line.length} chars, over Cloudflare's ${MAX_LINE}; ${hashes.size} distinct inline scripts is too many to hash`);
	process.exit(1);
}

// Insert as the last header of the /* rule. Any CSP already there (a re-run on
// the same dist/) is replaced, so running twice is the same as running once.
const lines = (await readFile(HEADERS, 'utf8')).split('\n').filter((l) => !/^\s+Content-Security-Policy:/i.test(l));
const start = lines.findIndex((l) => l.trim() === '/*' && !/^\s/.test(l));
if (start < 0) {
	console.error(`gen-csp: ${HEADERS} has no /* rule to attach the policy to`);
	process.exit(1);
}
let end = start + 1;
while (end < lines.length && /^\s+\S/.test(lines[end])) end++;
lines.splice(end, 0, line);
await writeFile(HEADERS, lines.join('\n'));

console.log(`gen-csp: ${hashes.size} inline script hash(es), policy ${line.length} chars, written to ${HEADERS}`);
