<script lang="ts">
	import favicon from '$lib/assets/favicon.svg';
	import '../app.css';
	import { page } from '$app/stores';
	import { fly } from 'svelte/transition';

	let { children } = $props();
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
</svelte:head>

<nav class="nav-bar">
	<div class="nav-content">
		<a href="/" class="nav-brand">Digital Mirror</a>
		<a href="/" class="nav-link">Dashboard</a>
	</div>
</nav>

{#key $page.url.pathname}
	<main class="page-content" in:fly={{ y: 8, duration: 200, delay: 100 }} out:fly={{ y: -8, duration: 150 }}>
		{@render children()}
	</main>
{/key}

<style>
	.nav-bar {
		position: sticky;
		top: 0;
		z-index: 100;
		background: var(--glass-bg);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		border-bottom: 1px solid var(--glass-border);
	}

	.nav-content {
		max-width: 1200px;
		margin: 0 auto;
		padding: 0.75rem 2rem;
		display: flex;
		align-items: center;
		gap: 2rem;
	}

	.nav-brand {
		font-size: 1.125rem;
		font-weight: 700;
		color: var(--text-primary);
		letter-spacing: -0.025em;
	}

	.nav-brand:hover {
		color: var(--accent);
	}

	.nav-link {
		font-size: 0.875rem;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.nav-link:hover {
		color: var(--text-primary);
	}

	.page-content {
		max-width: 1200px;
		margin: 0 auto;
		padding: 2rem;
	}
</style>
