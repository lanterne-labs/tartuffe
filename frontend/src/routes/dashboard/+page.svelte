<script lang="ts">
		import TartuffeScore from '$lib/components/TartuffeScore.svelte';
	import ScoreCompare from '$lib/components/ScoreCompare.svelte';
	import Podium from '$lib/components/Podium.svelte';
	import { onMount, onDestroy } from 'svelte';
	import type { PersonSummary } from '$lib/types';
	import { realtimeManager, subscribeToPersonEvents, clearOldEvents } from '$lib/realtime';
	import { API_BASE } from '$lib/config';
	import { personPath } from '$lib/share';
	import { lang, tr } from '$lib/i18n';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	let persons: PersonSummary[] = $state([]);
	// The podium is its own component (Podium.svelte); the lineup needs the candidates.
	let podium: Podium | undefined = $state();
	let candidates: PersonSummary[] = $state([]);
	let panelsLoading = $state(true);
	let loading = $state(true);
	let error: string | null = $state(null);
	let currentPage = $state(0);
	let pageSize = 50;
	let hasMore = $state(true);
	// The others: their facts are collected, nothing is judged; no score.
	let others: PersonSummary[] = $state([]);
	let othersLoading = $state(true);
	let othersPage = $state(0);
	let othersHasMore = $state(true);
	const OTHERS_PAGE = 24;
	let entityFilter: '' | 'party' | 'human' = $state('');
	let searchQuery = $state('');
	let debounceTimer: ReturnType<typeof setTimeout> | null = null;


	function handleSearchInput(event: Event) {
		const target = event.target as HTMLInputElement;
		searchQuery = target.value;

		if (debounceTimer) {
			clearTimeout(debounceTimer);
		}
		debounceTimer = setTimeout(() => {
			loadPersons(0);
			loadOthers(0);
			loadPanels();
		}, 300);
	}

	function buildQuery(extra: Record<string, string | number>): string {
		const params = new URLSearchParams();
		if (entityFilter) params.set('entity_type', entityFilter);
		if (searchQuery) params.set('search', searchQuery);
		for (const [k, v] of Object.entries(extra)) {
			params.set(k, String(v));
		}
		return params.toString();
	}

	async function loadPersons(page = 0) {
		error = null;
		try {
			loading = true;
			const offset = page * pageSize;
			const qs = buildQuery({ limit: pageSize, offset, candidate: 'true' });
			const response = await fetch(`${API_BASE}/api/persons?${qs}`);

			if (!response.ok) {
				throw new Error(tr('error.generic', currentLang));
			}

			const data = await response.json();
			persons = page === 0 ? data : [...persons, ...data];
			hasMore = data.length === pageSize;
			currentPage = page;
		} catch (err) {
			console.error('failed to load the candidates', err);
			error = tr('error.generic', currentLang);
		} finally {
			loading = false;
		}
	}

	async function loadOthers(page = 0) {
		try {
			othersLoading = true;
			const qs = buildQuery({ limit: OTHERS_PAGE, offset: page * OTHERS_PAGE, candidate: 'false' });
			const response = await fetch(`${API_BASE}/api/persons?${qs}`);
			if (!response.ok) throw new Error(`HTTP ${response.status}`);
			const data: PersonSummary[] = (await response.json()) ?? [];
			others = page === 0 ? data : [...others, ...data];
			othersHasMore = data.length === OTHERS_PAGE;
			othersPage = page;
		} catch (err) {
			// The candidates' error banner offers the retry.
			console.error('failed to load the other figures', err);
		} finally {
			othersLoading = false;
		}
	}

	async function loadPanels() {
		try {
			panelsLoading = true;
			const [candRes] = await Promise.all([fetch(`${API_BASE}/api/persons?candidate=true&limit=100`), podium?.load()]);
			if (candRes.ok) candidates = (await candRes.json()) ?? [];
		} catch (err) {
			// Non-fatal: the candidates' error banner offers the retry.
			console.error('failed to load the ranking panels', err);
		} finally {
			panelsLoading = false;
		}
	}

	function setFilter(filter: '' | 'party' | 'human') {
		entityFilter = filter;
		loadPersons(0);
		loadOthers(0);
		loadPanels();
	}

	async function refreshAll() {
		await Promise.all([loadPersons(0), loadOthers(0), loadPanels()]);
	}

	function loadMore() {
		loadPersons(currentPage + 1);
	}



	function handleRealtimeEvent(_event: any) {
		refreshAll();
	}

	let unsubscribe: (() => void) | null = null;

	onMount(() => {
		loadPersons();
		loadOthers();
		loadPanels();
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
	<title>tartuffe.me — {tr('dashboard.title', currentLang)}</title>
</svelte:head>

<div class="dashboard">
	<div class="podium-wrap"><Podium bind:this={podium} lang={currentLang} /></div>

	{#if error}
		<div class="error-banner">
			<p>{error}</p>
			<button class="retry-btn" onclick={() => refreshAll()}>{tr('home.retry', currentLang)}</button>
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
				placeholder={tr('home.search_placeholder', currentLang)}
				value={searchQuery}
				oninput={handleSearchInput}
				class="search-input"
			/>
		</div>

		<div class="filter-pills" role="group" aria-label="Filter by entity type">
			<button
				class="pill"
				class:active={entityFilter === ''}
				onclick={() => setFilter('')}
				aria-pressed={entityFilter === ''}
			>{tr('home.filter.all', currentLang)}</button>
			<button
				class="pill"
				class:active={entityFilter === 'human'}
				onclick={() => setFilter('human')}
				aria-pressed={entityFilter === 'human'}
			>{tr('home.filter.people', currentLang)}</button>
			<button
				class="pill"
				class:active={entityFilter === 'party'}
				onclick={() => setFilter('party')}
				aria-pressed={entityFilter === 'party'}
			>{tr('home.filter.parties', currentLang)}</button>
		</div>
	</div>


	{#if candidates.length > 1}
		<section class="lineup-section">
			<h2 class="section-title">{tr('score.compare.title', currentLang)}</h2>
			<ScoreCompare people={candidates} lang={currentLang} />
		</section>
	{/if}

	<header class="section-header">
		<h2 class="section-title">{tr('home.candidates', currentLang)}</h2>
		<p class="section-sub">{tr('home.candidates_sub', currentLang)}</p>
	</header>

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
				<a href={personPath(person.slug)} class="card">
					<div class="card-content">
						<div class="card-left">
							<TartuffeScore honesty={person.honesty_score} lang={currentLang} image={person.image_url} initial={person.name.charAt(0).toUpperCase()} facts={person.fact_count} reliable={person.score_reliable} />
							<div class="card-info">
								<div class="card-name">
									<span class="name-text">{person.name}</span>
									{#if person.entity_type === 'party'}
										<span class="badge">{tr('home.party_badge', currentLang)}</span>
									{/if}
								</div>
								<div class="card-meta">
									{person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)}
								</div>
							</div>
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
			<button class="load-more-btn" onclick={loadMore}>{tr('home.load_more', currentLang)}</button>
		</div>
	{/if}

	<section class="others">
		<header class="section-header">
			<h2 class="section-title">{tr('home.sourced', currentLang)}</h2>
			<p class="section-sub">{tr('home.sourced_note', currentLang)}</p>
		</header>
		{#if othersLoading && others.length === 0}
			<div class="loading-indicator"><div class="spinner"></div></div>
		{:else if others.length === 0}
			<p class="empty">{tr('home.no_data', currentLang)}</p>
		{:else}
			<div class="persons-grid">
				{#each others as person (person.id)}
					<a href={personPath(person.slug)} class="card">
						<div class="card-content">
							<div class="card-left">
								{#if person.image_url}
									<img src={person.image_url} alt="" class="other-avatar" loading="lazy" />
								{:else}
									<div class="other-avatar other-avatar-placeholder" aria-hidden="true">{person.name.charAt(0).toUpperCase()}</div>
								{/if}
								<div class="card-info">
									<div class="card-name">
										<span class="name-text">{person.name}</span>
										{#if person.entity_type === 'party'}
											<span class="badge">{tr('home.party_badge', currentLang)}</span>
										{/if}
									</div>
									<div class="card-meta">
										{person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)} · {tr('home.no_detection', currentLang)}
									</div>
								</div>
							</div>
						</div>
					</a>
				{/each}
			</div>
			{#if othersHasMore && !othersLoading}
				<div class="load-more">
					<button class="load-more-btn" onclick={() => loadOthers(othersPage + 1)}>{tr('home.load_more', currentLang)}</button>
				</div>
			{/if}
		{/if}
	</section>
</div>

<style>
	.section-header {
		margin-bottom: 1rem;
	}
	.section-header .section-title {
		margin-bottom: 0.25rem;
	}
	.section-sub {
		font-size: 0.8125rem;
		color: var(--text-secondary);
		max-width: 60ch;
		margin-left: auto;
		margin-right: auto;
		text-align: center;
	}
	.others {
		margin-top: 3rem;
		padding-top: 1.5rem;
		border-top: 1px solid var(--border-card);
	}
	.other-avatar {
		width: 48px;
		height: 48px;
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
	}
	.other-avatar-placeholder {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-weight: 600;
	}

	.dashboard {
		max-width: 1200px;
		margin: 0 auto;
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
		display: inline-flex;
		gap: 0;
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-full);
		padding: 0.25rem;
	}

	.pill {
		background: transparent;
		color: var(--text-secondary);
		border: none;
		padding: 0.5rem 1.1rem;
		border-radius: var(--radius-full);
		font-size: 0.875rem;
		font-weight: 600;
		font-family: var(--font-family);
		cursor: pointer;
		transition: all var(--transition-fast);
	}

	.pill:hover {
		color: var(--text-primary);
	}

	.pill.active {
		background: var(--accent);
		color: white;
		box-shadow: 0 0 12px var(--accent-glow);
	}

	.podium-wrap {
		margin-bottom: 2rem;
	}

	.lineup-section {
		margin-bottom: 2.5rem;
	}



	.empty {
		color: var(--text-muted);
		font-size: 0.875rem;
		padding: 0.5rem 0.625rem;
	}

	.section-title {
		font-size: 1.1rem;
		font-weight: 700;
		letter-spacing: -0.01em;
		margin-bottom: 1rem;
		color: var(--text-primary);
	}

	/* Grid */
	.persons-grid {
		display: grid;
		/* A column never wider than the screen: a narrow phone would scroll sideways. */
		grid-template-columns: repeat(auto-fill, minmax(min(340px, 100%), 1fr));
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
		gap: 0.875rem;
		flex: 1;
		min-width: 0;
	}

	.card-info {
		flex: 1;
		min-width: 0;
	}

	.card-name {
		font-weight: 600;
		font-size: 0.938rem;
		color: var(--text-primary);
		text-transform: capitalize;
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	/* A bare text node in a flex box is cut, not ellipsized: the name is a
	   box of its own that shrinks, and the badge keeps its width. */
	.name-text {
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.badge {
		background: color-mix(in oklch, var(--accent) 15%, transparent);
		color: var(--accent);
		font-size: 0.75rem;
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

	/* Skeleton */
	.skeleton-card {
		padding: 1.25rem;
	}

	.skeleton-header {
		display: flex;
		align-items: center;
		gap: 0.875rem;
	}

	.skeleton-avatar {
		width: 64px;
		height: 64px;
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
		width: 52px;
		height: 52px;
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
