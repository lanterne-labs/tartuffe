<script lang="ts">
	// One person among the others on the Tartuffe-Score: the band of the dial
	// laid flat, filled up to the person in focus, whose portrait stands over
	// it; every other candidate is a small face under the band, at their own
	// place. A click on a face puts that person in focus.
	import { tr, type Lang } from '$lib/i18n';
	import { displayName } from '$lib/share';
	import { tartuffeOf, tartuffeColor, dialOf, MARKS } from './TartuffeScore.svelte';
	import type { PersonSummary } from '$lib/types';

	interface Props {
		people: PersonSummary[];
		lang: Lang;
		/** The person in focus at first; without it, one chosen at random. */
		focus?: number | null;
	}
	let { people, lang, focus = null }: Props = $props();

	// Below this many facts a person has no place on the band (as on the dial).
	const MIN_FACTS = 10;
	const SMALL = 30;

	let rated = $derived(
		people
			.map((p) => ({ p, t: tartuffeOf(p.honesty_score) }))
			.filter((x): x is { p: PersonSummary; t: number } => x.t !== null && (x.p.fact_count ?? 0) >= MIN_FACTS)
			.map(({ p, t }) => ({ p, t, at: dialOf(t) }))
			.sort((a, b) => a.at - b.at)
	);

	// The focus: the one given, else a random one (no candidate is put first).
	let chosen = $state<number | null>(null);
	$effect(() => {
		if (chosen === null && rated.length > 0) {
			const given = rated.find((r) => r.p.id === focus);
			chosen = (given ?? rated[Math.floor(Math.random() * rated.length)]).p.id;
		}
	});
	let current = $derived(rated.find((r) => r.p.id === chosen) ?? null);
	// How many are more tartuffe (above) and more coherent (below) than the focus.
	let above = $derived(current ? rated.filter((r) => r.t > current.t).length : 0);
	let below = $derived(current ? rated.filter((r) => r.t < current.t).length : 0);
	let rank = $derived.by(() => {
		const n = rated.length - 1;
		if (above > 0 && below > 0) return tr('score.compare.both', lang, { a: above, b: below, n });
		if (above > 0) return tr('score.compare.coherent', lang, { a: above, n });
		if (below > 0) return tr('score.compare.tartuffe', lang, { b: below, n });
		return tr('score.compare.tie', lang);
	});

	// The place on the band, in % of its width: 3 to 97 so the ends show whole.
	const left = (at: number) => 3 + at * 0.94;

	// The small faces climb down in rows where they would overlap; past
	// ROWS rows they pile up, each a little to the right of the one before.
	const ROWS = 3;
	const PILE = 14;
	let width = $state(0);
	let spots = $derived.by(() => {
		const ends: number[] = [];
		return rated.map(({ at }) => {
			let x = (left(at) / 100) * width;
			let row = 0;
			while (row < ROWS && ends[row] !== undefined && ends[row] > x - SMALL - 3) row++;
			if (row === ROWS) {
				row = ends.indexOf(Math.min(...ends));
				x = Math.max(x, ends[row] + PILE);
			}
			ends[row] = x;
			return { x, row };
		});
	});
	let depth = $derived(spots.reduce((m, s) => Math.max(m, s.row + 1), 1));
	// The portrait in focus keeps whole inside the lane; its needle stays on the place.
	const BIG = 66;
	let place = $derived(current ? (left(current.at) / 100) * width : 0);
	let face = $derived(Math.min(Math.max(place, BIG / 2), width - BIG / 2));
</script>

{#if current}
	<div class="compare" style="--c: {tartuffeColor(current.at)}">
		<div class="head">
			<a class="name" href="/person/{current.p.id}">{displayName(current.p.name)} →</a>
			<span class="value">{tr('score.short', lang, { n: current.t })}</span>
			<span class="rank">{rank}</span>
		</div>

		<div class="lane" bind:clientWidth={width}>
			<div class="top">
				<span class="me" style="left: {face}px">
					{#key current.p.id}
						{#if current.p.image_url}
							<img src={current.p.image_url} alt={displayName(current.p.name)} />
						{:else}
							<span class="noimg">{current.p.name.charAt(0).toUpperCase()}</span>
						{/if}
					{/key}
				</span>
				<span class="needle" style="left: {place}px"></span>
			</div>
			<div class="band" style="--lane-w: {width}px">
				<span class="fill" style="width: {left(current.at)}%"></span>
				{#each MARKS as m, i (i)}
					<span class="notch" class:notch--long={i % 10 === 0} style="left: {left(m)}%"></span>
				{/each}
			</div>
			<div class="others" style="height: {depth * (SMALL + 6) + 4}px">
				{#each rated as r, i (r.p.id)}
					<button
						class="other"
						class:other--on={r.p.id === current.p.id}
						style="left: {spots[i].x}px; top: {spots[i].row * (SMALL + 6) + 4}px; --oc: {tartuffeColor(r.at)}"
						onclick={() => (chosen = r.p.id)}
						aria-pressed={r.p.id === current.p.id}
						title={tr('score.lineup.who', lang, { name: displayName(r.p.name), n: r.t })}
					>
						{#if r.p.image_url}
							<img src={r.p.image_url} alt={displayName(r.p.name)} />
						{:else}
							<span class="noimg">{r.p.name.charAt(0).toUpperCase()}</span>
						{/if}
					</button>
				{/each}
			</div>
		</div>
		<div class="ends"><span>{tr('score.lineup.coherent', lang)}</span><span>{tr('score.lineup.tartuffe', lang)}</span></div>
		<p class="hint">{tr('score.compare.hint', lang)}</p>
	</div>
{/if}

<style>
	.compare {
		padding: 1.1rem 1.1rem 0.9rem;
		border-radius: var(--radius-lg, 18px);
		background: var(--bg-card);
		border: 1px solid var(--border-card, var(--glass-border));
		text-align: center;
	}
	.head {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.15rem;
		margin-bottom: 0.4rem;
	}
	.name {
		font-size: 1.15rem;
		font-weight: 700;
		color: var(--text-primary);
		text-decoration: none;
	}
	.name:hover {
		text-decoration: underline;
	}
	.value {
		font-size: 1.05rem;
		font-weight: 800;
		color: var(--c);
		font-variant-numeric: tabular-nums;
	}
	.rank {
		font-size: 0.8rem;
		color: var(--text-secondary);
	}
	.lane {
		position: relative;
		margin: 0 0.4rem;
	}
	.top {
		position: relative;
		height: 84px;
	}
	.me {
		position: absolute;
		bottom: 16px;
		transform: translateX(-50%);
		transition: left 0.5s cubic-bezier(0.22, 1, 0.36, 1);
	}
	.me img,
	.me .noimg {
		animation: pop 0.35s ease-out;
	}
	.me img,
	.me .noimg {
		display: block;
		width: 66px;
		height: 66px;
		border-radius: 50%;
		object-fit: cover;
		border: 4px solid var(--c);
		box-shadow: 0 6px 18px rgba(0, 0, 0, 0.6);
		background: var(--bg-elevated, #1c1a22);
	}
	.needle {
		position: absolute;
		bottom: 0;
		width: 3px;
		height: 14px;
		border-radius: 2px;
		background: #fff;
		transform: translateX(-50%);
		transition: left 0.5s cubic-bezier(0.22, 1, 0.36, 1);
	}
	@keyframes pop {
		from {
			opacity: 0;
			transform: scale(0.7);
		}
	}
	.band {
		position: relative;
		height: 12px;
		border-radius: 3px;
		background: rgba(255, 255, 255, 0.1);
		overflow: hidden;
	}
	/* The fill shows the whole gradient's width, cut at the person. */
	.fill {
		position: absolute;
		inset: 0 auto 0 0;
		background: linear-gradient(90deg, var(--green), var(--amber), var(--red)) 0 0 / var(--lane-w, 100%) 100% no-repeat;
		transition: width 0.5s cubic-bezier(0.22, 1, 0.36, 1);
	}
	.notch {
		position: absolute;
		top: 0;
		bottom: 0;
		width: 1px;
		background: var(--bg-card, #141419);
	}
	.notch--long {
		width: 2px;
	}
	.others {
		position: relative;
	}
	.other {
		position: absolute;
		transform: translateX(-50%);
		width: 30px;
		height: 30px;
		padding: 0;
		border: 2px solid transparent;
		border-radius: 50%;
		background: none;
		cursor: pointer;
		opacity: 0.55;
		filter: grayscale(1);
		transition:
			opacity 0.2s,
			filter 0.2s,
			transform 0.15s;
	}
	.other:hover,
	.other:focus-visible {
		opacity: 1;
		filter: none;
		border-color: var(--oc);
		transform: translateX(-50%) scale(1.15);
		z-index: 2;
	}
	.other--on {
		opacity: 1;
		filter: none;
		border-color: var(--oc);
	}
	.other img,
	.other .noimg {
		width: 100%;
		height: 100%;
		border-radius: 50%;
		object-fit: cover;
		display: grid;
		place-items: center;
		background: var(--bg-elevated, #1c1a22);
		color: var(--text-muted);
		font-size: 0.75rem;
		font-weight: 700;
	}
	.ends {
		display: flex;
		justify-content: space-between;
		margin: 0.3rem 0.4rem 0;
		font-size: 0.62rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	.hint {
		margin: 0.5rem 0 0;
		font-size: 0.75rem;
		color: var(--text-muted);
	}
	@media (prefers-reduced-motion: reduce) {
		.me,
		.needle,
		.fill,
		.me img {
			transition: none;
			animation: none;
		}
	}
</style>
