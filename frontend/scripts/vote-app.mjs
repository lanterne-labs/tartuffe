// Builds the vote app: the election routes of the site under the vote
// app's own frame (src/vote-app), as static files (build-vote/), with
// VOTE_APP=1 so that svelte.config.js takes the static adapter and the
// assembled routes (.vote-routes/).
import { cpSync, rmSync, mkdirSync, existsSync, lstatSync, readlinkSync, symlinkSync } from 'node:fs';
import { execSync } from 'node:child_process';

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
	execSync('npx vite build', { stdio: 'inherit', env: { ...process.env, VOTE_APP: '1' } });
} finally {
	if (mirrorTarget) symlinkSync(mirrorTarget, mirror);
	rmSync(routes, { recursive: true, force: true });
}
