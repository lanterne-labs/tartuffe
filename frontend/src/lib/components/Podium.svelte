<script lang="ts">
	// The podium of the most tartuffe: the three candidates with the most
	// contradictions for their facts, among those whose score is reliable. No
	// podium of the most coherent while scores are still being established: a
	// clean score on few facts would rank who was looked at least. Three
	// steps, the first in the middle and the highest; the list stays in rank
	// order for a reader, the steps are placed with `order`. On the landing
	// page and the dashboard.
	//
	// The arrows go back through the weeks: the API keeps the podium each
	// past week ended on (GET /api/podium?week=), with the scores of then.
	// A change of week slides: towards the past the podium leaves to the
	// right and the older one comes from the left, as pages of a calendar.
	import { onMount } from 'svelte';
	import { fly } from 'svelte/transition';
	import { cubicOut } from 'svelte/easing';
	import TartuffeScore from './TartuffeScore.svelte';
	import { tr, type Lang } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import { personPath } from '$lib/share';
	import type { PodiumWeek } from '$lib/types';

	interface Props {
		lang: Lang;
	}
	let { lang }: Props = $props();

	// The podium shown; null until the first answer.
	let podium = $state<PodiumWeek | null>(null);
	let loading = $state(true);
	// The podiums already read, by week ('' is the live one): an arrow back
	// to a week seen is instant.
	const seen = new Map<string, PodiumWeek>();

	let persons = $derived(podium?.persons ?? []);
	// The weeks to walk, the latest first: the live one, then the past ones.
	let weeks = $derived(['', ...(podium?.weeks ?? [])]);
	let at = $derived(podium && !podium.live ? weeks.indexOf(podium.week) : 0);
	let older = $derived(at >= 0 && at + 1 < weeks.length ? weeks[at + 1] : null);
	let newer = $derived(at > 0 ? weeks[at - 1] : null);
	let weekLabel = $derived.by(() => {
		if (!podium || podium.live) return tr('home.podium_this_week', lang);
		const day = new Date(podium.week + 'T00:00:00Z').toLocaleDateString(lang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC' });
		return tr('home.podium_week_of', lang, { day });
	});

	// The way the next podium comes from: -1 from the left (an older week),
	// 1 from the right (a newer one), 0 without a slide (the first load, a
	// refresh, a reader who asked for less motion).
	let from = $state(0);
	const SLIDE_MS = 380;

	function go(week: string | null, direction: number) {
		if (week === null) return;
		const still = typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
		from = still ? 0 : direction;
		show(week);
	}

	onMount(() => load());
	// load reads the live podium again (the dashboard's refresh).
	export async function load() {
		seen.delete('');
		from = 0;
		await show('');
	}

	async function show(week: string) {
		const cached = seen.get(week);
		if (cached) {
			podium = cached;
			return;
		}
		try {
			loading = true;
			const res = await fetch(`${API_BASE}/api/podium${week ? `?week=${week}` : ''}`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const got = (await res.json()) as PodiumWeek;
			seen.set(week, got);
			podium = got;
		} catch (err) {
			// Non-fatal: the podium shows "no data"; the page's own banner offers the retry.
			console.error('failed to load the podium', err);
		} finally {
			loading = false;
		}
	}
</script>

<section class="podium-section" aria-labelledby="podium-title">
	<header class="podium-header">
		<h2 id="podium-title">{tr('home.least_honest', lang)}</h2>
		<p>{tr('home.podium_sub', lang)}</p>
		{#if weeks.length > 1}
			<nav class="weeks" aria-label={tr('home.podium_weeks', lang)}>
				<button type="button" class="arrow" disabled={older === null || loading} onclick={() => go(older, -1)} aria-label={tr('home.podium_older', lang)}>‹</button>
				<span class="week" aria-live="polite">{weekLabel}</span>
				<button type="button" class="arrow" disabled={newer === null || loading} onclick={() => go(newer, 1)} aria-label={tr('home.podium_newer', lang)}>›</button>
			</nav>
		{/if}
	</header>
	{#if loading && persons.length === 0}
		<ol class="podium" aria-busy="true">
			{#each [1, 2, 3] as rank (rank)}
				<li class="step step--{rank}">
					<div class="skeleton skeleton-rank-avatar"></div>
					<div class="skeleton skeleton-rank-name"></div>
					<div class="block"><span class="pos">{rank}</span></div>
				</li>
			{/each}
		</ol>
	{:else if persons.length === 0}
		<p class="empty">{tr('home.no_data', lang)}</p>
	{:else}
		<!-- The two podiums of a slide share one cell: the one leaving and the
		     one coming move side by side. -->
		<div class="slides">
			{#key podium?.live ? '' : podium?.week}
				<ol class="podium" in:fly={{ x: `${from * 100}%`, duration: from ? SLIDE_MS : 0, easing: cubicOut, opacity: 1 }} out:fly={{ x: `${-from * 100}%`, duration: from ? SLIDE_MS : 0, easing: cubicOut, opacity: 1 }}>
					{#each persons as person (person.id)}
						<li class="step step--{person.rank}">
							<a href={personPath(person.slug)} class="laureate">
								<TartuffeScore honesty={person.honesty_score} {lang} size={person.rank === 1 ? 'md' : 'sm'} compact image={person.image_url} initial={person.name.charAt(0).toUpperCase()} facts={person.fact_count} reliable={true} />
								<span class="name">{person.name}</span>
								<span class="meta">{person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', lang)}</span>
							</a>
							<div class="block"><span class="pos">{person.rank}</span></div>
						</li>
					{/each}
				</ol>
			{/key}
		</div>
	{/if}
</section>

<style>
	.podium-section {
		padding: 1.25rem 1rem 0;
		border-radius: var(--radius-md);
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		box-shadow: var(--shadow-card);
		overflow: hidden;
		text-align: left;
	}

	.podium-header {
		text-align: center;
		margin-bottom: 1.25rem;
	}

	.podium-header h2 {
		font-size: 1.1rem;
		font-weight: 700;
		letter-spacing: -0.01em;
		color: var(--red);
		margin: 0;
	}

	.podium-header p {
		margin: 0.25rem 0 0;
		font-size: 0.8rem;
		color: var(--text-muted);
	}

	/* The weeks: an arrow back, the week shown, an arrow forward. */
	.weeks {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.5rem;
		margin-top: 0.75rem;
	}

	.week {
		min-width: 11.5rem;
		font-size: 0.8rem;
		font-weight: 600;
		color: var(--text-primary);
		font-variant-numeric: tabular-nums;
	}

	.arrow {
		width: 2rem;
		height: 2rem;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0 0 0.15rem;
		border-radius: 50%;
		border: 1px solid var(--border-card);
		background: transparent;
		color: var(--text-primary);
		font-size: 1.25rem;
		line-height: 1;
		cursor: pointer;
		transition: background var(--transition-fast);
	}

	.arrow:hover:not(:disabled) {
		background: var(--bg-card-hover);
	}

	.arrow:disabled {
		opacity: 0.3;
		cursor: default;
	}

	.slides {
		display: grid;
		align-items: end;
	}

	.slides > .podium {
		grid-area: 1 / 1;
		min-width: 0;
	}

	.podium {
		list-style: none;
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		align-items: end;
		gap: 0.5rem;
		margin: 0;
		padding: 0;
	}

	.step {
		min-width: 0;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
	}

	.step--1 { order: 2; }
	.step--2 { order: 1; }
	.step--3 { order: 3; }

	.laureate {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.25rem;
		width: 100%;
		max-width: 100%;
		min-width: 0;
		padding: 0.5rem 0.25rem 0;
		text-decoration: none;
		color: inherit;
		border-radius: var(--radius-sm);
		transition: background var(--transition-fast);
	}

	.laureate:hover {
		background: var(--bg-card-hover);
	}

	.name {
		display: block;
		width: 100%;
		max-width: 100%;
		font-weight: 600;
		font-size: 0.938rem;
		text-transform: capitalize;
		text-align: center;
		color: var(--text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.step--1 .name { font-size: 1.05rem; }

	.meta {
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.block {
		width: 100%;
		display: flex;
		align-items: flex-start;
		justify-content: center;
		padding-top: 0.5rem;
		border-radius: var(--radius-sm) var(--radius-sm) 0 0;
		background: linear-gradient(180deg, rgba(220, 38, 38, 0.28), rgba(220, 38, 38, 0.06));
		border: 1px solid rgba(220, 38, 38, 0.35);
		border-bottom: none;
	}

	.step--1 .block { height: 5.5rem; background: linear-gradient(180deg, rgba(220, 38, 38, 0.45), rgba(220, 38, 38, 0.08)); }
	.step--2 .block { height: 4rem; }
	.step--3 .block { height: 3rem; }

	.pos {
		font-size: 1.25rem;
		font-weight: 800;
		color: var(--text-primary);
		opacity: 0.85;
	}

	.empty {
		text-align: center;
		color: var(--text-muted);
		padding-bottom: 1.25rem;
	}

	.skeleton-rank-avatar {
		width: 56px;
		height: 56px;
		border-radius: 50%;
		flex-shrink: 0;
	}
	.skeleton-rank-name {
		width: 6rem;
		height: 0.9rem;
	}

	@media (max-width: 480px) {
		.podium { gap: 0.25rem; }
		.week { min-width: 9.5rem; }
		.podium-section { padding: 1rem 0.5rem 0; }
		.name { font-size: 0.8rem; }
		.step--1 .name { font-size: 0.9rem; }
		.meta { font-size: 0.7rem; }
		/* The dials fit their column: the middle one a little larger. */
		.laureate :global(.ts) { --w: 100%; }
		.step--1 .laureate { padding-left: 0; padding-right: 0; }
	}
</style>
