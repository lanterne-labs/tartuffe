<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { PersonSummary } from '$lib/types';
	import { realtimeManager, subscribeToPersonEvents, clearOldEvents } from '$lib/realtime';
	import { API_BASE } from '$lib/config';

	let persons: PersonSummary[] = [];
	let loading = true;
	let error: string | null = null;
	let currentPage = 0;
	let pageSize = 50;
	let hasMore = true;

	async function loadPersons(page = 0) {
		error = null;
		try {
			loading = true;
			const offset = page * pageSize;
			const response = await fetch(`${API_BASE}/api/persons?limit=${pageSize}&offset=${offset}`);

			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}

			const data = await response.json();
			persons = page === 0 ? data : [...persons, ...data];
			hasMore = data.length === pageSize;
			currentPage = page;
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		} finally {
			loading = false;
		}
	}

	async function refreshPersons() {
		// Reload first page to get updated rankings
		await loadPersons(0);
	}

	function loadMore() {
		loadPersons(currentPage + 1);
	}

	function getHonestyLabel(score: number): string {
		if (score >= 0.8) return 'Honest';
		if (score >= 0.4) return 'Mixed';
		return 'Dishonest';
	}

	function getHonestyColor(score: number): string {
		if (score >= 0.8) return '#22c55e'; // green
		if (score >= 0.4) return '#f59e0b'; // amber
		return '#ef4444'; // red
	}

	function handleRealtimeEvent(event: any) {
		// When any person's score changes, refresh the entire list
		// since rankings might have changed
		console.log('Person updated via realtime:', event);
		refreshPersons();
	}

	let unsubscribe: (() => void) | null = null;

	onMount(() => {
		loadPersons();

		// Connect to realtime updates
		realtimeManager.connect();

		// Listen for any person updates (we refresh the whole list since rankings change)
		unsubscribe = subscribeToPersonEvents(-1, handleRealtimeEvent); // -1 means listen to all persons
	});

	onDestroy(() => {
		if (unsubscribe) {
			unsubscribe();
		}
		realtimeManager.disconnect();
		clearOldEvents();
	});
</script>

<svelte:head>
	<title>Digital Mirror - Persons</title>
</svelte:head>

<div class="container">
	<h1>Digital Mirror</h1>
	<p>Persons ranked by honesty score</p>

	{#if error}
		<div class="error">
			<p>Error: {error}</p>
			<button on:click={() => loadPersons(0)}>Retry</button>
		</div>
	{/if}

	<div class="persons-list">
		{#each persons as person (person.id)}
			<div class="person-card">
				<div class="person-header">
					<h2>
						<a href="/person/{person.id}">{person.name}</a>
						{#if person.entity_type === 'party'}
							<span class="party-badge">Party</span>
						{/if}
					</h2>
				</div>

				<div class="honesty-score">
					<div class="score-bar">
						<div
							class="score-fill"
							style="width: {person.honesty_score * 100}%; background-color: {getHonestyColor(person.honesty_score)}"
						></div>
					</div>
					<div class="score-info">
						<span class="score-label">{getHonestyLabel(person.honesty_score)}</span>
						<span class="score-value">{(person.honesty_score * 100).toFixed(1)}%</span>
					</div>
				</div>
			</div>
		{/each}
	</div>

	{#if loading}
		<div class="loading">
			<p>Loading...</p>
		</div>
	{/if}

	{#if hasMore && !loading}
		<div class="load-more">
			<button on:click={loadMore}>Load More</button>
		</div>
	{/if}
</div>

<style>
	.container {
		max-width: 800px;
		margin: 0 auto;
		padding: 2rem;
		font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
	}

	h1 {
		color: #1f2937;
		margin-bottom: 0.5rem;
	}

	p {
		color: #6b7280;
		margin-bottom: 2rem;
	}

	.error {
		background: #fee2e2;
		border: 1px solid #fecaca;
		border-radius: 0.5rem;
		padding: 1rem;
		margin-bottom: 2rem;
	}

	.error p {
		margin: 0 0 1rem 0;
		color: #dc2626;
	}

	.error button {
		background: #dc2626;
		color: white;
		border: none;
		padding: 0.5rem 1rem;
		border-radius: 0.25rem;
		cursor: pointer;
	}

	.persons-list {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.person-card {
		background: white;
		border: 1px solid #e5e7eb;
		border-radius: 0.5rem;
		padding: 1.5rem;
		box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
		transition: box-shadow 0.2s;
	}

	.person-card:hover {
		box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
	}

	.person-header {
		display: flex;
		align-items: center;
		margin-bottom: 1rem;
	}

	.person-header h2 {
		margin: 0;
		font-size: 1.25rem;
		font-weight: 600;
		color: #1f2937;
		flex: 1;
	}

	.person-header h2 a {
		color: inherit;
		text-decoration: none;
	}

	.person-header h2 a:hover {
		color: #3b82f6;
	}

	.party-badge {
		background: #dbeafe;
		color: #1e40af;
		font-size: 0.75rem;
		font-weight: 500;
		padding: 0.25rem 0.5rem;
		border-radius: 0.25rem;
		margin-left: 0.5rem;
	}

	.honesty-score {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.score-bar {
		height: 8px;
		background: #e5e7eb;
		border-radius: 4px;
		overflow: hidden;
	}

	.score-fill {
		height: 100%;
		transition: width 0.3s ease;
	}

	.score-info {
		display: flex;
		justify-content: space-between;
		align-items: center;
		font-size: 0.875rem;
	}

	.score-label {
		font-weight: 500;
		color: #374151;
	}

	.score-value {
		font-weight: 600;
		color: #1f2937;
	}

	.loading, .load-more {
		text-align: center;
		padding: 2rem;
	}

	.load-more button {
		background: #3b82f6;
		color: white;
		border: none;
		padding: 0.75rem 1.5rem;
		border-radius: 0.375rem;
		font-weight: 500;
		cursor: pointer;
		transition: background-color 0.2s;
	}

	.load-more button:hover {
		background: #2563eb;
	}
</style>
