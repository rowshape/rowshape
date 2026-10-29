// Wraps every content <table> in a labelled, keyboard-focusable scroll region:
//
//   <div class="table-scroll" role="region" tabindex="0" aria-label="Flags">
//
// Why: Starlight lets a wide table scroll sideways (the table itself gets
// `overflow: auto` at narrow widths), and a scroll container nothing can focus
// is one a keyboard user cannot scroll — axe's scrollable-region-focusable, on
// seven tables at 375px (the CLI flag tables, the GitHub Action inputs, the
// privacy matrix). Making the scroller focusable then makes it a thing a screen
// reader announces, so it needs a role and a name, and the name has to differ
// between two tables on one page or the regions are indistinguishable
// (landmark-unique).
//
// The name is the nearest preceding heading — what a reader would call the
// table anyway ("Flags", "Inputs") — or "Table" when there is none. A second
// table under the same heading gets ", table 2".
//
// Done at build time rather than in client JS so it costs no bytes and works
// before (and without) scripts. Applies to the CLI reference too: that Markdown
// comes from `go run ./tools/gencli`, and fixing it here rather than in the
// generator keeps the generator emitting plain Markdown tables.
//
// Dependency-free, like remark-link-finding-codes.mjs: it walks the hast itself.

const HEADING = /^h[1-6]$/;

function textOf(node) {
	if (node.type === 'text') return node.value;
	return (node.children || []).map(textOf).join('');
}

export default function rehypeScrollableTables() {
	return (tree) => {
		let heading = '';
		const used = new Map(); // label -> count

		function walk(node) {
			const children = node.children;
			if (!children) return;
			for (let i = 0; i < children.length; i++) {
				const child = children[i];
				if (child.type !== 'element') continue;
				if (HEADING.test(child.tagName)) {
					heading = textOf(child).trim();
					continue;
				}
				const alreadyWrapped =
					node.type === 'element' && (node.properties?.className || []).includes('table-scroll');
				if (child.tagName === 'table' && !alreadyWrapped) {
					const base = heading || 'Table';
					const n = (used.get(base) || 0) + 1;
					used.set(base, n);
					children[i] = {
						type: 'element',
						tagName: 'div',
						properties: {
							className: ['table-scroll'],
							role: 'region',
							tabIndex: 0,
							ariaLabel: n === 1 ? base : `${base}, table ${n}`,
						},
						children: [child],
					};
					continue;
				}
				walk(child);
			}
		}
		walk(tree);
	};
}
