<script lang="ts">
	// The "?" beside a score: how this number is made. A person's inputs come
	// from the API (GET /api/person/{id}/score); a stance's from its card.
	import Spinner from './Spinner.svelte';
	import Modal from './Modal.svelte';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import { scoreTerms, stanceInputs } from '$lib/score';
	import TartuffeScore, { tartuffeOf } from '$lib/components/TartuffeScore.svelte';
	import { displayName } from '$lib/share';
	import type { ScoreExplanation } from '$lib/types';

	interface Props {
		name: string;
		// A person's score: their id.
		personId?: number;
		// A stance's score: its label, facts and contradiction pairs.
		stance?: { label: string; facts: number; pairs: number };
	}

	let { name, personId, stance }: Props = $props();

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let open = $state(false);
	let root: HTMLElement | undefined = $state();

	// The "?" can sit inside a card that is a link: a click here must not
	// follow it, except a link of the explanation itself.
	function keepClick(e: MouseEvent) {
		e.stopPropagation();
		const a = (e.target as Element | null)?.closest('a');
		if (!a || !root?.contains(a)) e.preventDefault();
	}
	let expl = $state<ScoreExplanation | null>(null);
	let loadError = $state(false);

	async function show() {
		open = true;
		if (personId === undefined || expl) return;
		loadError = false;
		try {
			const res = await fetch(`${API_BASE}/api/person/${personId}/score`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			expl = await res.json();
		} catch (err) {
			console.error('failed to load the score explanation', err);
			loadError = true;
		}
	}

	let inputs = $derived(
		stance ? stanceInputs(stance.facts, stance.pairs) : expl ? { facts: expl.facts, bundles: expl.bundles, pairs: expl.pairs } : null
	);
	let score = $derived(stance ? (inputs ? scoreTerms(inputs).score : null) : (expl?.score ?? null));
</script>

<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
<span class="score-help" bind:this={root} onclick={keepClick} onkeydown={(e) => e.stopPropagation()}>
	<button class="q" onclick={show} aria-label={tr('score.help', currentLang)} title={tr('score.help', currentLang)}>?</button>
	<Modal {open} title={tr(stance ? 'score.title.stance' : 'score.title', currentLang, { name: displayName(name), topic: stance?.label ?? '' })} onclose={() => (open = false)} closeLabel={tr('auth.close', currentLang)}>
		{#if loadError}
			<p class="err">{tr('score.error', currentLang)}</p>
		{:else if !inputs || score == null}
			<p class="muted loading"><Spinner label={tr('person.loading', currentLang)} /></p>
		{:else}
			<p class="big"><strong>{tartuffeOf(score) ?? 0}</strong>/100</p>
			<div class="baro"><TartuffeScore honesty={score} lang={currentLang} size="md" facts={inputs.facts} contradictions={inputs.bundles} showEvidence={false} /></div>
			<p class="what">{tr('score.what', currentLang)}</p>

			<dl class="inputs">
				<div><dt>{tr('score.facts', currentLang)}</dt><dd>{inputs.facts}</dd></div>
				<div><dt>{tr('score.contradictions', currentLang)}</dt><dd>{inputs.bundles}</dd></div>
				{#if expl && expl.members_score != null}
					<div><dt>{tr('score.members', currentLang)}</dt><dd>{expl.members}</dd></div>
				{/if}
			</dl>

			<p class="muted small"><a href="/about#score">{tr('score.more', currentLang)}</a></p>
		{/if}
	</Modal>
</span>

<style>
	.baro {
		display: flex;
		justify-content: center;
		margin: 0 0 0.75rem;
	}
	.score-help {
		display: inline-flex;
		vertical-align: middle;
	}
	.q {
		width: 18px;
		height: 18px;
		padding: 0;
		border-radius: 50%;
		border: 1px solid var(--border-card-hover);
		background: transparent;
		color: var(--text-secondary);
		font: 600 0.7rem/1 var(--font-family);
		cursor: pointer;
	}
	.q:hover {
		color: var(--text-primary);
		border-color: var(--accent);
	}
	.big {
		font-family: var(--font-serif);
		font-variant-numeric: lining-nums;
		font-size: 2.4rem;
		margin: 0;
		line-height: 1.1;
	}
	.what {
		color: var(--text-primary);
		line-height: 1.5;
		margin: 0.5rem 0 1rem;
	}
	.inputs {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(0, 1fr));
		gap: 0.5rem;
		margin: 0 0 1rem;
	}
	.inputs div {
		padding: 0.5rem 0.6rem;
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
	}
	.inputs dt {
		font-size: 0.72rem;
		color: var(--text-muted);
	}
	.inputs dd {
		margin: 0.1rem 0 0;
		font-size: 1.1rem;
		font-weight: 600;
	}
	.muted {
		color: var(--text-secondary);
	}
	.small {
		font-size: 0.8rem;
		line-height: 1.5;
	}
	.err {
		color: var(--red);
	}
</style>
