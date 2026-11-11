<script lang="ts">
	// One person among the others on the Tartuffe-Score: the band of the dial
	// laid flat, filled up to the person in focus, whose portrait stands over
	// it; every other candidate is a small face under the band, at their own
	// place. A click on a face puts that person in focus.
	import { tr, type Lang } from '$lib/i18n';
	import { displayName, personPath } from '$lib/share';
	import { tartuffeOf, tartuffeColor, dialOf, MARKS, coveredText } from './TartuffeScore.svelte';
	import HintSheet from './HintSheet.svelte';
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
	// The small faces: large enough for a finger (CSS .other, --small). On a
	// narrow lane (a phone) they are a little larger when they still fit in
	// the rows without piling up.
	const SMALL = 34;
	const SMALL_PHONE = 40;

	// A touch screen has no hover: the 🚧 of the portrait in focus opens its
	// text in a sheet on a tap. Read after mount: the page is server-rendered.
	let touch = $state(false);
	$effect(() => {
		touch = window.matchMedia('(hover: none)').matches;
	});

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
	// How many are more tartuffe (above) and more coherent (below) than the
	// focus, among the people whose score is reliable: a clean score on few
	// facts is no evidence of coherence. A focus being consolidated is not
	// ranked yet.
	let sure = $derived(rated.filter((r) => r.p.score_reliable !== false));
	let above = $derived(current ? sure.filter((r) => r.p.id !== current.p.id && r.t > current.t).length : 0);
	let below = $derived(current ? sure.filter((r) => r.p.id !== current.p.id && r.t < current.t).length : 0);
	let rank = $derived.by(() => {
		if (current?.p.score_reliable === false) return tr('score.compare.unsure', lang, { s: coveredText(current.p.covered_facts, lang) });
		const n = sure.filter((r) => r.p.id !== current?.p.id).length;
		if (above > 0 && below > 0) return tr('score.compare.both', lang, { a: above, b: below, n });
		if (above > 0) return tr('score.compare.coherent', lang, { a: above, n });
		if (below > 0) return tr('score.compare.tartuffe', lang, { b: below, n });
		return tr('score.compare.tie', lang);
	});

	// What the 🚧 says, in the words of the dial (TartuffeScore): on hover,
	// on focus, and for the portrait in focus on a tap (a phone has no hover).
	function unsureText(p: PersonSummary): string {
		return tr('score.consolidating', lang, {
			f: (p.fact_count ?? 0).toLocaleString(lang === 'en' ? 'en-GB' : 'fr-FR'),
			s: coveredText(p.covered_facts, lang)
		});
	}
	let unsureOpen = $state(false);
	$effect(() => {
		// Another person in focus: the explanation of the one before closes.
		void chosen;
		unsureOpen = false;
	});

	// The place on the band, in % of its width: 3 to 97 so the ends show whole.
	const left = (at: number) => 3 + at * 0.94;

	// The small faces climb down in rows where they would overlap; past
	// ROWS rows they pile up, each a little to the right of the one before.
	const ROWS = 3;
	const PILE = 14;
	const PILE_PHONE = 18;
	let width = $state(0);
	function layout(size: number, pile: number) {
		const ends: number[] = [];
		let piled = false;
		const spots = rated.map(({ at }) => {
			let x = (left(at) / 100) * width;
			let row = 0;
			while (row < ROWS && ends[row] !== undefined && ends[row] > x - size - 3) row++;
			if (row === ROWS) {
				piled = true;
				row = ends.indexOf(Math.min(...ends));
				x = Math.max(x, ends[row] + pile);
			}
			ends[row] = x;
			return { x, row };
		});
		return { spots, piled };
	}
	// The faces' size and places: the larger size on a narrow lane when it
	// fits without a pile, else the usual one.
	let sized = $derived.by(() => {
		if (width > 0 && width < 480) {
			const big = layout(SMALL_PHONE, PILE_PHONE);
			if (!big.piled) return { spots: big.spots, small: SMALL_PHONE };
		}
		return { spots: layout(SMALL, PILE).spots, small: SMALL };
	});
	let spots = $derived(sized.spots);
	let small = $derived(sized.small);
	let depth = $derived(spots.reduce((m, s) => Math.max(m, s.row + 1), 1));
	// The portrait in focus keeps whole inside the lane; its needle stays on the place.
	const BIG = 66;
	let place = $derived(current ? (left(current.at) / 100) * width : 0);
	let face = $derived(Math.min(Math.max(place, BIG / 2), width - BIG / 2));
</script>

{#if current}
	<div class="compare" style="--c: {tartuffeColor(current.at)}">
		<div class="head">
			<a class="name" href={personPath(current.p.slug)}>{displayName(current.p.name)} →</a>
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
						{#if current.p.score_reliable === false}
							<!-- The same mark as on the dial: a score being consolidated. -->
							<!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
							<!-- On a touch screen the text opens in a sheet, and the blur
							     does not close it: the tap that closes the sheet takes the
							     focus first, and would land on the page beneath. -->
							<span
								class="unsure-mark unsure-mark--big"
								class:unsure-mark--on={unsureOpen && !touch}
								class:unsure-mark--left={current.at > 50}
								tabindex="0"
								role="note"
								aria-label={unsureText(current.p)}
								onclick={() => (unsureOpen = !unsureOpen)}
								onkeydown={(e) => e.key === 'Escape' && (unsureOpen = false)}
								onblur={() => {
									if (!touch) unsureOpen = false;
								}}
							>🚧<span class="unsure-tip" aria-hidden="true">{unsureText(current.p)}</span></span>
							{#if touch}
								<HintSheet open={unsureOpen} onclose={() => (unsureOpen = false)}>
									{unsureText(current.p)}
								</HintSheet>
							{/if}
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
			<div class="others" style="height: {depth * (small + 6) + 4}px; --small: {small}px">
				{#each rated as r, i (r.p.id)}
					<button
						class="other"
						class:other--on={r.p.id === current.p.id}
						class:other--unsure={r.p.score_reliable === false}
						style="left: {spots[i].x}px; top: {spots[i].row * (small + 6) + 4}px; --oc: {tartuffeColor(r.at)}"
						onclick={() => (chosen = r.p.id)}
						aria-pressed={r.p.id === current.p.id}
						title={r.p.score_reliable === false ? undefined : tr('score.lineup.who', lang, { name: displayName(r.p.name), n: r.t })}
						aria-label={tr('score.lineup.who', lang, { name: displayName(r.p.name), n: r.t }) + (r.p.score_reliable === false ? ` (${tr('score.consolidating.short', lang)})` : '')}
					>
						{#if r.p.image_url}
							<img src={r.p.image_url} alt={displayName(r.p.name)} />
						{:else}
							<span class="noimg">{r.p.name.charAt(0).toUpperCase()}</span>
						{/if}
						{#if r.p.score_reliable === false}
							<!-- No native title on this face: the bubble says who and why. -->
							<span class="unsure-mark" class:unsure-mark--left={r.at > 50} aria-hidden="true"
								>🚧<span class="unsure-tip"><strong>{tr('score.lineup.who', lang, { name: displayName(r.p.name), n: r.t })}</strong>{unsureText(r.p)}</span></span
							>
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
		width: var(--small, 34px);
		height: var(--small, 34px);
		padding: 0;
		border: 2px solid transparent;
		border-radius: 50%;
		background: none;
		cursor: pointer;
		opacity: 0.55;
		transition:
			opacity 0.2s,
			transform 0.15s;
	}
	.other:hover,
	.other:focus-visible {
		opacity: 1;
		border-color: var(--oc);
		transform: translateX(-50%) scale(1.15);
		z-index: 2;
	}
	.other--on {
		opacity: 1;
		border-color: var(--oc);
	}
	/* The face is grey until it is in focus; the mark on it keeps its colour. */
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
		filter: grayscale(1);
		transition: filter 0.2s;
	}
	.other:hover img,
	.other:hover .noimg,
	.other:focus-visible img,
	.other:focus-visible .noimg,
	.other--on img,
	.other--on .noimg {
		filter: none;
	}
	.ends {
		display: flex;
		justify-content: space-between;
		margin: 0.3rem 0.4rem 0;
		font-size: 0.72rem;
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
	.other--unsure:not(.other--on) {
		opacity: 0.45;
		border-style: dashed;
	}
	/* Under the mouse it is as bright as the others: its bubble is read. */
	.other--unsure:not(.other--on):hover,
	.other--unsure:not(.other--on):focus-visible {
		opacity: 1;
	}
	/* The consolidation mark of the dial (TartuffeScore), at the foot of the
	   face: the small faces are grey and dim, the mark stays in colour. */
	.unsure-mark {
		position: absolute;
		right: -4px;
		bottom: -4px;
		z-index: 2;
		display: grid;
		place-items: center;
		width: 1.25em;
		height: 1.25em;
		font-size: 0.8rem;
		line-height: 1;
	}
	.unsure-mark--big {
		right: -2px;
		bottom: -2px;
		font-size: 1.15rem;
		cursor: help;
	}
	/* The bubble of the dial. It opens to the right of the mark, and to the
	   left for a face on the right half of the band, so it stays in the card. */
	.unsure-tip {
		display: none;
		position: absolute;
		bottom: calc(100% + 0.35rem);
		left: -0.3rem;
		width: min(15rem, 75vw);
		padding: 0.5rem 0.65rem;
		border-radius: var(--radius-md, 10px);
		background: var(--bg-elevated, #1c1a22);
		border: 1px solid var(--amber);
		box-shadow: 0 6px 24px rgba(0, 0, 0, 0.5);
		color: var(--text-primary);
		font-size: 0.78rem;
		font-weight: 500;
		line-height: 1.4;
		text-align: left;
		white-space: normal;
		pointer-events: none;
	}
	.unsure-tip strong {
		display: block;
		margin-bottom: 0.2rem;
	}
	.unsure-mark--left .unsure-tip {
		left: auto;
		right: -0.3rem;
	}
	/* On a small face the whole face shows it: the mark alone is a few pixels.
	   The hover is the mouse's: a touch screen keeps a tapped face "hovered",
	   and its bubble would stay; there the tap puts the face in focus, whose
	   🚧 opens the sheet. */
	@media (hover: hover) {
		.other:hover .unsure-tip,
		.unsure-mark:hover .unsure-tip {
			display: block;
		}
	}
	.other:focus-visible .unsure-tip,
	.unsure-mark:focus-visible .unsure-tip,
	.unsure-mark--on .unsure-tip {
		display: block;
	}
</style>
