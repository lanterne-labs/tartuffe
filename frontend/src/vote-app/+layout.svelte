<script lang="ts">
	// The vote app's frame (https://lanterne-labs.github.io): the fonts and
	// styles of the site, a strip naming the site, the language toggle.
	// The event's pages bring their own sub-menu.
	import '@fontsource/inter/400.css';
	import '@fontsource/inter/500.css';
	import '@fontsource/inter/600.css';
	import '@fontsource/inter/700.css';
	import '@fontsource/cormorant-garamond/500.css';
	import '@fontsource/cormorant-garamond/600.css';
	import '@fontsource/cormorant-garamond/700.css';
	import favicon from '$lib/assets/favicon.svg';
	import '$lib/../app.css';
	import { lang, setLang, tr } from '$lib/i18n';
	import type { Lang } from '$lib/i18n';

	let { children } = $props();
	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<meta name="robots" content="noindex" />
	<!-- No Referer: the sites the app loads from (the candidates' photos on
	     Wikimedia, ZKPassport, the relays) are not told the page asking is the vote. -->
	<meta name="referrer" content="no-referrer" />
</svelte:head>

<header class="vote-app-strip">
	<a class="brand" href="https://tartuffe.me">tartuffe<span class="dot">.</span>me</a>
	<span class="what">{tr('event.menu.vote', currentLang)}</span>
	<div class="lang-toggle" role="group" aria-label="Language">
		<button class="lang-pill" class:active={currentLang === 'fr'} onclick={() => setLang('fr')}>FR</button>
		<button class="lang-pill" class:active={currentLang === 'en'} onclick={() => setLang('en')}>EN</button>
	</div>
</header>

<main class="vote-app-main">
	{@render children()}
</main>

<style>
	.vote-app-strip {
		display: flex;
		align-items: center;
		gap: 1rem;
		padding: 0.6rem 1rem;
		border-bottom: 1px solid var(--glass-border);
	}
	.brand {
		font-family: var(--font-serif, 'Cormorant Garamond', serif);
		font-weight: 700;
		font-size: 1.25rem;
		color: var(--text-primary);
		text-decoration: none;
	}
	.dot {
		color: var(--accent);
	}
	.what {
		color: var(--text-muted);
		font-size: 0.85rem;
	}
	.lang-toggle {
		margin-left: auto;
		display: flex;
		gap: 0.25rem;
	}
	.lang-pill {
		border: 1px solid var(--glass-border);
		background: none;
		color: var(--text-muted);
		border-radius: var(--radius-full);
		padding: 0.15rem 0.6rem;
		font-size: 0.75rem;
		cursor: pointer;
	}
	.lang-pill.active {
		color: var(--text-primary);
		border-color: var(--accent);
	}
	.vote-app-main {
		max-width: 60rem;
		margin: 0 auto;
		padding: 0 1rem 3rem;
	}
</style>
