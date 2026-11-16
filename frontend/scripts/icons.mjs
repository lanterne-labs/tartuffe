// The icons of the site, drawn from its mark (src/lib/assets/favicon.svg):
// static/favicon.svg and favicon-48.png for the browsers and the link
// previews that read the page (Slack, Discord), static/favicon.ico for what
// asks for that path without reading the page (the server sends it without
// a content type, so the page never links it), static/apple-touch-icon.png
// for iOS (a full square: iOS rounds it itself). Run it after a change of the mark: node scripts/icons.mjs
import { readFileSync, writeFileSync } from 'node:fs';
import sharp from 'sharp';

const root = new URL('..', import.meta.url).pathname;
const svg = readFileSync(root + 'src/lib/assets/favicon.svg', 'utf8');
writeFileSync(root + 'static/favicon.svg', svg);

const png = (source, size) => sharp(Buffer.from(source), { density: (72 * size) / 64 }).resize(size, size).png().toBuffer();

const square = svg.replace(/<circle[^>]*fill="([^"]+)"[^>]*\/>/, '<rect width="64" height="64" fill="$1"/>');
if (square === svg) throw new Error('the mark has no background circle to square');
writeFileSync(root + 'static/apple-touch-icon.png', await png(square, 180));
writeFileSync(root + 'static/favicon-48.png', await png(svg, 48));

// An .ico that holds PNG images (every browser since IE 11 reads them).
const sizes = [16, 32, 48];
const images = await Promise.all(sizes.map((s) => png(svg, s)));
const header = Buffer.alloc(6 + 16 * sizes.length);
header.writeUInt16LE(1, 2);
header.writeUInt16LE(sizes.length, 4);
let offset = header.length;
sizes.forEach((s, i) => {
	const e = 6 + 16 * i;
	header.writeUInt8(s, e);
	header.writeUInt8(s, e + 1);
	header.writeUInt16LE(1, e + 4);
	header.writeUInt16LE(32, e + 6);
	header.writeUInt32LE(images[i].length, e + 8);
	header.writeUInt32LE(offset, e + 12);
	offset += images[i].length;
});
writeFileSync(root + 'static/favicon.ico', Buffer.concat([header, ...images]));
console.log('static/favicon.svg, favicon-48.png, favicon.ico, apple-touch-icon.png written');
