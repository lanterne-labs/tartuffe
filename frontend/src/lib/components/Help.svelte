<script lang="ts">
	// A small "?" beside a word the reader may not know: a click opens its
	// explanation in a bubble under it (a mouse also opens it on hover), a
	// touch screen in a box in the middle of the screen (HintSheet). Escape,
	// a click elsewhere or a second click closes it.
	import HintSheet from './HintSheet.svelte';

	interface Props {
		/** The explanation. */
		text: string;
		/** What the button is, for a screen reader ("What is a stance?"). */
		label: string;
		/** The side the bubble opens towards, so it stays on the screen. */
		align?: 'start' | 'end';
	}
	let { text, label, align = 'start' }: Props = $props();

	const uid = $props.id();
	let open = $state(false);
	let touch = $state(false);
	let root: HTMLElement | undefined = $state();
	$effect(() => {
		touch = window.matchMedia('(hover: none)').matches;
	});
	function onWindowClick(e: MouseEvent) {
		if (open && root && !root.contains(e.target as Node)) open = false;
	}
	function onKey(e: KeyboardEvent) {
		if (e.key === 'Escape') open = false;
	}
	// The "?" may sit in a card that is itself a button: the click stays here.
	function toggle(e: MouseEvent) {
		e.stopPropagation();
		open = !open;
	}
</script>

<svelte:window onclick={onWindowClick} onkeydown={onKey} />

<span class="help" class:open bind:this={root}>
	<button type="button" class="q" onclick={toggle} aria-expanded={open} aria-controls="{uid}-tip" aria-label={label}>?</button>
	{#if !touch}
		<span class="tip tip--{align}" id="{uid}-tip" role="tooltip">{text}</span>
	{/if}
</span>
{#if touch}
	<HintSheet {open} onclose={() => (open = false)}>{text}</HintSheet>
{/if}

<style>
	.help {
		position: relative;
		display: inline-flex;
		vertical-align: middle;
	}
	.q {
		display: inline-grid;
		place-items: center;
		width: 1.15rem;
		height: 1.15rem;
		padding: 0;
		border: 1px solid var(--border-card-hover);
		border-radius: 50%;
		background: rgba(255, 255, 255, 0.05);
		font: inherit;
		font-size: 0.68rem;
		font-weight: 700;
		line-height: 1;
		letter-spacing: 0;
		color: var(--text-muted);
		cursor: pointer;
		transition: color 0.15s, border-color 0.15s, background 0.15s;
	}
	.q:hover,
	.open .q {
		color: var(--text-primary);
		border-color: var(--text-secondary);
		background: rgba(255, 255, 255, 0.1);
	}
	/* A finger needs more than the dot: the tap area grows around it. */
	@media (pointer: coarse) {
		.q::after {
			content: '';
			position: absolute;
			inset: -0.7rem;
		}
	}
	.tip {
		position: absolute;
		top: calc(100% + 0.5rem);
		z-index: 40;
		width: max-content;
		max-width: min(20rem, 80vw);
		padding: 0.7rem 0.85rem;
		border-radius: 0.8rem;
		border: 1px solid var(--border-card-hover);
		background: #241c2b;
		box-shadow: 0 20px 40px -12px rgba(0, 0, 0, 0.7);
		font-family: var(--font-family);
		font-size: 0.84rem;
		font-weight: 400;
		line-height: 1.45;
		letter-spacing: 0;
		text-align: left;
		text-transform: none;
		white-space: normal;
		color: var(--text-secondary);
		opacity: 0;
		visibility: hidden;
		transform: translateY(-4px);
		transition:
			opacity 0.15s,
			transform 0.15s,
			visibility 0.15s;
	}
	.tip--start {
		left: -0.5rem;
	}
	.tip--end {
		right: -0.5rem;
	}
	.open .tip,
	.help:hover .tip {
		opacity: 1;
		visibility: visible;
		transform: none;
	}
</style>
