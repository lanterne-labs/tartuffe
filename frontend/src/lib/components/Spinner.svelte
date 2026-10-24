<script lang="ts">
	// The wheel of anything that waits. label is read by screen readers.
	let { label = '', size = 'md' }: { label?: string; size?: 'sm' | 'md' | 'lg' } = $props();
</script>

<span class="spinner spinner--{size}" role="status" aria-label={label || undefined}></span>

<style>
	.spinner {
		display: inline-block;
		flex-shrink: 0;
		border-radius: 50%;
		border: 2.5px solid color-mix(in oklch, var(--text-muted) 35%, transparent);
		border-top-color: var(--accent-hover);
		animation: spin 0.8s linear infinite;
		vertical-align: middle;
	}
	.spinner--sm {
		width: 1.1rem;
		height: 1.1rem;
		border-width: 2px;
	}
	.spinner--md {
		width: 1.6rem;
		height: 1.6rem;
	}
	.spinner--lg {
		width: 2.6rem;
		height: 2.6rem;
		border-width: 3px;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.spinner {
			animation-duration: 2.4s;
		}
	}
</style>
