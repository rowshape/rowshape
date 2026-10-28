// Generates public/favicon.ico (32px) and public/apple-touch-icon.png (180px)
// from public/favicon.svg, the one source of the mark.
//
// Run: node scripts/make-icons.mjs   (npm run icons)
//
// Committed, not built, for the same reason as og.png (see make-og.mjs): the
// artifacts are reviewable and the build stays free of rasterization. The mark
// is pure geometry, so unlike the social card there is no font risk — the
// reason is reviewability alone.
//
// Why the two raster files at all, when every current browser takes the SVG:
// /favicon.ico is requested unprompted by feed readers, link unfurlers, bookmark
// sync and older browsers, and its absence is a 404 in every server log; iOS
// ignores SVG icons and screenshots the page for the home screen without
// apple-touch-icon.

import { readFile, writeFile } from 'node:fs/promises';
import sharp from 'sharp';

const svg = await readFile('public/favicon.svg');

// ICO is a 6-byte header, one 16-byte directory entry per image, then the
// images. Since Vista an entry may hold a PNG verbatim, which every consumer
// that still asks for .ico understands, so no BMP encoder is needed.
const png32 = await sharp(svg, { density: 300 }).resize(32, 32).png({ compressionLevel: 9 }).toBuffer();
const header = Buffer.alloc(6 + 16);
header.writeUInt16LE(0, 0); // reserved
header.writeUInt16LE(1, 2); // type: icon
header.writeUInt16LE(1, 4); // one image
header.writeUInt8(32, 6); // width
header.writeUInt8(32, 7); // height
header.writeUInt8(0, 8); // no palette
header.writeUInt8(0, 9); // reserved
header.writeUInt16LE(1, 10); // colour planes
header.writeUInt16LE(32, 12); // bits per pixel
header.writeUInt32LE(png32.length, 14); // image size
header.writeUInt32LE(header.length, 18); // image offset
const ico = Buffer.concat([header, png32]);
await writeFile('public/favicon.ico', ico);
console.log(`public/favicon.ico — 32x32, ${ico.length} bytes`);

// iOS rounds the corners itself and fills transparency with black, so the
// touch icon is the mark's square full-bleed: the background colour to the
// edge, the glyph at the same proportions.
const square = String(svg).replace(/rx="7"/, 'rx="0"');
const touch = await sharp(Buffer.from(square), { density: 1200 })
	.resize(180, 180)
	.flatten({ background: '#2e4a7d' })
	.png({ compressionLevel: 9, palette: true })
	.toBuffer();
await writeFile('public/apple-touch-icon.png', touch);
console.log(`public/apple-touch-icon.png — 180x180, ${touch.length} bytes`);
