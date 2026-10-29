// The Findings sidebar: one collapsed group per namespace (RS-CONSTRAINT,
// RS-DATA, ...), each listing its codes.
//
// Why not `autogenerate`: it renders the 27 pages as one flat list of full
// titles, which wrapped to two or three lines each and pushed every other
// section of the site below the fold on a finding page. A reader arriving from
// a CI log has a code, not a title, so the items are labelled by code (the
// generator writes `sidebar.label`) and grouped by the namespace the code
// already names.
//
// Why read the directory instead of listing slugs here: the pages are generated
// from the findings catalog, and a hand-kept list in this file would be the one
// place a new code could be forgotten. Order is namespace, then code — which is
// also the order Starlight's previous/next links follow, so paging through the
// findings walks one family at a time.

import { readdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const FINDINGS_DIR = join(dirname(fileURLToPath(import.meta.url)), 'content', 'docs', 'findings');

export function findingsSidebar() {
	const slugs = readdirSync(FINDINGS_DIR)
		.filter((f) => /^rs-[a-z]+-\d+\.md$/.test(f))
		.map((f) => f.replace(/\.md$/, ''))
		.sort();

	const byNamespace = new Map();
	for (const slug of slugs) {
		const ns = slug.replace(/-\d+$/, '').toUpperCase();
		if (!byNamespace.has(ns)) byNamespace.set(ns, []);
		byNamespace.get(ns).push(`findings/${slug}`);
	}

	return [
		{ slug: 'findings', label: 'Finding catalog' },
		...[...byNamespace].map(([ns, items]) => ({ label: ns, collapsed: true, items })),
	];
}
