<script lang="ts">
	// Opened by the phone's camera on the QR code a new device shows: this
	// device, if it has the voting key, sends it there (LinkSend). The code is
	// in the fragment, which the browser never sends to a server.
	import { onMount } from 'svelte';
	import { lang, tr } from '$lib/i18n';
	import { parseLink, voterKey } from '$lib/voter';
	import LinkSend from '$lib/components/LinkSend.svelte';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let link = $state<{ channel: string; pub: Uint8Array } | null>(null);
	let invalid = $state(false);

	onMount(() => {
		link = parseLink(window.location.hash);
		invalid = link === null;
		// The code is used once: it leaves the address bar and the history.
		history.replaceState(null, '', '/link');
	});
</script>

<svelte:head>
	<title>{tr('link.page.title', currentLang)} — tartuffe.me</title>
	<meta name="robots" content="noindex" />
</svelte:head>

<section class="page">
	<h1>{tr('link.page.title', currentLang)}</h1>
	{#if invalid}
		<p>{tr('link.page.invalid', currentLang)}</p>
	{:else if link}
		{#if !$voterKey}
			<p>{tr('link.send.no_key', currentLang)}</p>
		{:else}
			<LinkSend lang={currentLang} {link} />
		{/if}
	{/if}
	<p class="back"><a href="/profile">{tr('link.page.profile', currentLang)}</a></p>
</section>

<style>
	.page {
		max-width: 34rem;
		margin: 2rem auto;
		padding: 0 1rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: 1.8rem;
		margin: 0 0 1rem;
	}
	.back {
		margin-top: 1.5rem;
		font-size: 0.9rem;
	}
</style>
