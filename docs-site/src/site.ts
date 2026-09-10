/**
 * The canonical origin, in one place.
 *
 * Three build outputs need it as an ABSOLUTE URL and cannot derive it from the
 * request: the sitemap, robots.txt, and the og:image and JSON-LD tags. Each one
 * that keeps its own copy is a place the site can half-move. astro.config.mjs
 * imports this too, so `site` and everything derived from it cannot disagree.
 */
export const SITE = 'https://rowshape.com';

/** The source repository, referenced from structured data and the social links. */
export const REPO = 'https://github.com/rowshape/rowshape';
