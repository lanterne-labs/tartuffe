<script lang="ts">
	// The event's pages (its presentation, the vote, how it works) wear the
	// event's colour, not the checker's, and share its strip and sub-menu.
	import { page } from '$app/stores';
	import { API_BASE } from '$lib/config';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';
	import type { PollEvent } from '$lib/types';

	let { children } = $props();

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));
	let event = $state<PollEvent | null>(null);

	$effect(() => {
		const s = slug;
		fetch(`${API_BASE}/api/polls`)
			.then((r) => (r.ok ? r.json() : Promise.reject(new Error(`HTTP ${r.status}`))))
			.then((events: PollEvent[]) => (event = events.find((e) => e.slug === s) ?? null))
			.catch((err) => console.error('failed to load the event', err));
	});

	let tabs = $derived([
		{ href: paths.intro, label: tr('event.menu.intro', currentLang) },
		{ href: paths.vote, label: tr('event.menu.vote', currentLang) },
		{ href: paths.how, label: tr('event.menu.how', currentLang) }
	]);

	function dateText(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}
</script>

<div class="event-page">
	<p class="event-strip">
		<span class="event-strip-tag">{tr('event.tag', currentLang)}</span>
		<span>{event?.closes_at ? tr('event.strip.ends', currentLang, { d: dateText(event.closes_at) }) : tr('event.strip', currentLang)}</span>
		<a href="/lab">{tr('event.strip.why', currentLang)} →</a>
	</p>
	<nav class="event-tabs" aria-label={tr('event.menu.label', currentLang)}>
		{#each tabs as t (t.href)}
			<a href={t.href} class:active={$page.url.pathname === t.href} aria-current={$page.url.pathname === t.href ? 'page' : undefined}>{t.label}</a>
		{/each}
	</nav>
	{@render children()}
</div>

<style>
	.event-page {
		--accent: var(--event-strong);
		--accent-hover: var(--event);
		--accent-glow: var(--event-glow);
		--shadow-glow: 0 0 20px var(--event-glow);
	}
	.event-strip {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		align-items: center;
		gap: 0.4rem 0.9rem;
		max-width: 46rem;
		margin: 0.5rem auto 0;
		padding: 0.55rem 1rem;
		border: 1px dashed var(--event);
		border-radius: var(--radius-full);
		background: var(--event-glow);
		font-size: 0.85rem;
		text-align: center;
	}
	.event-strip-tag {
		font-size: 0.66rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		padding: 0.15rem 0.5rem;
		border-radius: var(--radius-full);
		background: var(--event-strong);
		color: white;
	}
	.event-strip a {
		color: var(--event);
	}
	.event-tabs {
		display: flex;
		justify-content: center;
		gap: 0.25rem;
		width: fit-content;
		max-width: 100%;
		margin: 1rem auto 0;
		padding: 4px;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
		background: var(--bg-card);
	}
	.event-tabs a {
		padding: 0.4rem 1rem;
		border-radius: var(--radius-full);
		color: var(--text-secondary);
		font-size: 0.85rem;
		font-weight: 600;
		white-space: nowrap;
		text-decoration: none;
	}
	.event-tabs a:hover {
		color: var(--text-primary);
	}
	.event-tabs a.active {
		background: var(--event-strong);
		color: white;
	}
	@media (max-width: 560px) {
		.event-strip {
			border-radius: var(--radius-lg);
		}
		.event-tabs a {
			padding: 0.4rem 0.7rem;
			font-size: 0.8rem;
		}
	}
</style>
