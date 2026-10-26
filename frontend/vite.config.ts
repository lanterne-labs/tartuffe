import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [sveltekit()],
	// The prover (bb.js, noir_js) loads WebAssembly and workers of its own:
	// Vite's dependency pre-bundling breaks them.
	optimizeDeps: { exclude: ['@aztec/bb.js', '@noir-lang/noir_js', '@noir-lang/acvm_js', '@noir-lang/noirc_abi'] },
	server: {
		// Cross-origin isolation gives the prover threads (in production the
		// service worker static/coi-sw.js sets the same headers).
		headers: { 'Cross-Origin-Opener-Policy': 'same-origin', 'Cross-Origin-Embedder-Policy': 'credentialless' }
	}
});
