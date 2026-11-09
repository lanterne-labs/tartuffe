<script lang="ts">
	// The mark of the site, the domino mask: in the nav and, large, as the
	// emblem of the landing page.
	interface Props {
		size?: 'nav' | 'hero';
	}
	let { size = 'nav' }: Props = $props();
</script>

<svg class="brand-mark {size}" viewBox="0 0 64 40" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
	<path d="M4 12 C8 4, 16 2, 22 6 C26 8, 28 10, 32 12 C36 10, 38 8, 42 6 C48 2, 56 4, 60 12 C62 20, 58 28, 50 32 C44 34, 38 32, 34 26 C33 24, 31 24, 30 26 C26 32, 20 34, 14 32 C6 28, 2 20, 4 12 Z" />
	<ellipse cx="18" cy="17" rx="5" ry="2.5" />
	<ellipse cx="46" cy="17" rx="5" ry="2.5" />
</svg>

<style>
	.brand-mark { display: block; flex-shrink: 0; }
	.nav { width: 38px; height: 24px; }
	.hero { width: 190px; height: 120px; margin: 0.5rem auto 0.75rem; color: var(--text-primary); filter: drop-shadow(0 0 28px var(--accent-glow)); }
	@media (max-width: 860px) {
		.nav { width: 30px; height: 19px; }
	}
</style>
