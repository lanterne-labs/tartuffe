<script lang="ts">
	// The event's pages (its presentation, the vote, how it works) wear the
	// event's colour, not the checker's, and share its sub-menu. The lab's
	// banner (EventBanner) is for the other pages.
	import { page } from '$app/stores';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';

	let { children } = $props();

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));

	let tabs = $derived([
		{ href: paths.intro, label: tr('event.menu.intro', currentLang) },
		{ href: paths.vote, label: tr('event.menu.vote', currentLang) },
		{ href: paths.how, label: tr('event.menu.how', currentLang) },
		{ href: paths.verify, label: tr('event.menu.verify', currentLang) }
	]);

</script>

<div class="event-page">
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
	/* A phone: the four tabs do not fit on one line, so they take a grid of
	   two by two, every cell the same size and the same pill, instead of a
	   wrap that leaves one tab alone on the second row. */
	@media (max-width: 560px) {
		.event-tabs {
			display: grid;
			grid-template-columns: 1fr 1fr;
			width: 100%;
			border-radius: var(--radius-lg);
		}
		.event-tabs a {
			padding: 0.6rem 0.5rem;
			font-size: 0.8rem;
			text-align: center;
		}
	}
</style>
