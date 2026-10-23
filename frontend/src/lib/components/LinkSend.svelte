<script lang="ts">
	// The side of a device link on the device that has the voting key: it
	// reads the QR code the new device shows (with the camera here, or the
	// phone's own camera opens /link), asks the voter to confirm, and sends
	// the key, and a session of the account if signed in, encrypted for the
	// new device only.
	import { onDestroy } from 'svelte';
	import { get } from 'svelte/store';
	import { tr, type Lang } from '$lib/i18n';
	import { session, authFetch } from '$lib/auth';
	import { voterKey, parseLink, answerLink, type LinkPayload } from '$lib/voter';

	let { lang, link = null, ondone }: { lang: Lang; link?: { channel: string; pub: Uint8Array } | null; ondone?: () => void } = $props();

	let target = $state<{ channel: string; pub: Uint8Array } | null>(null);
	$effect(() => {
		if (link) target = link;
	});
	let scanning = $state(false);
	let phase: 'idle' | 'sending' | 'sent' | 'error' = $state('idle');
	let error = $state('');
	let video: HTMLVideoElement | undefined = $state();
	let stream: MediaStream | null = null;
	let raf = 0;

	async function scan() {
		error = '';
		scanning = true;
		try {
			stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: 'environment' } });
			await Promise.resolve();
			if (!video) return;
			video.srcObject = stream;
			await video.play();
			const { default: jsQR } = await import('jsqr');
			const canvas = document.createElement('canvas');
			const ctx = canvas.getContext('2d', { willReadFrequently: true });
			const tick = () => {
				if (!video || !ctx || !scanning) return;
				if (video.readyState === video.HAVE_ENOUGH_DATA) {
					canvas.width = video.videoWidth;
					canvas.height = video.videoHeight;
					ctx.drawImage(video, 0, 0);
					const img = ctx.getImageData(0, 0, canvas.width, canvas.height);
					const code = jsQR(img.data, img.width, img.height);
					const parsed = code ? parseLink(code.data) : null;
					if (parsed) {
						stop();
						target = parsed;
						return;
					}
				}
				raf = requestAnimationFrame(tick);
			};
			raf = requestAnimationFrame(tick);
		} catch (err) {
			console.warn('camera unavailable', err);
			error = tr('link.send.no_camera', lang);
			stop();
		}
	}

	function stop() {
		scanning = false;
		cancelAnimationFrame(raf);
		stream?.getTracks().forEach((t) => t.stop());
		stream = null;
	}

	async function send() {
		const key = get(voterKey);
		if (!key || !target) return;
		phase = 'sending';
		try {
			const payload: LinkPayload = { secret: key.secret };
			const s = get(session);
			if (s) {
				// A session of its own for the new device: signing out here
				// does not sign it out.
				const res = await authFetch('/api/auth/sessions', { method: 'POST' });
				if (res.ok) {
					const fresh = (await res.json()) as { token: string; expires_at: string };
					payload.session = { ...fresh, user: s.user };
				}
			}
			await answerLink(target, payload);
			phase = 'sent';
			ondone?.();
		} catch (err) {
			console.error('device link failed', err);
			error = err instanceof Error ? err.message : String(err);
			phase = 'error';
		}
	}

	onDestroy(stop);
</script>

<div class="send">
	{#if !$voterKey}
		<p class="muted">{tr('link.send.no_key', lang)}</p>
	{:else if phase === 'sent'}
		<p class="ok">{tr('link.send.sent', lang)}</p>
	{:else if target}
		<p class="confirm">{tr($session ? 'link.send.confirm_account' : 'link.send.confirm', lang)}</p>
		<p class="muted small">{tr('link.send.own_only', lang)}</p>
		<div class="actions">
			<button class="primary" onclick={send} disabled={phase === 'sending'}>{tr('link.send.button', lang)}</button>
			<button class="secondary" onclick={() => (target = null)} disabled={phase === 'sending'}>{tr('vote.cancel', lang)}</button>
		</div>
	{:else}
		<ol class="steps">
			<li>{tr('link.send.step1', lang)}</li>
			<li>{tr('link.send.step2', lang)}</li>
		</ol>
		{#if scanning}
			<!-- svelte-ignore a11y_media_has_caption -->
			<video bind:this={video} playsinline muted class="camera"></video>
			<button class="secondary" onclick={stop}>{tr('vote.cancel', lang)}</button>
		{:else}
			<button class="primary" onclick={scan}>{tr('link.send.scan', lang)}</button>
		{/if}
	{/if}
	{#if error}
		<p class="error" role="alert">{error}</p>
	{/if}
</div>

<style>
	.send {
		display: grid;
		gap: 0.5rem;
	}
	.steps {
		margin: 0;
		padding-left: 1.25rem;
		font-size: 0.88rem;
		line-height: 1.5;
	}
	.camera {
		width: 100%;
		max-height: 320px;
		border-radius: var(--radius-md);
		background: black;
		object-fit: cover;
	}
	.confirm {
		margin: 0;
		line-height: 1.5;
		font-weight: 600;
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
	.primary,
	.secondary {
		padding: 0.5rem 1rem;
		border-radius: var(--radius-sm);
		font: inherit;
		font-size: 0.88rem;
		font-weight: 600;
		cursor: pointer;
		justify-self: start;
	}
	.primary {
		background: var(--accent);
		color: white;
		border: none;
	}
	.secondary {
		background: transparent;
		color: var(--text-primary);
		border: 1px solid var(--glass-border);
	}
	.primary:disabled,
	.secondary:disabled {
		opacity: 0.5;
	}
	.muted {
		color: var(--text-secondary);
		font-size: 0.85rem;
		margin: 0;
		line-height: 1.45;
	}
	.small {
		font-size: 0.8rem;
	}
	.ok {
		color: var(--green);
		margin: 0;
	}
	.error {
		color: var(--red);
		font-size: 0.85rem;
		margin: 0;
	}
</style>
