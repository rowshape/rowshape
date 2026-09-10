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

import { writeFile, mkdir } from 'node:fs/promises';
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

// The mark from public/favicon.svg, unchanged. It is geometry, not type, so it
// rasterizes identically everywhere — which is exactly why the brand element
// that carries the most weight here is the one with no font dependency.
const MARK = `<path fill-rule="evenodd" d="M81 36 64 0 47 36l-1 2-9-10a6 6 0 0 0-9 9l10 10h-2L0 64l36 17h2L28 91a6 6 0 1 0 9 9l9-10 1 2 17 36 17-36v-2l9 10a6 6 0 1 0 9-9l-9-9 2-1 36-17-36-17-2-1 9-9a6 6 0 1 0-9-9l-9 10v-2Zm-17 2-2 5c-4 8-11 15-19 19l-5 2 5 2c8 4 15 11 19 19l2 5 2-5c4-8 11-15 19-19l5-2-5-2c-8-4-15-11-19-19l-2-5Z" clip-rule="evenodd"/><path d="M118 19a6 6 0 0 0-9-9l-3 3a6 6 0 1 0 9 9l3-3Zm-96 4c-2 2-6 2-9 0l-3-3a6 6 0 1 1 9-9l3 3c3 2 3 6 0 9Zm0 82c-2-2-6-2-9 0l-3 3a6 6 0 1 0 9 9l3-3c3-2 3-6 0-9Zm96 4a6 6 0 0 1-9 9l-3-3a6 6 0 1 1 9-9l3 3Z"/>`;

const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${W}" height="${H}" viewBox="0 0 ${W} ${H}">
  <rect width="${W}" height="${H}" fill="${BG}"/>

  <!-- Mark + wordmark, sharing a baseline. -->
  <g transform="translate(80 74) scale(0.5)" fill="${WHITE}">${MARK}</g>
  <text x="176" y="146" font-family="${SANS}" font-size="76" font-weight="bold"
        fill="${WHITE}" letter-spacing="-2">rowshape</text>

  <!-- The one sentence. Two lines, because at 1200px wide a single 44px line of
       this length wraps in the preview card and gets cropped mid-word. -->
  <text x="80" y="268" font-family="${SANS}" font-size="52" fill="${WHITE}">The type-checker for</text>
  <text x="80" y="334" font-family="${SANS}" font-size="52" fill="${WHITE}">database migrations.</text>

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
const png = await sharp(Buffer.from(svg)).png({ compressionLevel: 9 }).toBuffer();
await writeFile('public/og.png', png);

const { width, height } = await sharp(png).metadata();
console.log(`public/og.png — ${width}x${height}, ${(png.length / 1024).toFixed(1)} KiB`);
if (width !== W || height !== H) {
	console.error(`expected ${W}x${H}`);
	process.exit(1);
}
