import { sveltekit } from '@sveltejs/kit/vite';
import { existsSync } from 'node:fs';
import { defineConfig, loadEnv } from 'vite';

// The test shortcuts of the vote (a simulated passport, ZKPassport's mock
// documents) are for `vite dev` only: a build that names one fails, so none
// can reach a deployed site. The servers refuse such proofs anyway (the
// registry's provider and the ingest's --dev); this keeps the bundle clean.
const TEST_ONLY = ['VITE_VOTE_SIMULATE_PASSPORT', 'VITE_ZKPASSPORT_DEV'];

export default defineConfig(({ command, mode }) => {
	// A real `vite build` (svelte-check and vitest load this file too).
	if (command === 'build' && process.argv.includes('build')) {
		const env = { ...loadEnv(mode, process.cwd(), 'VITE_'), ...process.env };
		const set = TEST_ONLY.filter((k) => env[k]);
		if (set.length) throw new Error(`${set.join(', ')}: test shortcuts, for vite dev only, never in a build`);
		// A local trial's ledger linked into static/ (see the vote's local
		// stack) would be copied into the build.
		if (existsSync('static/mirror')) throw new Error('static/mirror: a local trial’s ledger, never in a build');
	}
	return {
		plugins: [sveltekit()],
		// The vote's library (@isoloir/web) loads its WebAssembly next to its
		// module, and the prover (bb.js, noir_js) loads WebAssembly and workers
		// of its own: Vite's dependency pre-bundling breaks them.
		optimizeDeps: { exclude: ['@isoloir/web', '@aztec/bb.js', '@noir-lang/noir_js', '@noir-lang/acvm_js', '@noir-lang/noirc_abi'] },
		// The prover's worker (@isoloir/web/prove.worker) imports bb.js and
		// noir_js lazily: an ES module worker keeps the code split.
		worker: { format: 'es' },
		server: {
			// Cross-origin isolation gives the prover threads (in production the
			// service worker static/coi-sw.js sets the same headers).
			headers: { 'Cross-Origin-Opener-Policy': 'same-origin', 'Cross-Origin-Embedder-Policy': 'credentialless' }
		}
	};
});
