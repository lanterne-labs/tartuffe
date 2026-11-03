// Builds the vote app: the election routes of the site under the vote
// app's own frame (src/vote-app), as static files (build-vote/), with
// VOTE_APP=1 so that svelte.config.js takes the static adapter and the
// assembled routes (.vote-routes/).
//
// The published app never calls the site's API: the candidates' cards of
// each event of VITE_VOTE_POLLS (slug:id,…) are fetched here from
// VITE_API_BASE (GET /api/polls/<slug>) into static/vote/candidates/<slug>.json,
// and the app reads them (VITE_VOTE_STATIC_CANDIDATES=1). Their photos are
// shrunk to 256 px and shipped with the app (static/vote/candidates/<slug>/):
// the originals weigh up to a few MB each, and their host would see every
// voter's address.
import sharp from 'sharp';
import { cpSync, rmSync, mkdirSync, existsSync, lstatSync, readlinkSync, symlinkSync, writeFileSync } from 'node:fs';
import { execSync } from 'node:child_process';

const api = (process.env.VITE_API_BASE ?? '').replace(/\/$/, '');
const slugs = (process.env.VITE_VOTE_POLLS ?? '')
	.split(',')
	.map((p) => p.split(':')[0].trim())
	.filter(Boolean);
if (!api) throw new Error('vote app: VITE_API_BASE is not set (the candidates come from its /api/polls/<slug>)');
if (slugs.length === 0) throw new Error('vote app: VITE_VOTE_POLLS names no event (slug:id,…)');

const candidates = 'static/vote';
rmSync(candidates, { recursive: true, force: true });
mkdirSync(`${candidates}/candidates`, { recursive: true });
for (const slug of slugs) {
	const url = `${api}/api/polls/${encodeURIComponent(slug)}`;
	const res = await fetch(url);
	if (!res.ok) throw new Error(`vote app: ${url} answered HTTP ${res.status}`);
	const p = await res.json();
	// What the vote pages show of the event and its candidates (lib/vote/candidates.ts).
	const meta = {
		event: p.event,
		candidates: p.results.options
			.filter((o) => o.kind === 'candidate')
			.map((o) => ({ name: o.name, person_id: o.person_id ?? undefined, image_url: o.image_url ?? undefined, honesty_score: o.honesty_score ?? undefined }))
	};
	mkdirSync(`${candidates}/candidates/${slug}`, { recursive: true });
	for (const c of meta.candidates) {
		if (!c.image_url || c.person_id === undefined) continue;
		const r = await fetch(c.image_url);
		if (!r.ok) throw new Error(`vote app: the photo of ${c.name}, ${c.image_url}, answered HTTP ${r.status}`);
		const file = `candidates/${slug}/${c.person_id}.jpg`;
		await sharp(Buffer.from(await r.arrayBuffer()))
			.rotate()
			.resize(256, 256, { fit: 'inside', withoutEnlargement: true })
			.jpeg({ quality: 80, mozjpeg: true })
			.toFile(`${candidates}/${file}`);
		// Relative to the app's base path (lib/vote/candidates.ts).
		c.image_url = `vote/${file}`;
	}
	writeFileSync(`${candidates}/candidates/${slug}.json`, JSON.stringify(meta));
	console.log(`vote app: ${slug}, ${meta.candidates.length} candidates`);
}

const routes = '.vote-routes';
rmSync(routes, { recursive: true, force: true });
mkdirSync(routes);
cpSync('src/vote-app', routes, { recursive: true });
cpSync('src/routes/election', `${routes}/election`, { recursive: true });
// The developer's mirror symlink in static/ is not part of the app.
const mirror = 'static/mirror';
const mirrorTarget = existsSync(mirror) && lstatSync(mirror).isSymbolicLink() ? readlinkSync(mirror) : null;
if (mirrorTarget) rmSync(mirror);
try {
	execSync('npx vite build', { stdio: 'inherit', env: { ...process.env, VOTE_APP: '1', VITE_VOTE_STATIC_CANDIDATES: '1' } });
} finally {
	if (mirrorTarget) symlinkSync(mirrorTarget, mirror);
	rmSync(routes, { recursive: true, force: true });
	rmSync(candidates, { recursive: true, force: true });
}
