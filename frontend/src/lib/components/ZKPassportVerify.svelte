<script lang="ts">
	// Proves the holder's document with the ZKPassport app, for a voting
	// key. The server issues a one-time challenge; the request built here asks
	// the app to prove the holder's age (and nationality) and binds the
	// challenge and the key's commitment into the proof; onProof sends the
	// proofs to the API, which checks them itself
	// (backend/accounts/providers/zkpassport.go) and admits the commitment.
	import Steps, { type StepState } from './Steps.svelte';
	import { onDestroy } from 'svelte';
	import { challenge as askChallenge, binding, voterKeyOpen } from '$lib/voter';
	import { tr, type Lang } from '$lib/i18n';
	import favicon from '$lib/assets/favicon.svg';
	import ZKProofAnimation from './ZKProofAnimation.svelte';
	import { eventPaths } from '$lib/event';

	interface Props {
		config: Record<string, unknown>;
		lang: Lang;
		// commitment is the voting key's, bound into the proof.
		commitment: string;
		// onProof sends the proofs; a refusal is shown here.
		onProof: (payload: { challenge: string; proofs: unknown; result: unknown }) => Promise<void>;
	}

	let { config, lang, commitment, onProof }: Props = $props();

	type Step = 'idle' | 'starting' | 'scan' | 'received' | 'proving' | 'verifying' | 'rejected' | 'error';
	let step: Step = $state('idle');
	let url = $state('');
	let qr = $state('');
	let error = $state('');

	// flow is the scan's progress: the QR code scanned, the steps in the
	// app, the proof computed on the phone, its check here. A refusal or an
	// error stops it at the step that was running.
	const FLOW = ['qr', 'app', 'proof', 'check'] as const;
	const at: Record<string, number> = { starting: 0, scan: 0, received: 1, proving: 2, verifying: 3 };
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

	const isPhone = typeof navigator !== 'undefined' && /Android|iPhone|iPad|iPod/i.test(navigator.userAgent);

	async function start() {
		cancelled = false;
		error = '';
		step = 'starting';
		try {
			const challenge = await askChallenge('zkpassport');

			// Loaded on demand: the SDK is large and only this flow needs it.
			const { ZKPassport } = await import('@zkpassport/sdk');
			const zk = new ZKPassport(String(config.domain));
			let builder = await zk.request({
				name: 'tartuffe.me',
				logo: new URL(favicon, window.location.origin).href,
				purpose: tr('zk.purpose', lang),
				scope: String(config.scope),
				devMode: Boolean(config.dev_mode)
			});
			const minAge = Number(config.min_age ?? 0);
			if (minAge > 0) builder = builder.gte('age', minAge);
			const nationality = String(config.nationality ?? '');
			// eslint-disable-next-line @typescript-eslint/no-explicit-any
			if (nationality) builder = builder.in('nationality', [nationality as any]);
			builder = builder.bind('custom_data', binding(challenge, commitment));
			const req = builder.done();

			url = req.url;
			const QRCode = await import('qrcode');
			qr = await QRCode.toDataURL(req.url, { margin: 2, width: 480, errorCorrectionLevel: 'L' });
			step = 'scan';

			req.onRequestReceived(() => !cancelled && (step = 'received'));
			req.onGeneratingProof(() => !cancelled && (step = 'proving'));
			req.onReject(() => !cancelled && (step = 'rejected'));
			req.onError((e: unknown) => {
				if (cancelled) return;
				error = String(e);
				step = 'error';
			});
			req.onSuccess(async ({ proofs, result }: { proofs: unknown; result: unknown }) => {
				if (cancelled) return;
				step = 'verifying';
				try {
					await onProof({ challenge, proofs, result });
				} catch (err) {
					error = err instanceof Error ? err.message : String(err);
					step = 'error';
				}
			});
		} catch (err) {
			console.error('zkpassport verification failed', err);
			error = err instanceof Error ? err.message : String(err);
			step = 'error';
		}
	}

	function reset() {
		cancelled = true;
		step = 'idle';
		url = '';
		qr = '';
	}

	onDestroy(() => (cancelled = true));
</script>

<section class="zk">
	<div class="zk-head">
		<h3>{tr('zk.title', lang)}</h3>
		<span class="badge">{tr('zk.badge', lang)}</span>
	</div>
	<p class="why">{tr('zk.why', lang)}</p>
	{#if step === 'idle'}
		<ZKProofAnimation {lang} />
		<ol class="steps">
			<li>{tr('zk.step1', lang)}</li>
			<li>{tr('zk.step2', lang)}</li>
			<li>{tr('zk.step3', lang)}</li>
		</ol>
		<button class="primary" onclick={start}>{tr('zk.start', lang)}</button>
		<a class="more" href="{eventPaths().how}#cle" onclick={() => voterKeyOpen.set(false)}>{tr('zk.how', lang)}</a>
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
	{:else if step === 'rejected'}
		<p class="status warn">{tr('zk.rejected', lang)}</p>
		<button class="primary" onclick={start}>{tr('zk.retry', lang)}</button>
	{:else if step === 'error'}
		<p class="status err">{tr('zk.error', lang)} {error}</p>
		<button class="primary" onclick={start}>{tr('zk.retry', lang)}</button>
	{/if}

	{#if config.dev_mode}
		<p class="dev">{tr('zk.dev_mode', lang)}</p>
	{/if}
</section>

<style>
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
		margin-bottom: 0.25rem;
	}
	h3 {
		font-size: 0.95rem;
		margin: 0;
	}
	.why {
		margin: 0 0 0.75rem;
		font-size: 0.9rem;
		line-height: 1.5;
		color: var(--text-secondary);
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
	.steps {
		margin: 0 0 0.9rem;
		padding-left: 1.2rem;
		font-size: 0.82rem;
		line-height: 1.5;
		color: var(--text-secondary);
	}
	.primary {
		display: inline-block;
		width: 100%;
		padding: 0.65rem 1rem;
		background: var(--accent);
		color: white;
		border: none;
		border-radius: var(--radius-sm);
		font-weight: 600;
		font-size: 0.9rem;
		text-align: center;
		cursor: pointer;
	}
	.primary:hover {
		background: var(--accent-hover);
		color: white;
	}
	.block {
		margin-bottom: 0.6rem;
	}
	.more {
		display: block;
		margin-top: 0.6rem;
		font-size: 0.8rem;
		text-align: center;
	}
	.qr {
		display: block;
		margin: 0.25rem auto 0.6rem;
		border-radius: var(--radius-sm);
		background: white;
	}
	.status {
		font-size: 0.9rem;
		margin: 0.5rem 0 0.75rem;
		color: var(--text-primary);
	}
	.warn {
		color: var(--amber);
	}
	.err {
		color: var(--red);
		word-break: break-word;
	}
	.link {
		display: block;
		margin: 0 auto;
		background: none;
		border: none;
		color: var(--text-muted);
		text-decoration: underline;
		cursor: pointer;
		font-size: 0.8rem;
	}
	.dev {
		margin: 0.75rem 0 0;
		font-size: 0.72rem;
		color: var(--amber);
	}
</style>
