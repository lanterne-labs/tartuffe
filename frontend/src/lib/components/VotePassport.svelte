<script lang="ts">
	// The passport scan (isoloir SPEC §6, step 1): the ZKPassport app proves
	// on the phone that the holder meets the registry's rules (an adult
	// French national with a valid passport) and binds the registration
	// key's custom data into the proof. Nothing goes to our server: the
	// proof goes to the session, which posts it with the registration. No
	// cloud prover: the proof is made on the device. The identifier is the
	// document's own, non-salted one, so that a passport cannot register
	// twice under another identifier. The query is the registry's: the
	// ingest's verifier rebuilds it from the same rules.
	import Steps, { type StepState } from './Steps.svelte';
	import { onDestroy } from 'svelte';
	import { tr, type Lang } from '$lib/i18n';
	import favicon from '$lib/assets/favicon.svg';
	import ZKProofAnimation from './ZKProofAnimation.svelte';
	import { messageOf } from '$lib/vote/errors';
	import type { PassportProof, RegistrationRequest } from '@isoloir/web';

	interface Props {
		lang: Lang;
		request: RegistrationRequest;
		// The server's clock, ms (the session's): the proof's day and date.
		serverNow: () => number;
		devMode?: boolean;
		// simulate shows a button that stands in for the app (local trials).
		simulate?: boolean;
		// onProof takes the proof; what it does with it (the registration)
		// is the caller's, with its own states: it never throws back here.
		onProof: (p: PassportProof) => Promise<void>;
	}

	let { lang, request, serverNow, devMode = false, simulate = false, onProof }: Props = $props();

	// The app, once it took the request, is given this long before the
	// page says it does not answer (the voter can always cancel before).
	const APP_TIMEOUT = 3 * 60_000;

	// The proof's date, Unix seconds, on the server's clock (SPEC §6 step 4).
	const proofDate = () => Math.floor(serverNow() / 1000);

	// A dev registry takes any bundle that names a uid.
	async function simulateScan() {
		step = 'posting';
		const id = Array.from(crypto.getRandomValues(new Uint8Array(16)), (b) => b.toString(16).padStart(2, '0')).join('');
		await onProof({ uid: 'dev-' + id, proof: { uid: 'dev-' + id, simulated: true, customData: request.customData }, proofDate: proofDate() });
	}

	type Step = 'idle' | 'starting' | 'scan' | 'received' | 'proving' | 'posting' | 'rejected' | 'error';
	let step: Step = $state('idle');
	let url = $state('');
	let qr = $state('');
	let error = $state('');
	let detail = $state('');

	const FLOW = ['qr', 'app', 'proof', 'check'] as const;
	const at: Record<string, number> = { starting: 0, scan: 0, received: 1, proving: 2, posting: 3 };
	let reached = $state(0);
	$effect(() => {
		if (step in at) reached = at[step];
	});
	let flow = $derived(
		FLOW.map((k, i) => ({
			label: tr(`zk.flow.${k}`, lang),
			state: (i < reached ? 'ok' : i > reached ? 'todo' : step === 'rejected' || step === 'error' ? 'failed' : 'running') as StepState
		}))
	);
	let cancelled = false;
	let appTimer: ReturnType<typeof setTimeout> | null = null;
	const isPhone = typeof navigator !== 'undefined' && /Android|iPhone|iPad|iPod/i.test(navigator.userAgent);

	function armTimeout() {
		if (appTimer) clearTimeout(appTimer);
		appTimer = setTimeout(() => {
			if (cancelled || (step !== 'received' && step !== 'proving')) return;
			error = tr('zk.timeout', lang);
			detail = '';
			step = 'error';
		}, APP_TIMEOUT);
	}

	function fail(e: unknown) {
		console.error('zkpassport', e);
		error = tr('zk.error', lang);
		detail = messageOf(e);
		step = 'error';
	}

	async function start() {
		cancelled = false;
		error = '';
		detail = '';
		step = 'starting';
		try {
			const { ZKPassport, NullifierType } = await import('@zkpassport/sdk');
			const e = request.eligibility;
			const zk = new ZKPassport(request.domain);
			let builder = await zk.request({
				name: 'tartuffe.me',
				logo: new URL(favicon, window.location.origin).href,
				purpose: tr('zk.purpose', lang),
				scope: request.scope,
				devMode,
				mode: 'compressed',
				// The proof is valid as long as the registry says; the ingest
				// judges freshness by proof_date.
				validity: request.validity,
				uniqueIdentifierType: NullifierType.NON_SALTED
			});
			if (e.min_age) builder = builder.gte('age', e.min_age);
			// eslint-disable-next-line @typescript-eslint/no-explicit-any
			if (e.nationality?.length) builder = builder.in('nationality', e.nationality as any);
			// eslint-disable-next-line @typescript-eslint/no-explicit-any
			if (e.document_type) builder = (builder as any).eq('document_type', e.document_type);
			// A passport still valid on the proof's day: the base proof does
			// not check the expiry, a query does (the verifier expects that day).
			if (e.require_expiry) builder = builder.gte('expiry_date', new Date(serverNow()));
			builder = builder.bind('custom_data', request.customData);
			const req = builder.done();
			url = req.url;
			const QRCode = await import('qrcode');
			qr = await QRCode.toDataURL(req.url, { margin: 2, width: 480, errorCorrectionLevel: 'L' });
			step = 'scan';
			req.onRequestReceived(() => {
				if (cancelled) return;
				step = 'received';
				armTimeout();
			});
			req.onGeneratingProof(() => {
				if (cancelled) return;
				step = 'proving';
				armTimeout();
			});
			req.onReject(() => !cancelled && (step = 'rejected'));
			req.onError((e: unknown) => {
				if (cancelled) return;
				fail(e);
			});
			// The result names the document's identifier; the proofs are
			// verified by the ingest (its verifier rebuilds this query), so a
			// verdict of the browser is only logged.
			req.onResult(async ({ uniqueIdentifier, verified, result, proofs }) => {
				if (cancelled) return;
				if (appTimer) clearTimeout(appTimer);
				if (!verified) console.warn('zkpassport: the SDK did not verify the proofs in the browser; the ingest will');
				if (!uniqueIdentifier) {
					fail(new Error('the passport app returned no unique identifier'));
					return;
				}
				step = 'posting';
				await onProof({ uid: uniqueIdentifier, proof: { proofs, result }, proofDate: proofDate() });
			});
		} catch (err) {
			fail(err);
		}
	}

	function reset() {
		cancelled = true;
		if (appTimer) clearTimeout(appTimer);
		step = 'idle';
		url = '';
		qr = '';
		error = '';
		detail = '';
	}

	onDestroy(() => {
		cancelled = true;
		if (appTimer) clearTimeout(appTimer);
	});
</script>

<section class="zk">
	<div class="zk-head">
		<h3>{tr('zk.title', lang)}</h3>
		<span class="badge">{tr('zk.badge', lang)}</span>
	</div>
	<details class="only">
		<summary>{tr('zk.more', lang)}</summary>
		<p>{tr('zk.why', lang)}</p>
		<p>{tr('zk.needs', lang)}</p>
		<p>{tr('v2.passport.only', lang)}</p>
	</details>
	{#if step === 'idle'}
		<ZKProofAnimation {lang} />
		<button class="primary" onclick={start}>{tr('zk.start', lang)}</button>
		{#if simulate}
			<button class="link" onclick={simulateScan}>{tr('zk.simulate', lang)}</button>
		{/if}
	{/if}
	{#if step !== 'idle'}
		<div class="flow"><Steps layout="row" steps={flow} /></div>
	{/if}
	{#if step === 'scan'}
		{#if isPhone}
			<a class="primary block" href={url}>{tr('zk.open_app', lang)}</a>
			<p class="muted small">{tr('zk.phone_hint', lang)}</p>
		{:else}
			<img class="qr" src={qr} alt={tr('zk.qr_alt', lang)} width="260" height="260" />
			<p class="muted small">{tr('zk.scan_hint', lang)}</p>
		{/if}
		<p class="muted small">{tr('zk.get_app', lang)} <a href="https://zkpassport.id" target="_blank" rel="noopener">zkpassport.id</a></p>
		<button class="link" onclick={reset}>{tr('zk.cancel', lang)}</button>
	{:else if step === 'received' || step === 'proving' || step === 'starting'}
		<button class="link" onclick={reset}>{tr('zk.cancel', lang)}</button>
	{:else if step === 'rejected'}
		<p class="status warn">{tr('zk.rejected', lang)}</p>
		<button class="primary" onclick={start}>{tr('zk.retry', lang)}</button>
	{:else if step === 'error'}
		<p class="status err" role="alert">{error}</p>
		{#if detail}
			<details class="tech"><summary>{tr('v2.details', lang)}</summary><p class="muted small detail">{detail}</p></details>
		{/if}
		<button class="primary" onclick={start}>{tr('zk.retry', lang)}</button>
	{/if}
	{#if devMode}
		<p class="dev">{tr('zk.dev_mode', lang)}</p>
	{/if}
</section>

<style>
	.only {
		margin: 0 0 0.8rem;
		font-size: 0.82rem;
		color: var(--text-muted);
	}
	.only summary {
		cursor: pointer;
		text-decoration: underline;
		text-underline-offset: 0.15em;
	}
	.only p {
		margin: 0.45rem 0 0;
		line-height: 1.45;
		color: var(--text-secondary);
	}
	.tech {
		margin: 0 0 0.6rem;
		font-size: 0.78rem;
		color: var(--text-muted);
	}
	.tech summary {
		cursor: pointer;
	}
	.tech p {
		margin: 0.3rem 0 0;
		font-family: var(--font-mono, monospace);
		overflow-wrap: anywhere;
	}
	.flow {
		margin: 0.75rem 0;
	}
	.zk {
		padding: 1rem;
		border: 1px solid var(--accent);
		border-radius: var(--radius-md);
		background: var(--bg-card);
	}
	.zk-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}
	h3 {
		font-size: 0.95rem;
		margin: 0;
	}
	.badge {
		font-size: 0.65rem;
		font-weight: 700;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--green);
		border: 1px solid var(--green);
		border-radius: var(--radius-full);
		padding: 0.1rem 0.5rem;
		white-space: nowrap;
	}
	.muted {
		color: var(--text-secondary);
		font-size: 0.85rem;
		line-height: 1.45;
		margin: 0 0 0.75rem;
	}
	.small {
		font-size: 0.78rem;
		text-align: center;
	}
	.detail {
		font-family: var(--font-mono, monospace);
		word-break: break-word;
	}
	.primary {
		display: inline-block;
		width: 100%;
		padding: 0.65rem 1rem;
		background: var(--accent);
		color: white;
		border: none;
		border-radius: var(--radius-md);
		font-weight: 600;
		font-size: 0.95rem;
		cursor: pointer;
		text-align: center;
		text-decoration: none;
	}
	.primary:hover {
		background: var(--accent-hover);
	}
	.block {
		margin-bottom: 0.5rem;
	}
	.qr {
		display: block;
		margin: 0 auto 0.5rem;
		border-radius: var(--radius-md);
		background: white;
		padding: 0.5rem;
		width: 260px;
		height: 260px;
	}
	.link {
		background: none;
		border: none;
		color: var(--text-secondary);
		text-decoration: underline;
		cursor: pointer;
		font-size: 0.85rem;
		display: block;
		margin: 0 auto;
	}
	.status {
		margin: 0.5rem 0;
		font-size: 0.9rem;
	}
	.warn {
		color: var(--amber);
	}
	.err {
		color: var(--red);
	}
	.dev {
		margin: 0.75rem 0 0;
		font-size: 0.75rem;
		color: var(--amber);
	}
</style>
