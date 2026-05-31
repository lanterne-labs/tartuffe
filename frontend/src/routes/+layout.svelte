<script lang="ts">
	import favicon from '$lib/assets/favicon.svg';
	import '../app.css';
	import { page } from '$app/stores';
	import { fly } from 'svelte/transition';
	import { lang, setLang, tr } from '$lib/i18n';

	let { children } = $props();

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
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
			<a href="/" class="nav-link">{tr('nav.dashboard', currentLang)}</a>
			<a href="/about" class="nav-link">{tr('nav.about', currentLang)}</a>
		</div>
	</div>
</nav>

{#key $page.url.pathname}
	<main class="page-content" in:fly={{ y: 8, duration: 200, delay: 100 }} out:fly={{ y: -8, duration: 150 }}>
		{@render children()}
	</main>
{/key}

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

	@media (max-width: 560px) {
		.nav-content {
			padding: 0.75rem 1rem;
			gap: 1rem;
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
	}

	.page-content {
		max-width: 1200px;
		margin: 0 auto;
		padding: 2rem;
	}
</style>
