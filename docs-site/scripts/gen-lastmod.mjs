// Computes the last-modified date of every docs page from git, into
// src/generated/lastmod.json.
//
// Run: node scripts/gen-lastmod.mjs   (automatically, via `npm run build`)
//
// Two consumers: <lastmod> in the sitemap, and dateModified on the TechArticle
// each page emits. Both are freshness signals, and both are only worth having if
// the date is real — a mtime is not (a fresh clone sets every file to checkout
// time, which would tell a crawler the entire site changed today, every deploy).
// The git author/commit date of the file is the real one.
//
// The failure mode this guards hardest against is a SHALLOW CLONE.
// actions/checkout defaults to fetch-depth: 1, which leaves exactly one commit
// in history, so `git log` returns that commit's date for every file and the
// output looks perfectly valid while being uniformly wrong. That is why this
// refuses to run against a shallow repository rather than degrading, and why
// docs-deploy.yml now asks for full history.

import { writeFile, mkdir, readdir } from 'node:fs/promises';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { join, relative } from 'node:path';

const run = promisify(execFile);
const CONTENT = 'src/content/docs';
const OUT = 'src/generated/lastmod.json';

/** Every content file, as a repo path git will recognize. */
async function contentFiles(dir = CONTENT, out = []) {
	for (const entry of await readdir(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) await contentFiles(path, out);
		else if (/\.mdx?$/.test(path)) out.push(path);
	}
	return out;
}

/**
 * The route a content file builds to.
 *
 * src/content/docs/index.mdx            -> /
 * src/content/docs/findings/index.md    -> /findings/
 * src/content/docs/findings/rs-tx-001.md -> /findings/rs-tx-001/
 */
function routeFor(file) {
	const rel = relative(CONTENT, file).replace(/\\/g, '/');
	const slug = rel.replace(/\.mdx?$/, '').replace(/(^|\/)index$/, '');
	return slug ? `/${slug}/` : '/';
}

async function git(args) {
	const { stdout } = await run('git', args, { cwd: process.cwd() });
	return stdout.trim();
}

async function main() {
	if ((await git(['rev-parse', '--is-shallow-repository'])) === 'true') {
		console.error(
			'refusing to compute lastmod from a SHALLOW clone.\n' +
				'`git log` would return the single available commit for every file, so every\n' +
				'page would claim the same modification date and the sitemap would tell a\n' +
				'crawler the whole site changed today. Fetch full history\n' +
				'(actions/checkout with fetch-depth: 0) and re-run.'
		);
		process.exit(1);
	}

	const files = await contentFiles();
	const dates = {};
	let uncommitted = 0;

	for (const file of files) {
		// %cI is the committer date in strict ISO 8601. Committer, not author:
		// a rebase or a cherry-pick moves content into the branch at the commit
		// date, and that is when the published page actually changed.
		const iso = await git(['log', '-1', '--format=%cI', '--', file]);
		if (!iso) {
			// A page added but not yet committed. Emitting today's date would be a
			// guess; omitting it lets the sitemap simply not claim one.
			uncommitted++;
			continue;
		}
		dates[routeFor(file)] = iso;
	}

	await mkdir('src/generated', { recursive: true });
	await writeFile(OUT, JSON.stringify({ generatedFrom: 'git', dates }, null, '\t') + '\n');

	console.log(
		`${OUT}: ${Object.keys(dates).length} route(s) dated from git` +
			(uncommitted ? `, ${uncommitted} uncommitted and left undated` : '')
	);
}

await main();
