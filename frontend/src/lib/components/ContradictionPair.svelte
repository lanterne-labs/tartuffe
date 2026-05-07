<script lang="ts">
	import type { ContradictionWithFacts } from '$lib/types';
	import { TALLY_FORM_ID } from '$lib/config';

	interface Props {
		c: ContradictionWithFacts;
		onGoToFact: (id: number) => void;
		onReport: (c: ContradictionWithFacts) => void;
	}

	let { c, onGoToFact, onReport }: Props = $props();

	function formatDate(s: string): string {
		return new Date(s).toLocaleDateString();
	}

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

<div class="pair">
	<div class="contradiction-header">
		<span class="contradiction-id">#{c.id}</span>
		<span class="contradiction-date">{formatDate(c.detected_at)}</span>
		{#if TALLY_FORM_ID}
			<button class="report-btn-inline" title="Report this contradiction" aria-label="Report this contradiction" onclick={() => onReport(c)}>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
					<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
					<line x1="12" y1="9" x2="12" y2="13"/>
					<line x1="12" y1="17" x2="12.01" y2="17"/>
				</svg>
			</button>
		{/if}
	</div>
	<div class="contradiction-reason">{c.reason}</div>
	<div class="facts-comparison">
		<div class="comp-fact comp-a">
			<div class="comp-label">
				<strong>Statement A</strong>
				{#if c.fact_a_date}
					<span class="comp-date">{formatDateWithTime(c.fact_a_date)}</span>
				{/if}
			</div>
			<div class="comp-text"><span class="fact-id">[{c.fact_a_id}]</span> {c.fact_a_text}</div>
			<div class="comp-links">
				<button class="link-btn" onclick={() => onGoToFact(c.fact_a_id)}>View in Facts</button>
				{#each c.fact_a_source_urls as url, i}
					<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">Source{c.fact_a_source_urls.length > 1 ? ` ${i + 1}` : ''}</a>
				{/each}
			</div>
		</div>
		<div class="comp-fact comp-b">
			<div class="comp-label">
				<strong>Statement B</strong>
				{#if c.fact_b_date}
					<span class="comp-date">{formatDateWithTime(c.fact_b_date)}</span>
				{/if}
			</div>
			<div class="comp-text"><span class="fact-id">[{c.fact_b_id}]</span> {c.fact_b_text}</div>
			<div class="comp-links">
				<button class="link-btn" onclick={() => onGoToFact(c.fact_b_id)}>View in Facts</button>
				{#each c.fact_b_source_urls as url, i}
					<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">Source{c.fact_b_source_urls.length > 1 ? ` ${i + 1}` : ''}</a>
				{/each}
			</div>
		</div>
	</div>
</div>

<style>
	.pair {
		padding: 0.75rem 0;
		border-top: 1px dashed var(--border-card);
	}

	.pair:first-child {
		border-top: none;
		padding-top: 0;
	}

	.contradiction-header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-bottom: 0.375rem;
	}

	.contradiction-id {
		font-weight: 600;
	}

	.contradiction-reason {
		font-size: 0.875rem;
		color: var(--text-primary);
		margin-bottom: 0.625rem;
	}

	.facts-comparison {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
	}

	.comp-fact {
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-sm);
		padding: 0.75rem;
	}

	.comp-label {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		font-size: 0.75rem;
		margin-bottom: 0.375rem;
	}

	.comp-date {
		color: var(--text-muted);
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

	@media (max-width: 640px) {
		.facts-comparison {
			grid-template-columns: 1fr;
		}
	}
</style>
