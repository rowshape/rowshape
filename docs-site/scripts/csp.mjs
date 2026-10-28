// What the Content-Security-Policy is, and which inline scripts it must allow.
//
// Shared by gen-csp.mjs (writes the policy after `astro build`) and
// check-build.mjs (re-derives the hashes and fails if the policy misses one), so
// the two cannot disagree about what counts as an executable inline script.
//
// Dependency-free on purpose, like check-build: a regex over built HTML is exact
// here because a <script> body is raw text to the HTML parser — it ends at the
// first `</script`, which is precisely what the regex stops at, and the bytes
// between are the bytes the browser hashes.

import { createHash } from 'node:crypto';

/**
 * `type` values whose inline content a browser executes, and so which CSP
 * script-src governs. Inline import maps and speculation rules are governed by
 * script-src too, even though they are JSON. Everything else — notably
 * application/ld+json structured data — is an inert data block that CSP ignores,
 * and hashing it would put one useless hash per page into a header that
 * Cloudflare truncates at 2,000 characters.
 */
const EXECUTABLE_TYPES = new Set([
	'',
	'module',
	'importmap',
	'speculationrules',
	'text/javascript',
	'application/javascript',
	'application/ecmascript',
	'text/ecmascript',
	'application/x-javascript',
	'text/jscript',
]);

/** Every inline <script> in a page that the browser will execute. */
export function executableInlineScripts(html) {
	const out = [];
	for (const m of html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script\s*>/gi)) {
		const attrs = m[1];
		if (/\bsrc\s*=/i.test(attrs)) continue; // external: covered by 'self'
		const type = (attrs.match(/\btype\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/i) ?? [])
			.slice(1)
			.find((v) => v !== undefined) ?? '';
		// MIME parameters (`text/javascript; charset=utf-8`) do not change whether
		// it runs; the browser compares the essence.
		if (!EXECUTABLE_TYPES.has(type.split(';')[0].trim().toLowerCase())) continue;
		out.push(m[2]);
	}
	return out;
}

/** The CSP source expression for a script body: sha256 over its UTF-8 bytes. */
export function scriptHash(body) {
	return `'sha256-${createHash('sha256').update(body, 'utf8').digest('base64')}'`;
}

/**
 * Inline event-handler attributes (onclick=…) and javascript: URLs cannot be
 * allowed by a script hash without 'unsafe-hashes', which this policy does not
 * grant. If one appears, the build must fail rather than ship a dead button.
 */
export function inlineHandlers(html) {
	const found = [];
	for (const tag of html.matchAll(/<[a-zA-Z][^>]*>/g)) {
		const h = tag[0].match(/\s(on[a-z]+)\s*=/i);
		if (h) found.push(h[1]);
		if (/\s(?:href|src|action|formaction)\s*=\s*["']?\s*javascript:/i.test(tag[0])) found.push('javascript: URL');
	}
	return found;
}

/**
 * The policy. Each directive is the narrowest that the site, as measured in a
 * browser with this header enforced, works under:
 *
 * - script-src: our own origin, plus one hash per distinct inline script
 *   (Starlight's theme/sidebar bootstraps). 'wasm-unsafe-eval' is Pagefind's
 *   WebAssembly search index; it permits compiling wasm, not eval() of strings.
 * - style-src 'unsafe-inline': Starlight and Expressive Code write style=""
 *   attributes into the markup and set styles from script. Hashes cannot cover
 *   attributes, and CSS injection is a far smaller risk than script injection.
 * - img-src data:: the copy button's and Starlight's icons are data: SVGs in CSS.
 * - worker-src 'self': Pagefind starts `new Worker('/pagefind/pagefind-worker.js')`
 *   — a same-origin URL, not a blob:, so blob: is not granted.
 * - frame-ancestors 'none': the reason this is a header and not a <meta>; a meta
 *   CSP cannot carry frame-ancestors.
 */
export function buildPolicy(hashes) {
	return [
		"default-src 'self'",
		`script-src 'self' 'wasm-unsafe-eval' ${[...hashes].sort().join(' ')}`.trim(),
		"style-src 'self' 'unsafe-inline'",
		"img-src 'self' data:",
		"font-src 'self'",
		"connect-src 'self'",
		"worker-src 'self'",
		"object-src 'none'",
		"base-uri 'self'",
		"form-action 'self'",
		"frame-ancestors 'none'",
		'upgrade-insecure-requests',
	].join('; ');
}
