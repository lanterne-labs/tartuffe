<script lang="ts">
	import type { VoteCounts } from '$lib/types';
	import { API_BASE } from '$lib/config';
	import { lang, tr, type Lang } from '$lib/i18n';

	interface Props {
		baseFactId: number;
		initialVotes?: VoteCounts;
	}

	let { baseFactId, initialVotes }: Props = $props();

	let currentLang = $state<Lang>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	const SESSION_KEY = `vote:${baseFactId}`;
	let userVote = $state<string | null>(null);
	let counts = $state<VoteCounts>(initialVotes ?? { agree: 0, disagree: 0, not_a_contradiction: 0 });
	let submitting = $state(false);

	$effect(() => {
		if (typeof window !== 'undefined') {
			userVote = sessionStorage.getItem(SESSION_KEY);
		}
	});

	async function vote(v: string) {
		if (submitting) return;
		submitting = true;
		try {
			const res = await fetch(`${API_BASE}/api/contradictions/${baseFactId}/vote`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ vote: v })
			});
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const updated: VoteCounts = await res.json();
			counts = updated;
			userVote = v;
			if (typeof window !== 'undefined') {
				sessionStorage.setItem(SESSION_KEY, v);
			}
		} catch {
			// Non-fatal: vote UI degrades silently
		} finally {
			submitting = false;
		}
	}

	const total = $derived(counts.agree + counts.disagree + counts.not_a_contradiction);
</script>

<div class="vote-widget" aria-label={tr('vote.label', currentLang)}>
	<span class="vote-prompt">{tr('vote.prompt', currentLang)}</span>
	<div class="vote-buttons">
		<button
			class="vote-btn vote-agree"
			class:active={userVote === 'agree'}
			disabled={submitting}
			onclick={() => vote('agree')}
			title={tr('vote.agree', currentLang)}
			aria-pressed={userVote === 'agree'}
		>
			<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
				<polyline points="20 6 9 17 4 12"/>
			</svg>
			{tr('vote.agree', currentLang)}
			{#if counts.agree > 0}<span class="vote-count">{counts.agree}</span>{/if}
		</button>
		<button
			class="vote-btn vote-disagree"
			class:active={userVote === 'disagree'}
			disabled={submitting}
			onclick={() => vote('disagree')}
			title={tr('vote.disagree', currentLang)}
			aria-pressed={userVote === 'disagree'}
		>
			<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
				<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
			</svg>
			{tr('vote.disagree', currentLang)}
			{#if counts.disagree > 0}<span class="vote-count">{counts.disagree}</span>{/if}
		</button>
		<button
			class="vote-btn vote-fp"
			class:active={userVote === 'not_a_contradiction'}
			disabled={submitting}
			onclick={() => vote('not_a_contradiction')}
			title={tr('vote.not_a_contradiction', currentLang)}
			aria-pressed={userVote === 'not_a_contradiction'}
		>
			<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
				<circle cx="12" cy="12" r="10"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/>
			</svg>
			{tr('vote.not_a_contradiction', currentLang)}
			{#if counts.not_a_contradiction > 0}<span class="vote-count">{counts.not_a_contradiction}</span>{/if}
		</button>
	</div>
	{#if total > 0}
		<span class="vote-total">{total} {tr(total === 1 ? 'vote.singular' : 'vote.plural', currentLang)}</span>
	{/if}
</div>

<style>
	.vote-widget {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		padding-top: 0.5rem;
		margin-top: 0.375rem;
		border-top: 1px dashed var(--border-card);
	}

	.vote-prompt {
		font-size: 0.7rem;
		color: var(--text-muted);
		font-weight: 500;
		white-space: nowrap;
	}

	.vote-buttons {
		display: flex;
		gap: 0.375rem;
		flex-wrap: wrap;
	}

	.vote-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		padding: 0.2rem 0.5rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--border-card);
		background: transparent;
		font-size: 0.7rem;
		font-family: var(--font-family);
		color: var(--text-muted);
		cursor: pointer;
		transition: all var(--transition-fast);
		line-height: 1;
	}

	.vote-btn:hover:not(:disabled) {
		border-color: var(--text-secondary);
		color: var(--text-secondary);
	}

	.vote-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.vote-agree.active {
		border-color: var(--green);
		color: var(--green);
		background: rgba(95, 165, 106, 0.12);
	}

	.vote-disagree.active {
		border-color: var(--red);
		color: var(--red);
		background: rgba(194, 51, 74, 0.12);
	}

	.vote-fp.active {
		border-color: var(--amber);
		color: var(--amber);
		background: rgba(212, 146, 58, 0.12);
	}

	.vote-count {
		font-weight: 600;
		font-size: 0.65rem;
		background: var(--bg-card);
		padding: 0.05rem 0.3rem;
		border-radius: var(--radius-full);
	}

	.vote-total {
		font-size: 0.65rem;
		color: var(--text-muted);
		margin-left: auto;
	}
</style>
