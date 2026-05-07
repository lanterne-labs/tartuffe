<script lang="ts">
	interface Props {
		score: number;
		size?: number;
		showLabel?: boolean;
		label?: string;
	}

	let { score, size = 56, showLabel = false, label = '' }: Props = $props();

	const RADIUS = 14;
	const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

	function color(s: number): string {
		if (s >= 0.8) return 'var(--green)';
		if (s >= 0.4) return 'var(--amber)';
		return 'var(--red)';
	}

	let textSize = $derived(Math.max(7, Math.round(size * 0.18)));
</script>

<div class="gauge-wrap">
	<svg viewBox="0 0 36 36" style="width: {size}px; height: {size}px;">
		<circle cx="18" cy="18" r={RADIUS} fill="none" stroke="var(--bg-secondary)" stroke-width="2.5" />
		<circle
			cx="18" cy="18" r={RADIUS}
			fill="none"
			stroke={color(score)}
			stroke-width="2.5"
			stroke-linecap="round"
			stroke-dasharray="{score * CIRCUMFERENCE} {CIRCUMFERENCE}"
			transform="rotate(-90 18 18)"
		/>
		<text x="18" y="18" text-anchor="middle" dominant-baseline="central" style="font-size: {textSize}px;" class="gauge-text">
			{Math.round(score * 100)}
		</text>
	</svg>
	{#if showLabel && label}
		<span class="gauge-label">{label}</span>
	{/if}
</div>

<style>
	.gauge-wrap {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.25rem;
	}

	.gauge-text {
		font-weight: 700;
		fill: var(--text-primary);
	}

	.gauge-label {
		font-size: 0.625rem;
		color: var(--text-muted);
		font-weight: 500;
	}
</style>
