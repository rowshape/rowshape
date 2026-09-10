import { defineCollection, z } from 'astro:content';
import { docsLoader } from '@astrojs/starlight/loaders';
import { docsSchema } from '@astrojs/starlight/schema';

export const collections = {
	docs: defineCollection({
		loader: docsLoader(),
		schema: docsSchema({
			extend: z.object({
				// The `<title>` tag, when it should differ from the page heading.
				//
				// Starlight uses `title` for the h1, the sidebar entry, the breadcrumb
				// AND the title tag. Those want different things: a heading can be a
				// full sentence, a title tag has about 60 characters before a results
				// page truncates it. Set this only where the two genuinely diverge —
				// where it is absent, `title` is used and the behaviour is unchanged.
				//
				// Applied in src/starlightRouteData.ts.
				seoTitle: z.string().max(60).optional(),
			}),
		}),
	}),
};
