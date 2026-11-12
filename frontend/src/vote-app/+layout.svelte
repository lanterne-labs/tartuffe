<script lang="ts">
	// The vote app's frame (https://vote.tartuffe.me): the fonts and styles
	// of the site and its header, the same component as on the site
	// (SiteNav), whose links leave for tartuffe.me only on a click. The
	// event's title comes from the app's own build-time file, never from
	// the site.
	import '@fontsource-variable/schibsted-grotesk';
	import '@fontsource-variable/big-shoulders';
	import '@fontsource/inter/400.css';
	import '@fontsource/inter/500.css';
	import '@fontsource/inter/600.css';
	import '@fontsource/inter/700.css';
	import '@fontsource/cormorant-garamond/500.css';
	import '@fontsource/cormorant-garamond/600.css';
	import '@fontsource/cormorant-garamond/700.css';
	import favicon from '$lib/assets/favicon.svg';
	import '$lib/../app.css';
	import { page } from '$app/stores';
	import { onMount } from 'svelte';
	import { lang } from '$lib/i18n';
	import type { Lang } from '$lib/i18n';
	import SiteNav from '$lib/components/SiteNav.svelte';
	import { FEATURED_SLUG } from '$lib/event';
	import { loadEventMeta } from '$lib/vote/candidates';
	import { voteKey } from '$lib/vote/isoloir';

	let { children } = $props();
	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	const SITE = 'https://tartuffe.me';
	let featured = $state<{ slug: string; title_fr: string; title_en: string } | null>(null);
	onMount(async () => {
		try {
			featured = (await loadEventMeta(FEATURED_SLUG)).event;
		} catch (err) {
			console.error('the event of the vote app', err);
		}
	});
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<meta name="robots" content="noindex" />
	<!-- No Referer: the sites the app loads from (the candidates' photos on
	     Wikimedia, ZKPassport, the relays) are not told the page asking is the vote. -->
	<meta name="referrer" content="no-referrer" />
</svelte:head>

<SiteNav lang={currentLang} pathname={$page.url.pathname} site={SITE} {featured} showProfile={!!$voteKey} />

<main class="vote-app-main">
	{@render children()}
</main>

<style>
	.vote-app-main {
		max-width: 60rem;
		margin: 0 auto;
		padding: 0 1rem 3rem;
	}
</style>
