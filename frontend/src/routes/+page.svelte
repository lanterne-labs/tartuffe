<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import type { PersonSummary } from '$lib/types';
	import { realtimeManager, subscribeToPersonEvents, clearOldEvents } from '$lib/realtime';
	import { API_BASE } from '$lib/config';

	let persons: PersonSummary[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);
	let currentPage = $state(0);
	let pageSize = 50;
	let hasMore = $state(true);
	let entityFilter: '' | 'party' | 'human' = $state('');
	let searchQuery = $state('');
	let debounceTimer: ReturnType<typeof setTimeout> | null = null;

	const SCORE_CIRCUMFERENCE = 2 * Math.PI * 14;

	function handleSearchInput(event: Event) {
		const target = event.target as HTMLInputElement;
		searchQuery = target.value;

		if (debounceTimer) {
			clearTimeout(debounceTimer);
		}
		debounceTimer = setTimeout(() => {
			loadPersons(0);
		}, 300);
	}

	async function loadPersons(page = 0) {
		error = null;
		try {
			loading = true;
			const offset = page * pageSize;
			const entityParam = entityFilter ? `&entity_type=${entityFilter}` : '';
			const searchParam = searchQuery ? `&search=${encodeURIComponent(searchQuery)}` : '';
			const response = await fetch(`${API_BASE}/api/persons?limit=${pageSize}&offset=${offset}${entityParam}${searchParam}`);

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

	function setFilter(filter: '' | 'party' | 'human') {
		entityFilter = filter;
		loadPersons(0);
	}

	async function refreshPersons() {
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
		if (score >= 0.8) return 'var(--green)';
		if (score >= 0.4) return 'var(--amber)';
		return 'var(--red)';
	}

	function handleRealtimeEvent(event: any) {
		refreshPersons();
	}

	let unsubscribe: (() => void) | null = null;

	onMount(() => {
		loadPersons();
		realtimeManager.connect();
		unsubscribe = subscribeToPersonEvents(-1, handleRealtimeEvent);
	});

	onDestroy(() => {
		if (unsubscribe) {
			unsubscribe();
		}
		if (debounceTimer) {
			clearTimeout(debounceTimer);
		}
		realtimeManager.disconnect();
		clearOldEvents();
	});
</script>

<svelte:head>
	<title>Digital Mirror - Dashboard</title>
</svelte:head>

<div class="dashboard">
	<div class="dashboard-header">
		<h1>Political Honesty Tracker</h1>
		<p class="subtitle">Persons ranked by popularity and honesty score</p>
	</div>

	{#if error}
		<div class="error-banner">
			<p>{error}</p>
			<button class="retry-btn" onclick={() => loadPersons(0)}>Retry</button>
		</div>
	{/if}

	<div class="controls">
		<div class="search-wrapper">
			<svg class="search-icon" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<circle cx="11" cy="11" r="8"></circle>
				<path d="m21 21-4.35-4.35"></path>
			</svg>
			<input
				type="text"
				placeholder="Search by name..."
				value={searchQuery}
				oninput={handleSearchInput}
				class="search-input"
			/>
		</div>

		<div class="filter-pills">
			<button
				class="pill"
				class:active={entityFilter === ''}
				onclick={() => setFilter('')}
			>All</button>
			<button
				class="pill"
				class:active={entityFilter === 'party'}
				onclick={() => setFilter('party')}
			>Parties</button>
			<button
				class="pill"
				class:active={entityFilter === 'human'}
				onclick={() => setFilter('human')}
			>Persons</button>
		</div>
	</div>

	{#if loading && persons.length === 0}
		<div class="persons-grid">
			{#each Array(6) as _}
				<div class="card skeleton-card">
					<div class="skeleton-header">
						<div class="skeleton skeleton-avatar"></div>
						<div class="skeleton-text">
							<div class="skeleton skeleton-name"></div>
							<div class="skeleton skeleton-sub"></div>
						</div>
						<div class="skeleton skeleton-ring"></div>
					</div>
				</div>
			{/each}
		</div>
	{:else}
		<div class="persons-grid">
			{#each persons as person (person.id)}
				<a href="/person/{person.id}" class="card">
					<div class="card-content">
						<div class="card-left">
							{#if person.image_url}
								<img src={person.image_url} alt={person.name} class="avatar" />
							{:else}
								<div class="avatar avatar-placeholder">
									{person.name.charAt(0).toUpperCase()}
								</div>
							{/if}
							<div class="card-info">
								<div class="card-name">
									{person.name}
									{#if person.entity_type === 'party'}
										<span class="badge">Party</span>
									{/if}
								</div>
								<div class="card-meta">
									{person.fact_count} fact{person.fact_count !== 1 ? 's' : ''}
									<span class="dot"></span>
									{getHonestyLabel(person.honesty_score)}
								</div>
							</div>
						</div>
						<div class="score-ring-wrapper">
							<svg class="score-ring" viewBox="0 0 36 36">
								<circle
									cx="18" cy="18" r="14"
									fill="none"
									stroke="var(--bg-secondary)"
									stroke-width="3"
								/>
								<circle
									cx="18" cy="18" r="14"
									fill="none"
									stroke={getHonestyColor(person.honesty_score)}
									stroke-width="3"
									stroke-linecap="round"
									stroke-dasharray="{person.honesty_score * SCORE_CIRCUMFERENCE} {SCORE_CIRCUMFERENCE}"
									transform="rotate(-90 18 18)"
								/>
								<text x="18" y="18" text-anchor="middle" dominant-baseline="central" class="ring-text">
									{(person.honesty_score * 100).toFixed(0)}
								</text>
							</svg>
						</div>
					</div>
				</a>
			{/each}
		</div>
	{/if}

	{#if loading && persons.length > 0}
		<div class="loading-indicator">
			<div class="spinner"></div>
		</div>
	{/if}

	{#if hasMore && !loading}
		<div class="load-more">
			<button class="load-more-btn" onclick={loadMore}>Load More</button>
		</div>
	{/if}
</div>

<style>
	.dashboard {
		max-width: 1200px;
		margin: 0 auto;
	}

	.dashboard-header {
		margin-bottom: 2rem;
	}

	.dashboard-header h1 {
		font-size: 1.75rem;
		font-weight: 700;
		letter-spacing: -0.025em;
		margin-bottom: 0.25rem;
	}

	.subtitle {
		color: var(--text-secondary);
		font-size: 0.875rem;
	}

	/* Error Banner */
	.error-banner {
		background: var(--error-bg);
		border: 1px solid var(--error-border);
		border-radius: var(--radius-md);
		padding: 1rem 1.25rem;
		margin-bottom: 1.5rem;
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}

	.error-banner p {
		color: var(--red);
		font-size: 0.875rem;
	}

	.retry-btn {
		background: var(--red);
		color: white;
		border: none;
		padding: 0.375rem 0.875rem;
		border-radius: var(--radius-sm);
		font-size: 0.813rem;
		font-weight: 500;
		cursor: pointer;
		transition: opacity var(--transition-fast);
	}

	.retry-btn:hover {
		opacity: 0.9;
	}

	/* Controls */
	.controls {
		display: flex;
		gap: 1rem;
		margin-bottom: 1.5rem;
		flex-wrap: wrap;
		align-items: center;
	}

	.search-wrapper {
		position: relative;
		flex: 1;
		min-width: 200px;
	}

	.search-icon {
		position: absolute;
		left: 0.75rem;
		top: 50%;
		transform: translateY(-50%);
		color: var(--text-muted);
		pointer-events: none;
	}

	.search-input {
		width: 100%;
		padding: 0.625rem 0.75rem 0.625rem 2.25rem;
		font-size: 0.875rem;
		font-family: var(--font-family);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		background: var(--bg-secondary);
		color: var(--text-primary);
		transition: border-color var(--transition-fast), box-shadow var(--transition-fast);
	}

	.search-input:focus {
		outline: none;
		border-color: var(--accent);
		box-shadow: 0 0 0 3px var(--accent-glow);
	}

	.search-input::placeholder {
		color: var(--text-muted);
	}

	.filter-pills {
		display: flex;
		gap: 0.375rem;
	}

	.pill {
		background: var(--bg-secondary);
		color: var(--text-secondary);
		border: 1px solid var(--border-card);
		padding: 0.5rem 1rem;
		border-radius: var(--radius-full);
		font-size: 0.813rem;
		font-weight: 500;
		font-family: var(--font-family);
		cursor: pointer;
		transition: all var(--transition-fast);
	}

	.pill:hover {
		color: var(--text-primary);
		border-color: var(--border-card-hover);
	}

	.pill.active {
		background: var(--accent);
		color: white;
		border-color: var(--accent);
		box-shadow: 0 0 12px var(--accent-glow);
	}

	/* Grid */
	.persons-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
		gap: 1rem;
	}

	/* Card */
	.card {
		background: var(--bg-card);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		padding: 1.25rem;
		box-shadow: var(--shadow-card);
		transition: transform var(--transition-normal), box-shadow var(--transition-normal), border-color var(--transition-normal);
		display: block;
		color: inherit;
		text-decoration: none;
	}

	.card:hover {
		transform: translateY(-2px);
		box-shadow: var(--shadow-card-hover);
		border-color: var(--border-card-hover);
	}

	.card-content {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
	}

	.card-left {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex: 1;
		min-width: 0;
	}

	.avatar {
		width: 44px;
		height: 44px;
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
	}

	.avatar-placeholder {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-weight: 600;
		font-size: 1.125rem;
		border: 1px solid var(--border-card);
	}

	.card-info {
		min-width: 0;
	}

	.card-name {
		font-weight: 600;
		font-size: 0.938rem;
		color: var(--text-primary);
		text-transform: capitalize;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.badge {
		background: rgba(59, 130, 246, 0.15);
		color: var(--accent);
		font-size: 0.688rem;
		font-weight: 500;
		padding: 0.125rem 0.5rem;
		border-radius: var(--radius-full);
		flex-shrink: 0;
	}

	.card-meta {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-top: 0.125rem;
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.dot {
		width: 3px;
		height: 3px;
		border-radius: 50%;
		background: var(--text-muted);
	}

	/* Score Ring */
	.score-ring-wrapper {
		flex-shrink: 0;
	}

	.score-ring {
		width: 44px;
		height: 44px;
	}

	.ring-text {
		font-size: 9px;
		font-weight: 600;
		fill: var(--text-primary);
	}

	/* Skeleton */
	.skeleton-card {
		padding: 1.25rem;
	}

	.skeleton-header {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}

	.skeleton-avatar {
		width: 44px;
		height: 44px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.skeleton-text {
		flex: 1;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.skeleton-name {
		height: 14px;
		width: 60%;
	}

	.skeleton-sub {
		height: 10px;
		width: 40%;
	}

	.skeleton-ring {
		width: 44px;
		height: 44px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	/* Loading */
	.loading-indicator {
		display: flex;
		justify-content: center;
		padding: 2rem;
	}

	.spinner {
		width: 24px;
		height: 24px;
		border: 2px solid var(--border-card);
		border-top-color: var(--accent);
		border-radius: 50%;
		animation: spin 0.6s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	/* Load More */
	.load-more {
		text-align: center;
		padding: 2rem;
	}

	.load-more-btn {
		background: transparent;
		color: var(--accent);
		border: 1px solid var(--accent);
		padding: 0.625rem 1.5rem;
		border-radius: var(--radius-full);
		font-size: 0.875rem;
		font-weight: 500;
		font-family: var(--font-family);
		cursor: pointer;
		transition: all var(--transition-fast);
	}

	.load-more-btn:hover {
		background: var(--accent);
		color: white;
		box-shadow: var(--shadow-glow);
	}
</style>
