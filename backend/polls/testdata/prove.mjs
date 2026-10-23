// prove.mjs makes a Semaphore proof for the Go tests with the browser's own
// libraries (frontend/node_modules) and circuit files (frontend/static).
// stdin:  { secret, others: [secret...], message, scope }
// stdout: { proof, commitment, root }
import { createRequire } from 'node:module';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const front = join(dirname(fileURLToPath(import.meta.url)), '../../../frontend');
const require = createRequire(join(front, 'package.json'));
const { Identity } = require('@semaphore-protocol/identity');
const { Group } = require('@semaphore-protocol/group');
const { generateProof } = require('@semaphore-protocol/proof');

const chunks = [];
for await (const c of process.stdin) chunks.push(c);
const req = JSON.parse(Buffer.concat(chunks).toString('utf8'));
const me = new Identity(req.secret);
const group = new Group([...(req.others ?? []).map((s) => new Identity(s).commitment), me.commitment]);
const artifacts = {
	wasm: new Uint8Array(readFileSync(join(front, 'static/semaphore/semaphore-20.wasm'))),
	zkey: new Uint8Array(readFileSync(join(front, 'static/semaphore/semaphore-20.zkey')))
};
const proof = await generateProof(me, group, BigInt(req.message), BigInt(req.scope), 20, artifacts);
process.stdout.write(JSON.stringify({ proof, commitment: String(me.commitment), root: String(group.root) }) + '\n', () => process.exit(0));
