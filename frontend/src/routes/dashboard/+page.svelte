<script lang="ts">
	import { eventPaths, showVoters } from '$lib/event';
	import ScoreHelp from '$lib/components/ScoreHelp.svelte';
	import { onMount, onDestroy } from 'svelte';
	import type { PersonSummary, PollEvent, PollResponse } from '$lib/types';
	import { realtimeManager, subscribeToPersonEvents, clearOldEvents } from '$lib/realtime';
	import { API_BASE } from '$lib/config';
	import { lang, tr } from '$lib/i18n';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	let persons: PersonSummary[] = $state([]);
	let topHonest: PersonSummary[] = $state([]);
	let topDishonest: PersonSummary[] = $state([]);
	let panelsLoading = $state(true);
	let loading = $state(true);
	let error: string | null = $state(null);
	let currentPage = $state(0);
	let pageSize = 50;
	let hasMore = $state(true);
	let entityFilter: '' | 'party' | 'human' = $state('');
	let searchQuery = $state('');
	let debounceTimer: ReturnType<typeof setTimeout> | null = null;

	// The featured pre-poll (the presidential election) heads the page.
	let featured: PollEvent | null = $state(null);
	let featuredVoters: number | null = $state(null);
	let featuredCandidates: number | null = $state(null);

	async function loadFeatured() {
		try {
			const res = await fetch(`${API_BASE}/api/polls`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const events: PollEvent[] = await res.json();
			featured = events.find((e) => e.featured) ?? null;
			if (!featured) return;
			const pr = await fetch(`${API_BASE}/api/polls/${encodeURIComponent(featured.slug)}`);
			if (!pr.ok) throw new Error(`HTTP ${pr.status}`);
			const poll: PollResponse = await pr.json();
			featuredVoters = poll.results.voters;
			featuredCandidates = poll.results.options.filter((o) => o.kind === 'candidate').length;
		} catch (err) {
			console.error('failed to load the featured poll', err);
		}
	}

	const PANEL_SIZE = 3;
	const PANEL_MIN_FACTS = 3;
	const SCORE_CIRCUMFERENCE = 2 * Math.PI * 14;

	function handleSearchInput(event: Event) {
		const target = event.target as HTMLInputElement;
		searchQuery = target.value;

		if (debounceTimer) {
			clearTimeout(debounceTimer);
		}
		debounceTimer = setTimeout(() => {
			loadPersons(0);
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
			const qs = buildQuery({ limit: pageSize, offset });
			const response = await fetch(`${API_BASE}/api/persons?${qs}`);

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

	async function loadPanels() {
		try {
			panelsLoading = true;
			const bestQs = buildQuery({ limit: PANEL_SIZE, sort: 'honesty_desc', min_facts: PANEL_MIN_FACTS });
			const worstQs = buildQuery({ limit: PANEL_SIZE, sort: 'honesty_asc', min_facts: PANEL_MIN_FACTS });
			const [bestRes, worstRes] = await Promise.all([
				fetch(`${API_BASE}/api/persons?${bestQs}`),
				fetch(`${API_BASE}/api/persons?${worstQs}`)
			]);
			if (bestRes.ok) topHonest = (await bestRes.json()) ?? [];
			if (worstRes.ok) topDishonest = (await worstRes.json()) ?? [];
		} catch (err) {
			// Panel errors are non-fatal; main list error banner covers retries.
		} finally {
			panelsLoading = false;
		}
	}

	function setFilter(filter: '' | 'party' | 'human') {
		entityFilter = filter;
		loadPersons(0);
		loadPanels();
	}

	async function refreshAll() {
		await Promise.all([loadPersons(0), loadPanels()]);
	}

	function loadMore() {
		loadPersons(currentPage + 1);
	}


	function getHonestyColor(score: number): string {
		if (score >= 0.8) return 'var(--green)';
		if (score >= 0.4) return 'var(--amber)';
		return 'var(--red)';
	}

	function handleRealtimeEvent(_event: any) {
		refreshAll();
	}

	let unsubscribe: (() => void) | null = null;

	onMount(() => {
		loadFeatured();
		loadPersons();
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
	<title>tartuffe.me — {tr('home.poll.title', currentLang)}</title>
	<meta name="description" content={tr('home.poll.lead', currentLang)} />
</svelte:head>

<div class="dashboard">
	{#if featured}
		<a class="poll-hero" href={eventPaths(featured.slug).vote}>
			<div class="poll-hero-text">
				<span class="poll-hero-kicker">{currentLang === 'en' ? featured.title_en : featured.title_fr}</span>
				<h1 class="poll-hero-title">{tr('home.poll.title', currentLang)}</h1>
				{#if showVoters(featuredVoters)}
					<p class="poll-hero-meta">
						{tr('poll.voters', currentLang, { n: (featuredVoters ?? 0).toLocaleString(currentLang === 'en' ? 'en-GB' : 'fr-FR') })}
						{#if featuredCandidates}<span class="dot"></span>{tr('home.poll.candidates', currentLang, { n: featuredCandidates })}{/if}
					</p>
				{/if}
			</div>
			<span class="poll-hero-cta">{tr('home.poll.cta', currentLang)} →</span>
		</a>
	{/if}

	{#if error}
		<div class="error-banner">
			<p>{error}</p>
			<button class="retry-btn" onclick={() => loadPersons(0)}>{tr('home.retry', currentLang)}</button>
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

	<div class="ranking-panels">
		<section class="rank-panel rank-panel--best">
			<header class="rank-panel-header">
				<h2>{tr('home.most_honest', currentLang)}</h2>
			</header>
			{#if panelsLoading && topHonest.length === 0}
				<ul class="rank-list">
					{#each Array(PANEL_SIZE) as _}
						<li class="rank-row skeleton-row">
							<div class="skeleton skeleton-rank-avatar"></div>
							<div class="skeleton skeleton-rank-name"></div>
							<div class="skeleton skeleton-rank-score"></div>
						</li>
					{/each}
				</ul>
			{:else if topHonest.length === 0}
				<p class="empty">{tr('home.no_data', currentLang)}</p>
			{:else}
				<ol class="rank-list">
					{#each topHonest as person, i (person.id)}
						<li>
							<a href="/person/{person.id}" class="rank-row">
								<span class="rank-pos">{i + 1}</span>
								{#if person.image_url}
									<img src={person.image_url} alt={person.name} class="rank-avatar" />
								{:else}
									<div class="rank-avatar rank-avatar-placeholder">
										{person.name.charAt(0).toUpperCase()}
									</div>
								{/if}
								<div class="rank-info">
									<div class="rank-name">{person.name}</div>
									<div class="rank-meta">{person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)}</div>
								</div>
								<div class="rank-score rank-score--good">{(person.honesty_score * 100).toFixed(0)}</div>
								<ScoreHelp name={person.name} personId={person.id} />
							</a>
						</li>
					{/each}
				</ol>
			{/if}
		</section>

		<section class="rank-panel rank-panel--worst">
			<header class="rank-panel-header">
				<h2>{tr('home.least_honest', currentLang)}</h2>
			</header>
			{#if panelsLoading && topDishonest.length === 0}
				<ul class="rank-list">
					{#each Array(PANEL_SIZE) as _}
						<li class="rank-row skeleton-row">
							<div class="skeleton skeleton-rank-avatar"></div>
							<div class="skeleton skeleton-rank-name"></div>
							<div class="skeleton skeleton-rank-score"></div>
						</li>
					{/each}
				</ul>
			{:else if topDishonest.length === 0}
				<p class="empty">{tr('home.no_data', currentLang)}</p>
			{:else}
				<ol class="rank-list">
					{#each topDishonest as person, i (person.id)}
						<li>
							<a href="/person/{person.id}" class="rank-row">
								<span class="rank-pos">{i + 1}</span>
								{#if person.image_url}
									<img src={person.image_url} alt={person.name} class="rank-avatar" />
								{:else}
									<div class="rank-avatar rank-avatar-placeholder">
										{person.name.charAt(0).toUpperCase()}
									</div>
								{/if}
								<div class="rank-info">
									<div class="rank-name">{person.name}</div>
									<div class="rank-meta">{person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)}</div>
								</div>
								<div class="rank-score rank-score--bad">{(person.honesty_score * 100).toFixed(0)}</div>
								<ScoreHelp name={person.name} personId={person.id} />
							</a>
						</li>
					{/each}
				</ol>
			{/if}
		</section>
	</div>

	<h2 class="section-title">{tr('home.all_entities', currentLang)}</h2>

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
										<span class="badge">{tr('home.party_badge', currentLang)}</span>
									{/if}
								</div>
								<div class="card-meta">
									{person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)}
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
							<ScoreHelp name={person.name} personId={person.id} />
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
</div>

<style>
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

	/* Ranking panels */
	.ranking-panels {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
		margin-bottom: 2rem;
	}

	@media (max-width: 720px) {
		.ranking-panels {
			grid-template-columns: minmax(0, 1fr);
		}
	}

	.rank-panel {
		min-width: 0;
		border-radius: var(--radius-md);
		padding: 1.25rem;
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		box-shadow: var(--shadow-card);
	}

	.rank-panel--best,
	.rank-panel--worst {
		background: var(--bg-card);
		border: 1px solid var(--border-card);
	}

	.rank-panel-header {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		margin-bottom: 0.75rem;
	}

	.rank-panel-header h2 {
		font-size: 1rem;
		font-weight: 700;
		letter-spacing: -0.01em;
	}

	.rank-panel--best .rank-panel-header h2 { color: var(--green); }
	.rank-panel--worst .rank-panel-header h2 { color: var(--red); }


	.rank-list {
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.rank-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.5rem 0.625rem;
		border-radius: var(--radius-sm);
		text-decoration: none;
		color: inherit;
		transition: background var(--transition-fast);
	}

	.rank-row:hover {
		background: rgba(255, 255, 255, 0.04);
	}

	.rank-pos {
		font-size: 0.75rem;
		font-weight: 700;
		color: var(--text-muted);
		width: 1rem;
		text-align: center;
		flex-shrink: 0;
	}

	.rank-avatar {
		width: 56px;
		height: 56px;
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
		border: 1px solid var(--border-card);
	}

	.rank-avatar-placeholder {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-weight: 600;
		font-size: 1.25rem;
	}

	.rank-info {
		flex: 1;
		min-width: 0;
	}

	.rank-name {
		font-weight: 600;
		font-size: 0.938rem;
		text-transform: capitalize;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		color: var(--text-primary);
	}

	.rank-meta {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-top: 0.125rem;
	}

	.rank-score {
		font-size: 1.125rem;
		font-weight: 700;
		flex-shrink: 0;
		min-width: 2.25rem;
		text-align: right;
	}

	.rank-score--good { color: var(--green); }
	.rank-score--bad { color: var(--red); }

	.skeleton-row {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.5rem 0.625rem;
	}

	.skeleton-rank-avatar {
		width: 56px;
		height: 56px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.skeleton-rank-name {
		flex: 1;
		height: 14px;
	}

	.skeleton-rank-score {
		width: 2.25rem;
		height: 18px;
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
		grid-template-columns: repeat(auto-fill, minmax(340px, 1fr));
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

	.avatar {
		width: 64px;
		height: 64px;
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
		font-size: 1.5rem;
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
		display: flex;
		align-items: center;
		gap: 0.35rem;
	}

	.score-ring {
		width: 52px;
		height: 52px;
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

	.poll-hero {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1.5rem;
		margin-bottom: 2rem;
		padding: 1.75rem 2rem;
		border: 1px solid var(--accent);
		border-radius: var(--radius-lg);
		background:
			radial-gradient(120% 140% at 0% 0%, var(--accent-glow), transparent 60%),
			var(--bg-card);
		color: var(--text-primary);
		box-shadow: var(--shadow-glow);
		transition: transform var(--transition-normal), box-shadow var(--transition-normal);
	}

	.poll-hero:hover {
		transform: translateY(-2px);
		color: var(--text-primary);
		box-shadow: 0 0 32px var(--accent-glow);
	}

	.poll-hero-kicker {
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.12em;
		text-transform: uppercase;
		color: var(--accent-hover);
	}

	.poll-hero-title {
		font-family: var(--font-serif);
		font-size: clamp(1.75rem, 4vw, 2.5rem);
		font-weight: 600;
		line-height: 1.1;
		margin: 0.25rem 0 0.5rem;
	}


	.poll-hero-meta {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin: 0.75rem 0 0;
		font-size: 0.85rem;
		color: var(--text-muted);
	}

	.poll-hero-cta {
		flex-shrink: 0;
		padding: 0.75rem 1.25rem;
		border-radius: var(--radius-full);
		background: var(--accent);
		color: white;
		font-weight: 600;
		white-space: nowrap;
	}

	@media (max-width: 700px) {
		.poll-hero {
			flex-direction: column;
			align-items: flex-start;
			padding: 1.25rem;
		}
	}
</style>
