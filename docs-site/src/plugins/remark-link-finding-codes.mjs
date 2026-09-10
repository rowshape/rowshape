// Turns every mention of a finding code — RS-LOCK-001, `RS-DATA-014` — into a
// link to that finding's page.
//
// Why a plugin and not 30 hand-written links: the codes are the site's natural
// internal-link graph and the pages already reference each other constantly in
// prose ("this is the reverse of RS-DATA-001", "RS-PERF-002 only fires when
// ..."). Written by hand, that graph is only as complete as whoever remembered,
// and it rots the first time a page is edited. Generated from the mention, it
// cannot be incomplete and cannot go stale.
//
// Rules, all of which exist because the naive version gets them wrong:
//
//   - Only codes that HAVE a page are linked. The catalog is the source of
//     truth; a link to a code with no page is a 404 the link checker would
//     rightly fail on.
//   - A page never links to itself. A self-link on every page is noise, and
//     Google reads a page linking to itself as nothing at all.
//   - Nothing inside a fenced code block is touched. A code in a JSON verdict
//     or a CLI transcript is sample output, not a reference.
//   - Nothing already inside a link is touched, so the hand-written catalog
//     entries are left exactly as they are.
//
// Dependency-free on purpose: it walks the mdast itself rather than pulling in
// unist-util-visit, matching scripts/check-build.mjs's reasoning about tools
// that need four hundred packages to do something small.

import { readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, basename } from 'node:path';

const FINDINGS_DIR = join(
	dirname(fileURLToPath(import.meta.url)),
	'..',
	'content',
	'docs',
	'findings'
);

/** Set of codes that actually have a page, e.g. "RS-LOCK-001". */
function knownCodes() {
	return new Set(
		readdirSync(FINDINGS_DIR)
			.filter((f) => /^rs-[a-z]+-\d+\.md$/.test(f))
			.map((f) => basename(f, '.md').toUpperCase())
	);
}

const CODE = /\bRS-[A-Z]+-\d{3}\b/g;

/** The docs route for a code: RS-LOCK-001 -> /findings/rs-lock-001/ */
function routeFor(code) {
	return `/findings/${code.toLowerCase()}/`;
}

export default function remarkLinkFindingCodes() {
	const codes = knownCodes();

	return (tree, file) => {
		// The code this page IS, if it is a finding page, so it does not link to
		// itself. Derived from the filename, which is what the route comes from.
		const self = file.path ? basename(file.path).replace(/\.mdx?$/, '').toUpperCase() : '';

		const linkable = (code) => codes.has(code) && code !== self;

		/**
		 * @param {any} node   the parent whose children may be rewritten
		 * @param {boolean} inLink  true once inside a link, so nothing nests
		 */
		function walk(node, inLink) {
			if (!node || !Array.isArray(node.children)) return;

			const out = [];
			for (const child of node.children) {
				// Fenced code and math are sample output, not prose.
				if (child.type === 'code' || child.type === 'math') {
					out.push(child);
					continue;
				}

				if (!inLink && child.type === 'inlineCode' && linkable(child.value.trim())) {
					const code = child.value.trim();
					out.push({
						type: 'link',
						url: routeFor(code),
						children: [{ type: 'inlineCode', value: code }],
					});
					continue;
				}

				if (!inLink && child.type === 'text' && CODE.test(child.value)) {
					CODE.lastIndex = 0;
					let last = 0;
					let m;
					const parts = [];
					while ((m = CODE.exec(child.value)) !== null) {
						if (!linkable(m[0])) continue;
						if (m.index > last) {
							parts.push({ type: 'text', value: child.value.slice(last, m.index) });
						}
						parts.push({
							type: 'link',
							url: routeFor(m[0]),
							children: [{ type: 'inlineCode', value: m[0] }],
						});
						last = m.index + m[0].length;
					}
					if (parts.length) {
						if (last < child.value.length) {
							parts.push({ type: 'text', value: child.value.slice(last) });
						}
						out.push(...parts);
						continue;
					}
				}

				walk(child, inLink || child.type === 'link' || child.type === 'linkReference');
				out.push(child);
			}
			node.children = out;
		}

		walk(tree, false);
	};
}
