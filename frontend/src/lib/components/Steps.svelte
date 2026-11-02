<script lang="ts">
	// The steps of an operation, each with its state: done (✓), running (the
	// wheel), failed (!), or still to come. A row for a sequence the user
	// goes through, a list for a set of checks.
	import Spinner from './Spinner.svelte';

	export type StepState = 'todo' | 'running' | 'ok' | 'failed';

	let { steps, layout = 'list' }: { steps: { label: string; state: StepState }[]; layout?: 'row' | 'list' } = $props();
</script>

<ol class="steps steps--{layout}">
	{#each steps as s, i (i)}
		<li class="step step--{s.state}">
			<span class="icon" aria-hidden="true">
				{#if s.state === 'running'}
					<Spinner size="sm" />
				{:else if s.state === 'ok'}
					<svg viewBox="0 0 24 24"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>
				{:else if s.state === 'failed'}
					!
				{:else}
					{layout === 'row' ? i + 1 : ''}
				{/if}
			</span>
			<span class="label">{s.label}</span>
		</li>
	{/each}
</ol>

<style>
	.steps {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.steps--list {
		display: grid;
		gap: 0.4rem;
	}
	.steps--row {
		display: grid;
		grid-auto-flow: column;
		grid-auto-columns: minmax(0, 1fr);
		gap: 0.25rem;
	}
	.step {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.85rem;
		color: var(--text-muted);
	}
	.steps--row .step {
		position: relative;
		flex-direction: column;
		gap: 0.35rem;
		text-align: center;
		font-size: 0.75rem;
		line-height: 1.25;
	}
	/* The line between two steps of a row. */
	.steps--row .step:not(:last-child)::after {
		content: '';
		position: absolute;
		top: 0.7rem;
		left: calc(50% + 1rem);
		right: calc(-50% + 1rem);
		height: 2px;
		background: var(--glass-border);
	}
	.steps--row .step--ok:not(:last-child)::after {
		background: var(--green);
	}
	.icon {
		width: 1.4rem;
		height: 1.4rem;
		flex-shrink: 0;
		display: grid;
		place-items: center;
		border-radius: 50%;
		border: 1.5px solid var(--glass-border);
		font-size: 0.72rem;
		font-weight: 700;
	}
	.step--running,
	.step--ok {
		color: var(--text-primary);
	}
	.step--running .icon {
		border: none;
	}
	.step--ok .icon {
		border: none;
		background: var(--green);
	}
	.step--ok svg {
		width: 0.85rem;
		height: 0.85rem;
		fill: none;
		stroke: white;
		stroke-width: 3;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.step--failed {
		color: var(--red);
	}
	.step--failed .icon {
		border: none;
		background: var(--red);
		color: white;
	}
</style>
