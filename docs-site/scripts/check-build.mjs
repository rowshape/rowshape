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
import { executableInlineScripts, inlineHandlers, scriptHash } from './csp.mjs';

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

/**
 * The ceiling for JS a page can pull in LATER — the targets of dynamic import()
 * in the modules it loads, plus everything those statically import.
 *
 * This is the half the budget above used to miss. It counted <script src>, and
 * Starlight's search launcher is a 3 KB <script src> whose one job was to
 * import() the 94 KB Pagefind UI on the first idle tick of every page — so the
 * site shipped ~100 KB of JS per visit while this check reported 11-14. The
 * launcher now loads that chunk only on search intent (src/components/
 * Search.astro), which is what makes "deferred" a separate, honest number:
 * bytes a visitor pays for only if they reach for the feature.
 *
 * The cap sits ~20 KiB over the Pagefind UI, so a second lazy feature of that
 * size, or a lazy-loaded framework, has to be a decision.
 */
const DEFERRED_JS_BUDGET_BYTES = 112 * 1024;

/**
 * The render-blocking CSS budget, in bytes, per page.
 *
 * Measured before it was set: the heaviest page (a CLI reference page, which
 * also loads the code-block stylesheet) is 80 KiB and the rest are 62. The
 * ceiling is 96 KiB — enough headroom for the docs to grow, low enough that a
 * UI library or an icon font would not fit under it.
 *
 * Every byte of this blocks first paint, which is what makes it a Largest
 * Contentful Paint number rather than a page-weight one.
 */
const CSS_BUDGET_BYTES = 96 * 1024;

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

/**
 * Resolve an internal link to the file that must exist in dist/.
 *
 * `from` is the page the link was written on, so a RELATIVE link resolves the
 * way a browser resolves it. That case used to be skipped as "rare in Starlight
 * output", which was wrong: the finding catalog links all 27 of its pages as
 * `./rs-lock-001/`, so the site's densest and most important set of internal
 * links was the one set nothing verified. Renaming a slug would have 404'd
 * silently.
 */
function resolveTarget(link, from) {
	const clean = link.split('#')[0].split('?')[0];
	if (!clean) return null;
	let path;
	if (clean.startsWith('/')) {
		if (clean === '/') return join(DIST, 'index.html');
		path = join(DIST, clean);
	} else {
		// Relative to the DIRECTORY the page is in — dist/findings/index.html has
		// directory dist/findings, so ./rs-lock-001/ is dist/findings/rs-lock-001.
		path = resolve(dirname(from), clean);
	}
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
			const target = resolveTarget(link, page);
			if (!target) continue;
			if (!existsSync(target)) broken.push(`${page} -> ${link} (expected ${target})`);
		}
	}
	return broken;
}

/**
 * The local modules a JS file imports, split by how: a static import loads with
 * the importer, a dynamic import() only when the code reaches it. Bundler output
 * is regular enough for a regex — `from"./x.js"`, `import"./x.js"`,
 * import(`./x.js`) — and a specifier this misses (a computed one, like
 * Pagefind's own `${bundlePath}pagefind.js`) is not ours to resolve anyway.
 */
function moduleImports(code) {
	const local = (spec) => /^(\.{1,2}\/|\/)/.test(spec);
	const statics = [...code.matchAll(/(?:\bfrom|\bimport)\s*["'`]([^"'`]+)["'`]/g)].map((m) => m[1]);
	const dynamics = [...code.matchAll(/\bimport\s*\(\s*["'`]([^"'`$]+)["'`]\s*\)/g)].map((m) => m[1]);
	return { statics: statics.filter(local), dynamics: dynamics.filter(local) };
}

/** Resolve an import specifier against the URL path of the module it is in. */
function resolveModule(spec, fromUrl) {
	return new URL(spec, `http://x${fromUrl}`).pathname;
}

async function checkJSBudget(pages) {
	const over = [];
	const cache = new Map(); // url -> { size, statics, dynamics }
	async function mod(url) {
		if (!cache.has(url)) {
			const path = join(DIST, url);
			if (!existsSync(path)) cache.set(url, null);
			else {
				const code = await readFile(path, 'utf8');
				const { statics, dynamics } = moduleImports(code);
				cache.set(url, {
					size: Buffer.byteLength(code, 'utf8'),
					statics: statics.map((s) => resolveModule(s, url)),
					dynamics: dynamics.map((s) => resolveModule(s, url)),
				});
			}
		}
		return cache.get(url);
	}
	// Walk static edges from `roots` into `seen`; collect dynamic edges as we go.
	async function closure(roots, seen, dyn) {
		const queue = [...roots];
		while (queue.length) {
			const url = queue.pop();
			if (seen.has(url)) continue;
			const m = await mod(url);
			if (!m) continue;
			seen.add(url);
			queue.push(...m.statics);
			dyn.push(...m.dynamics);
		}
	}
	const sum = async (set) => {
		let n = 0;
		for (const url of set) n += (await mod(url)).size;
		return n;
	};

	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		const rel = page.replace(/\\/g, '/');

		// Initial: module scripts the page loads, everything they statically
		// import, and inline scripts (and their static imports).
		let inline = 0;
		const roots = [];
		const dyn = [];
		for (const m of html.matchAll(/<script[^>]*\bsrc="([^"]+)"/g)) {
			if (m[1].startsWith('/')) roots.push(m[1]);
		}
		// Inline scripts ship on the page itself and count too — except JSON-LD,
		// which is a data block the browser never executes. Charging structured
		// data to a budget that exists to keep framework runtimes out would make
		// the two compete, and the answer would be to drop the metadata.
		const pageUrl = '/' + rel.slice(DIST.length + 1).replace(/index\.html$/, '');
		for (const m of html.matchAll(/<script(?![^>]*\bsrc=)([^>]*)>([\s\S]*?)<\/script>/g)) {
			if (/type="application\/ld\+json"/.test(m[1])) continue;
			inline += Buffer.byteLength(m[2], 'utf8');
			const { statics, dynamics } = moduleImports(m[2]);
			roots.push(...statics.map((s) => resolveModule(s, pageUrl)));
			dyn.push(...dynamics.map((s) => resolveModule(s, pageUrl)));
		}
		const initialSet = new Set();
		await closure(roots, initialSet, dyn);
		const initial = inline + (await sum(initialSet));

		// Deferred: what dynamic imports would add on top, transitively.
		const deferredSet = new Set(initialSet);
		const more = [];
		while (dyn.length) {
			await closure(dyn.splice(0), deferredSet, more);
			dyn.push(...more.splice(0));
		}
		for (const url of initialSet) deferredSet.delete(url);
		const deferred = await sum(deferredSet);

		console.log(
			`  ${(initial / 1024).toFixed(1)} KiB JS + ${(deferred / 1024).toFixed(1)} KiB deferred  ${rel}  ` +
				`(${initialSet.size} module(s) on load, ${deferredSet.size} on demand)`
		);
		if (initial > JS_BUDGET_BYTES) {
			over.push(`${rel}: ${initial} bytes of client JS on load, over the ${JS_BUDGET_BYTES} budget`);
		}
		if (deferred > DEFERRED_JS_BUDGET_BYTES) {
			over.push(
				`${rel}: ${deferred} bytes of client JS behind dynamic import(), over the ${DEFERRED_JS_BUDGET_BYTES} deferred budget`
			);
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
		// Starlight hardcodes og:type="article". True for the 47 docs pages, false
		// for the splash homepage and the 404, which are not documents.
		const ogType = html.match(/<meta property="og:type" content="([^"]*)"/)?.[1];
		const shouldBeWebsite = rel === 'dist/index.html' || rel === 'dist/404.html';
		if (shouldBeWebsite && ogType !== 'website') {
			problems.push(`${rel}: og:type is "${ogType}" — this page is a website, not an article`);
		}
		if (!shouldBeWebsite && ogType !== 'article') {
			problems.push(`${rel}: og:type is "${ogType}" — a documentation page is an article`);
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
		} else if (n < DESC_MIN) {
			problems.push(`${rel}: description is ${n} chars, under ${DESC_MIN} — too short to be used as the snippet`);
		}

		const prior = byDesc.get(desc);
		if (prior) problems.push(`${rel}: description duplicates ${prior}`);
		else byDesc.set(desc, rel);
	}
	return problems;
}

/**
 * Structured data.
 *
 * JSON-LD fails silently in both directions: malformed JSON is dropped without
 * a word, and INVENTED data is worse than none — a fabricated aggregateRating
 * is the classic way a site gets its structured data distrusted wholesale. So
 * this asserts the block parses, says what it claims to be, and contains no
 * node the repo cannot back up.
 */
const FABRICATED_TYPES = ['aggregateRating', 'review', 'ratingValue', 'reviewCount'];

async function checkStructuredData(pages) {
	const problems = [];
	let found = 0;

	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		const rel = page.replace(/\\/g, '/');
		for (const m of html.matchAll(
			/<script type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/g
		)) {
			found++;
			let data;
			try {
				data = JSON.parse(m[1]);
			} catch (e) {
				problems.push(`${rel}: JSON-LD does not parse — ${e.message}`);
				continue;
			}
			if (data['@context'] !== 'https://schema.org') {
				problems.push(`${rel}: JSON-LD @context is not https://schema.org`);
			}
			const nodes = data['@graph'] ?? [data];
			for (const node of nodes) {
				if (!node['@type']) problems.push(`${rel}: a JSON-LD node has no @type`);
			}
			for (const key of FABRICATED_TYPES) {
				if (m[1].includes(`"${key}"`)) {
					problems.push(
						`${rel}: JSON-LD contains "${key}" — rowshape has no ratings or reviews, and ` +
							'claiming them is how a site gets its structured data ignored entirely'
					);
				}
			}
		}
	}

	// Coverage, not just validity. Every content page carries structured data:
	// the homepage describes the software, everything else describes where it
	// sits (BreadcrumbList) and what it is (TechArticle). The 404 carries none,
	// deliberately — it is not a document, and it is out of the sitemap for the
	// same reason.
	for (const page of pages) {
		const rel = page.replace(/\\/g, '/');
		const html = await readFile(page, 'utf8');
		const has = (type) => html.includes(`"@type":"${type}"`);
		const isHome = rel === 'dist/index.html';
		const is404 = rel === 'dist/404.html';

		if (is404) {
			if (html.includes('application/ld+json')) {
				problems.push(`${rel}: the 404 page should carry no structured data`);
			}
			continue;
		}
		if (isHome) {
			if (!has('SoftwareApplication')) problems.push(`${rel}: no SoftwareApplication`);
			continue;
		}
		if (!has('BreadcrumbList')) problems.push(`${rel}: no BreadcrumbList`);
		if (!has('TechArticle')) problems.push(`${rel}: no TechArticle`);

		// A breadcrumb whose positions are not 1..n, or whose trail does not end
		// at the page it is on, is worse than none: it describes a site structure
		// that is not there.
		const m = html.match(/<script type="application\/ld\+json"[^>]*>([\s\S]*?)<\/script>/);
		if (!m) continue;
		let graph;
		try {
			graph = JSON.parse(m[1])['@graph'] ?? [];
		} catch {
			continue; // already reported above
		}
		const crumb = graph.find((n) => n['@type'] === 'BreadcrumbList');
		if (!crumb) continue;
		const items = crumb.itemListElement ?? [];
		items.forEach((item, i) => {
			if (item.position !== i + 1) {
				problems.push(`${rel}: breadcrumb position ${item.position} at index ${i}`);
			}
		});
		const route = '/' + rel.replace(/^dist\//, '').replace(/index\.html$/, '');
		const last = items[items.length - 1];
		if (last && new URL(last.item).pathname !== route) {
			problems.push(
				`${rel}: breadcrumb ends at ${new URL(last.item).pathname}, not at ${route}`
			);
		}
	}

	if (found === 0) problems.push('no JSON-LD anywhere in dist/');
	return problems;
}

/**
 * The sitemap, and the freshness dates in it.
 *
 * A <lastmod> is only a useful hint if it is true. The two ways to make it
 * false are both silent: a shallow clone gives every page the same date (which
 * gen-lastmod.mjs refuses outright), and a date drawn from file mtime rather
 * than git makes every page look modified at checkout time. So this asserts the
 * dates are real ISO timestamps, are not in the future, and — the part that
 * matters — AGREE with the dateModified the page itself publishes. Two
 * freshness claims that disagree are worse than one.
 */
async function checkSitemap(pages) {
	const problems = [];
	const path = join(DIST, 'sitemap-0.xml');
	if (!existsSync(path)) return ['no sitemap-0.xml in dist/'];

	const xml = await readFile(path, 'utf8');
	const entries = [...xml.matchAll(/<url><loc>([^<]+)<\/loc>(?:<lastmod>([^<]+)<\/lastmod>)?/g)];
	if (entries.length === 0) return ['sitemap-0.xml contains no <url> entries'];

	// What the pages themselves claim, so the two sources can be compared.
	const claimed = new Map();
	for (const page of pages) {
		const rel = page.replace(/\\/g, '/');
		const route = '/' + rel.replace(/^dist\//, '').replace(/index\.html$/, '');
		const m = (await readFile(page, 'utf8')).match(/"dateModified":"([^"]+)"/);
		if (m) claimed.set(route, m[1]);
	}

	const now = Date.now();
	for (const [, loc, lastmod] of entries) {
		const route = new URL(loc).pathname;
		if (!lastmod) {
			problems.push(`sitemap: ${route} has no <lastmod>`);
			continue;
		}
		const t = Date.parse(lastmod);
		if (Number.isNaN(t)) {
			problems.push(`sitemap: ${route} has an unparseable <lastmod> "${lastmod}"`);
			continue;
		}
		if (t > now + 60_000) {
			problems.push(`sitemap: ${route} claims a <lastmod> in the future — ${lastmod}`);
		}
		const page = claimed.get(route);
		// Compared as instants: the sitemap normalizes to UTC, the page keeps the
		// commit's own offset, and those are the same moment written two ways.
		if (page && Date.parse(page) !== t) {
			problems.push(
				`sitemap: ${route} <lastmod> ${lastmod} disagrees with the page's dateModified ${page}`
			);
		}
	}

	// The 404 is not a document and must not be advertised as one.
	if (entries.some(([, loc]) => new URL(loc).pathname === '/404/')) {
		problems.push('sitemap: /404/ is listed');
	}
	return problems;
}

/**
 * The finding catalog must link every finding page.
 *
 * /findings/ is the hub: it is what the sidebar, the homepage and every
 * external mention point at, and a page it does not list is reachable only from
 * the sidebar — which is nav, not content. The list is maintained by hand, so
 * the failure is adding a finding page and forgetting the index. That is
 * exactly the kind of omission nobody notices, because the new page still
 * builds, still renders, and still appears in the sidebar.
 */
async function checkCatalogCoverage(pages) {
	const index = join(DIST, 'findings', 'index.html');
	if (!existsSync(index)) return ['no findings catalog at dist/findings/index.html'];

	const html = await readFile(index, 'utf8');
	const main = html.match(/<main[\s\S]*?<\/main>/)?.[0] ?? '';
	const linked = new Set();
	for (const m of main.matchAll(/href="([^"#?]+)"/g)) {
		const slug = m[1].replace(/\/$/, '').split('/').pop();
		if (slug) linked.add(slug);
	}

	const problems = [];
	for (const page of pages) {
		const rel = page.replace(/\\/g, '/');
		const m = rel.match(/^dist\/findings\/(rs-[a-z]+-\d+)\/index\.html$/);
		if (m && !linked.has(m[1])) {
			problems.push(`findings catalog does not link ${m[1]} — add it to findings/index.md`);
		}
	}
	return problems;
}

/**
 * No page links to itself from its own content.
 *
 * A self-link is dead weight to a reader and is read as nothing by a crawler.
 * The finding-code autolinker skips them by construction; this catches the
 * hand-written kind.
 */
async function checkSelfLinks(pages) {
	const problems = [];
	for (const page of pages) {
		const rel = page.replace(/\\/g, '/');
		const route = '/' + rel.replace(/^dist\//, '').replace(/index\.html$/, '');
		const main = (await readFile(page, 'utf8')).match(/<main[\s\S]*?<\/main>/)?.[0] ?? '';
		for (const m of main.matchAll(/href="(\/[^"#?]*)"/g)) {
			const target = m[1].endsWith('/') ? m[1] : `${m[1]}/`;
			if (target === route) problems.push(`${rel}: links to itself`);
		}
	}
	return problems;
}

/**
 * llms.txt lists every page the sitemap does.
 *
 * It is generated from the content collection, so it cannot go stale on its
 * own — but SECTIONS in the endpoint is hand-ordered, and a section added to
 * the site and not to that list would quietly fall into the catch-all or, if
 * the catch-all were ever removed, out of the file entirely. This asserts the
 * two indexes agree.
 */
async function checkLlmsTxt() {
	const path = join(DIST, 'llms.txt');
	if (!existsSync(path)) return ['no llms.txt in dist/'];

	const text = await readFile(path, 'utf8');
	const listed = new Set([...text.matchAll(/\]\((https?:\/\/[^)]+)\)/g)].map((m) => m[1]));

	const problems = [];
	const sitemapPath = join(DIST, 'sitemap-0.xml');
	if (!existsSync(sitemapPath)) return ['no sitemap to compare llms.txt against'];
	const xml = await readFile(sitemapPath, 'utf8');

	for (const m of xml.matchAll(/<loc>([^<]+)<\/loc>/g)) {
		const url = m[1];
		// The homepage is the file's own subject, described in the header rather
		// than listed as a bullet.
		if (new URL(url).pathname === '/') continue;
		if (!listed.has(url)) problems.push(`llms.txt does not list ${url}`);
	}
	for (const url of listed) {
		if (new URL(url).hostname !== 'rowshape.com') continue;
		if (!xml.includes(`<loc>${url}</loc>`)) {
			problems.push(`llms.txt lists ${url}, which is not in the sitemap`);
		}
	}
	return problems;
}

/**
 * Exactly one h1 per page, and it says something.
 *
 * The homepage's h1 was the bare word "rowshape" — the one thing a reader who
 * arrived already knows, and the one thing a reader who has not heard of it
 * cannot use. An h1 that is only the site name is the heading equivalent of the
 * "rowshape | rowshape" title iteration 3 removed, and the brand is already in
 * the site header. Zero or several h1s is the other failure: it leaves the page
 * with no single statement of what it is about.
 */
const GENERIC_LINK_TEXT = new Set([
	'here',
	'click here',
	'read more',
	'more',
	'link',
	'this',
	'this link',
	'learn more',
	'see here',
	'read this',
	'see',
]);

async function checkHeadings(pages) {
	const problems = [];
	for (const page of pages) {
		const rel = page.replace(/\\/g, '/');
		const html = await readFile(page, 'utf8');
		const h1s = [...html.matchAll(/<h1[^>]*>([\s\S]*?)<\/h1>/g)].map((m) =>
			m[1].replace(/<[^>]+>/g, '').trim()
		);
		if (h1s.length !== 1) {
			problems.push(`${rel}: ${h1s.length} h1 elements, expected exactly 1`);
			continue;
		}
		if (h1s[0].toLowerCase() === 'rowshape') {
			problems.push(
				`${rel}: h1 is just the site name — say what the page is about; the brand is in the header`
			);
		}

		// Everything below is about the page's own content, so read <main> only:
		// the sidebar and header have their own headings and are nav, not prose.
		const main = html.match(/<main[\s\S]*?<\/main>/)?.[0] ?? '';
		const headings = [...main.matchAll(/<h([1-6])[^>]*>([\s\S]*?)<\/h\1>/g)].map((m) => ({
			level: Number(m[1]),
			text: m[2].replace(/<[^>]+>/g, '').replace(/\s+/g, ' ').trim(),
		}));

		// A skipped level (h2 straight to h4) breaks the document outline a screen
		// reader navigates by, and breaks the section boundaries a search engine
		// uses to pull a passage out of a long page. Audited across all 49 pages
		// before this was written: there were none, which is what makes it cheap
		// to require.
		let previous = 0;
		for (const heading of headings) {
			if (previous && heading.level > previous + 1) {
				problems.push(
					`${rel}: heading level jumps h${previous} -> h${heading.level} at "${heading.text.slice(0, 40)}"`
				);
			}
			if (!heading.text) problems.push(`${rel}: an h${heading.level} is empty`);
			previous = heading.level;
		}

		// Two headings with the same slug means one of the two #anchors silently
		// points at the other — including the anchors in the table of contents.
		const ids = new Set();
		for (const m of main.matchAll(/<h[2-6][^>]*\bid="([^"]+)"/g)) {
			if (ids.has(m[1])) problems.push(`${rel}: duplicate heading anchor #${m[1]}`);
			ids.add(m[1]);
		}

		// "Click here" tells a reader scanning links, and a crawler weighing them,
		// nothing about the destination.
		for (const m of main.matchAll(/<a\b[^>]*>([\s\S]*?)<\/a>/g)) {
			const text = m[1]
				.replace(/<[^>]+>/g, '')
				.replace(/\s+/g, ' ')
				.trim()
				.toLowerCase()
				.replace(/[.,:;!?]$/, '');
			if (GENERIC_LINK_TEXT.has(text)) {
				problems.push(`${rel}: link text "${text}" says nothing about where it goes`);
			}
		}
	}
	return problems;
}

/**
 * noindex, exactly where it belongs and nowhere else.
 *
 * The 404 page is served for missing paths with a real 404 status, but it also
 * lives at its own URL: /404 returns 200 (verified against the live site), which
 * is a soft 404 — an indexable page whose whole content is "this is not a page".
 * noindex is the remedy.
 *
 * The opposite mistake is far more expensive and far quieter: a stray noindex on
 * a real page removes it from search entirely, and nothing about the page looks
 * wrong. So this asserts both directions.
 */
async function checkNoindex(pages) {
	const problems = [];
	for (const page of pages) {
		const rel = page.replace(/\\/g, '/');
		const html = await readFile(page, 'utf8');
		const robots = html.match(/<meta name="robots" content="([^"]*)"/)?.[1] ?? '';
		const noindexed = /noindex/i.test(robots);
		const is404 = rel === 'dist/404.html';

		if (is404 && !noindexed) {
			problems.push(`${rel}: the 404 page must be noindex — /404 returns 200 and is otherwise indexable`);
		}
		if (!is404 && noindexed) {
			problems.push(`${rel}: noindex on a real page removes it from search entirely`);
		}
	}
	return problems;
}

/**
 * The critical rendering path: how heavy it is, and whose it is.
 *
 * Measured before anything was asserted. The site loads no web fonts (Starlight
 * resolves to a system stack), has no <img> elements, and makes no third-party
 * requests at all — so Core Web Vitals here are good by construction rather
 * than by tuning, and there was nothing to speed up. What there IS to do is
 * stop that quietly changing.
 *
 * The regressions this catches:
 *
 *   A THIRD-PARTY ORIGIN in the critical path. One Google Fonts link, one
 *   analytics script, one CDN stylesheet, and first paint now waits on a DNS
 *   lookup, a TLS handshake and someone else's uptime. On this site it is also
 *   a privacy question: rowshape's docs argue about what leaves your database,
 *   and a docs page that phones a third party undercuts the argument.
 *
 *   CSS WEIGHT, which blocks first paint the way the JS budget's target blocks
 *   interaction.
 *
 *   AN IMAGE WITHOUT DIMENSIONS. There are none today. The first one added
 *   without width and height reflows the page as it loads, which is Cumulative
 *   Layout Shift, and it is much easier to require than to retrofit.
 */
async function checkCriticalPath(pages) {
	const problems = [];
	const over = [];

	for (const page of pages) {
		const rel = page.replace(/\\/g, '/');
		const html = await readFile(page, 'utf8');

		let cssBytes = 0;
		for (const m of html.matchAll(/<link[^>]*rel="stylesheet"[^>]*>/g)) {
			const tag = m[0];
			const href = tag.match(/href="([^"]+)"/)?.[1];
			if (!href) continue;
			if (/^https?:\/\//.test(href)) {
				problems.push(`${rel}: third-party stylesheet ${href} — first paint would wait on another origin`);
				continue;
			}
			// A print stylesheet is not render-blocking for the screen.
			if (/media="print"/.test(tag)) continue;
			const path = join(DIST, href);
			if (existsSync(path)) cssBytes += (await stat(path)).size;
		}
		if (cssBytes > CSS_BUDGET_BYTES) {
			over.push(`${rel}: ${cssBytes} bytes of render-blocking CSS, over the ${CSS_BUDGET_BYTES} budget`);
		}

		for (const m of html.matchAll(/<script[^>]*\bsrc="(https?:\/\/[^"]+)"/g)) {
			problems.push(`${rel}: third-party script ${m[1]}`);
		}

		for (const m of html.matchAll(/<img[^>]*>/g)) {
			const tag = m[0];
			const src = tag.match(/src="([^"]+)"/)?.[1] ?? '(no src)';
			if (/^https?:\/\//.test(src)) problems.push(`${rel}: third-party image ${src}`);
			const hasDims = /\bwidth="/.test(tag) && /\bheight="/.test(tag);
			// An SVG sized entirely in CSS is not a layout-shift risk the same way.
			if (!hasDims) {
				problems.push(`${rel}: <img src="${src}"> has no width/height — it will shift the layout as it loads`);
			}
		}
	}
	return [...problems, ...over];
}

// ---------------------------------------------------------------------------
// Security: response headers, CSP, security.txt.
//
// A header file is invisible in review — nothing renders it, a typo in it fails
// silently, and Cloudflare only tells you by not sending the header. So the
// guard reads dist/_headers the way Cloudflare does and asserts the result.

/** Cloudflare Pages' documented limits on _headers. */
const HEADERS_MAX_RULES = 100;
const HEADERS_MAX_LINE = 2000;

/**
 * Parse a Cloudflare _headers file: an unindented line opens a rule for a path,
 * indented `Name: value` lines belong to it, `! Name` detaches. Returns rules in
 * file order with header names lowercased (HTTP header names are
 * case-insensitive, and so is Cloudflare's matching of them).
 */
function parseHeadersFile(text) {
	const rules = [];
	let current = null;
	for (const [i, raw] of text.split('\n').entries()) {
		const line = raw.replace(/\r$/, '');
		if (!line.trim() || line.trim().startsWith('#')) continue;
		if (!/^\s/.test(line)) {
			current = { path: line.trim(), headers: new Map(), detach: [], line: i + 1 };
			rules.push(current);
			continue;
		}
		if (!current) throw new Error(`_headers line ${i + 1}: header before any path`);
		const body = line.trim();
		if (body.startsWith('!')) {
			current.detach.push(body.slice(1).trim().toLowerCase());
			continue;
		}
		const colon = body.indexOf(':');
		if (colon < 1) throw new Error(`_headers line ${i + 1}: not "Name: value": ${body}`);
		const name = body.slice(0, colon).trim().toLowerCase();
		const value = body.slice(colon + 1).trim();
		current.headers.set(name, [...(current.headers.get(name) ?? []), value]);
	}
	return rules;
}

/**
 * What /* must send. Exact values, not "present": `max-age=0` is a present HSTS
 * header too, and the value is the whole point.
 */
const REQUIRED_SITE_HEADERS = {
	'strict-transport-security': 'max-age=31536000; includeSubDomains',
	'x-content-type-options': 'nosniff',
	'referrer-policy': 'strict-origin-when-cross-origin',
	'permissions-policy': 'camera=(), microphone=(), geolocation=(), payment=(), usb=()',
	'x-frame-options': 'DENY',
	'cross-origin-opener-policy': 'same-origin',
};

/**
 * A content-hashed asset name. Vite: `name.XXXXXXXX.ext`, 8 base64url chars.
 * Expressive Code: `ec.xxxxx.ext`, 5 base36 chars of a djb2a hash of the content
 * (astro-expressive-code's getHashedRouteWithContent). Both are content hashes,
 * which is what makes `immutable` safe. A loose "has a dotted segment" test would
 * pass Vite's `[name].js` output, whose names are long and underscore-heavy.
 */
const HASHED_ASSET = /(?:\.[A-Za-z0-9_-]{8}|^ec\.[0-9a-z]{5})\.[a-z0-9]+$/;

async function checkHeaders() {
	const path = join(DIST, '_headers');
	if (!existsSync(path)) return ['no _headers in dist/ — public/_headers was not copied'];
	const text = await readFile(path, 'utf8');
	const problems = [];

	for (const [i, line] of text.split('\n').entries()) {
		if (line.length > HEADERS_MAX_LINE) {
			problems.push(`_headers line ${i + 1} is ${line.length} chars; Cloudflare drops lines over ${HEADERS_MAX_LINE}`);
		}
	}
	let rules;
	try {
		rules = parseHeadersFile(text);
	} catch (e) {
		return [...problems, e.message];
	}
	if (rules.length > HEADERS_MAX_RULES) {
		problems.push(`_headers has ${rules.length} rules; Cloudflare ignores everything past ${HEADERS_MAX_RULES}`);
	}

	const site = rules.find((r) => r.path === '/*');
	if (!site) {
		problems.push('_headers has no /* rule — no page gets the security headers');
	} else {
		for (const [name, want] of Object.entries(REQUIRED_SITE_HEADERS)) {
			const got = site.headers.get(name);
			if (!got) problems.push(`_headers /* does not set ${name}`);
			else if (got.length > 1 || got[0] !== want) problems.push(`_headers /* ${name} is "${got.join(', ')}", want "${want}"`);
		}
		// Cloudflare JOINS a header set by two matching rules. Cache-Control on /*
		// would be glued onto /_astro/*'s and the result would no longer say
		// `immutable` cleanly — so the broad rule must not set it at all.
		if (site.headers.has('cache-control')) problems.push('_headers /* sets Cache-Control, which every narrower rule would inherit joined to its own');
	}

	const astro = rules.find((r) => r.path === '/_astro/*');
	const cc = astro?.headers.get('cache-control')?.join(', ') ?? '';
	if (!/\bmax-age=31536000\b/.test(cc) || !/\bimmutable\b/.test(cc)) {
		problems.push(`_headers /_astro/* Cache-Control is "${cc}", want max-age=31536000 and immutable`);
	}

	// `immutable` on a file whose name does not change when its content does
	// means visitors keep the old file for a year. Every file the rule covers
	// must be content-addressed.
	const astroDir = join(DIST, '_astro');
	if (existsSync(astroDir)) {
		for (const file of await walk(astroDir, () => true)) {
			const name = file.slice(astroDir.length + 1);
			if (!HASHED_ASSET.test(name.split('/').pop())) {
				problems.push(`dist/_astro/${name} has no content hash in its name, but /_astro/* is served immutable`);
			}
		}
	}
	return problems;
}

/**
 * Tables a keyboard can scroll, and headers a screen reader can read.
 *
 * At 375px seven content tables scrolled sideways with nothing focusable to
 * scroll them by (axe scrollable-region-focusable), and the privacy matrix
 * opened with an empty <th>, so its first column was announced as nothing.
 * src/plugins/rehype-scrollable-tables.mjs now wraps every table in a named,
 * focusable region. This holds the line: a table that reaches dist/ any other
 * way — an .mdx page, raw HTML, a plugin-order change — fails here, as does a
 * header cell with no text, or two table regions on one page with one name.
 */
async function checkTables(pages) {
	const problems = [];
	const WRAP = /<div class="table-scroll" role="region" tabindex="0" aria-label="([^"]+)">\s*$/;
	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		const rel = page.replace(/\\/g, '/');
		const labels = new Set();
		for (const m of html.matchAll(/<table\b/g)) {
			const wrap = html.slice(Math.max(0, m.index - 300), m.index).match(WRAP);
			if (!wrap) {
				problems.push(`${rel}: a <table> outside a focusable, labelled .table-scroll region`);
				continue;
			}
			if (labels.has(wrap[1])) problems.push(`${rel}: two table regions named "${wrap[1]}"`);
			labels.add(wrap[1]);
		}
		for (const m of html.matchAll(/<th\b([^>]*)>([\s\S]*?)<\/th>/g)) {
			const text = m[2].replace(/<[^>]*>/g, '').replace(/&nbsp;|&#160;/g, ' ').trim();
			if (!text && !/\baria-label="[^"]+"/.test(m[1])) problems.push(`${rel}: empty <th> — a header cell with no name`);
		}
	}
	return problems;
}

/**
 * The CSP that gen-csp.mjs wrote must allow every inline script dist/ actually
 * contains. This re-derives the hashes from the built pages rather than trusting
 * the generator: a page added by a later build step, a generator that silently
 * skipped a file, or a dist/ built with plain `astro build` (no gen-csp) would
 * all ship a policy that blocks the theme bootstrap — a broken site that no
 * build error ever mentions, because the browser reports it only in the
 * visitor's console.
 */
async function checkCSP(pages) {
	const path = join(DIST, '_headers');
	if (!existsSync(path)) return []; // checkHeaders already reports this
	const rules = parseHeadersFile(await readFile(path, 'utf8'));
	const site = rules.find((r) => r.path === '/*');
	const problems = [];
	if (rules.some((r) => r.headers.has('content-security-policy-report-only'))) {
		problems.push('_headers sets Content-Security-Policy-Report-Only; the policy is verified and meant to be enforced');
	}
	const csp = site?.headers.get('content-security-policy');
	if (!csp) return [...problems, '_headers /* has no Content-Security-Policy — did the build skip scripts/gen-csp.mjs?'];
	if (csp.length > 1) return [...problems, `_headers /* sets Content-Security-Policy ${csp.length} times; Cloudflare would join them into one invalid header`];

	const directives = new Map(
		csp[0].split(';').map((d) => d.trim().split(/\s+/)).filter((d) => d[0]).map(([k, ...v]) => [k.toLowerCase(), v])
	);
	const scriptSrc = directives.get('script-src') ?? [];
	// Hashes are worthless next to these: 'unsafe-inline' is ignored when a hash
	// is present in CSP2+ browsers but honoured by older ones, and 'unsafe-eval'
	// reopens eval() of strings, which 'wasm-unsafe-eval' exists to avoid.
	for (const bad of ["'unsafe-inline'", "'unsafe-eval'", '*', 'data:', 'http:', 'https:']) {
		if (scriptSrc.includes(bad)) problems.push(`CSP script-src allows ${bad}`);
	}
	for (const [d, want] of [['object-src', "'none'"], ['frame-ancestors', "'none'"], ['base-uri', "'self'"]]) {
		if (directives.get(d)?.join(' ') !== want) problems.push(`CSP ${d} is "${directives.get(d)?.join(' ') ?? '(missing)'}", want ${want}`);
	}

	const allowed = new Set(scriptSrc);
	const missing = new Map(); // hash -> first page that needs it
	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		for (const body of executableInlineScripts(html)) {
			const h = scriptHash(body);
			if (!allowed.has(h) && !missing.has(h)) missing.set(h, page);
		}
		for (const h of inlineHandlers(html)) problems.push(`${page}: inline ${h} — CSP blocks it; move it into a script`);
	}
	for (const [h, page] of missing) {
		problems.push(`CSP script-src lacks ${h}, an inline script on ${page} (and maybe others) — the browser will refuse to run it`);
	}
	return problems;
}

/**
 * The icon set and the social card's weight.
 *
 * /favicon.ico is fetched unprompted — by feed readers, unfurlers, bookmark
 * sync — whatever the HTML says, so a missing one is a 404 in every log and a
 * blank tile in every one of those places. The site shipped only Starlight's
 * placeholder star as an SVG until SITE-7. og.png is downloaded by every link
 * preview, usually on a phone; palette-encoding took it from 55 KiB to 17, and
 * 40 KiB is where a truecolour regression would land. The PNG headers are read
 * by hand, as in checkSocialCard, to keep this script dependency-free.
 */
const OG_MAX_BYTES = 40 * 1024;

async function checkIcons() {
	const problems = [];
	const ico = join(DIST, 'favicon.ico');
	if (!existsSync(ico)) problems.push('dist/favicon.ico missing — run `npm run icons` and commit public/favicon.ico');
	else {
		const buf = await readFile(ico);
		// ICONDIR: reserved 0, type 1 (icon), count >= 1; first entry width byte.
		if (buf.length < 22 || buf.readUInt16LE(0) !== 0 || buf.readUInt16LE(2) !== 1 || buf.readUInt16LE(4) < 1) {
			problems.push('dist/favicon.ico is not an ICO file');
		} else if (buf[6] !== 32 || buf[7] !== 32) {
			problems.push(`dist/favicon.ico first image is ${buf[6] || 256}x${buf[7] || 256}, expected 32x32`);
		}
	}
	const touch = join(DIST, 'apple-touch-icon.png');
	if (!existsSync(touch)) problems.push('dist/apple-touch-icon.png missing — run `npm run icons`');
	else {
		const size = pngSize(await readFile(touch));
		if (!size || size.width !== 180 || size.height !== 180) problems.push('dist/apple-touch-icon.png is not a 180x180 PNG');
	}
	const head = await readFile(join(DIST, 'index.html'), 'utf8');
	if (!/<link rel="apple-touch-icon" href="\/apple-touch-icon\.png"/.test(head)) {
		problems.push('homepage does not link apple-touch-icon');
	}
	const og = join(DIST, 'og.png');
	if (existsSync(og)) {
		const n = (await stat(og)).size;
		if (n > OG_MAX_BYTES) problems.push(`dist/og.png is ${n} bytes, over ${OG_MAX_BYTES} — palette-encode it (npm run og)`);
	}
	return problems;
}

/**
 * No public page cites a document the reader cannot open.
 *
 * The code and the catalog were written against an internal product plan, and
 * its section numbers ("PRD §10") and invariant names ("INV-UNIQUENESS") leaked
 * into ~35 places on the site — the finding pages' References lists, the GitHub
 * Action guide, the agents page. To a reader they are citations to nothing: the
 * plan is not published. Each one was rewritten at its source to state the
 * commitment in plain words, or dropped; this keeps them from coming back
 * through the next generated page.
 *
 * The fixture spec IS published, so "RFC §8.3" is allowed — but only as a link
 * to the on-site spec page, which is where it can be followed from. That covers
 * the HTML <title> and meta tags too, where a link is impossible, so a section
 * number there fails.
 */
const PLAN_REF = /PRD §|\bINV-[A-Z]/;
const SPEC_REF = /\bRFC(?:-0001)?\s*§/;

async function checkInternalReferences(pages) {
	const problems = [];
	const report = (file, text, specText = text) => {
		const plan = text.match(PLAN_REF);
		if (plan) {
			const at = text.indexOf(plan[0]);
			problems.push(`${file}: internal planning reference "${text.slice(at, at + 24).replace(/\s+/g, ' ')}…"`);
		}
		const spec = specText.match(SPEC_REF);
		if (spec) {
			const at = specText.indexOf(spec[0]);
			problems.push(`${file}: spec reference "${specText.slice(at, at + 16)}" is not a link to /spec/`);
		}
	};
	for (const page of pages) {
		const html = await readFile(page, 'utf8');
		// A spec citation inside a link to the spec page is the allowed form.
		const linked = html.replace(/<a\b[^>]*href="\/spec\/[^"]*"[^>]*>[\s\S]*?<\/a>/g, '');
		// Quoted terminal output (`rowshape explain` prints "References: RFC §7.4")
		// cannot carry a link, and rewriting it would misquote the binary. The spec
		// is published, so an RFC citation inside <pre> (and the copy button's
		// data-code copy of it) is allowed; a PRD one is not, anywhere.
		report(page, linked, linked.replace(/<pre\b[\s\S]*?<\/pre>/g, '').replace(/\sdata-code="[^"]*"/g, ''));
	}
	const llms = join(DIST, 'llms.txt');
	if (existsSync(llms)) {
		report(llms, (await readFile(llms, 'utf8')).replace(/\[[^\]]*\]\(https?:\/\/[^)]*\/spec\/[^)]*\)/g, ''));
	}
	return problems;
}

/** How far ahead of the Expires date the build starts warning. */
const SECURITY_TXT_WARN_DAYS = 30;

/**
 * /.well-known/security.txt (RFC 9116) must exist, name a Contact, and carry an
 * Expires that has not passed and is under a year out (the RFC's own
 * recommendation, so a stale file cannot claim to be current for long).
 *
 * Expiry inside 30 days is a WARNING, not a failure, deliberately: the date runs
 * out on the calendar, not because of any change, so failing would block an
 * unrelated docs fix with no warning at all. Thirty days of yellow output on
 * every build is the notice; once the date has actually passed the file is
 * telling researchers something false, and that fails.
 */
async function checkSecurityTxt(now = new Date()) {
	const path = join(DIST, '.well-known', 'security.txt');
	if (!existsSync(path)) return ['no dist/.well-known/security.txt — public/.well-known/security.txt was not copied'];
	const fields = new Map();
	for (const line of (await readFile(path, 'utf8')).split('\n')) {
		const m = line.match(/^([A-Za-z-]+):\s*(.+?)\s*$/);
		if (m) fields.set(m[1].toLowerCase(), [...(fields.get(m[1].toLowerCase()) ?? []), m[2]]);
	}
	const problems = [];
	const contact = fields.get('contact') ?? [];
	if (!contact.length) problems.push('security.txt has no Contact');
	for (const c of contact) {
		// RFC 9116 §2.5.3: a web URI must be https.
		if (!/^(https:\/\/|mailto:|tel:)/.test(c)) problems.push(`security.txt Contact is not https:, mailto: or tel:: ${c}`);
	}
	const expires = fields.get('expires') ?? [];
	if (expires.length !== 1) {
		problems.push(`security.txt must have exactly one Expires, has ${expires.length}`);
	} else {
		const when = new Date(expires[0]);
		const days = (when - now) / 86_400_000;
		if (Number.isNaN(when.getTime())) problems.push(`security.txt Expires is not an RFC 3339 date: ${expires[0]}`);
		else if (days <= 0) problems.push(`security.txt expired on ${expires[0]} — set a new Expires under a year out`);
		else if (days > 366) problems.push(`security.txt Expires ${expires[0]} is more than a year out; RFC 9116 recommends less`);
		else if (days < SECURITY_TXT_WARN_DAYS) {
			console.warn(`WARNING: security.txt expires in ${Math.ceil(days)} day(s) (${expires[0]}); renew public/.well-known/security.txt\n`);
		}
	}
	const canonical = fields.get('canonical') ?? [];
	if (!canonical.includes('https://rowshape.com/.well-known/security.txt')) {
		problems.push('security.txt Canonical is not https://rowshape.com/.well-known/security.txt');
	}
	return problems;
}

/**
 * The finding sidebar lists every finding by its code, and the GitHub Action is
 * reachable from it.
 *
 * Finding items are labelled by code via generated frontmatter, and grouped by
 * a helper that reads the findings directory. Either half can quietly regress
 * to the old shape — a generator change that drops `sidebar.label` brings back
 * three-line titles, and the Action page is hidden from its autogenerated group
 * so it exists in the sidebar only through the Integrations entry.
 */
async function checkFindingsSidebar(pages) {
	const page = join(DIST, 'findings', 'index.html');
	if (!existsSync(page)) return ['no findings catalog to read the sidebar from'];
	const html = await readFile(page, 'utf8');
	const nav = html.match(/<nav[^>]*aria-label="Main"[\s\S]*?<\/nav>/)?.[0] ?? '';
	const links = new Map();
	for (const m of nav.matchAll(/<a\b[^>]*href="([^"]+)"[^>]*>([\s\S]*?)<\/a>/g)) {
		links.set(m[1], m[2].replace(/<[^>]+>/g, '').trim());
	}
	const problems = [];
	for (const p of pages) {
		const m = p.replace(/\\/g, '/').match(/^dist\/findings\/(rs-[a-z]+-\d+)\/index\.html$/);
		if (!m) continue;
		const label = links.get(`/findings/${m[1]}/`);
		if (label === undefined) problems.push(`sidebar does not list /findings/${m[1]}/`);
		else if (label !== m[1].toUpperCase()) {
			problems.push(`sidebar labels /findings/${m[1]}/ "${label.slice(0, 40)}", not its code`);
		}
	}
	if (!links.has('/reference/github-action/')) problems.push('sidebar does not link the GitHub Action page');
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
	const jsonld = await checkStructuredData(pages);
	const sitemap = await checkSitemap(pages);
	const catalog = await checkCatalogCoverage(pages);
	const llms = await checkLlmsTxt();
	const headings = await checkHeadings(pages);
	const noindex = await checkNoindex(pages);
	const critical = await checkCriticalPath(pages);
	const selfLinks = await checkSelfLinks(pages);
	const security = [...(await checkHeaders()), ...(await checkCSP(pages)), ...(await checkSecurityTxt())];
	const tables = await checkTables(pages);
	const icons = await checkIcons();

	let failed = false;
	if (security.length) {
		failed = true;
		console.error(`${security.length} security problem(s) (headers, CSP, security.txt):`);
		for (const s of security) console.error(`  ${s}`);
	}
	if (icons.length) {
		failed = true;
		console.error(`${icons.length} icon/social-card problem(s):`);
		for (const i of icons) console.error(`  ${i}`);
	}
	if (tables.length) {
		failed = true;
		console.error(`${tables.length} table accessibility problem(s):`);
		for (const t of tables) console.error(`  ${t}`);
	}
	if (critical.length) {
		failed = true;
		console.error(`${critical.length} critical-path problem(s):`);
		for (const c of critical) console.error(`  ${c}`);
	}
	if (noindex.length) {
		failed = true;
		console.error(`${noindex.length} indexability problem(s):`);
		for (const n of noindex) console.error(`  ${n}`);
	}
	if (headings.length) {
		failed = true;
		console.error(`${headings.length} heading problem(s):`);
		for (const h of headings) console.error(`  ${h}`);
	}
	if (llms.length) {
		failed = true;
		console.error(`${llms.length} llms.txt problem(s):`);
		for (const l of llms) console.error(`  ${l}`);
	}
	const linking = [...catalog, ...selfLinks];
	if (linking.length) {
		failed = true;
		console.error(`${linking.length} internal-linking problem(s):`);
		for (const l of linking) console.error(`  ${l}`);
	}
	if (sitemap.length) {
		failed = true;
		console.error(`${sitemap.length} sitemap problem(s):`);
		for (const p of sitemap.slice(0, 10)) console.error(`  ${p}`);
		if (sitemap.length > 10) console.error(`  … and ${sitemap.length - 10} more`);
	}
	if (jsonld.length) {
		failed = true;
		console.error(`${jsonld.length} structured-data problem(s):`);
		for (const j of jsonld) console.error(`  ${j}`);
	}
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
		console.error(`\n${over.length} client-JS budget problem(s):`);
		for (const o of over) console.error(`  ${o}`);
		console.error(
			'\nStarlight ships a little progressive-enhancement JS by design; a framework\n' +
				'runtime or an analytics bundle is what this budget exists to catch. Raising it\n' +
				'is a product decision, not a build fix.'
		);
	}
	// Self-reporting checks: each entry is one line, so adding one does not touch
	// the call list above or the summary below.
	for (const [what, problems] of [
		['internal-reference', await checkInternalReferences(pages)],
		['findings-sidebar', await checkFindingsSidebar(pages)],
	]) {
		if (!problems.length) continue;
		failed = true;
		console.error(`${problems.length} ${what} problem(s):`);
		for (const p of problems) console.error(`  ${p}`);
	}
	if (failed) process.exit(1);

	console.log(`OK: ${pages.length} pages, no broken internal links, robots.txt advertises a real sitemap, every page carries a real social card (under 40 KiB), favicon.ico and touch icon present, every <title> unique and under 60 chars, every description in range, structured data valid and complete, every sitemap URL dated from git, the catalog links every finding, llms.txt matches the sitemap, headings and link text sound, every table keyboard-scrollable with named headers, noindex only on the 404, no third-party origins, security headers and immutable caching in _headers, a CSP that allows every inline script, a current security.txt, no internal planning references, every finding in the sidebar by code, all within the ${JS_BUDGET_BYTES / 1024} KiB initial + ${DEFERRED_JS_BUDGET_BYTES / 1024} KiB deferred JS and ${CSS_BUDGET_BYTES / 1024} KiB CSS budgets`);
}

await main();
