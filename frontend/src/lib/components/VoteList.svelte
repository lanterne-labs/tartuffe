<script lang="ts">
	// The user's votes of one event, first choice first, with a slot per vote
	// still free. Editable: each vote moves up, down, or is removed (the
	// caller saves at once).
	import { tr, type Lang } from '$lib/i18n';

	interface Vote {
		person_id: number;
		name: string;
		image_url?: string;
		honesty_score?: number;
	}

	interface Props {
		votes: Vote[];
		max: number;
		lang: Lang;
		editable?: boolean;
		busy?: boolean;
		onmove?: (index: number, delta: number) => void;
		onremove?: (personId: number) => void;
	}

	let { votes, max, lang, editable = false, busy = false, onmove, onremove }: Props = $props();

	let free = $derived(Math.max(0, max - votes.length));
</script>

<div class="credits" aria-label={tr('vote.credits', lang, { used: votes.length, max })}>
	<span class="credit-dots" aria-hidden="true">
		{#each { length: Math.max(max, votes.length) } as _, i}
			<span class="credit-dot" class:used={i < votes.length}></span>
		{/each}
	</span>
	<span>{tr('vote.credits', lang, { used: votes.length, max })}</span>
</div>

<ol class="votes">
	{#each votes as v, i (v.person_id)}
		<li class="vote">
			<span class="rank">{i + 1}</span>
			<a href="/person/{v.person_id}" class="who">
				{#if v.image_url}
					<img src={v.image_url} alt="" class="photo" />
				{:else}
					<span class="photo photo--empty">{v.name.charAt(0).toUpperCase()}</span>
				{/if}
				<span class="name">{v.name}</span>
			</a>
			{#if v.honesty_score !== undefined}
				<span class="honesty" title={tr('poll.gauge.honesty', lang)}>{Math.round(v.honesty_score * 100)}<small>/100</small></span>
			{/if}
			{#if editable}
				<span class="actions">
					<button class="icon" onclick={() => onmove?.(i, -1)} disabled={busy || i === 0} aria-label={tr('poll.ballot.up', lang)} title={tr('poll.ballot.up', lang)}>↑</button>
					<button class="icon" onclick={() => onmove?.(i, 1)} disabled={busy || i === votes.length - 1} aria-label={tr('poll.ballot.down', lang)} title={tr('poll.ballot.down', lang)}>↓</button>
					<button class="remove" onclick={() => onremove?.(v.person_id)} disabled={busy} aria-label={tr('vote.remove_named', lang, { name: v.name })}>
						{tr('vote.remove', lang)}
					</button>
				</span>
			{/if}
		</li>
	{/each}
	{#if editable}
		{#each { length: free } as _, i}
			<li class="vote vote--free">
				<span class="rank rank--free">{votes.length + i + 1}</span>
				<span class="free-label">{tr('vote.slot_free', lang)}</span>
			</li>
		{/each}
	{/if}
</ol>

<style>
	.credits {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.8rem;
		color: var(--text-secondary);
		margin: 0 0 0.6rem;
	}
	.credit-dots {
		display: inline-flex;
		gap: 4px;
	}
	.credit-dot {
		width: 0.6rem;
		height: 0.6rem;
		border-radius: 50%;
		border: 1px solid var(--accent);
	}
	.credit-dot.used {
		background: var(--accent);
	}
	.votes {
		list-style: none;
		margin: 0 0 0.75rem;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
	}
	.vote {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.45rem 0.75rem;
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
		min-height: 2.9rem;
	}
	.vote--free {
		background: transparent;
		border: 1px dashed var(--glass-border);
	}
	.rank {
		width: 1.6rem;
		height: 1.6rem;
		flex-shrink: 0;
		display: grid;
		place-items: center;
		border-radius: 50%;
		background: var(--accent);
		color: white;
		font-weight: 700;
		font-size: 0.85rem;
	}
	.rank--free {
		background: transparent;
		border: 1px dashed var(--text-muted);
		color: var(--text-muted);
	}
	.free-label {
		font-size: 0.85rem;
		color: var(--text-muted);
	}
	.who {
		flex: 1;
		min-width: 0;
		display: flex;
		align-items: center;
		gap: 0.6rem;
		color: inherit;
		text-decoration: none;
	}
	.who:hover .name {
		color: var(--accent-hover);
	}
	.photo {
		width: 2rem;
		height: 2rem;
		flex-shrink: 0;
		border-radius: 50%;
		object-fit: cover;
		background: var(--bg-card);
	}
	.photo--empty {
		display: grid;
		place-items: center;
		font-weight: 700;
		color: var(--text-secondary);
	}
	.name {
		font-weight: 500;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.honesty {
		font-weight: 700;
		font-variant-numeric: tabular-nums;
		color: var(--text-secondary);
	}
	.honesty small {
		font-weight: 400;
		color: var(--text-muted);
	}
	.actions {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		flex-shrink: 0;
	}
	.icon,
	.remove {
		height: 1.9rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--text-secondary);
		cursor: pointer;
	}
	.icon {
		width: 1.9rem;
	}
	.remove {
		padding: 0 0.6rem;
		font-size: 0.75rem;
		font-weight: 600;
	}
	.icon:hover:not(:disabled) {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}
	.remove:hover:not(:disabled) {
		color: var(--red);
		border-color: var(--red);
	}
	.icon:disabled,
	.remove:disabled {
		opacity: 0.3;
		cursor: default;
	}
	@media (max-width: 480px) {
		.vote {
			flex-wrap: wrap;
			gap: 0.4rem 0.5rem;
			padding: 0.45rem 0.5rem;
		}
		.who {
			flex-basis: calc(100% - 2.1rem - 3.5rem);
		}
		.actions {
			width: 100%;
			justify-content: flex-end;
		}
	}
</style>
