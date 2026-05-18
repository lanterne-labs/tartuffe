<script lang="ts">
	import { page } from '$app/stores';
</script>

<svelte:head>
	<title>Error {$page.status} — tartuffe.me</title>
</svelte:head>

<div class="error-page">
	<div class="error-code">{$page.status}</div>
	<p class="error-message">{$page.error?.message || 'Something went wrong'}</p>
	<a href="/" class="back-btn">Back to Dashboard</a>
</div>

<style>
	.error-page {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		min-height: 60vh;
		text-align: center;
	}

	.error-code {
		font-size: 8rem;
		font-weight: 700;
		color: var(--text-muted);
		opacity: 0.3;
		line-height: 1;
		margin-bottom: 1rem;
	}

	.error-message {
		font-size: 1.25rem;
		color: var(--text-secondary);
		margin-bottom: 2rem;
	}

	.back-btn {
		display: inline-block;
		padding: 0.75rem 1.5rem;
		background: var(--accent);
		color: white;
		border-radius: var(--radius-full);
		font-weight: 500;
		transition: background var(--transition-fast), box-shadow var(--transition-fast);
	}

	.back-btn:hover {
		background: var(--accent-hover);
		box-shadow: var(--shadow-glow);
		color: white;
	}
</style>
