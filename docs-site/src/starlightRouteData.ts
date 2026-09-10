import { defineRouteMiddleware } from '@astrojs/starlight/route-data';

import { SITE, REPO } from './site';

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

	if (isSiteRoot) {
		const description =
			data.description ?? 'The type-checker for database migrations.';
		head.push({
			tag: 'script',
			attrs: { type: 'application/ld+json' },
			// JSON.stringify cannot emit a bare "</script>", but a description
			// containing "<" would still be raw HTML inside a script element, so the
			// one character that can break out is escaped. Cheap, and it means a
			// future edit to the frontmatter cannot silently corrupt the page.
			content: JSON.stringify(homepageJsonLd(description)).replace(/</g, '\\u003c'),
		});
	}
	// og:title is deliberately left alone. It is the share-card headline, where
	// there is room for the full sentence and no results page to truncate it.
});
