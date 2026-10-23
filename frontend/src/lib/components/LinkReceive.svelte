<script lang="ts">
	// The new device's side of a device link: it shows a QR code, the voter
	// scans it with the device that has the voting key, and the key (and the
	// account's session, if that device is signed in) arrives here, end to
	// end encrypted. One step, as with WhatsApp or Signal.
	import { onDestroy, onMount } from 'svelte';
	import { tr, type Lang } from '$lib/i18n';
	import { session } from '$lib/auth';
	import { offerLink, adoptSecret } from '$lib/voter';
	import type { AppUser } from '$lib/types';

	let { lang, ondone }: { lang: Lang; ondone: () => void } = $props();

	let qr = $state('');
	let url = $state('');
	let phase: 'starting' | 'waiting' | 'expired' | 'error' = $state('starting');
	let error = $state('');
	let abort: AbortController | null = null;

	async function start() {
		abort?.abort();
		abort = new AbortController();
		phase = 'starting';
		try {
			const offer = await offerLink();
			url = offer.url;
			const QRCode = await import('qrcode');
			qr = await QRCode.toDataURL(offer.url, { margin: 2, width: 420, errorCorrectionLevel: 'M' });
			phase = 'waiting';
			const got = await offer.wait(abort.signal);
			if (!got) {
				if (!abort.signal.aborted) phase = 'expired';
				return;
			}
			// The device that sent it keeps a copy.
			adoptSecret(got.secret, { linked: 1 });
			if (got.session && !$session) session.set(got.session as { token: string; expires_at: string; user: AppUser });
			ondone();
		} catch (err) {
			if (abort?.signal.aborted) return;
			console.error('device link failed', err);
			error = err instanceof Error ? err.message : String(err);
			phase = 'error';
		}
	}

	onMount(start);
	onDestroy(() => abort?.abort());
</script>

<section class="receive">
	<p class="lead">{tr('link.receive.lead', lang)}</p>
	{#if phase === 'starting'}
		<p class="muted">{tr('person.loading', lang)}</p>
	{:else if phase === 'waiting'}
		<img class="qr" src={qr} alt={tr('link.receive.qr_alt', lang)} width="240" height="240" />
		<ol class="steps">
			<li>{tr('link.receive.step1', lang)}</li>
			<li>{tr('link.receive.step2', lang)}</li>
		</ol>
		<p class="muted small">{tr('link.receive.waiting', lang)}</p>
		<details>
			<summary>{tr('link.receive.no_camera', lang)}</summary>
			<p class="url">{url}</p>
		</details>
	{:else if phase === 'expired'}
		<p class="muted">{tr('link.receive.expired', lang)}</p>
		<button class="primary" onclick={start}>{tr('zk.retry', lang)}</button>
	{:else}
		<p class="error">{error}</p>
		<button class="primary" onclick={start}>{tr('zk.retry', lang)}</button>
	{/if}
</section>

<style>
	.receive {
		padding: 1rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		background: var(--bg-card);
		text-align: center;
	}
	.lead {
		margin: 0 0 0.75rem;
		line-height: 1.5;
	}
	.qr {
		display: block;
		margin: 0 auto 0.75rem;
		background: white;
		border-radius: var(--radius-sm);
		max-width: 100%;
		height: auto;
	}
	.steps {
		text-align: left;
		margin: 0 0 0.5rem;
		padding-left: 1.25rem;
		font-size: 0.88rem;
		line-height: 1.5;
	}
	.muted {
		color: var(--text-secondary);
		font-size: 0.85rem;
		margin: 0;
	}
	.small {
		font-size: 0.8rem;
	}
	details {
		margin-top: 0.75rem;
		font-size: 0.8rem;
		color: var(--text-secondary);
	}
	.url {
		word-break: break-all;
		font-family: var(--font-mono, monospace);
		text-align: left;
	}
	.primary {
		margin-top: 0.5rem;
		padding: 0.5rem 1rem;
		background: var(--accent);
		color: white;
		border: none;
		border-radius: var(--radius-sm);
		font-weight: 600;
		cursor: pointer;
	}
	.error {
		color: var(--red);
		font-size: 0.85rem;
	}
</style>
