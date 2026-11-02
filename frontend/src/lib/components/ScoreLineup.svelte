<script lang="ts">
	// Every candidate on one Tartuffe-Score barometer: each face stands at its
	// score, in the colour of its place on the track. Faces closer than their
	// own width climb a row, so none hides another.
	import { tr, type Lang } from '$lib/i18n';
	import { displayName } from '$lib/share';
	import { tartuffeOf, tartuffeColor } from './TartuffeScore.svelte';
	import type { PersonSummary } from '$lib/types';

	let { people, lang }: { people: PersonSummary[]; lang: Lang } = $props();

	// Below this many facts a person has no place on the track (as on the barometer).
	const MIN_FACTS = 10;
	let width = $state(0);
	let face = $derived(width < 520 ? 34 : 46);
	// One row: the face, its number and the gap above.
	let rowHeight = $derived(face + 22);

	let placed = $derived.by(() => {
		const rated = people
			.map((p) => ({ p, t: tartuffeOf(p.honesty_score) }))
			.filter((x): x is { p: PersonSummary; t: number } => x.t !== null && (x.p.fact_count ?? 0) >= MIN_FACTS)
			.sort((a, b) => a.t - b.t);
		const ends: number[] = [];
		return rated.map(({ p, t }) => {
			const x = ((3 + (t / 100) * 94) / 100) * width;
			let row = 0;
			while (ends[row] !== undefined && ends[row] > x - face - 4) row++;
			ends[row] = x;
			return { p, t, x, row };
		});
	});
	let rows = $derived(placed.reduce((m, f) => Math.max(m, f.row + 1), 0));
</script>

{#if placed.length > 1}
	<div class="lineup" style="--face: {face}px">
		<div class="lane" bind:clientWidth={width} style="height: {rows * rowHeight + 18}px">
			<span class="bar"></span>
			{#each placed as { p, t, x, row } (p.id)}
				<a
					class="who"
					href="/person/{p.id}"
					style="left: {x}px; --c: {tartuffeColor(t)}"
					title={tr('score.lineup.who', lang, { name: displayName(p.name), n: t })}
				>
					{#if p.image_url}
						<img src={p.image_url} alt={displayName(p.name)} />
					{:else}
						<span class="noimg">{p.name.charAt(0).toUpperCase()}</span>
					{/if}
					<b>{t}</b>
					<span class="stem" style="height: {row * rowHeight + 6}px"></span>
				</a>
			{/each}
		</div>
		<div class="ends"><span>{tr('score.lineup.coherent', lang)}</span><span>{tr('score.lineup.tartuffe', lang)}</span></div>
	</div>
{/if}

<style>
	.lineup {
		padding: 1rem 1rem 0.8rem;
		border-radius: var(--radius-lg, 18px);
		background: var(--bg-card);
		border: 1px solid var(--border-card, var(--glass-border));
	}
	.lane {
		position: relative;
	}
	.bar {
		position: absolute;
		left: 0;
		right: 0;
		bottom: 0;
		height: 0.7rem;
		border-radius: 999px;
		background: linear-gradient(90deg, var(--green), var(--amber) 50%, var(--red));
	}
	.who {
		position: absolute;
		bottom: 0.35rem;
		transform: translateX(-50%);
		display: flex;
		flex-direction: column;
		align-items: center;
		text-decoration: none;
	}
	.who img,
	.noimg {
		position: relative;
		z-index: 2;
		width: var(--face);
		height: var(--face);
		border-radius: 50%;
		object-fit: cover;
		border: 3px solid var(--c);
		background: var(--bg-elevated, #1c1a22);
		transition: transform 0.15s ease;
	}
	.noimg {
		display: grid;
		place-items: center;
		color: var(--text-muted);
		font-weight: 700;
	}
	.who:hover img,
	.who:hover .noimg {
		transform: scale(1.12);
	}
	.who b {
		font-size: 0.7rem;
		font-weight: 800;
		color: var(--c);
		font-variant-numeric: tabular-nums;
		line-height: 1.2;
	}
	.stem {
		width: 2px;
		background: var(--c);
		opacity: 0.5;
	}
	.ends {
		display: flex;
		justify-content: space-between;
		margin-top: 0.4rem;
		font-size: 0.62rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
</style>
