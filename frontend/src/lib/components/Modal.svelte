<script lang="ts">
	// A modal dialog: open shows it, Escape, the × or a click on the backdrop
	// call onclose. The body and the actions are snippets of the caller, so
	// they keep the caller's styles.
	import type { Snippet } from 'svelte';

	interface Props {
		open: boolean;
		title: string;
		onclose: () => void;
		closeLabel: string;
		children: Snippet;
		actions?: Snippet;
	}

	let { open, title, onclose, closeLabel, children, actions }: Props = $props();

	let dialog: HTMLDialogElement | undefined = $state();
	const titleId = `modal-${Math.random().toString(36).slice(2)}`;

	$effect(() => {
		if (!dialog) return;
		if (open && !dialog.open) dialog.showModal();
		else if (!open && dialog.open) dialog.close();
	});

	function onBackdrop(e: MouseEvent) {
		if (e.target === dialog) onclose();
	}
</script>

<dialog bind:this={dialog} class="modal" aria-labelledby={titleId} onclose={onclose} onclick={onBackdrop}>
	<div class="modal-body">
		<header>
			<h2 id={titleId}>{title}</h2>
			<button class="close" onclick={onclose} aria-label={closeLabel}>×</button>
		</header>
		{@render children()}
		{#if actions}
			<footer>{@render actions()}</footer>
		{/if}
	</div>
</dialog>

<style>
	.modal {
		margin: auto;
		/* dvh: the height left under the browser's bars on a phone. */
		max-height: calc(100vh - 2rem);
		max-height: calc(100dvh - 2rem);
		overflow-y: auto;
		width: min(480px, calc(100vw - 2rem));
		padding: 0;
		background: var(--bg-secondary);
		color: var(--text-primary);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-card-hover);
	}
	.modal::backdrop {
		background: rgba(0, 0, 0, 0.6);
		backdrop-filter: blur(3px);
	}
	.modal-body {
		padding: 1.25rem 1.5rem 1.5rem;
		text-align: center;
	}
	/* The title centred, the close in the corner. */
	header {
		position: relative;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 0 1.75rem;
		margin-bottom: 0.75rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.5rem;
		font-weight: 600;
		margin: 0;
	}
	/* A 44px square under the finger; pulled out so the cross stays in the corner. */
	.close {
		position: absolute;
		right: -0.75rem;
		top: 50%;
		transform: translateY(-50%);
		display: grid;
		place-items: center;
		width: 2.75rem;
		height: 2.75rem;
		padding: 0;
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 1.5rem;
		cursor: pointer;
		line-height: 1;
	}
	.close:hover {
		color: var(--text-primary);
	}
	footer {
		display: flex;
		justify-content: center;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin-top: 1.25rem;
	}
</style>
