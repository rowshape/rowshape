import type { APIRoute } from 'astro';
import { getCollection } from 'astro:content';
import { SITE, REPO } from '../site';

/**
 * llms.txt — the site, in the form a language model can read in one fetch.
 *
 * The convention (llmstxt.org) is a Markdown index at /llms.txt: what this is,
 * then linked sections with a line of context each. It is not a ranking signal
 * and is not pretending to be one. It matters here for a more specific reason
 * than general AI-crawler etiquette: rowshape's wedge is being the tool inside
 * an agent's turn (PRD 8.2). An agent that has to guess at the finding codes,
 * or infer the CLI surface from a rendered HTML page full of navigation
 * chrome, is the failure this file removes.
 *
 * Generated from the content collection, not hand-written. A hand-written index
 * of 48 pages is a second copy of the sidebar that silently stops matching the
 * first — and this one would be wrong in the direction of telling a model about
 * pages that no longer exist.
 */

/** Section order and headings — the reading order, not the alphabet. */
const SECTIONS: { dir: string; heading: string; note: string }[] = [
	{ dir: 'install', heading: 'Install', note: 'Getting the binary.' },
	{
		dir: 'reference',
		heading: 'CLI reference',
		note: 'Every command and flag, generated from the binary itself.',
	},
	{
		dir: 'findings',
		heading: 'Findings',
		note: 'Every code a verdict can carry, what it means, and its remediation. `rowshape explain <CODE>` returns the same text.',
	},
	{
		dir: 'agent',
		heading: 'Agents & MCP',
		note: 'The MCP tools and the agent rule — how rowshape is used inside an agent loop.',
	},
	{ dir: 'spec', heading: 'Fixture spec', note: 'RFC-0001, the fixture format.' },
	{
		dir: 'fidelity',
		heading: 'Fidelity',
		note: 'What rowshape can and cannot know. Read this before trusting a PASS.',
	},
	{ dir: 'privacy', heading: 'Privacy', note: 'What a fixture does and does not contain.' },
];

export const GET: APIRoute = async () => {
	const docs = await getCollection('docs');

	// The route a doc entry builds to, matching Astro's own slugging.
	const routeFor = (id: string) => {
		const slug = id.replace(/\.mdx?$/, '').replace(/(^|\/)index$/, '');
		return slug ? `/${slug}/` : '/';
	};

	const lines: string[] = [
		'# rowshape',
		'',
		'> The type-checker for database migrations. Execute a proposed schema change',
		'> against production-shaped data in a disposable environment, and get back a',
		'> machine-readable verdict. A human and an agent get the same answer through',
		'> the same contract.',
		'',
		'A fixture is statistics computed from your database — row counts, null',
		'fractions, cardinality, fan-out distributions — and no rows. It commits to your',
		'repo. `rowshape validate` hydrates a disposable Postgres from it, applies the',
		'migration through your own runner, and returns a verdict whose confidence is',
		'capped by the weakest fact it rests on: if rowshape could only estimate',
		'something it returns WARN and names the command that would prove it, rather',
		'than guessing PASS.',
		'',
		`Source: ${REPO}`,
		'',
	];

	for (const section of SECTIONS) {
		const entries = docs
			.filter((d) => d.id === section.dir || d.id.startsWith(`${section.dir}/`))
			.sort((a, b) => a.id.localeCompare(b.id));
		if (entries.length === 0) continue;

		lines.push(`## ${section.heading}`, '', section.note, '');
		for (const entry of entries) {
			const url = new URL(routeFor(entry.id), SITE).href;
			const description = entry.data.description?.replace(/\s+/g, ' ').trim();
			lines.push(`- [${entry.data.title}](${url})${description ? `: ${description}` : ''}`);
		}
		lines.push('');
	}

	// Anything not claimed by a section above. Better to list a page under a
	// catch-all than to drop it silently because nobody updated SECTIONS.
	const claimed = new Set(
		SECTIONS.flatMap((s) =>
			docs.filter((d) => d.id === s.dir || d.id.startsWith(`${s.dir}/`)).map((d) => d.id)
		)
	);
	const rest = docs.filter(
		(d) => !claimed.has(d.id) && d.id !== 'index' && d.id !== '404'
	);
	if (rest.length > 0) {
		lines.push('## Other', '');
		for (const entry of rest) {
			const url = new URL(routeFor(entry.id), SITE).href;
			lines.push(`- [${entry.data.title}](${url})`);
		}
		lines.push('');
	}

	return new Response(lines.join('\n'), {
		headers: { 'Content-Type': 'text/plain; charset=utf-8' },
	});
};
