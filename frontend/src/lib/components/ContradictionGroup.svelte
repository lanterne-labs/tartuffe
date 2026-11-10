<script lang="ts">
	import { sourceDomain } from '$lib/sources';
	import FactVersions from './FactVersions.svelte';
	import ShareMenu from './ShareMenu.svelte';
	import ReportButton from './ReportButton.svelte';
	import LikeButton from './LikeButton.svelte';
	import type { ContradictionGroup } from '$lib/types';
	import { lang, tr, pickLang, whyOf, type Lang } from '$lib/i18n';

	interface Props {
		g: ContradictionGroup;
		// The person and the topic, for the shared links.
		person: { id: number; name: string };
		topic: string;
		onGoToFact: (id: number) => void;
		// highlight is the contradiction a link points at.
		highlight?: number | null;
	}

	let { g, person, topic, onGoToFact, highlight = null }: Props = $props();

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
			{#each g.base_fact_source_urls as url}
				<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn" title={url}>{sourceDomain(url)}</a>
			{/each}
			<span class="spacer"></span>
			<LikeButton kind="fact" targetId={g.base_fact_id} likes={g.base_likes} liked={g.base_liked} />
			<ShareMenu item={{ kind: 'fact', id: g.base_fact_id, personId: person.id, personName: person.name, topic, text: pickLang(g.base_fact_text, g.base_fact_text_en, currentLang) }} />
		</div>
		<FactVersions sourceUrls={g.base_fact_source_urls} restatements={g.base_restatements ?? []} lang={currentLang} />
	</div>

	<!-- Contradicting facts, each with the reason it conflicts. -->
	<div class="spokes">
		{#each g.contradicting_facts as f (f.contradiction_id)}
			<div class="spoke" id="c-{f.contradiction_id}" class:highlight={highlight === f.contradiction_id}>
				<div class="spoke-reason">{whyOf(f, currentLang)}</div>
				<div class="comp-text"><span class="fact-id">[{f.fact_id}]</span> {pickLang(f.fact_text, f.fact_text_en, currentLang)}</div>
				<div class="comp-links">
					{#if f.fact_date}
						<span class="comp-date">{formatDateWithTime(f.fact_date)}</span>
					{/if}
					<button class="link-btn" onclick={() => onGoToFact(f.fact_id)}>{tr('contradiction.view_in_facts', currentLang)}</button>
					{#each f.source_urls as url}
						<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn" title={url}>{sourceDomain(url)}</a>
					{/each}
					<span class="spacer"></span>
					<LikeButton kind="contradiction" targetId={f.contradiction_id} likes={f.likes} liked={f.liked} />
					<ShareMenu
						item={{
							kind: 'contradiction',
							id: f.contradiction_id,
							personId: person.id,
							personName: person.name,
							topic,
							text: pickLang(g.base_fact_text, g.base_fact_text_en, currentLang),
							other: pickLang(f.fact_text, f.fact_text_en, currentLang)
						}}
					/>
					<ReportButton
						kind="contradiction"
						targetId={f.contradiction_id}
						pair={{
							a: { id: g.base_fact_id, text: pickLang(g.base_fact_text, g.base_fact_text_en, currentLang), date: g.base_fact_date },
							b: { id: f.fact_id, text: pickLang(f.fact_text, f.fact_text_en, currentLang), date: f.fact_date },
							reason: whyOf(f, currentLang)
						}}
					/>
				</div>
				<FactVersions sourceUrls={f.source_urls} restatements={f.restatements ?? []} lang={currentLang} />
			</div>
		{/each}
	</div>
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
		line-height: 1.5;
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
	.spacer {
		flex: 1;
	}
	.spoke.highlight {
		border-color: var(--accent);
		box-shadow: 0 0 0 3px var(--accent-glow);
		transition: box-shadow 0.6s ease;
	}
</style>
