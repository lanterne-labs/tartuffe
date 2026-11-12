<script lang="ts">
	// The fonts are served by the site itself: no request leaves for a font
	// host (privacy: Google Fonts would see every visitor's address).
	import '@fontsource/inter/400.css';
	import { LAB_URL, LAB_DOMAIN } from '$lib/contact';
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
	// A page without an image of its own (a person's page has the photo) shows
	// the logo: X draws an empty grey box for a link without one. A PNG: X
	// reads no SVG (static/og-logo.svg is its source). 256 px on purpose:
	// from 300 px wide WhatsApp draws the image across the whole message.
	let pageMeta = $derived((($page.data as { meta?: { title?: string; description?: string; image?: string } })?.meta ?? {}));
	let meta = $derived({ ...defaultMeta, ...pageMeta, image: pageMeta.image ?? $page.url.origin + '/og-logo.png' });

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
	<meta property="og:image" content={meta.image} />
	<meta name="twitter:image" content={meta.image} />
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
	<p class="footer-lab">{tr('footer.lab', currentLang)} <a href={LAB_URL} target="_blank" rel="noopener">{LAB_DOMAIN}&nbsp;↗</a></p>
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
	<ul class="footer-social">
		<li>
			<a href={LAB_URL} target="_blank" rel="noopener" aria-label="Lanterne Labs — {LAB_DOMAIN}" title={LAB_DOMAIN}>
				<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 2a10 10 0 1 0 0 20 10 10 0 0 0 0-20zm6.93 6h-2.95a15.7 15.7 0 0 0-1.38-3.56A8.03 8.03 0 0 1 18.93 8zM12 4.04c.83 1.2 1.48 2.53 1.91 3.96h-3.82c.43-1.43 1.08-2.76 1.91-3.96zM4.26 14a8.2 8.2 0 0 1 0-4h3.38a16.5 16.5 0 0 0 0 4H4.26zm.81 2h2.95c.32 1.25.78 2.45 1.38 3.56A7.99 7.99 0 0 1 5.07 16zm2.95-8H5.07a7.99 7.99 0 0 1 4.33-3.56A15.7 15.7 0 0 0 8.02 8zM12 19.96c-.83-1.2-1.48-2.53-1.91-3.96h3.82c-.43 1.43-1.08 2.76-1.91 3.96zM14.34 14H9.66a14.7 14.7 0 0 1 0-4h4.68a14.7 14.7 0 0 1 0 4zm.26 5.56c.6-1.11 1.06-2.31 1.38-3.56h2.95a8.03 8.03 0 0 1-4.33 3.56zM16.36 14a16.5 16.5 0 0 0 0-4h3.38a8.2 8.2 0 0 1 0 4h-3.38z"/></svg>
			</a>
		</li>
		<li>
			<a href="https://x.com/lanternelabs" target="_blank" rel="noopener" aria-label="Lanterne Labs — X" title="X">
				<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z"/></svg>
			</a>
		</li>
		<li>
			<a href="https://github.com/lanterne-labs" target="_blank" rel="noopener" aria-label="Lanterne Labs — GitHub" title="GitHub">
				<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12"/></svg>
			</a>
		</li>
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
	.footer-social {
		flex-basis: 100%;
		display: flex;
		justify-content: center;
		gap: 0.5rem;
		list-style: none;
		margin: 0.25rem 0 0;
		padding: 0;
	}
	.footer-social a {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 2.5rem;
		height: 2.5rem;
	}
	.footer-social svg {
		width: 1.25rem;
		height: 1.25rem;
		fill: currentColor;
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
