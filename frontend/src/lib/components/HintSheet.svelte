<script lang="ts">
	// A hint on a touch screen: the text of a "?" or a mark, which a desktop
	// shows as a bubble beside it, opens in a box in the middle of the
	// screen over a dimmed page, and any tap closes it. The box is moved to
	// the body: a card with a filter or a transform would trap a fixed box
	// inside itself.
	import type { Snippet } from 'svelte';

	interface Props {
		open: boolean;
		onclose: () => void;
		children: Snippet;
	}
	let { open, onclose, children }: Props = $props();

	function portal(node: HTMLElement) {
		document.body.appendChild(node);
		return {
			destroy() {
				node.remove();
			}
		};
	}
	// A tap anywhere closes, and never reaches the card the hint sits in; a
	// link in the hint still goes where it points.
	function tap(e: MouseEvent) {
		e.stopPropagation();
		if (!(e.target as Element | null)?.closest('a')) e.preventDefault();
		onclose();
	}
</script>

{#if open}
	<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_static_element_interactions -->
	<div class="sheet" use:portal onclick={tap}>
		<div class="box" role="note">
			{@render children()}
		</div>
	</div>
{/if}

<style>
	.sheet {
		position: fixed;
		inset: 0;
		z-index: 1000;
		display: grid;
		place-items: center;
		padding: 1.5rem;
		background: rgba(0, 0, 0, 0.55);
		backdrop-filter: blur(2px);
		-webkit-backdrop-filter: blur(2px);
		animation: sheet-in 0.15s ease-out;
	}
	.box {
		width: min(100%, 22rem);
		padding: 1rem 1.1rem;
		border-radius: var(--radius-lg, 14px);
		background: var(--bg-elevated, #1c1a22);
		border: 1px solid var(--glass-border);
		box-shadow: 0 16px 48px rgba(0, 0, 0, 0.6);
		color: var(--text-primary);
		font-size: 0.95rem;
		line-height: 1.5;
		text-align: center;
	}
	.box :global(a) {
		display: block;
		margin-top: 0.6rem;
		font-weight: 600;
		color: var(--accent-hover, var(--accent));
	}
	@keyframes sheet-in {
		from {
			opacity: 0;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.sheet {
			animation: none;
		}
	}
</style>
