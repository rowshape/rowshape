// Post-build checks for the docs site: internal links resolve, and the client JS
// stays inside its budget.
//
// Deliberately dependency-free — it reads the built `dist/` the way a browser
// would. A link checker that needs a headless browser and four hundred packages to
// tell you a href is wrong is worse than the problem.
//
// Run: node scripts/check-build.mjs   (npm run check, after npm run build)

import { readFile, readdir, stat } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { join, dirname, resolve, extname } from 'node:path';

const DIST = 'dist';

/**
 * The client-JS budget, in bytes, for what a visitor loads on a page.
 *
 * PRD §9 chose Starlight partly for "ships zero JS by default". That is not
 * literally true and this file is where that stops being a belief: Starlight
 * emits a small amount of progressive-enhancement JS (theme toggle, table of
 * contents, the search launcher) on every page. What it does NOT ship is a UI
 * framework runtime, which is what the claim was actually protecting against.
 *
 * So the guard measures the real thing: a per-page ceiling low enough that no
 * framework runtime, analytics bundle, or component island can slip in without
 * tripping it. Raising this number is a product decision, not a build fix.
 */
const JS_BUDGET_BYTES = 32 * 1024;

/** Recursively collect files under dir matching a predicate. */
async function walk(dir, match, out = []) {
	for (const entry of await readdir(dir, { withFileTypes: true })) {
		const path = join(dir, entry.name);
		if (entry.isDirectory()) await walk(path, match, out);
		else if (match(path)) out.push(path);
	}
	return out;
}

/** Every href/src target a page references, as written. */
function extractLinks(html) {
	return [...html.matchAll(/(?:href|src)="([^"]+)"/g)].map((m) => m[1]);
}

/** Resolve an internal link to the file that must exist in dist/. */
function resolveTarget(link) {
	const clean = link.split('#')[0].split('?')[0];
	if (!clean || clean === '/') return join(DIST, 'index.html');
	const path = join(DIST, clean);
	if (extname(clean)) return path; // an asset: /_astro/x.js, /favicon.svg
	return join(path, 'index.html'); // a page: /install/ -> dist/install/index.html
}

async function checkLinks(pages) {
	const broken = [];
	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		for (const link of extractLinks(html)) {
			// External, protocol-relative, anchors, and data URIs are not ours to
			// verify. A link checker that hits the network is a flaky CI job.
			if (/^(https?:|\/\/|#|mailto:|data:)/.test(link)) continue;
			if (!link.startsWith('/')) continue; // relative links are rare in Starlight output
			const target = resolveTarget(link);
			if (!existsSync(target)) broken.push(`${page} -> ${link} (expected ${target})`);
		}
	}
	return broken;
}

async function checkJSBudget(pages) {
	const over = [];
	for (const page of pages) {
		const html = await readFile(page, 'utf8');

		// Module scripts the page actually loads.
		let total = 0;
		const loaded = [];
		for (const m of html.matchAll(/<script[^>]*\bsrc="([^"]+)"/g)) {
			const src = m[1];
			if (!src.startsWith('/')) continue;
			const path = join(DIST, src);
			if (!existsSync(path)) continue;
			total += (await stat(path)).size;
			loaded.push(src);
		}
		// Inline scripts ship on the page itself and count too.
		for (const m of html.matchAll(/<script(?![^>]*\bsrc=)[^>]*>([\s\S]*?)<\/script>/g)) {
			total += Buffer.byteLength(m[1], 'utf8');
		}

		const rel = page.replace(/\\/g, '/');
		console.log(`  ${(total / 1024).toFixed(1)} KiB JS  ${rel}  (${loaded.length} module script(s))`);
		if (total > JS_BUDGET_BYTES) {
			over.push(`${rel}: ${total} bytes of client JS, over the ${JS_BUDGET_BYTES} budget`);
		}
	}
	return over;
}

// The narrow, defensible privacy claim from PRD §8.1. The privacy page must
// reproduce it verbatim and must NOT make the broader "no production values
// leave" claim, which is false (PRD §11). Checked against the source Markdown
// (not the built HTML) so smart-quotes/entities don't confuse the match.
const PRIVACY_PAGE = 'src/content/docs/privacy/index.md';
const NARROW_CLAIM =
	'a fixture contains no rows from your database; it contains statistics computed ' +
	'from them; at --privacy standard some of those reveal the extremes of numeric ' +
	'and date columns; at --privacy strict none do.';

/** Normalize prose for a robust substring match: drop markdown emphasis/code
 * markers and collapse whitespace. */
function normalizeProse(s) {
	return s
		.toLowerCase()
		.replace(/[`*_]/g, '')
		.replace(/\s+/g, ' ')
		.trim();
}

async function checkPrivacyClaim() {
	const problems = [];
	if (!existsSync(PRIVACY_PAGE)) {
		return [`privacy page missing at ${PRIVACY_PAGE}`];
	}
	const text = normalizeProse(await readFile(PRIVACY_PAGE, 'utf8'));
	if (!text.includes(normalizeProse(NARROW_CLAIM))) {
		problems.push('privacy page does not reproduce the PRD §8.1 narrow claim verbatim');
	}
	// The broader claim may only appear disavowed. Every mention must have "false"
	// nearby; otherwise the page is asserting it.
	const broad = 'no production values leave';
	let i = text.indexOf(broad);
	while (i !== -1) {
		const window = text.slice(i, i + broad.length + 80);
		if (!window.includes('false')) {
			problems.push('privacy page states the broader "no production values leave" claim without disavowing it as false');
			break;
		}
		i = text.indexOf(broad, i + broad.length);
	}
	return problems;
}

// robots.txt is generated by src/pages/robots.txt.ts, which means it can stop
// being generated -- a rename, a config change, or an integration that claims
// src/pages/ would all remove it silently, and nothing else in this build would
// notice. The three things worth asserting are the three that make it useful:
// it exists, it points crawlers at a sitemap that is really there, and it has
// not started blocking the CSS/JS Google needs in order to render a page.
async function checkRobots() {
	const problems = [];
	const path = join(DIST, 'robots.txt');
	if (!existsSync(path)) {
		return ['no robots.txt in dist/ — src/pages/robots.txt.ts did not emit'];
	}
	const text = await readFile(path, 'utf8');

	const sitemap = text.match(/^Sitemap:\s*(\S+)$/m);
	if (!sitemap) {
		problems.push('robots.txt names no Sitemap');
	} else {
		let url;
		try {
			url = new URL(sitemap[1]);
		} catch {
			problems.push(`robots.txt Sitemap is not an absolute URL: ${sitemap[1]}`);
		}
		// The sitemap it advertises has to be one this build actually produced.
		if (url && !existsSync(join(DIST, url.pathname))) {
			problems.push(`robots.txt points at ${sitemap[1]}, which is not in dist/`);
		}
	}

	for (const m of text.matchAll(/^Disallow:\s*(\S+)$/gm)) {
		if ('/_astro/'.startsWith(m[1]) || '/favicon.svg'.startsWith(m[1])) {
			problems.push(
				`robots.txt Disallow: ${m[1]} blocks the assets a rendering crawler needs; ` +
					'a page that cannot load its CSS is judged as the broken thing it appears to be'
			);
		}
	}
	return problems;
}

// The social card. Starlight emits twitter:card=summary_large_image on every
// page whether or not an image exists, so the failure mode this guards is a
// blank rectangle in every Slack, X and LinkedIn preview — invisible from
// inside the repo, and invisible in the built HTML too unless something reads
// the tag and follows it. So: every page must declare an absolute og:image, the
// file must be in dist/, and the width/height it advertises must be the file's
// real dimensions, because scrapers that trust the tag and get a mismatch crop.
function pngSize(buf) {
	// IHDR is fixed at bytes 16..24 of any PNG. Parsed by hand rather than pulling
	// sharp in here — this script stays dependency-free on purpose.
	if (buf.length < 24 || buf.readUInt32BE(0) !== 0x89504e47) return null;
	return { width: buf.readUInt32BE(16), height: buf.readUInt32BE(20) };
}

async function checkSocialCard(pages) {
	const problems = [];
	// Keyed by the whole triple, not by src: keying on the URL alone lets a page
	// that declares the wrong dimensions be overwritten by the 48 that declare the
	// right ones, and the guard reports clean. Found by negative-testing it.
	const seen = new Map(); // "src|w|h" -> { src, width, height, page }

	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		const rel = page.replace(/\\/g, '/');
		const image = html.match(/<meta property="og:image" content="([^"]+)"/);
		if (!image) {
			problems.push(`${rel}: no og:image`);
			continue;
		}
		if (!/^https?:\/\//.test(image[1])) {
			problems.push(`${rel}: og:image "${image[1]}" is not absolute — scrapers ignore relative ones`);
			continue;
		}
		if (!html.includes('<meta name="twitter:image"')) {
			problems.push(`${rel}: og:image without twitter:image`);
		}
		const w = html.match(/<meta property="og:image:width" content="(\d+)"/);
		const h = html.match(/<meta property="og:image:height" content="(\d+)"/);
		if (w && h) {
			seen.set(`${image[1]}|${w[1]}|${h[1]}`, {
				src: image[1],
				width: Number(w[1]),
				height: Number(h[1]),
				page: rel,
			});
		}
	}

	for (const declared of seen.values()) {
		const { src } = declared;
		const path = join(DIST, new URL(src).pathname);
		if (!existsSync(path)) {
			problems.push(`og:image ${src} is not in dist/ — run \`npm run og\` and commit public/og.png`);
			continue;
		}
		const actual = pngSize(await readFile(path));
		if (!actual) {
			problems.push(`og:image ${src} is not a readable PNG`);
		} else if (actual.width !== declared.width || actual.height !== declared.height) {
			problems.push(
				`${declared.page}: og:image ${src} is ${actual.width}x${actual.height} but the page ` +
					`declares ${declared.width}x${declared.height}`
			);
		}
	}
	return problems;
}

/**
 * The title tag, in bytes a results page will actually show.
 *
 * 60 characters is not a rule Google publishes — the real limit is a pixel
 * width — but it is the width at which a desktop result starts to ellipsize,
 * and it is checkable. The findings catalog had titles up to 110 characters, so
 * a searcher saw the code and half a sentence with the brand cut off; the fix
 * (frontmatter `seoTitle`) is only worth having if something stops it regressing
 * the next time a heading is reworded.
 *
 * Duplicate titles are the other failure: 49 pages competing on the same string
 * is 49 pages Google has to guess between.
 */
const TITLE_MAX = 60;

async function checkTitles(pages) {
	const problems = [];
	const byTitle = new Map();

	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		const rel = page.replace(/\\/g, '/');
		const m = html.match(/<title>([^<]*)<\/title>/);
		if (!m || !m[1].trim()) {
			problems.push(`${rel}: no <title>`);
			continue;
		}
		// Entities are what a scraper decodes, so measure the decoded string.
		const title = m[1]
			.replace(/&#(\d+);/g, (_, d) => String.fromCodePoint(Number(d)))
			.replace(/&amp;/g, '&')
			.replace(/&lt;/g, '<')
			.replace(/&gt;/g, '>')
			.replace(/&quot;/g, '"')
			.replace(/&#39;/g, "'");

		if ([...title].length > TITLE_MAX) {
			problems.push(
				`${rel}: <title> is ${[...title].length} chars, over ${TITLE_MAX} — set a shorter ` +
					'`seoTitle` in frontmatter (it does not change the page heading)'
			);
		}

		// "rowshape | rowshape": the site title appended to a page title that is
		// already the site title. This is what the route middleware exists to stop.
		const parts = title.split('|').map((t) => t.trim().toLowerCase());
		if (parts.length > 1 && new Set(parts).size !== parts.length) {
			problems.push(`${rel}: <title> repeats itself across the delimiter — "${title}"`);
		}

		const prior = byTitle.get(title);
		if (prior) problems.push(`${rel}: <title> duplicates ${prior} — "${title}"`);
		else byTitle.set(title, rel);
	}
	return problems;
}

/**
 * Meta descriptions, in the length a result actually renders.
 *
 * Over ~160 characters Google truncates and the sentence loses its ending;
 * under ~70 it usually ignores the tag and synthesizes a snippet from the page
 * instead, which is the tag doing nothing. The catalog had descriptions from 25
 * to 349 characters, including one that opened by naming a DIFFERENT finding
 * code — a correct sentence in context and a useless snippet out of it.
 *
 * Duplicates matter for the same reason duplicate titles do: identical snippets
 * across pages give a searcher nothing to choose by.
 */
const DESC_MIN = 70;
const DESC_MAX = 160;

/**
 * Routes exempt from the FLOOR only.
 *
 * Everything under /reference/ is generated by `go run ./tools/gencli` from the
 * cobra command tree, and its description is the command's own `Short` line —
 * which is a one-line CLI help string and is right to be terse. Lengthening
 * them means editing Go help text that a staleness test pins, so it is a
 * separate decision (seo-prd.json SEO-16), not something to smuggle in behind a
 * docs build. The ceiling still applies: nothing is exempt from truncation.
 */
const DESC_FLOOR_EXEMPT = /^dist\/reference\//;

async function checkDescriptions(pages) {
	const problems = [];
	const byDesc = new Map();

	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		const rel = page.replace(/\\/g, '/');
		const m = html.match(/<meta name="description" content="([^"]*)"/);
		if (!m || !m[1].trim()) {
			problems.push(`${rel}: no meta description`);
			continue;
		}
		const desc = m[1];
		const n = [...desc].length;

		if (n > DESC_MAX) {
			problems.push(`${rel}: description is ${n} chars, over ${DESC_MAX} — it will be truncated mid-sentence`);
		} else if (n < DESC_MIN && !DESC_FLOOR_EXEMPT.test(rel)) {
			problems.push(`${rel}: description is ${n} chars, under ${DESC_MIN} — too short to be used as the snippet`);
		}

		const prior = byDesc.get(desc);
		if (prior) problems.push(`${rel}: description duplicates ${prior}`);
		else byDesc.set(desc, rel);
	}
	return problems;
}

async function main() {
	if (!existsSync(DIST)) {
		console.error(`no ${DIST}/ — run \`npm run build\` first`);
		process.exit(1);
	}
	const pages = await walk(DIST, (p) => p.endsWith('.html'));
	if (pages.length === 0) {
		console.error('no pages in dist/ — the build produced nothing');
		process.exit(1);
	}
	console.log(`checking ${pages.length} page(s)\n`);

	console.log('client JS per page:');
	const over = await checkJSBudget(pages);
	console.log('');

	const broken = await checkLinks(pages);
	const privacy = await checkPrivacyClaim();
	const robots = await checkRobots();
	const social = await checkSocialCard(pages);
	const titles = await checkTitles(pages);
	const descriptions = await checkDescriptions(pages);

	let failed = false;
	if (descriptions.length) {
		failed = true;
		console.error(`${descriptions.length} meta-description problem(s):`);
		for (const d of descriptions) console.error(`  ${d}`);
	}
	if (titles.length) {
		failed = true;
		console.error(`${titles.length} <title> problem(s):`);
		for (const t of titles) console.error(`  ${t}`);
	}
	if (social.length) {
		failed = true;
		console.error(`${social.length} social-card problem(s):`);
		for (const c of social.slice(0, 10)) console.error(`  ${c}`);
		if (social.length > 10) console.error(`  … and ${social.length - 10} more`);
	}
	if (robots.length) {
		failed = true;
		console.error(`${robots.length} robots.txt problem(s):`);
		for (const r of robots) console.error(`  ${r}`);
	}
	if (privacy.length) {
		failed = true;
		console.error(`${privacy.length} privacy-claim problem(s):`);
		for (const p of privacy) console.error(`  ${p}`);
	}
	if (broken.length) {
		failed = true;
		console.error(`${broken.length} broken internal link(s):`);
		for (const b of broken) console.error(`  ${b}`);
	}
	if (over.length) {
		failed = true;
		console.error(`\n${over.length} page(s) over the client-JS budget:`);
		for (const o of over) console.error(`  ${o}`);
		console.error(
			'\nStarlight ships a little progressive-enhancement JS by design; a framework\n' +
				'runtime or an analytics bundle is what this budget exists to catch. Raising it\n' +
				'is a product decision, not a build fix.'
		);
	}
	if (failed) process.exit(1);

	console.log(`OK: ${pages.length} pages, no broken internal links, robots.txt advertises a real sitemap, every page carries a real social card, every <title> unique and under 60 chars, every description in range, all within the ${JS_BUDGET_BYTES / 1024} KiB JS budget`);
}

await main();
