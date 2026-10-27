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
	import { lang, setLang, tr } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { API_BASE } from '$lib/config';
	import { session, signOut, refreshUser } from '$lib/auth';
	import SignInDialog from '$lib/components/SignInDialog.svelte';
	import EventBanner from '$lib/components/EventBanner.svelte';
	import { voteKey } from '$lib/vote/key';
	import { eventPaths } from '$lib/event';
	import type { PollEvent } from '$lib/types';

	let { children } = $props();

	// The link preview of the page (X, WhatsApp, search engines): a page sets
	// it with `meta` in the data of its load, the others get the site's.
	const defaultMeta = {
		title: 'tartuffe.me',
		description: 'Le labo tartuffe.me construit des outils pour des démocraties plus transparentes : le détecteur de contradictions des responsables politiques, et des expériences de vote en ligne privé.'
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

	// The nav's menus (the event's three pages, the About pages) open on a
	// click or a tap, on hover with a mouse, and close on Escape, outside, or
	// on a new page. One is open at a time.
	type Menu = 'event' | 'about';
	let openMenu = $state<Menu | null>(null);
	let menuEls = $state<Record<Menu, HTMLElement | null>>({ event: null, about: null });
	$effect(() => {
		$page.url.pathname;
		openMenu = null;
	});
	function toggleMenu(m: Menu) {
		openMenu = openMenu === m ? null : m;
	}
	function onWindowClick(e: MouseEvent) {
		const el = openMenu && menuEls[openMenu];
		if (el && !el.contains(e.target as Node)) openMenu = null;
	}
	function onWindowKey(e: KeyboardEvent) {
		if (e.key === 'Escape' && openMenu) {
			menuEls[openMenu]?.querySelector('button')?.focus();
			openMenu = null;
		}
	}
	function onMenuFocusOut(m: Menu, e: FocusEvent) {
		const el = menuEls[m];
		if (openMenu === m && el && e.relatedTarget && !el.contains(e.relatedTarget as Node)) openMenu = null;
	}
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

<svelte:window onclick={onWindowClick} onkeydown={onWindowKey} />

<nav class="nav-bar">
	<div class="nav-content">
		<a href="/" class="nav-brand" aria-label="tartuffe.me — home">
			<svg class="mask-glyph" viewBox="0 0 64 40" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
				<path d="M4 12 C8 4, 16 2, 22 6 C26 8, 28 10, 32 12 C36 10, 38 8, 42 6 C48 2, 56 4, 60 12 C62 20, 58 28, 50 32 C44 34, 38 32, 34 26 C33 24, 31 24, 30 26 C26 32, 20 34, 14 32 C6 28, 2 20, 4 12 Z"/>
				<ellipse cx="18" cy="17" rx="5" ry="2.5"/>
				<ellipse cx="46" cy="17" rx="5" ry="2.5"/>
			</svg>
			<span class="brand-text">
				<span class="brand-name">tartuffe<span class="brand-dot">.</span>me</span>
			</span>
		</a>
		<div class="nav-right">
			<div class="lang-toggle" role="group" aria-label="Language">
				<button
					class="lang-pill"
					class:active={currentLang === 'fr'}
					aria-pressed={currentLang === 'fr'}
					onclick={() => setLang('fr')}
					title="Français"
				>FR</button>
				<button
					class="lang-pill"
					class:active={currentLang === 'en'}
					aria-pressed={currentLang === 'en'}
					onclick={() => setLang('en')}
					title="English"
				>EN</button>
			</div>
			{#if featured}
				{@const ep = eventPaths(featured.slug)}
				<div class="nav-drop nav-event" class:open={openMenu === 'event'} bind:this={menuEls.event} onfocusout={(e) => onMenuFocusOut('event', e)}>
					<button
						class="nav-link nav-link--poll"
						class:active={$page.url.pathname.startsWith('/election/')}
						aria-haspopup="true"
						aria-expanded={openMenu === 'event'}
						aria-controls="event-menu"
						title={tr('event.label', currentLang)}
						onclick={() => toggleMenu('event')}
					>
						<span class="poll-tag">{tr('event.tag_short', currentLang)}</span>
						<span class="poll-title">{currentLang === 'en' ? featured.title_en : featured.title_fr}</span>
						<!-- On a phone the title does not fit: the year says it. -->
						<span class="poll-title-short">{featured.title_fr.match(/\d{4}/)?.[0] ?? featured.title_fr}</span>
						<svg class="caret" viewBox="0 0 12 12" aria-hidden="true"><path d="M3 4.5l3 3 3-3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
					</button>
					<div class="drop-menu" id="event-menu">
						{#each [['intro', ep.intro], ['vote', ep.vote], ['how', ep.how]] as [k, href] (k)}
							<a {href} class:active={$page.url.pathname === href} aria-current={$page.url.pathname === href ? 'page' : undefined}>
								<strong>{tr(`event.menu.${k}`, currentLang)}</strong>
								<span>{tr(`event.menu.${k}_sub`, currentLang)}</span>
							</a>
						{/each}
					</div>
				</div>
			{/if}
			<a href="/feed" class="nav-link nav-link--feed" class:active={$page.url.pathname === '/feed'}>{tr('nav.feed', currentLang)}</a>
			<a href="/dashboard" class="nav-link nav-link--home" class:active={$page.url.pathname === "/dashboard"}>{tr('nav.dashboard', currentLang)}</a>
			<div class="nav-drop nav-about nav-link--about" class:open={openMenu === 'about'} bind:this={menuEls.about} onfocusout={(e) => onMenuFocusOut('about', e)}>
				<button
					class="nav-link nav-link--menu"
					class:active={$page.url.pathname.startsWith('/about') || $page.url.pathname === '/lab'}
					aria-haspopup="true"
					aria-expanded={openMenu === 'about'}
					aria-controls="about-menu"
					onclick={() => toggleMenu('about')}
				>
					{tr('nav.about', currentLang)}
					<svg class="caret" viewBox="0 0 12 12" aria-hidden="true"><path d="M3 4.5l3 3 3-3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
				</button>
				<div class="drop-menu drop-menu--end" id="about-menu">
					{#each [['project', '/about'], ['lab', '/lab'], ['privacy', '/about/privacy']] as [k, href] (k)}
						<a {href} class:active={$page.url.pathname === href} aria-current={$page.url.pathname === href ? 'page' : undefined}>
							<strong>{tr(`nav.menu.${k}`, currentLang)}</strong>
							<span>{tr(`nav.menu.${k}_sub`, currentLang)}</span>
						</a>
					{/each}
				</div>
			</div>
			{#if userName}
				<span class="nav-user" title={tr('auth.signed_in_as', currentLang, { name: userName })}>
					<a href="/profile" class="nav-user-link" class:active={$page.url.pathname === '/profile'} aria-label={tr('nav.profile', currentLang)}>
						<span class="nav-user-name">{userName}</span>
						<span class="nav-user-short">{tr('nav.profile', currentLang)}</span>
					</a>
					<button class="nav-signout" onclick={signOut}>{tr('auth.sign_out', currentLang)}</button>
				</span>
			{:else if $voteKey}
				<a href="/profile" class="nav-link" class:active={$page.url.pathname === '/profile'}>{tr('nav.profile', currentLang)}</a>
			{/if}
		</div>
	</div>
</nav>

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
	<a href="/lab">{tr('nav.lab', currentLang)}</a>
	<span aria-hidden="true">·</span>
	<a href="/about">{tr('nav.about', currentLang)}</a>
	<span aria-hidden="true">·</span>
	<a href="/about/privacy">{tr('nav.privacy', currentLang)}</a>
	<span aria-hidden="true">·</span>
	<a href="/corrections">{tr('corrections.title', currentLang)}</a>
	<span aria-hidden="true">·</span>
	<a href="/feed">{tr('nav.feed', currentLang)}</a>
</footer>

<style>
	.nav-bar {
		position: sticky;
		top: 0;
		z-index: 100;
		background: var(--glass-bg);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		border-bottom: 1px solid var(--glass-border);
	}

	.nav-content {
		max-width: 1200px;
		margin: 0 auto;
		padding: 0.875rem 2rem;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 2rem;
	}

	.nav-brand {
		display: inline-flex;
		align-items: center;
		gap: 0.75rem;
		color: var(--text-primary);
		transition: color var(--transition-normal);
	}

	.nav-brand:hover {
		color: var(--accent);
	}

	.mask-glyph {
		width: 38px;
		height: 24px;
		flex-shrink: 0;
		transition: filter var(--transition-slow), transform var(--transition-slow);
	}

	.nav-brand:hover .mask-glyph {
		filter: drop-shadow(0 0 6px var(--accent-glow));
		transform: translateY(-1px);
	}

	.brand-text {
		display: inline-flex;
		align-items: baseline;
		line-height: 1;
		gap: 0.6rem;
	}

	.brand-name {
		font-family: var(--font-serif);
		font-size: 1.5rem;
		font-weight: 600;
		letter-spacing: 0.005em;
		color: inherit;
	}

	.brand-dot {
		color: var(--accent);
	}


	.nav-right {
		display: inline-flex;
		align-items: center;
		gap: 1rem;
	}

	.nav-link {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--text-secondary);
		letter-spacing: 0.02em;
	}

	.nav-link:hover {
		color: var(--text-primary);
	}

	/* The nav's menus share one look; each has its colour (--drop): the
	   event its blue, the site its own. */
	.nav-drop {
		position: relative;
		min-width: 0;
		--drop: var(--accent-hover);
		--drop-glow: color-mix(in oklch, var(--accent) 14%, transparent);
	}
	.nav-event {
		--drop: var(--event);
		--drop-glow: var(--event-glow);
	}
	.nav-link--menu {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		padding: 0;
		border: none;
		background: none;
		font-family: inherit;
		cursor: pointer;
	}
	.nav-link--menu.active {
		color: var(--text-primary);
	}
	.nav-link--poll {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		max-width: 100%;
		white-space: nowrap;
		color: var(--text-primary);
		padding: 0.25rem 0.6rem 0.25rem 0.3rem;
		border: 1px solid var(--event);
		border-radius: var(--radius-full);
		background: var(--event-glow);
		font-family: inherit;
		cursor: pointer;
	}
	.poll-title {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.poll-title-short {
		display: none;
	}
	@media (max-width: 480px) {
		.poll-title {
			display: none;
		}
		.poll-title-short {
			display: inline;
		}
	}
	.caret {
		width: 12px;
		height: 12px;
		flex-shrink: 0;
		transition: transform var(--transition-fast);
	}
	.nav-drop.open .caret {
		transform: rotate(180deg);
	}
	.drop-menu {
		position: absolute;
		top: 100%;
		left: 0;
		z-index: 110;
		display: grid;
		gap: 2px;
		min-width: 17rem;
		margin-top: 0.5rem;
		padding: 0.35rem;
		border: 1px solid var(--drop);
		border-radius: var(--radius-lg);
		background: var(--bg-secondary);
		box-shadow: 0 12px 32px rgba(0, 0, 0, 0.45);
		opacity: 0;
		visibility: hidden;
		transform: translateY(-4px);
		transition:
			opacity 0.15s ease,
			transform 0.15s ease,
			visibility 0.15s;
	}
	.drop-menu--end {
		left: auto;
		right: 0;
	}
	/* The gap between the button and the menu keeps the hover. */
	.drop-menu::before {
		content: '';
		position: absolute;
		top: -0.6rem;
		left: 0;
		right: 0;
		height: 0.6rem;
	}
	.nav-drop.open .drop-menu {
		opacity: 1;
		visibility: visible;
		transform: none;
	}
	@media (hover: hover) {
		.nav-drop:hover .drop-menu {
			opacity: 1;
			visibility: visible;
			transform: none;
		}
	}
	.drop-menu a {
		display: grid;
		gap: 0.1rem;
		padding: 0.55rem 0.75rem;
		border-radius: var(--radius-md);
		color: var(--text-primary);
		text-decoration: none;
	}
	.drop-menu a strong {
		font-size: 0.88rem;
		font-weight: 600;
	}
	.drop-menu a span {
		font-size: 0.75rem;
		color: var(--text-muted);
	}
	.drop-menu a:hover,
	.drop-menu a:focus-visible {
		background: var(--drop-glow);
	}
	.drop-menu a.active {
		background: var(--drop-glow);
		box-shadow: inset 3px 0 0 var(--drop);
	}

	.poll-tag {
		font-size: 0.62rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		padding: 0.12rem 0.45rem;
		border-radius: var(--radius-full);
		background: var(--event-strong);
		color: white;
	}

	.nav-link--poll:hover,
	.nav-link--poll.active {
		background: var(--event-strong);
		color: white;
	}

	.footer-lab {
		flex-basis: 100%;
		margin: 0 0 0.4rem;
		text-align: center;
		color: var(--text-muted);
		font-size: 0.8rem;
	}

	.nav-user {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.8rem;
		color: var(--text-muted);
	}

	.nav-user-name {
		max-width: 10rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		color: var(--text-secondary);
	}

	.nav-user-link {
		display: inline-flex;
		color: var(--text-secondary);
		text-decoration: none;
	}

	.nav-user-link:hover .nav-user-name,
	.nav-user-link.active .nav-user-name,
	.nav-user-link:hover .nav-user-short,
	.nav-user-link.active .nav-user-short {
		color: var(--text-primary);
		text-decoration: underline;
	}

	.nav-user-short {
		display: none;
		white-space: nowrap;
		color: var(--text-secondary);
	}

	.nav-signout {
		background: none;
		border: none;
		padding: 0;
		font-size: 0.75rem;
		color: var(--text-muted);
		text-decoration: underline;
		cursor: pointer;
	}

	.nav-signout:hover {
		color: var(--text-primary);
	}

	.lang-toggle {
		display: inline-flex;
		gap: 2px;
		padding: 2px;
		background: var(--bg-card);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
	}
	.lang-pill {
		background: transparent;
		border: none;
		padding: 0.2rem 0.55rem;
		border-radius: var(--radius-full);
		font-size: 0.7rem;
		font-weight: 600;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		cursor: pointer;
		transition: all var(--transition-normal);
	}
	.lang-pill:hover {
		color: var(--text-primary);
	}
	.lang-pill.active {
		background: var(--accent);
		color: white;
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
	.site-footer a {
		color: var(--text-secondary);
	}
	.site-footer a:hover {
		color: var(--accent-hover);
	}

	@media (max-width: 860px) {
		.nav-link--about {
			display: none;
		}
	}

	@media (max-width: 560px) {
		.nav-content {
			padding: 0.75rem 1rem;
			gap: 0.6rem;
		}
		.mask-glyph {
			width: 30px;
			height: 19px;
		}
		.brand-name {
			font-size: 1.25rem;
		}
		.nav-right {
			gap: 0.6rem;
			min-width: 0;
		}
		.nav-user-name,
		.nav-link--home,
		.nav-link--feed,
		.nav-link--about {
			display: none;
		}
		.nav-user-short {
			display: inline;
		}
		/* On a phone, sign out from the profile page. */
		.nav-signout {
			display: none;
		}
		.poll-tag {
			display: none;
		}
		.nav-link--poll {
			min-width: 0;
			font-size: 0.8rem;
			padding: 0.25rem 0.5rem 0.25rem 0.6rem;
		}
		.drop-menu {
			position: fixed;
			top: 3.4rem;
			left: 1rem;
			right: 1rem;
			min-width: 0;
		}
		.drop-menu::before {
			display: none;
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
	}
</style>
