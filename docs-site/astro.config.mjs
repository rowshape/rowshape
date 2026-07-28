// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// The one place the stack is deliberately not Go (PRD §9). The Go decision was
// about the binary — single static artifact, no runtime, hydrate is CPU-bound —
// and none of that reasoning applies to a documentation site. Astro Starlight is
// purpose-built for docs plus a landing page, and it deploys onto the Cloudflare
// Pages standard the rest of this org already uses.
//
// https://astro.build/config
export default defineConfig({
	site: 'https://rowshape.com',
	integrations: [
		starlight({
			title: 'rowshape',
			description:
				'The type-checker for database migrations — a human and an agent get the same answer through the same contract.',
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/rowshape/rowshape' },
			],
			editLink: {
				baseUrl: 'https://github.com/rowshape/rowshape/edit/main/docs-site/',
			},
			// The sections the docs are organized around. Content lands in P4-T4
			// (install, findings, privacy) and P4-T5 (agents/MCP, fixture spec); this
			// is the shape it lands into.
			sidebar: [
				{ label: 'Install', items: [{ autogenerate: { directory: 'install' } }] },
				// Generated from the cobra tree by `go run ./tools/gencli`, with a
				// staleness test, so the reference cannot drift from the binary.
				{ label: 'CLI reference', items: [{ autogenerate: { directory: 'reference' } }] },
				{ label: 'Findings', items: [{ autogenerate: { directory: 'findings' } }] },
				{ label: 'Privacy', items: [{ autogenerate: { directory: 'privacy' } }] },
				// What the tool can and cannot know. PRD §14 committed to saying this
				// in the docs ("Fixture fidelity is a tarpit ... Say so in the docs")
				// and the page did not exist until CR4-T13.
				{ label: 'Fidelity', items: [{ autogenerate: { directory: 'fidelity' } }] },
				{ label: 'Agents & MCP', items: [{ autogenerate: { directory: 'agent' } }] },
				{ label: 'Fixture spec', items: [{ autogenerate: { directory: 'spec' } }] },
			],
		}),
	],
});
