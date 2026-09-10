import type { APIRoute } from 'astro';

// robots.txt, generated at build time rather than dropped in public/.
//
// The site had none at all: every crawler that asked got a 404, and the sitemap
// was discoverable only through the <link rel="sitemap"> hint in the head, which
// not every crawler reads. A static public/robots.txt would have fixed that and
// then quietly rotted the moment `site` changed in astro.config.mjs -- the
// sitemap URL is absolute and there is nothing to keep two copies of the origin
// in agreement. So it is an endpoint, and the origin comes from the one place
// that already owns it.
//
// What is disallowed, and why:
//
//   /pagefind/  Pagefind's search index. It is the site's own prose sliced into
//               fragment JSON and an index binary -- duplicate content with no
//               canonical, no title, and no value to a searcher who arrives on
//               it. Crawling it also costs budget that should go to the 49 real
//               pages. The search box loads these with fetch() at runtime, which
//               robots.txt does not govern, so blocking it costs the feature
//               nothing.
//
// What is deliberately NOT disallowed:
//
//   /_astro/    The CSS and JS bundles. Google renders pages before judging them
//               and blocking assets makes a rendered page look broken. This is
//               the single most common self-inflicted robots.txt wound and the
//               reason this file names it rather than leaving it to inference.
//
// No AI crawler is blocked. rowshape's wedge is being the default inside an
// agent's turn (PRD 8.2); a site that hides its own documentation from the
// models it wants to be recommended by is arguing against itself.
export const GET: APIRoute = ({ site }) => {
	if (!site) {
		// `site` is set in astro.config.mjs and the sitemap integration already
		// depends on it. If it ever goes missing, failing the build is right:
		// shipping a robots.txt with a relative or absent Sitemap line is worse
		// than shipping none, because it looks correct.
		throw new Error('astro.config.mjs must set `site` — robots.txt needs an absolute sitemap URL');
	}

	const sitemap = new URL('sitemap-index.xml', site).href;
	const llms = new URL('llms.txt', site).href;

	const body = `# https://rowshape.com — the type-checker for database migrations.
# Source: https://github.com/rowshape/rowshape/blob/main/docs-site/src/pages/robots.txt.ts

User-agent: *
Allow: /
Disallow: /pagefind/

Sitemap: ${sitemap}

# A Markdown index of this site for language models (llmstxt.org). There is no
# standard directive for it; this is a comment so that anything already fetching
# robots.txt can find it.
# llms.txt: ${llms}
`;

	return new Response(body, {
		headers: {
			'Content-Type': 'text/plain; charset=utf-8',
		},
	});
};
