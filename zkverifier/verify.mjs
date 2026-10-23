// verify.mjs checks the ZKPassport proofs of one sign-in. The API
// (backend/accounts/providers/zkpassport.go) runs it once per verification: the request is one
// JSON object on stdin, the answer one JSON object on stdout.
//
// stdin:  { proofs, result, challenge (the custom data the proof binds), domain, scope, minAge, nationality, devMode, rpcUrl }
// stdout: { verified, uniqueIdentifier, errors }
//
// The query is rebuilt here from the server's own settings and the one-time
// challenge, never taken from the browser, so a tampered request cannot pass.
// Verification is local (verifierMode "local"): the open-source verifier
// bundled in @zkpassport/sdk checks the proofs; only the registry roots of
// the certificates are read on-chain (Ethereum, through rpcUrl or the SDK's
// public endpoint). No ZKPassport account, key or API is used.

import { createRequire } from 'node:module';

// The CommonJS build: the ESM one imports the browser polyfill path
// 'buffer/', which the Node ESM loader refuses.
const { ZKPassport } = createRequire(import.meta.url)('@zkpassport/sdk');

async function readStdin() {
	const chunks = [];
	for await (const chunk of process.stdin) chunks.push(chunk);
	return JSON.parse(Buffer.concat(chunks).toString('utf8'));
}

function reply(obj) {
	process.stdout.write(JSON.stringify(obj) + '\n', () => process.exit(0));
}

try {
	const req = await readStdin();
	for (const k of ['proofs', 'result', 'challenge', 'domain', 'scope']) {
		if (!req[k]) throw new Error(`missing ${k}`);
	}
	if (!Array.isArray(req.proofs) || req.proofs.length === 0) throw new Error('proofs must be a non-empty array');

	const zk = new ZKPassport(req.domain);
	let builder = zk.createQuery();
	if (req.minAge) builder = builder.gte('age', req.minAge);
	if (req.nationality) builder = builder.in('nationality', [req.nationality]);
	builder = builder.bind('custom_data', req.challenge);
	const { query } = builder.done();

	const { verified, uniqueIdentifier, queryResultErrors } = await zk.verify({
		proofs: req.proofs,
		originalQuery: query,
		queryResult: req.result,
		scope: req.scope,
		devMode: Boolean(req.devMode),
		verifierMode: 'local',
		config: req.rpcUrl ? { rpcUrl: req.rpcUrl } : undefined
	});
	reply({ verified: Boolean(verified && uniqueIdentifier), uniqueIdentifier: uniqueIdentifier ?? '', errors: queryResultErrors ?? null });
} catch (err) {
	// A malformed request is not verified; the API logs the message.
	reply({ verified: false, uniqueIdentifier: '', errors: { message: String(err?.message ?? err) } });
}
