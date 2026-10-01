<script lang="ts">
	import type { Restatement } from '$lib/types';
	import { tr, pickLang, type Lang } from '$lib/i18n';
	import { outletSummary, sourceDomain } from '$lib/sources';

	// A claim group shown under its parent fact (the group's most concrete
	// wording, displayed by the caller): one collapsed line, "Reported N times ·
	// lemonde.fr, liberation.fr +2", that expands to every other wording with
	// its source. Nothing when the fact stands alone.
	interface Props {
		sourceUrls: string[]; // the parent fact's own sources
		restatements: Restatement[];
		lang: Lang;
	}

	let { sourceUrls, restatements, lang }: Props = $props();

	let outlets = $derived(outletSummary([...sourceUrls, ...restatements.map((r) => r.source_url)]));
</script>

{#if restatements.length > 0}
	<!-- The click only stops the enclosing card from toggling; <details> itself is keyboard-accessible. -->
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
	<details class="versions" onclick={(e) => e.stopPropagation()}>
		<summary>
			{tr('fact.reported_n_times', lang, { n: restatements.length + 1 })}
			<span class="outlets">· {outlets.shown.join(', ')}{outlets.more > 0 ? ` +${outlets.more}` : ''}</span>
		</summary>
		<ul>
			{#each restatements as r, i (i)}
				<li>
					{pickLang(r.fact_text, r.fact_text_en, lang)}
					{#if r.source_url}
						<a href={r.source_url} target="_blank" rel="noopener noreferrer" title={r.source_url}>{sourceDomain(r.source_url)}</a>
					{/if}
				</li>
			{/each}
		</ul>
	</details>
{/if}

<style>
	.versions {
		margin-top: 0.35rem;
		font-size: 0.8rem;
		color: var(--text-secondary);
	}
	summary {
		cursor: pointer;
	}
	.outlets {
		color: var(--text-muted);
	}
	ul {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	li {
		margin: 0.35rem 0 0 0.75rem;
		padding-left: 0.5rem;
		border-left: 2px solid var(--border-card-hover);
	}
	li a {
		margin-left: 0.4rem;
		color: var(--text-muted);
	}
</style>
