import { defineRouteMiddleware } from '@astrojs/starlight/route-data';

import { SITE, REPO } from './site';
import { lastmodFor } from './lastmod';

/**
 * Structured data for the homepage.
 *
 * The site emitted none at all, which means a search engine had to infer from
 * prose that rowshape is a free, MIT-licensed developer tool rather than being
 * told. Three nodes in one graph, which is the shape Google expects: who
 * publishes the site, what the site is, and what the software is.
 *
 * Everything here is a fact checkable in this repo — the licence from LICENSE,
 * the platforms from .goreleaser.yaml's goos list, the language from go.mod, the
 * install methods from the install page. There is deliberately no
 * `aggregateRating`: rowshape has no ratings, and inventing them to win a star
 * in a result is the exact thing that gets structured data ignored.
 */
function homepageJsonLd(description: string) {
	return {
		'@context': 'https://schema.org',
		'@graph': [
			{
				'@type': 'Organization',
				'@id': `${SITE}/#organization`,
				name: 'rowshape',
				url: SITE,
				logo: `${SITE}/favicon.svg`,
				sameAs: [REPO],
			},
			{
				'@type': 'WebSite',
				'@id': `${SITE}/#website`,
				name: 'rowshape',
				url: SITE,
				description,
				publisher: { '@id': `${SITE}/#organization` },
				inLanguage: 'en',
			},
			{
				'@type': 'SoftwareApplication',
				'@id': `${SITE}/#software`,
				name: 'rowshape',
				applicationCategory: 'DeveloperApplication',
				applicationSubCategory: 'Database migration testing',
				description,
				url: SITE,
				// linux, darwin and windows, per .goreleaser.yaml.
				operatingSystem: 'macOS, Linux, Windows',
				programmingLanguage: 'Go',
				codeRepository: REPO,
				downloadUrl: `${SITE}/install/`,
				softwareHelp: `${SITE}/reference/`,
				license: 'https://opensource.org/licenses/MIT',
				isAccessibleForFree: true,
				offers: {
					'@type': 'Offer',
					price: '0',
					priceCurrency: 'USD',
				},
				publisher: { '@id': `${SITE}/#organization` },
			},
		],
	};
}

/**
 * The `<title>` a search engine shows is not always the heading a reader wants
 * at the top of the page, and Starlight ties them together: it emits
 * `${frontmatter.title} | ${siteTitle}` for every route, with no way to vary
 * one without moving the other.
 *
 * Two consequences this middleware exists to fix.
 *
 * 1. The homepage shipped as `rowshape | rowshape`. The frontmatter title is the
 *    site title, so the delimiter logic appends the brand to itself and the most
 *    valuable title on the site says nothing twice. A homepage title is the one
 *    place the brand and the category both have to appear.
 *
 * 2. Titles run long. A results page truncates around 60 characters, and the
 *    findings catalog carries titles up to 110 — so what a searcher sees is the
 *    code and half a sentence, with the brand cut off entirely. The page heading
 *    is right to be a full sentence; the title tag is not. `seoTitle` in
 *    frontmatter decouples them, and where it is absent nothing changes.
 */
/**
 * The label each top-level section is known by, keyed by its URL segment.
 *
 * These mirror the sidebar groups in astro.config.mjs. They are written out
 * rather than read from the sidebar because a breadcrumb needs a URL for each
 * step and the sidebar config carries labels for groups that have none — and
 * because a section added without a label here should fail the build rather
 * than silently emit a breadcrumb with a raw slug in it. Every section listed
 * has an index page at /<segment>/, which is what makes the trail clickable.
 */
const SECTIONS: Record<string, string> = {
	install: 'Install',
	reference: 'CLI reference',
	findings: 'Findings',
	privacy: 'Privacy',
	fidelity: 'Fidelity',
	agent: 'Agents & MCP',
	spec: 'Fixture spec',
};

/**
 * Structured data for a documentation page: where it sits, and what it is.
 *
 * BreadcrumbList is what lets a result show `rowshape.com › Findings ›
 * RS-LOCK-001` instead of a bare URL, and it tells a crawler the site has a
 * shape rather than 49 unrelated pages. TechArticle is the honest type for
 * this content — not Article, which is for editorial, and not FAQPage, which
 * these pages are not.
 *
 * dateModified is the git commit date of the page's source, from the map
 * scripts/gen-lastmod.mjs writes before each build — the same map the sitemap's
 * <lastmod> uses, so a page cannot advertise two different freshness dates.
 * There is deliberately no datePublished: it would be the file's FIRST commit,
 * which for pages moved between repos or split out of another file is a date
 * about the file rather than about the content. An absent date is valid; a
 * made-up one is not.
 */
function docPageJsonLd(
	pathname: string,
	title: string,
	description: string | undefined,
	siteTitle: string
) {
	// The git commit date of the page's source. Absent for a page not yet
	// committed, in which case the article claims no date at all.
	const dateModified = lastmodFor(pathname);
	const segments = pathname.split('/').filter(Boolean);
	const url = new URL(pathname, SITE).href;

	const trail: { name: string; item: string }[] = [{ name: siteTitle, item: `${SITE}/` }];
	const section = segments[0];
	if (section) {
		const label = SECTIONS[section];
		if (!label) {
			// Loud on purpose. A new top-level section is a content decision that
			// should be reflected here, and a breadcrumb reading "faq" instead of
			// "Frequently asked questions" is the kind of thing nobody notices.
			throw new Error(
				`No breadcrumb label for section "${section}" (${pathname}) — add it to SECTIONS in src/starlightRouteData.ts`
			);
		}
		trail.push({ name: label, item: `${SITE}/${section}/` });
	}
	// A section index page IS its section: do not repeat it as its own child.
	const isSectionIndex = segments.length === 1;
	if (!isSectionIndex && segments.length > 0) {
		trail.push({ name: title, item: url });
	}

	const graph: Record<string, unknown>[] = [
		{
			'@type': 'BreadcrumbList',
			'@id': `${url}#breadcrumb`,
			itemListElement: trail.map((step, i) => ({
				'@type': 'ListItem',
				position: i + 1,
				name: step.name,
				item: step.item,
			})),
		},
		{
			'@type': 'TechArticle',
			'@id': `${url}#article`,
			// Google truncates a headline past ~110 characters. The page title is
			// the headline here, and the findings titles run to 96.
			headline: title.slice(0, 110),
			...(description ? { description } : {}),
			...(dateModified ? { dateModified } : {}),
			url,
			inLanguage: 'en',
			isPartOf: { '@id': `${SITE}/#website` },
			publisher: { '@id': `${SITE}/#organization` },
			about: { '@id': `${SITE}/#software` },
		},
	];

	return { '@context': 'https://schema.org', '@graph': graph };
}

/** Serialize a JSON-LD graph for inline embedding in a script tag. */
function serializeJsonLd(data: unknown): string {
	// JSON.stringify cannot emit a bare "</script>", but a "<" in any string
	// value would still be raw HTML inside a script element, so the one character
	// that can break out is escaped.
	return JSON.stringify(data).replace(/</g, '\\u003c');
}

export const onRequest = defineRouteMiddleware((context) => {
	const { starlightRoute } = context.locals;
	const { entry, siteTitle, head } = starlightRoute;

	// `seoTitle` is declared in src/content.config.ts. Cast because the docs
	// schema type does not carry our extension.
	const data = entry.data as typeof entry.data & { seoTitle?: string };

	// The homepage: the frontmatter title IS the site title, so appending the
	// site title is pure duplication. Say what rowshape is instead — this is the
	// title that ranks for the brand query, and "rowshape" alone tells a searcher
	// who has not heard of it nothing at all.
	const isSiteRoot = data.title.trim().toLowerCase() === siteTitle.trim().toLowerCase();

	// When `seoTitle` is set it IS the whole title tag, suffix included or not.
	// That matters on the findings pages: the suffix costs ten characters of a
	// sixty-character budget, and `RS-LOCK-001` is already the string a reader
	// searches for — the brand adds nothing there that the code does not.
	const title = data.seoTitle
		? data.seoTitle
		: isSiteRoot
			? `${siteTitle} — the type-checker for database migrations`
			: `${data.title} | ${siteTitle}`;

	for (const tag of head) {
		if (tag.tag === 'title') tag.content = title;
	}

	// The homepage describes the software; every other page describes where it
	// sits and what it is. The 404 gets neither: it is not a document, and it is
	// excluded from the sitemap for the same reason.
	const is404 = context.url.pathname.replace(/\/$/, '') === '/404';
	if (isSiteRoot) {
		const description = data.description ?? 'The type-checker for database migrations.';
		head.push({
			tag: 'script',
			attrs: { type: 'application/ld+json' },
			content: serializeJsonLd(homepageJsonLd(description)),
		});
	} else if (!is404) {
		head.push({
			tag: 'script',
			attrs: { type: 'application/ld+json' },
			content: serializeJsonLd(
				docPageJsonLd(context.url.pathname, data.title, data.description, siteTitle)
			),
		});
	}
	// og:title is deliberately left alone. It is the share-card headline, where
	// there is room for the full sentence and no results page to truncate it.
});
