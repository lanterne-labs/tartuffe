<script lang="ts">
	// The pipeline the audit covers (voters, relays, server, public log,
	// results), each part lighting up in turn, looping: what the Verify
	// page does for real. Without motion every part shows checked.
	import type { Lang } from '$lib/i18n';

	let { lang }: { lang: Lang } = $props();
	const NAMES = { fr: ['Électeurs', 'Relais', 'Serveur', 'Registre', 'Résultats'], en: ['Voters', 'Relays', 'Server', 'Log', 'Results'] };
	const ICONS = [
		'M12 11.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM5 20c0-3.6 3.1-6 7-6s7 2.4 7 6',
		'M12 11v9M8.5 7.5a5 5 0 0 1 7 0M5.5 4.5a9 9 0 0 1 13 0M12 11.5a1 1 0 1 0 0-2 1 1 0 0 0 0 2z',
		'M5 4h14v7H5zM5 13h14v7H5zM8 7.5h.01M8 16.5h.01',
		'M6 4h10a3 3 0 0 1 3 3v13H9a3 3 0 0 1-3-3zM6 17a3 3 0 0 1 3-3h10M10 8h5',
		'M6 20v-7M12 20V5M18 20v-10'
	];
</script>

<div class="pipe" aria-hidden="true">
	{#each NAMES[lang] as name, i (i)}
		{#if i > 0}<span class="link" style="--i: {i - 0.5}"></span>{/if}
		<span class="node" style="--i: {i}">
			<span class="disc">
				<svg viewBox="0 0 24 24"><path d={ICONS[i]} /></svg>
				<span class="tick">✓</span>
			</span>
			<span class="name">{name}</span>
		</span>
	{/each}
</div>

<style>
	.pipe {
		display: flex;
		align-items: flex-start;
		justify-content: center;
		margin: 0 auto;
	}
	.node {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.35rem;
		width: 3.9rem;
		flex-shrink: 0;
	}
	.disc {
		position: relative;
		width: 2.9rem;
		height: 2.9rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: color-mix(in oklch, var(--green) 22%, var(--bg-card));
		border: 1.5px solid var(--green);
		color: var(--green);
	}
	.disc svg {
		width: 1.35rem;
		height: 1.35rem;
		fill: none;
		stroke: currentColor;
		stroke-width: 1.8;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.tick {
		position: absolute;
		right: -0.25rem;
		bottom: -0.25rem;
		width: 1.1rem;
		height: 1.1rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: var(--green);
		color: #0a0a0f;
		font-size: 0.65rem;
		font-weight: 900;
	}
	.name {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-secondary);
	}
	.link {
		flex: 1;
		min-width: 0.8rem;
		max-width: 3rem;
		height: 2px;
		margin-top: 1.45rem;
		background: var(--green);
	}
	/* A narrow phone: the five parts fit the width. */
	@media (max-width: 400px) {
		.node {
			width: 3.2rem;
		}
		.disc {
			width: 2.5rem;
			height: 2.5rem;
		}
		.link {
			min-width: 0.4rem;
			margin-top: 1.25rem;
		}
	}
	/* Each part turns green in turn (0.7 s apart), holds, and the loop starts again. */
	@media (prefers-reduced-motion: no-preference) {
		.disc,
		.link,
		.tick {
			animation: light 6s infinite;
			animation-delay: calc(var(--i) * 0.7s);
			animation-fill-mode: backwards;
		}
		.link {
			animation-name: light-link;
		}
		.tick {
			animation-name: light-tick;
		}
	}
	@keyframes light {
		0% {
			background: var(--bg-card);
			border-color: var(--glass-border);
			color: var(--text-muted);
		}
		8%,
		80% {
			background: color-mix(in oklch, var(--green) 22%, var(--bg-card));
			border-color: var(--green);
			color: var(--green);
		}
		100% {
			background: var(--bg-card);
			border-color: var(--glass-border);
			color: var(--text-muted);
		}
	}
	@keyframes light-link {
		0% {
			background: var(--glass-border);
		}
		8%,
		80% {
			background: var(--green);
		}
		100% {
			background: var(--glass-border);
		}
	}
	@keyframes light-tick {
		0%,
		4% {
			transform: scale(0);
		}
		10%,
		80% {
			transform: scale(1);
		}
		100% {
			transform: scale(0);
		}
	}
</style>
