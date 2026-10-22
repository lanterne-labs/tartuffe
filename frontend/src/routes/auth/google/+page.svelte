<script lang="ts">
	// Where Google sends the sign-in back (src/lib/google.ts): the ID token is
	// in the fragment, which never reaches a server. In the popup it goes back
	// to the page that opened it; else this page finishes the sign-in itself.
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { signIn } from '$lib/auth';
	import { readPending, clearPending } from '$lib/google';
	import { lang, tr, type Lang } from '$lib/i18n';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let error: string | null = $state(null);

	onMount(async () => {
		const q = new URLSearchParams(window.location.hash.slice(1));
		const msg = { type: 'google-signin', state: q.get('state'), id_token: q.get('id_token'), error: q.get('error') };
		history.replaceState(null, '', window.location.pathname);
		if (window.opener && window.opener !== window) {
			try {
				window.opener.postMessage(msg, window.location.origin);
				window.close();
				return;
			} catch (err) {
				console.warn('the opener of the Google popup is gone', err);
			}
		}
		const pending = readPending();
		if (!pending || pending.state !== msg.state || !msg.id_token) {
			error = msg.error ?? tr('auth.google.failed', currentLang);
			return;
		}
		try {
			await signIn('google', { challenge: pending.nonce, id_token: msg.id_token });
			clearPending();
			// A popup that lost its opener closes: the session reached the page.
			if (window.name === 'google-signin') window.close();
			goto(pending.back || '/', { replaceState: true });
		} catch (err) {
			console.error('Google sign-in failed', err);
			error = err instanceof Error ? err.message : String(err);
		}
	});
</script>

<svelte:head><title>{tr('auth.title', currentLang)} — tartuffe.me</title></svelte:head>

<div class="callback">
	{#if error}
		<p class="error">{error}</p>
		<a href="/">{tr('auth.google.back', currentLang)}</a>
	{:else}
		<p>{tr('auth.google.signing_in', currentLang)}</p>
	{/if}
</div>

<style>
	.callback {
		max-width: 28rem;
		margin: 4rem auto;
		text-align: center;
		color: var(--text-secondary);
	}
	.error {
		color: var(--red);
	}
</style>
