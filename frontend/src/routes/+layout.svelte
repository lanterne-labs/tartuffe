<script lang="ts">
	// The fonts are served by the site itself: no request leaves for a font
	// host (privacy: Google Fonts would see every visitor's address).
	import '@fontsource/inter/400.css';
	import '@fontsource/inter/500.css';
	import '@fontsource/inter/600.css';
	import '@fontsource/inter/700.css';
	import '@fontsource/cormorant-garamond/500.css';
	import '@fontsource/cormorant-garamond/600.css';
	import '@fontsource/cormorant-garamond/700.css';
	import favicon from '$lib/assets/favicon.svg';
	import '../app.css';
	import { page } from '$app/stores';
	import { fly } from 'svelte/transition';
	import { lang, tr } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { API_BASE } from '$lib/config';
	import { session, signOut, refreshUser } from '$lib/auth';
	import SignInDialog from '$lib/components/SignInDialog.svelte';
	import EventBanner from '$lib/components/EventBanner.svelte';
	import SiteNav from '$lib/components/SiteNav.svelte';
	import { voteKey } from '$lib/vote/isoloir';
	import type { PollEvent } from '$lib/types';

	let { children } = $props();

	// The link preview of the page (X, WhatsApp, search engines): a page sets
	// it with `meta` in the data of its load, the others get the site's.
	const defaultMeta = {
		title: 'tartuffe.me',
		description: 'tartuffe.me, un projet de Lanterne Labs, laboratoire indépendant d’outils pour une démocratie plus transparente : le détecteur de contradictions des responsables politiques, et des expériences de vote en ligne privé.'
	};
	let meta = $derived({ ...defaultMeta, ...(($page.data as { meta?: { title?: string; description?: string; image?: string } })?.meta ?? {}) });

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	let userName = $state<string | null>(null);
	$effect(() => session.subscribe((s) => (userName = s ? s.user.display_name || tr('auth.anonymous', currentLang) : null)));

	// The featured pre-poll, the lab's temporary event, gets a link in the
	// nav with its end date, and a card in a corner (EventBanner).
	let featured = $state<PollEvent | null>(null);

	onMount(async () => {
		// The stored session may be stale: its pseudonym may have changed.
		refreshUser();
		try {
			const res = await fetch(`${API_BASE}/api/polls`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const events: PollEvent[] = await res.json();
			featured = events.find((e) => e.featured) ?? null;
		} catch (err) {
			console.error('failed to load the featured poll', err);
		}
	});
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
	<link rel="canonical" href={$page.url.origin + $page.url.pathname} />
	<meta name="description" content={meta.description} />
	<meta property="og:site_name" content="tartuffe.me" />
	<meta property="og:type" content="website" />
	<meta property="og:locale" content="fr_FR" />
	<meta property="og:url" content={$page.url.href} />
	<meta property="og:title" content={meta.title} />
	<meta property="og:description" content={meta.description} />
	{#if meta.image}
		<meta property="og:image" content={meta.image} />
		<meta name="twitter:image" content={meta.image} />
	{/if}
	<meta name="twitter:card" content="summary" />
	<meta name="twitter:title" content={meta.title} />
	<meta name="twitter:description" content={meta.description} />
</svelte:head>

<SiteNav lang={currentLang} pathname={$page.url.pathname} {featured} {userName} showProfile={!!$voteKey} onsignout={signOut} />

<SignInDialog />
{#if featured}
	<EventBanner event={featured} lang={currentLang} />
{/if}

{#key $page.url.pathname}
	<main class="page-content" in:fly={{ y: 8, duration: 200, delay: 100 }} out:fly={{ y: -8, duration: 150 }}>
		{@render children()}
	</main>
{/key}

<footer class="site-footer">
	<p class="footer-lab">{tr('footer.lab', currentLang)}</p>
	<!-- The dot is drawn by CSS on every link but the first, so a wrapped
	     line never ends with one. -->
	<ul class="footer-links">
		<li><a href="/lab">{tr('nav.lab', currentLang)}</a></li>
		<li><a href="/about">{tr('nav.about', currentLang)}</a></li>
		<li><a href="/about/privacy">{tr('nav.privacy', currentLang)}</a></li>
		<li><a href="/corrections">{tr('corrections.title', currentLang)}</a></li>
		<li><a href="/feed">{tr('nav.feed', currentLang)}</a></li>
		<li><a href="/contact">{tr('nav.contact', currentLang)}</a></li>
		<li><a href="/mentions-legales">{tr('nav.legal', currentLang)}</a></li>
	</ul>
</footer>

<style>
	.footer-lab {
		flex-basis: 100%;
		margin: 0 0 0.4rem;
		text-align: center;
		color: var(--text-muted);
		font-size: 0.8rem;
	}

	.site-footer {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: 0.5rem 0.75rem;
		max-width: 1200px;
		margin: 3rem auto 0;
		padding: 1.5rem 1rem 2rem;
		border-top: 1px solid var(--glass-border);
		color: var(--text-muted);
		font-size: 0.8rem;
	}
	.footer-links {
		flex-basis: 100%;
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: 0.5rem 0;
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.footer-links li {
		display: inline-flex;
		align-items: center;
		white-space: nowrap;
	}
	.footer-links li + li::before {
		content: '·';
		margin: 0 0.375rem;
	}
	.site-footer a {
		color: var(--text-secondary);
	}
	.site-footer a:hover {
		color: var(--accent-hover);
	}
	/* A finger needs a link at least 40px tall. */
	@media (pointer: coarse) {
		.footer-links {
			row-gap: 0;
		}
		.site-footer a {
			display: inline-flex;
			align-items: center;
			min-height: 2.5rem;
			padding: 0 0.25rem;
		}
		.footer-links li + li::before {
			margin: 0 0.125rem;
		}
	}


	.page-content {
		max-width: 1200px;
		margin: 0 auto;
		padding: 2rem;
	}
	@media (max-width: 560px) {
		.page-content {
			padding: 1.25rem 1rem;
		}
		/* Room for the event's bar at the bottom of a phone (EventBanner). */
		.site-footer {
			padding-bottom: calc(4.5rem + env(safe-area-inset-bottom, 0px));
		}
		/* The links wrap on a phone, and a dot would then open or close a
		   line: a gap separates them instead. */
		.footer-links {
			column-gap: 0.9rem;
		}
		.footer-links li + li::before {
			display: none;
		}
	}
</style>
