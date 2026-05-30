<script lang="ts">
	import type { BundledContradiction, BundledContradictionFact } from '$lib/types';
	import { lang, tr, pickLang, type Lang } from '$lib/i18n';

	interface Props {
		b: BundledContradiction;
		onGoToFact: (id: number) => void;
	}

	let { b, onGoToFact }: Props = $props();

	let currentLang = $state<Lang>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

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

<div class="bundle-card">
	<div class="bundle-header">
		{#if b.pair_count > 1}
			<span class="pair-count">{b.pair_count} pairs</span>
		{/if}
		<span class="detected-date">{formatDate(b.detected_at)}</span>
	</div>

	<div class="bundle-reason">{pickLang(b.reason, b.reason_en, currentLang)}</div>

	<div class="facts-list">
		{#each b.facts as fact (fact.id)}
			<div class="fact-card">
				<div class="fact-meta">
					<span class="fact-id">[{fact.id}]</span>
					{#if fact.date}
						<span class="fact-date">{formatDateWithTime(fact.date)}</span>
					{/if}
					{#if fact.stance_label}
						<span class="fact-stance">{pickLang(fact.stance_label, fact.stance_label_en, currentLang)}</span>
					{/if}
				</div>
				<div class="fact-text">{pickLang(fact.text, fact.text_en, currentLang)}</div>
				<div class="fact-links">
					<button class="link-btn" onclick={() => onGoToFact(fact.id)}>{tr('contradiction.view_in_facts', currentLang)}</button>
					{#each fact.source_urls as url, i}
						<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">
							Source{fact.source_urls.length > 1 ? ` ${i + 1}` : ''}
						</a>
					{/each}
				</div>
			</div>
		{/each}
	</div>
</div>

<style>
	.bundle-card {
		padding: 0.75rem 0;
		border-top: 1px dashed var(--border-card);
	}

	.bundle-card:first-child {
		border-top: none;
		padding-top: 0;
	}

	.bundle-header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-bottom: 0.375rem;
	}

	.pair-count {
		font-weight: 600;
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-sm);
		padding: 0.125rem 0.375rem;
	}

	.bundle-reason {
		font-size: 0.875rem;
		color: var(--text-primary);
		margin-bottom: 0.625rem;
	}

	.facts-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.fact-card {
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-sm);
		padding: 0.75rem;
	}

	.fact-meta {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-bottom: 0.375rem;
		flex-wrap: wrap;
	}

	.fact-id {
		font-weight: 600;
	}

	.fact-stance {
		font-style: italic;
	}

	.fact-text {
		font-size: 0.813rem;
		line-height: 1.5;
		color: var(--text-primary);
		margin-bottom: 0.5rem;
	}

	.fact-links {
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
		text-decoration: none;
	}

	.link-btn:hover {
		border-color: var(--accent);
	}
</style>
