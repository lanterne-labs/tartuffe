// The prover (SPEC §6.5): the circuit artifact and the prover's setup are
// loaded in the background as soon as the voter means to vote, so that the
// only wait is the proof itself. noir_js executes the circuit, bb.js
// proves it (UltraHonk, the noir-recursive target the server verifies).

import type { Sources } from './sources';

export interface ProofResult {
	proof: Uint8Array;
	publicInputs: string[];
	returnValue: unknown;
}

export interface Prover {
	prove(inputs: Record<string, unknown>): Promise<ProofResult>;
}

interface CompiledCircuit {
	bytecode: string;
	abi: unknown;
}

const SRS_POINTS = 1 << 17; // bb.js takes the compressed SRS in blocks of 2^17 points (4 MB)

let warm: Promise<Prover> | null = null;

// threads: all the cores up to 8 when the page is cross-origin isolated
// (the service worker shim sets the headers), else one.
function threads(): number {
	if (typeof navigator === 'undefined') return 1;
	if (!self.crossOriginIsolated) return 1;
	return Math.min(navigator.hardwareConcurrency || 1, 8);
}

// warmUp loads the artifact from the mirror and starts the prover; calling
// it again returns the same prover. The SRS comes through bb.js's loader;
// the service worker serves it from our host (static/sw.js).
export function warmUp(src: Sources, onStage?: (s: 'artifact' | 'backend' | 'ready') => void): Promise<Prover> {
	if (warm) return warm;
	warm = (async () => {
		onStage?.('artifact');
		const text = await src.text('circuit/ballot.json');
		if (!text) throw new Error('no circuit on the mirror');
		const circuit = JSON.parse(text) as CompiledCircuit;
		onStage?.('backend');
		const [{ Noir }, { Barretenberg, UltraHonkBackend }] = await Promise.all([import('@noir-lang/noir_js'), import('@aztec/bb.js')]);
		const noir = new Noir(circuit as never);
		console.warn('vote: prover starting, threads', threads());
		// The SRS: enough points for the circuit (15k gates: 2^15 covers it
		// with the ZK masking), not bb.js's 2^19 default (16 MB).
		const api = await Barretenberg.new({ threads: threads(), srsSize: SRS_POINTS });
		const backend = new UltraHonkBackend(circuit.bytecode, api);
		// A first proof-sized operation makes the SRS download and the key
		// setup happen now, not at the vote.
		await backend.getVerificationKey({ verifierTarget: 'noir-recursive' });
		console.warn('vote: prover ready');
		onStage?.('ready');
		return {
			async prove(inputs) {
				const t0 = performance.now();
				const { witness, returnValue } = await noir.execute(inputs as never);
				console.warn('vote: witness', Math.round(performance.now() - t0), 'ms');
				const p = await backend.generateProof(witness, { verifierTarget: 'noir-recursive' });
				console.warn('vote: proof', Math.round(performance.now() - t0), 'ms');
				return { proof: p.proof, publicInputs: p.publicInputs, returnValue };
			}
		} satisfies Prover;
	})();
	warm.catch(() => (warm = null));
	return warm;
}

export function proverReady(): boolean {
	return warm !== null;
}
