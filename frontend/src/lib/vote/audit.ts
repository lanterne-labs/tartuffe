// The audit of the vote, run in the visitor's browser (@isoloir/web
// audit): the whole log read from the mirror, every ballot proof checked
// with bb.js, every passport proof with the ZKPassport SDK (the
// certificate registry read on Ethereum through a public RPC), every
// tally recomputed, and the relays' copies compared with the log.

import { audit, bbVerifier, zkPassportVerifier, WebSocketTransport, type AuditProgress, type AuditResult, type AuditWasm, type ZkPassportSdk } from '@isoloir/web';
import { LOG_KEY, mirrorSources, wasmModule } from './isoloir';

// Public Ethereum endpoints (chainlist.org/chain/1), tried in order: the
// passport check only reads the certificate registry's roots.
const ETH_RPCS = ['https://ethereum-rpc.publicnode.com', 'https://eth.drpc.org', 'https://1rpc.io/eth'];

// A mirror file from the first source that has it: its text or bytes.
async function fromMirror<T>(path: string, read: (r: Response) => Promise<T>): Promise<T | null> {
	let failure: unknown = null;
	for (const s of mirrorSources()) {
		try {
			const r = await fetch(s.url(path, ''), { credentials: 'omit', cache: 'no-cache' });
			if (r.status === 404) continue;
			if (!r.ok) throw new Error(`HTTP ${r.status}`);
			return await read(r);
		} catch (e) {
			failure = e;
		}
	}
	if (failure) throw new Error(`${path}: ${failure instanceof Error ? failure.message : String(failure)}`);
	return null;
}

export type { AuditProgress, AuditResult };

/** Runs the audit; `onProgress` follows it. */
export async function runAudit(onProgress: (p: AuditProgress) => void, signal?: AbortSignal): Promise<AuditResult> {
	const wasm = (await wasmModule()) as unknown as Parameters<typeof audit>[0] & AuditWasm;
	const sdk = (await import('@zkpassport/sdk')) as unknown as ZkPassportSdk;
	const passports = ETH_RPCS.map((rpc) => zkPassportVerifier(sdk, rpc));
	return audit(wasm, {
		text: (p) => fromMirror(p, (r) => r.text()),
		logKey: LOG_KEY,
		verifyProof: bbVerifier((p) => fromMirror(p, async (r) => new Uint8Array(await r.arrayBuffer()))),
		// An endpoint that fails gives way to the next; a refusal is a refusal.
		verifyPassport: async (req) => {
			let last: unknown = null;
			for (const v of passports) {
				try {
					return await v(req);
				} catch (e) {
					last = e;
				}
			}
			throw last;
		},
		relays: new WebSocketTransport(),
		onProgress,
		signal
	});
}
