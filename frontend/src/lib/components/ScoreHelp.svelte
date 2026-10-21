<script lang="ts">
	// The "?" beside a score: how this number is made. A person's inputs come
	// from the API (GET /api/person/{id}/score); a stance's from its card.
	import Modal from './Modal.svelte';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import { scoreTerms, stanceInputs, RATIO_DECAY, ABSOLUTE_DECAY, EXTRA_DECAY, MIN_DENOMINATOR } from '$lib/score';
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
	let terms = $derived(inputs ? scoreTerms(inputs) : null);
	let pct = (x: number) => Math.round(x * 100);
	let fmt = (x: number) => x.toLocaleString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { maximumFractionDigits: 3 });

	// What the number says, in words.
	let reading = $derived.by(() => {
		if (!inputs || !terms) return '';
		if (inputs.facts === 0) return tr('score.read.nothing', currentLang);
		if (inputs.bundles === 0) {
			return tr(inputs.facts < MIN_DENOMINATOR ? 'score.read.clean_few' : 'score.read.clean_many', currentLang, { f: inputs.facts });
		}
		const one = inputs.bundles === 1 ? '_one' : '';
		if (inputs.facts < MIN_DENOMINATOR) return tr(`score.read.few${one}`, currentLang, { f: inputs.facts, b: inputs.bundles });
		return tr(`score.read.ratio${one}`, currentLang, { f: inputs.facts, b: inputs.bundles, r: Math.max(1, Math.round(inputs.facts / inputs.bundles)) });
	});
</script>

<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
<span class="score-help" bind:this={root} onclick={keepClick} onkeydown={(e) => e.stopPropagation()}>
	<button class="q" onclick={show} aria-label={tr('score.help', currentLang)} title={tr('score.help', currentLang)}>?</button>
	<Modal {open} title={tr(stance ? 'score.title.stance' : 'score.title', currentLang, { name: displayName(name), topic: stance?.label ?? '' })} onclose={() => (open = false)} closeLabel={tr('auth.close', currentLang)}>
		{#if loadError}
			<p class="err">{tr('score.error', currentLang)}</p>
		{:else if !inputs || !terms}
			<p class="muted">{tr('person.loading', currentLang)}</p>
		{:else}
			<p class="big"><strong>{pct(stance ? terms.score : (expl?.score ?? terms.score))}</strong>/100</p>
			<p class="reading">{reading}</p>

			<dl class="inputs">
				<div><dt>{tr('score.facts', currentLang)}</dt><dd>{inputs.facts}</dd></div>
				<div><dt>{tr('score.bundles', currentLang)}</dt><dd>{inputs.bundles}</dd></div>
				<div><dt>{tr('score.extras', currentLang)}</dt><dd>{terms.extras}</dd></div>
			</dl>

			<div class="formula" aria-label={tr('score.formula', currentLang)}>
				<code>exp(−{RATIO_DECAY} × {inputs.bundles} / {terms.denominator} − {ABSOLUTE_DECAY} × {inputs.bundles} − {EXTRA_DECAY} × ln(1 + {terms.extras}))</code>
				<code>= exp(−{fmt(terms.ratioTerm)} − {fmt(terms.absoluteTerm)} − {fmt(terms.extraTerm)}) = {fmt(terms.score)}</code>
			</div>
			<ul class="notes">
				<li>{tr('score.note.bundle', currentLang)}</li>
				<li>{tr('score.note.floor', currentLang, { n: MIN_DENOMINATOR })}</li>
				{#if stance}
					<li>{tr('score.note.stance', currentLang)}</li>
				{/if}
			</ul>

			{#if expl && expl.members_score != null}
				<div class="party">
					<p>{tr('score.party', currentLang, { own: pct(expl.own_score), members: pct(expl.members_score), n: expl.members, total: pct(expl.score) })}</p>
				</div>
			{/if}
			<p class="muted small">{tr('score.consistency', currentLang)} <a href="/about#score">{tr('score.more', currentLang)}</a></p>
		{/if}
	</Modal>
</span>

<style>
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
	.reading {
		color: var(--text-primary);
		line-height: 1.5;
		margin: 0.25rem 0 1rem;
	}
	.inputs {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
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
	.formula {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		margin-bottom: 0.75rem;
		overflow-x: auto;
	}
	code {
		font-size: 0.75rem;
		color: var(--text-secondary);
		white-space: nowrap;
	}
	.notes {
		margin: 0 0 0.75rem;
		padding-left: 1.1rem;
		color: var(--text-secondary);
		font-size: 0.82rem;
		line-height: 1.5;
	}
	.party p {
		font-size: 0.85rem;
		line-height: 1.5;
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
