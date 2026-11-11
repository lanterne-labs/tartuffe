<script lang="ts">
	// The podium of the most tartuffe: the three candidates with the most
	// contradictions for their facts, among those whose score is reliable. No
	// podium of the most coherent while scores are still being established: a
	// clean score on few facts would rank who was looked at least. Three
	// steps, the first in the middle and the highest; the list stays in rank
	// order for a reader, the steps are placed with `order`. On the landing
	// page and the dashboard.
	import { onMount } from 'svelte';
	import TartuffeScore from './TartuffeScore.svelte';
	import { tr, type Lang } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import { personPath } from '$lib/share';
	import type { PersonSummary } from '$lib/types';

	interface Props {
		lang: Lang;
	}
	let { lang }: Props = $props();

	const SIZE = 3;
	const MIN_FACTS = 3;

	let worst: PersonSummary[] = $state([]);
	let loading = $state(true);

	onMount(load);
	export async function load() {
		try {
			loading = true;
			// Among the candidates only (only they are judged) and among those
			// whose score is reliable: enough contradictions expected.
			const qs = new URLSearchParams({ limit: '100', sort: 'honesty_asc', min_facts: String(MIN_FACTS), candidate: 'true' });
			const res = await fetch(`${API_BASE}/api/persons?${qs}`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			worst = (((await res.json()) as PersonSummary[] | null) ?? []).filter((p) => p.score_reliable !== false).slice(0, SIZE);
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
	</header>
	{#if loading && worst.length === 0}
		<ol class="podium" aria-busy="true">
			{#each [1, 2, 3] as rank (rank)}
				<li class="step step--{rank}">
					<div class="skeleton skeleton-rank-avatar"></div>
					<div class="skeleton skeleton-rank-name"></div>
					<div class="block"><span class="pos">{rank}</span></div>
				</li>
			{/each}
		</ol>
	{:else if worst.length === 0}
		<p class="empty">{tr('home.no_data', lang)}</p>
	{:else}
		<ol class="podium">
			{#each worst as person, i (person.id)}
				<li class="step step--{i + 1}">
					<a href={personPath(person.slug)} class="laureate">
						<TartuffeScore honesty={person.honesty_score} {lang} size={i === 0 ? 'md' : 'sm'} compact image={person.image_url} initial={person.name.charAt(0).toUpperCase()} facts={person.fact_count} reliable={person.score_reliable} covered={person.covered_facts} />
						<span class="name">{person.name}</span>
						<span class="meta">{person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', lang)}</span>
					</a>
					<div class="block"><span class="pos">{i + 1}</span></div>
				</li>
			{/each}
		</ol>
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
		.podium-section { padding: 1rem 0.5rem 0; }
		.name { font-size: 0.8rem; }
		.step--1 .name { font-size: 0.9rem; }
		.meta { font-size: 0.7rem; }
		/* The dials fit their column: the middle one a little larger. */
		.laureate :global(.ts) { --w: 100%; }
		.step--1 .laureate { padding-left: 0; padding-right: 0; }
	}
</style>
