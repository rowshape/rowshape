// Generates public/og.png — the social card every page points at.
//
// Run: node scripts/make-og.mjs   (npm run og)
//
// The output is COMMITTED, and the build never runs this. That is deliberate.
// Rasterizing SVG text goes through the host's fontconfig, so the same source
// renders differently — or with the text silently missing — depending on which
// fonts the machine happens to have. A card that renders here and comes out
// blank on a CI runner is worse than no card, and it fails invisibly because
// nothing downstream reads pixels. Generating once, eyeballing the result, and
// committing the PNG makes the artifact the reviewable thing.
//
// Fonts are named as a stack ending in a generic family so that regenerating on
// a machine without Liberation/DejaVu degrades to something rather than nothing.

import { readFile, writeFile, mkdir } from 'node:fs/promises';
import sharp from 'sharp';

const W = 1200;
const H = 630;

// Starlight's dark palette, so the card and the site it links to agree.
const BG = '#0b0e14';
const PANEL = '#141922';
const RULE = '#232a36';
const WHITE = '#ffffff';
const MUTED = '#98a2b3';
const RED = '#f87171';
const GREEN = '#4ade80';

const SANS = "Liberation Sans, Helvetica, Arial, sans-serif";
const MONO = "DejaVu Sans Mono, Liberation Mono, monospace";

// The mark, read from public/favicon.svg rather than copied, so the card cannot
// drift from the favicon. It is geometry, not type, so it rasterizes identically
// everywhere — which is exactly why the brand element that carries the most
// weight here is the one with no font dependency.
const MARK = (await readFile('public/favicon.svg', 'utf8')).replace(/^<svg[^>]*>|<\/svg>\s*$/g, '');

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
  <rect width="${W}" height="${H}" fill="${BG}"/>

  <!-- Mark + wordmark, sharing a baseline. -->
  <g transform="translate(80 74) scale(2)">${MARK}</g>
  <text x="176" y="146" font-family="${SANS}" font-size="76" font-weight="bold"
        fill="${WHITE}" letter-spacing="-2">rowshape</text>

  <!-- The one sentence. Two lines, because at 1200px wide a single 44px line of
       this length wraps in the preview card and gets cropped mid-word. -->
  <text x="80" y="268" font-family="${SANS}" font-size="52" fill="${WHITE}">The type-checker for</text>
  <text x="80" y="334" font-family="${SANS}" font-size="52" fill="${WHITE}">PostgreSQL migrations.</text>

  <text x="80" y="392" font-family="${SANS}" font-size="27" fill="${MUTED}">A human and an agent get the same answer through the same contract.</text>

  <!-- A verdict, because the product is the verdict. The migration name and the
       code agree: RS-LOCK-001 is the full-table-rewrite finding, so the file it
       is raised against has to be an ALTER, not an index build. -->
  <rect x="80" y="436" width="1040" height="114" rx="12" fill="${PANEL}" stroke="${RULE}"/>
  <text x="112" y="480" font-family="${MONO}" font-size="24" fill="${MUTED}">$ rowshape validate 0042_alter_users.sql</text>
  <text x="112" y="522" font-family="${MONO}" font-size="24" fill="${RED}" font-weight="bold">FAIL</text>
  <text x="200" y="522" font-family="${MONO}" font-size="24" fill="${WHITE}">RS-LOCK-001</text>
  <text x="380" y="522" font-family="${MONO}" font-size="24" fill="${MUTED}">ACCESS EXCLUSIVE on users, ~4m</text>

  <rect x="0" y="${H - 8}" width="${W}" height="8" fill="${GREEN}"/>
</svg>`;

await mkdir('public', { recursive: true });
// Palette-encoded. The card is flat colour plus antialiased text — a few dozen
// distinct colours — so a 64-entry palette is visually lossless (checked by
// eye at 3x on the wordmark and the terminal text), and it took the file from
// 55 KiB of truecolour to 17. Every unfurl of every link downloads this, often
// on a phone. check-build holds it under 40 KiB.
const png = await sharp(Buffer.from(svg))
	.png({ palette: true, colours: 64, effort: 10, compressionLevel: 9 })
	.toBuffer();
await writeFile('public/og.png', png);

const { width, height } = await sharp(png).metadata();
console.log(`public/og.png — ${width}x${height}, ${(png.length / 1024).toFixed(1)} KiB`);
if (width !== W || height !== H) {
	console.error(`expected ${W}x${H}`);
	process.exit(1);
}
