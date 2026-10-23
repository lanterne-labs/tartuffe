<script lang="ts" module>
	// A message from one actor to another, or what one actor does on its own
	// (a note on its lifeline, over span columns from it, or up to it for the
	// last ones).
	export type Step = { from: number; to: number; label: string; dashed?: boolean } | { at: number; note: string; span?: number };
	export type Actor = { label: string; sub?: string };
</script>

<script lang="ts">
	// A sequence diagram: the actors as lifelines, the messages in order, each
	// labelled with what it carries. Built with a CSS grid, not a picture, so
	// its text stays readable and wraps on a phone.
	let { actors, steps, caption }: { actors: Actor[]; steps: Step[]; caption?: string } = $props();
	let n = $derived(actors.length);
</script>

<figure class="seq" style="--n: {n}">
	<div class="grid" role="list" aria-label={caption}>
		{#each actors as a, i (i)}
			<div class="actor" style="grid-column: {i + 1}; grid-row: 1">
				<strong>{a.label}</strong>
				{#if a.sub}<span>{a.sub}</span>{/if}
			</div>
			<div class="lifeline" style="grid-column: {i + 1}; grid-row: 2 / span {steps.length}" aria-hidden="true"></div>
		{/each}
		{#each steps as s, k (k)}
			{#if 'note' in s}
				{@const span = Math.min(s.span ?? 1, n)}
				<div class="note" role="listitem" style="grid-column: {Math.min(s.at, n - span) + 1} / span {span}; grid-row: {k + 2}">
					<span class="num">{k + 1}</span>{s.note}
				</div>
			{:else}
				{@const lo = Math.min(s.from, s.to)}
				{@const hi = Math.max(s.from, s.to)}
				<div class="msg" role="listitem" class:back={s.to < s.from} class:dashed={s.dashed} style="grid-column: {lo + 1} / {hi + 2}; grid-row: {k + 2}; --k: {hi - lo + 1}">
					<span class="label"><span class="num">{k + 1}</span>{s.label}</span>
					<span class="line" aria-hidden="true"></span>
				</div>
			{/if}
		{/each}
	</div>
	{#if caption}<figcaption>{caption}</figcaption>{/if}
</figure>

<style>
	.seq {
		margin: 1.25rem 0 1.75rem;
		padding: 1rem 0.75rem 0.75rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(var(--n), minmax(0, 1fr));
		column-gap: 0;
		row-gap: 0.35rem;
		position: relative;
	}
	.actor {
		z-index: 1;
		margin: 0 0.25rem 0.5rem;
		padding: 0.45rem 0.4rem;
		border: 1px solid var(--event);
		border-radius: var(--radius-md);
		background: var(--bg-secondary);
		text-align: center;
		display: flex;
		flex-direction: column;
		justify-content: center;
		gap: 0.1rem;
	}
	.actor strong {
		font-size: 0.85rem;
		line-height: 1.2;
	}
	.actor span {
		font-size: 0.7rem;
		color: var(--text-muted);
		line-height: 1.25;
	}
	.lifeline {
		justify-self: center;
		width: 0;
		border-left: 2px dashed var(--glass-border);
		margin-top: -0.85rem;
	}
	.msg {
		z-index: 1;
		display: flex;
		flex-direction: column;
		justify-content: flex-end;
		gap: 0.2rem;
		padding: 0.3rem 0 0.25rem;
	}
	.label {
		padding: 0 0.35rem;
		font-size: 0.8rem;
		line-height: 1.35;
		color: var(--text-primary);
		text-align: center;
	}
	.line {
		position: relative;
		height: 2px;
		margin: 0 calc(100% / (2 * var(--k)));
		background: var(--event);
	}
	.dashed .line {
		background: repeating-linear-gradient(90deg, var(--event) 0 6px, transparent 6px 10px);
	}
	.line::after {
		content: '';
		position: absolute;
		top: -4px;
		right: -1px;
		border: 5px solid transparent;
		border-left: 8px solid var(--event);
		border-right: 0;
	}
	.back .line::after {
		right: auto;
		left: -1px;
		border-left: 0;
		border-right: 8px solid var(--event);
	}
	.note {
		z-index: 1;
		justify-self: center;
		width: calc(100% - 0.5rem);
		max-width: 22rem;
		padding: 0.4rem 0.55rem;
		border-left: 3px solid var(--event);
		border-radius: var(--radius-sm);
		background: var(--bg-secondary);
		font-size: 0.78rem;
		line-height: 1.4;
		color: var(--text-secondary);
	}
	.num {
		display: inline-grid;
		place-items: center;
		width: 1.15rem;
		height: 1.15rem;
		margin-right: 0.35rem;
		border-radius: 50%;
		background: var(--event-strong);
		color: white;
		font-size: 0.65rem;
		font-weight: 700;
		vertical-align: 0.1em;
	}
	figcaption {
		margin-top: 0.6rem;
		font-size: 0.78rem;
		color: var(--text-muted);
		text-align: center;
	}
	@media (max-width: 560px) {
		.seq {
			padding: 0.75rem 0.35rem 0.6rem;
		}
		.actor {
			margin: 0 0.15rem 0.5rem;
			padding: 0.35rem 0.2rem;
		}
		.actor strong {
			font-size: 0.72rem;
		}
		.actor span {
			font-size: 0.62rem;
		}
		.label {
			font-size: 0.72rem;
			padding: 0 0.15rem;
		}
		.note {
			font-size: 0.7rem;
		}
	}
</style>
