<script lang="ts">
	import type { ContradictionGroup } from '$lib/types';
	import { TALLY_FORM_ID } from '$lib/config';
	import { lang, tr, pickLang, type Lang } from '$lib/i18n';
	import VoteWidget from './VoteWidget.svelte';

	interface Props {
		g: ContradictionGroup;
		onGoToFact: (id: number) => void;
		onReport: (g: ContradictionGroup) => void;
	}

	let { g, onGoToFact, onReport }: Props = $props();

	let currentLang = $state<Lang>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	function formatDateWithTime(s: string): string {
		return new Date(s).toLocaleString([], {
			year: 'numeric',
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}
</script>

<div class="group">
	<div class="group-header">
		<span class="group-count">{tr('contradiction.facts_in_tension', currentLang, { n: g.contradicting_facts.length + 1 })}</span>
		{#if TALLY_FORM_ID}
			<button class="report-btn-inline" title="Report this contradiction" aria-label="Report this contradiction" onclick={() => onReport(g)}>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
					<line x1="12" y1="9" x2="12" y2="13"/>
					<line x1="12" y1="17" x2="12.01" y2="17"/>
				</svg>
			</button>
		{/if}
	</div>

	{#if g.summary}
		<div class="group-summary">{pickLang(g.summary, g.summary_en ?? undefined, currentLang)}</div>
	{/if}

	<!-- Base (anchor) fact, shown once. -->
	<div class="base-fact">
		<div class="base-label">
			<strong>{tr('contradiction.base_fact', currentLang)}</strong>
			{#if g.base_fact_date}
				<span class="comp-date">{formatDateWithTime(g.base_fact_date)}</span>
			{/if}
		</div>
		<div class="comp-text"><span class="fact-id">[{g.base_fact_id}]</span> {pickLang(g.base_fact_text, g.base_fact_text_en, currentLang)}</div>
		<div class="comp-links">
			<button class="link-btn" onclick={() => onGoToFact(g.base_fact_id)}>{tr('contradiction.view_in_facts', currentLang)}</button>
			{#each g.base_fact_source_urls as url, i}
				<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">Source{g.base_fact_source_urls.length > 1 ? ` ${i + 1}` : ''}</a>
			{/each}
		</div>
	</div>

	<!-- Contradicting facts, each with the reason it conflicts. -->
	<div class="spokes">
		{#each g.contradicting_facts as f (f.contradiction_id)}
			<div class="spoke">
				<div class="spoke-reason">{pickLang(f.reason, f.reason_en, currentLang)}</div>
				<div class="comp-text"><span class="fact-id">[{f.fact_id}]</span> {pickLang(f.fact_text, f.fact_text_en, currentLang)}</div>
				<div class="comp-links">
					{#if f.fact_date}
						<span class="comp-date">{formatDateWithTime(f.fact_date)}</span>
					{/if}
					<button class="link-btn" onclick={() => onGoToFact(f.fact_id)}>{tr('contradiction.view_in_facts', currentLang)}</button>
					{#each f.source_urls as url, i}
						<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">Source{f.source_urls.length > 1 ? ` ${i + 1}` : ''}</a>
					{/each}
				</div>
			</div>
		{/each}
	</div>

	<VoteWidget baseFactId={g.base_fact_id} initialVotes={g.votes} />
</div>

<style>
	.group {
		padding: 0.75rem 0;
		border-top: 1px dashed var(--border-card);
	}
	.group:first-child {
		border-top: none;
		padding-top: 0;
	}

	.group-header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-bottom: 0.375rem;
	}
	.group-count {
		font-weight: 600;
	}

	.group-summary {
		font-size: 0.875rem;
		color: var(--text-primary);
		margin-bottom: 0.625rem;
	}

	.base-fact {
		background: var(--bg-secondary);
		border: 1px solid var(--accent);
		border-radius: var(--radius-sm);
		padding: 0.75rem;
		margin-bottom: 0.625rem;
	}
	.base-label {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		font-size: 0.75rem;
		margin-bottom: 0.375rem;
	}

	.spokes {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		/* indent spokes under the base to read as "base, then its conflicts" */
		padding-left: 0.75rem;
		border-left: 2px solid var(--border-card);
	}
	.spoke {
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-sm);
		padding: 0.75rem;
	}
	.spoke-reason {
		font-size: 0.813rem;
		color: var(--text-secondary);
		margin-bottom: 0.375rem;
	}

	.comp-date {
		color: var(--text-muted);
		font-size: 0.75rem;
	}
	.comp-text {
		font-size: 0.813rem;
		line-height: 1.5;
		color: var(--text-primary);
		margin-bottom: 0.5rem;
	}
	.fact-id {
		font-size: 0.688rem;
		font-weight: 600;
		color: var(--text-muted);
		margin-right: 0.25rem;
	}
	.comp-links {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
	}
	.link-btn {
		background: transparent;
		border: 1px solid var(--border-card);
		color: var(--accent);
		font-size: 0.75rem;
		font-family: var(--font-family);
		padding: 0.25rem 0.5rem;
		border-radius: var(--radius-sm);
		cursor: pointer;
	}
	.link-btn:hover {
		border-color: var(--accent);
	}
	.report-btn-inline {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		padding: 0;
		background: transparent;
		border: none;
		color: var(--text-secondary);
		opacity: 0.6;
		cursor: pointer;
	}
	.report-btn-inline:hover {
		color: var(--red);
		opacity: 1;
	}
</style>
