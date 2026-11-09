<script lang="ts">
	// The site's header, the same on the site and in the vote app (built from
	// the same code, never fetched: a page of tartuffe.me loaded by the vote
	// app would tell our server who opens the vote). In the vote app `site`
	// is https://tartuffe.me: the event's pages stay in the app, every other
	// link leaves for the site, and nothing loads before a click.
	import { setLang, tr, type Lang } from '$lib/i18n';
	import BrandMark from './BrandMark.svelte';
	import { eventPaths } from '$lib/event';

	type Featured = { slug: string; title_fr: string; title_en: string };
	let {
		lang,
		pathname,
		site = '',
		featured = null,
		userName = null,
		showProfile = false,
		onsignout = () => {}
	}: { lang: Lang; pathname: string; site?: string; featured?: Featured | null; userName?: string | null; showProfile?: boolean; onsignout?: () => void } = $props();

	// The nav's menus (the event's pages, the About pages) open on a click or
	// a tap, on hover with a mouse, and close on Escape, outside, or on a new
	// page. One is open at a time.
	type Menu = 'event' | 'about';
	let openMenu = $state<Menu | null>(null);
	let menuEls = $state<Record<Menu, HTMLElement | null>>({ event: null, about: null });
	$effect(() => {
		pathname;
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
</script>

<svelte:window onclick={onWindowClick} onkeydown={onWindowKey} />

<nav class="nav-bar">
	<div class="nav-content">
		<a href={site || '/'} class="nav-brand" aria-label="tartuffe.me — home">
			<span class="mask-glyph"><BrandMark size="nav" /></span>
			<span class="brand-text">
				<span class="brand-name">tartuffe<span class="brand-dot">.</span>me</span>
			</span>
		</a>
		<div class="nav-right">
			{#if featured}
				{@const ep = eventPaths(featured.slug)}
				<div class="nav-drop nav-event" class:open={openMenu === 'event'} bind:this={menuEls.event} onfocusout={(e) => onMenuFocusOut('event', e)}>
					<button
						class="nav-link nav-link--poll"
						class:active={pathname.startsWith('/election/')}
						aria-haspopup="true"
						aria-expanded={openMenu === 'event'}
						aria-controls="event-menu"
						title={tr('event.label', lang)}
						onclick={() => toggleMenu('event')}
					>
						<span class="poll-tag">{tr('event.tag_short', lang)}</span>
						<span class="poll-title">{lang === 'en' ? featured.title_en : featured.title_fr}</span>
						<!-- On a phone the title does not fit: the year says it. -->
						<span class="poll-title-short">{featured.title_fr.match(/\d{4}/)?.[0] ?? featured.title_fr}</span>
						<svg class="caret" viewBox="0 0 12 12" aria-hidden="true"><path d="M3 4.5l3 3 3-3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
					</button>
					<div class="drop-menu" id="event-menu">
						{#each [['intro', ep.intro], ['vote', ep.vote], ['how', ep.how], ['verify', ep.verify]] as [k, href] (k)}
							<a {href} class:active={pathname === href} aria-current={pathname === href ? 'page' : undefined}>
								<strong>{tr(`event.menu.${k}`, lang)}</strong>
							</a>
						{/each}
					</div>
				</div>
			{/if}
			<a href="{site}/feed" class="nav-link nav-link--feed" class:active={pathname === '/feed'}>{tr('nav.feed', lang)}</a>
			<a href="{site}/dashboard" class="nav-link nav-link--home" class:active={pathname === "/dashboard"}>{tr('nav.dashboard', lang)}</a>
			<div class="nav-drop nav-about nav-link--about" class:open={openMenu === 'about'} bind:this={menuEls.about} onfocusout={(e) => onMenuFocusOut('about', e)}>
				<button
					class="nav-link nav-link--menu"
					class:active={pathname.startsWith('/about') || pathname === '/lab'}
					aria-haspopup="true"
					aria-expanded={openMenu === 'about'}
					aria-controls="about-menu"
					onclick={() => toggleMenu('about')}
				>
					{tr('nav.about', lang)}
					<svg class="caret" viewBox="0 0 12 12" aria-hidden="true"><path d="M3 4.5l3 3 3-3" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" /></svg>
				</button>
				<div class="drop-menu drop-menu--end" id="about-menu">
					{#each [['project', '/about'], ['lab', '/lab'], ['privacy', '/about/privacy']] as [k, href] (k)}
						<a href="{site}{href}" class:active={pathname === href} aria-current={pathname === href ? 'page' : undefined}>
							<strong>{tr(`nav.menu.${k}`, lang)}</strong>
						</a>
					{/each}
				</div>
			</div>
			{#if userName}
				<span class="nav-user" title={tr('auth.signed_in_as', lang, { name: userName })}>
					<a href="{site}/profile" class="nav-user-link" class:active={pathname === '/profile'} aria-label={tr('nav.profile', lang)}>
						<span class="nav-user-name">{userName}</span>
						<span class="nav-user-short">{tr('nav.profile', lang)}</span>
					</a>
					<button class="nav-signout" onclick={onsignout}>{tr('auth.sign_out', lang)}</button>
				</span>
			{:else if showProfile}
				<a href="{site}/profile" class="nav-link" class:active={pathname === '/profile'}>{tr('nav.profile', lang)}</a>
			{/if}
			<div class="lang-toggle" role="group" aria-label="Language">
				<button
					class="lang-pill"
					class:active={lang === 'fr'}
					aria-pressed={lang === 'fr'}
					onclick={() => setLang('fr')}
					title="Français"
				>FR</button>
				<button
					class="lang-pill"
					class:active={lang === 'en'}
					aria-pressed={lang === 'en'}
					onclick={() => setLang('en')}
					title="English"
				>EN</button>
			</div>
		</div>
	</div>
</nav>

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
		display: block;
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
		color: var(--on-accent);
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
</style>
