<script lang="ts">
	import favicon from '$lib/assets/favicon.svg';
	import '../app.css';
	import { page } from '$app/stores';
	import { fly } from 'svelte/transition';
	import { lang, setLang, tr } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { API_BASE } from '$lib/config';
	import { session, signOut } from '$lib/auth';
	import SignInDialog from '$lib/components/SignInDialog.svelte';
	import type { PollEvent } from '$lib/types';

	let { children } = $props();

	// The link preview of the page (X, WhatsApp, search engines): a page sets
	// it with `meta` in the data of its load, the others get the site's.
	const defaultMeta = {
		title: 'tartuffe.me',
		description: 'Un miroir public de ce que disent et votent les responsables politiques : leurs faits, leurs positions, leurs contradictions, avec les sources.'
	};
	let meta = $derived({ ...defaultMeta, ...(($page.data as { meta?: { title?: string; description?: string; image?: string } })?.meta ?? {}) });

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	let userName = $state<string | null>(null);
	$effect(() => session.subscribe((s) => (userName = s ? s.user.display_name || tr('auth.anonymous', currentLang) : null)));

	// The featured pre-poll gets a link in the nav.
	let featured = $state<PollEvent | null>(null);
	onMount(async () => {
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
				<span class="brand-subtitle">{tr('nav.brand_subtitle', currentLang)}</span>
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
				<a href="/election/{featured.slug}" class="nav-link nav-link--poll" class:active={$page.url.pathname.startsWith('/election/')}>
					{currentLang === 'en' ? featured.title_en : featured.title_fr}
				</a>
			{/if}
			<a href="/feed" class="nav-link nav-link--feed" class:active={$page.url.pathname === '/feed'}>{tr('nav.feed', currentLang)}</a>
			<a href="/" class="nav-link nav-link--home">{tr('nav.dashboard', currentLang)}</a>
			<a href="/about" class="nav-link nav-link--about" class:active={$page.url.pathname === '/about'}>{tr('nav.about', currentLang)}</a>
			{#if userName}
				<span class="nav-user" title={tr('auth.signed_in_as', currentLang, { name: userName })}>
					<a href="/profile" class="nav-user-link" class:active={$page.url.pathname === '/profile'} aria-label={tr('nav.profile', currentLang)}>
						<span class="nav-user-name">{userName}</span>
						<span class="nav-user-short">{tr('nav.profile', currentLang)}</span>
					</a>
					<button class="nav-signout" onclick={signOut}>{tr('auth.sign_out', currentLang)}</button>
				</span>
			{/if}
		</div>
	</div>
</nav>

<SignInDialog />

{#key $page.url.pathname}
	<main class="page-content" in:fly={{ y: 8, duration: 200, delay: 100 }} out:fly={{ y: -8, duration: 150 }}>
		{@render children()}
	</main>
{/key}

<footer class="site-footer">
	<a href="/about">{tr('nav.about', currentLang)}</a>
	<span aria-hidden="true">·</span>
	<a href="/corrections">{tr('corrections.title', currentLang)}</a>
	<span aria-hidden="true">·</span>
	<a href="/feed">{tr('nav.feed', currentLang)}</a>
	<span aria-hidden="true">·</span>
	<a href="/verification">{tr('footer.verification', currentLang)}</a>
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

	.brand-subtitle {
		font-family: var(--font-serif);
		font-size: 1rem;
		font-weight: 400;
		font-style: italic;
		letter-spacing: 0.01em;
		color: var(--text-muted);
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

	.nav-link--poll {
		white-space: nowrap;
		color: var(--text-primary);
		padding: 0.3rem 0.75rem;
		border: 1px solid var(--accent);
		border-radius: var(--radius-full);
		background: var(--accent-glow);
	}

	.nav-link--poll:hover,
	.nav-link--poll.active {
		background: var(--accent);
		color: white;
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
		.brand-subtitle {
			display: none;
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
		.nav-link--poll {
			min-width: 0;
			overflow: hidden;
			text-overflow: ellipsis;
			font-size: 0.8rem;
			padding: 0.25rem 0.6rem;
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
