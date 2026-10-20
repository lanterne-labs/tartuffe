<script lang="ts">
	// A ring filled to `value` (0–1) with `text` in the middle. With value null
	// the ring stays empty (a hidden result).
	interface Props {
		value: number | null;
		text: string;
		color: string;
		size?: number;
		caption?: string;
		sub?: string;
	}

	let { value, text, color, size = 88, caption = '', sub = '' }: Props = $props();

	const RADIUS = 15.5;
	const CIRCUMFERENCE = 2 * Math.PI * RADIUS;
	let filled = $derived(value === null ? 0 : Math.max(0, Math.min(1, value)) * CIRCUMFERENCE);
</script>

<div class="ring">
	<svg viewBox="0 0 36 36" style="width: {size}px; height: {size}px;" role="img" aria-label="{caption} {text}">
		<circle cx="18" cy="18" r={RADIUS} fill="none" stroke="var(--bg-secondary)" stroke-width="3" />
		{#if filled > 0}
		<circle
			cx="18" cy="18" r={RADIUS}
			fill="none"
			stroke={color}
			stroke-width="3"
			stroke-linecap="round"
			stroke-dasharray="{filled} {CIRCUMFERENCE}"
			transform="rotate(-90 18 18)"
			class="arc"
		/>
		{/if}
		<text x="18" y="18" text-anchor="middle" dominant-baseline="central" class="ring-text">{text}</text>
	</svg>
	{#if caption}<span class="ring-caption">{caption}</span>{/if}
	{#if sub}<span class="ring-sub">{sub}</span>{/if}
</div>

<style>
	.ring {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.2rem;
		min-width: 0;
	}
	.arc {
		transition: stroke-dasharray var(--transition-slow), stroke var(--transition-slow);
	}
	.ring-text {
		font-size: 8px;
		font-weight: 700;
		fill: var(--text-primary);
	}
	.ring-caption {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-secondary);
		text-align: center;
	}
	.ring-sub {
		font-size: 0.6875rem;
		color: var(--text-muted);
		text-align: center;
	}
</style>
