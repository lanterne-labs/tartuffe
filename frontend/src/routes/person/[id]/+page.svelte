<script lang="ts">
	import { page } from '$app/stores';
	import { onMount, onDestroy } from 'svelte';
	import type { PersonSummary, FactWithSource, ContradictionWithFacts, PaginatedResponse, StanceDocument } from '$lib/types';
	import { realtimeManager, subscribeToPersonEvents, clearOldEvents } from '$lib/realtime';
	import { API_BASE, TALLY_FORM_ID } from '$lib/config';

	interface MemberInfo {
		id: number;
		name: string;
		entity_type: string;
		honesty_score: number;
		image_url?: string;
	}

	const SCORE_CIRCUMFERENCE = 2 * Math.PI * 14;

	let person: PersonSummary | null = $state(null);
	let facts: FactWithSource[] = $state([]);
	let contradictions: ContradictionWithFacts[] = $state([]);
	let members: MemberInfo[] = $state([]);
	let stances: StanceDocument[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);
	let activeTab: 'facts' | 'contradictions' | 'stances' = $state('facts');
	let membersExpanded = $state(false);

	let factsPage = $state(0);
	let contradictionsPage = $state(0);
	let factsPageSize = 25;
	let contradictionsPageSize = 25;
	let hasMoreFacts = $state(true);
	let hasMoreContradictions = $state(true);
	let totalFacts = $state(0);
	let totalContradictions = $state(0);

	let expandedFacts: Record<number, boolean> = $state({});
	// Per-bundle expander state. Key is the bundle key (string for stable null/solo handling).
	let expandedBundles: Record<string, boolean> = $state({});

	// Group contradictions by bundle_id. Solo rows (bundle_id = null) each become their own
	// group, keyed by the contradiction id under "solo:<id>". Order: largest bundles first.
	type ContradictionBundle = {
		key: string;
		bundle_id: number | null;
		label: string | null;
		summary: string | null;
		size: number;
		items: ContradictionWithFacts[];
	};
	let contradictionBundles: ContradictionBundle[] = $derived.by(() => {
		const map = new Map<string, ContradictionBundle>();
		for (const c of contradictions) {
			const key = c.bundle_id !== null ? `b:${c.bundle_id}` : `solo:${c.id}`;
			let bundle = map.get(key);
			if (!bundle) {
				bundle = {
					key,
					bundle_id: c.bundle_id,
					label: c.bundle_label,
					summary: c.bundle_summary,
					// bundle_size is the *server-side* count for this person; the loaded items list
					// may be smaller if pagination hasn't pulled them all yet.
					size: c.bundle_size > 0 ? c.bundle_size : 1,
					items: []
				};
				map.set(key, bundle);
			}
			bundle.items.push(c);
		}
		return Array.from(map.values()).sort((a, b) => b.size - a.size || a.key.localeCompare(b.key));
	});
	let factPersons: Record<number, PersonSummary[]> = $state({});
	let factPersonsPage: Record<number, number> = $state({});
	let factPersonsHasMore: Record<number, boolean> = $state({});
	const personsPageSize = 10;

	let highlightedFactId: number | null = $state(null);

	let personId = $derived(Number($page.params.id));

	async function goToFact(factId: number) {
		activeTab = 'facts';

		const factExists = facts.some(f => f.fact_id === factId);
		if (!factExists) {
			await loadFacts(0);
		}

		highlightedFactId = factId;

		setTimeout(() => {
			const element = document.getElementById(`fact-${factId}`);
			if (element) {
				element.scrollIntoView({ behavior: 'smooth', block: 'center' });
			}
			setTimeout(() => {
				highlightedFactId = null;
			}, 2000);
		}, 100);
	}

	async function loadPerson() {
		try {
			const response = await fetch(`${API_BASE}/api/person/${personId}`);
			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}
			person = await response.json();
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		}
	}

	async function loadFacts(page = 0) {
		try {
			const offset = page * factsPageSize;
			const response = await fetch(`${API_BASE}/api/person/${personId}/facts?limit=${factsPageSize}&offset=${offset}`);
			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}
			const data: PaginatedResponse<FactWithSource> = await response.json();
			facts = page === 0 ? data.items : [...facts, ...data.items];
			totalFacts = data.total;
			hasMoreFacts = facts.length < data.total;
			factsPage = page;
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		}
	}

	async function loadContradictions(page = 0) {
		try {
			const offset = page * contradictionsPageSize;
			const response = await fetch(`${API_BASE}/api/person/${personId}/contradictions?limit=${contradictionsPageSize}&offset=${offset}`);
			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}
			const data: PaginatedResponse<ContradictionWithFacts> = await response.json();
			contradictions = page === 0 ? data.items : [...contradictions, ...data.items];
			totalContradictions = data.total;
			hasMoreContradictions = contradictions.length < data.total;
			contradictionsPage = page;
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		}
	}

	async function loadMembers() {
		try {
			const response = await fetch(`${API_BASE}/api/person/${personId}/members`);
			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}
			members = await response.json();
		} catch (err) {
			error = err instanceof Error ? `Failed to load members: ${err.message}` : 'Failed to load members';
		}
	}

	async function loadStances() {
		try {
			const response = await fetch(`${API_BASE}/api/person/${personId}/stances`);
			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}
			stances = await response.json();
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		}
	}

	async function loadData() {
		loading = true;
		error = null;

		await Promise.all([loadPerson(), loadFacts(), loadContradictions(), loadStances()]);

		if (person?.entity_type === 'party') {
			await loadMembers();
		}

		loading = false;
	}

	function loadMoreFacts() {
		loadFacts(factsPage + 1);
	}

	function loadMoreContradictions() {
		loadContradictions(contradictionsPage + 1);
	}

	async function loadFactPersons(factId: number, page = 0) {
		try {
			const offset = page * personsPageSize;
			const response = await fetch(`${API_BASE}/api/facts/${factId}/persons?limit=${personsPageSize}&offset=${offset}`);
			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}
			const data = await response.json();

			if (page === 0) {
				factPersons[factId] = data;
			} else {
				factPersons[factId] = [...(factPersons[factId] || []), ...data];
			}

			factPersonsPage[factId] = page;
			factPersonsHasMore[factId] = data.length === personsPageSize;
		} catch (err) {
			error = err instanceof Error ? `Failed to load persons: ${err.message}` : 'Failed to load persons';
		}
	}

	async function toggleFactPersons(factId: number) {
		const isCurrentlyExpanded = expandedFacts[factId];
		if (!isCurrentlyExpanded && !factPersons[factId]) {
			await loadFactPersons(factId, 0);
		}
		expandedFacts[factId] = !isCurrentlyExpanded;
	}

	async function loadMorePersons(factId: number) {
		const nextPage = (factPersonsPage[factId] || 0) + 1;
		await loadFactPersons(factId, nextPage);
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

	function formatDate(dateString: string): string {
		return new Date(dateString).toLocaleDateString();
	}

	function formatDateWithTime(dateString: string): string {
		const date = new Date(dateString);
		return date.toLocaleString([], {
			year: 'numeric',
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	// --- User-complaints (Tally) ---
	// One helper per kind so call sites read as `reportFact(fact)` etc., with no
	// string-typed kind parameter to get wrong. The Tally popup carries the kind +
	// the relevant id as hidden fields; the triage CLI reads them back via the API.
	function openTally(hiddenFields: Record<string, string | number>) {
		if (!TALLY_FORM_ID) {
			console.warn('VITE_TALLY_FORM_ID is not set; report button is a no-op.');
			return;
		}
		// Tally embed.js is loaded async from app.html; the global may not exist yet
		// on a very early click. Bail gracefully — the user can re-click.
		const tally = (window as any).Tally;
		if (!tally?.openPopup) {
			console.warn('Tally widget is still loading; please retry in a moment.');
			return;
		}
		tally.openPopup(TALLY_FORM_ID, {
			layout: 'modal',
			width: 460,
			hiddenFields
		});
	}

	function reportFact(fact: FactWithSource) {
		const preview = fact.fact_text.length > 80 ? fact.fact_text.slice(0, 80) + '…' : fact.fact_text;
		openTally({
			target_kind: 'fact',
			fact_id: fact.fact_id,
			contradiction_id: '',
			stance_id: '',
			target_label: `Fact #${fact.fact_id}: ${preview}`,
			source_url: typeof location !== 'undefined' ? location.href : ''
		});
	}

	function reportContradiction(c: ContradictionWithFacts) {
		openTally({
			target_kind: 'contradiction',
			contradiction_id: c.id,
			fact_id: '',
			stance_id: '',
			target_label: `Contradiction #${c.id} (facts #${c.fact_a_id} ↔ #${c.fact_b_id})`,
			source_url: typeof location !== 'undefined' ? location.href : ''
		});
	}

	function reportStance(s: StanceDocument) {
		const preview = s.label.length > 60 ? s.label.slice(0, 60) + '…' : s.label;
		openTally({
			target_kind: 'stance',
			stance_id: s.id,
			fact_id: '',
			contradiction_id: '',
			target_label: `Stance #${s.id}: ${preview}`,
			source_url: typeof location !== 'undefined' ? location.href : ''
		});
	}

	function handleRealtimeEvent(_event: any) {
		loadPerson();
		if (activeTab === 'facts') {
			loadFacts(0);
		} else if (activeTab === 'contradictions') {
			loadContradictions(0);
		}
	}

	let unsubscribe: (() => void) | null = null;

	onMount(() => {
		loadData();
		realtimeManager.connect();
		unsubscribe = subscribeToPersonEvents(personId, handleRealtimeEvent);
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
	<title>{person?.name || 'Person'} - Digital Mirror</title>
</svelte:head>

<div class="detail-page">
	<a href="/" class="back-link">
		<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
			<path d="m15 18-6-6 6-6"/>
		</svg>
		Back to Dashboard
	</a>

	{#if loading && !person}
		<div class="person-hero skeleton-hero">
			<div class="skeleton skeleton-avatar-lg"></div>
			<div class="skeleton-hero-text">
				<div class="skeleton skeleton-title"></div>
				<div class="skeleton skeleton-subtitle"></div>
			</div>
		</div>
		<div class="skeleton-facts">
			{#each Array(3) as _}
				<div class="card">
					<div class="skeleton skeleton-line-long"></div>
					<div class="skeleton skeleton-line-short"></div>
				</div>
			{/each}
		</div>
	{:else if error && !person}
		<div class="error-banner">
			<p>{error}</p>
			<button class="retry-btn" onclick={loadData}>Retry</button>
		</div>
	{:else if person}
		<div class="person-hero">
			<div class="hero-left">
				{#if person.image_url}
					<img src={person.image_url} alt={person.name} class="hero-avatar" />
				{:else}
					<div class="hero-avatar hero-avatar-placeholder">
						{person.name.charAt(0).toUpperCase()}
					</div>
				{/if}
				<div class="hero-info">
					<h1>
						{person.name}
						{#if person.entity_type === 'party'}
							<span class="badge">Party</span>
						{/if}
					</h1>
					<p class="hero-label">{getHonestyLabel(person.honesty_score)} — {(person.honesty_score * 100).toFixed(1)}%</p>
				</div>
			</div>
			<div class="hero-gauge">
				<svg viewBox="0 0 36 36" class="gauge-svg">
					<circle cx="18" cy="18" r="14" fill="none" stroke="var(--bg-secondary)" stroke-width="2.5" />
					<circle
						cx="18" cy="18" r="14"
						fill="none"
						stroke={getHonestyColor(person.honesty_score)}
						stroke-width="2.5"
						stroke-linecap="round"
						stroke-dasharray="{person.honesty_score * SCORE_CIRCUMFERENCE} {SCORE_CIRCUMFERENCE}"
						transform="rotate(-90 18 18)"
					/>
					<text x="18" y="18" text-anchor="middle" dominant-baseline="central" class="gauge-text">
						{(person.honesty_score * 100).toFixed(0)}
					</text>
				</svg>
				<span class="gauge-label">Score</span>
			</div>
		</div>

		{#if person.entity_type !== 'human' && person.members_score != null}
			<div class="score-breakdown">
				<div class="mini-gauge">
					<svg viewBox="0 0 36 36" class="mini-gauge-svg">
						<circle cx="18" cy="18" r="14" fill="none" stroke="var(--bg-secondary)" stroke-width="2.5" />
						<circle cx="18" cy="18" r="14" fill="none"
							stroke={getHonestyColor(person.own_score ?? person.honesty_score)}
							stroke-width="2.5" stroke-linecap="round"
							stroke-dasharray="{(person.own_score ?? person.honesty_score) * SCORE_CIRCUMFERENCE} {SCORE_CIRCUMFERENCE}"
							transform="rotate(-90 18 18)" />
						<text x="18" y="18" text-anchor="middle" dominant-baseline="central" class="mini-gauge-text">
							{((person.own_score ?? person.honesty_score) * 100).toFixed(0)}
						</text>
					</svg>
					<span class="mini-gauge-label">Own Score</span>
				</div>
				<div class="mini-gauge">
					<svg viewBox="0 0 36 36" class="mini-gauge-svg">
						<circle cx="18" cy="18" r="14" fill="none" stroke="var(--bg-secondary)" stroke-width="2.5" />
						<circle cx="18" cy="18" r="14" fill="none"
							stroke={getHonestyColor(person.members_score)}
							stroke-width="2.5" stroke-linecap="round"
							stroke-dasharray="{person.members_score * SCORE_CIRCUMFERENCE} {SCORE_CIRCUMFERENCE}"
							transform="rotate(-90 18 18)" />
						<text x="18" y="18" text-anchor="middle" dominant-baseline="central" class="mini-gauge-text">
							{(person.members_score * 100).toFixed(0)}
						</text>
					</svg>
					<span class="mini-gauge-label">Members Score</span>
				</div>
				<div class="mini-gauge">
					<svg viewBox="0 0 36 36" class="mini-gauge-svg">
						<circle cx="18" cy="18" r="14" fill="none" stroke="var(--bg-secondary)" stroke-width="2.5" />
						<circle cx="18" cy="18" r="14" fill="none"
							stroke={getHonestyColor(person.honesty_score)}
							stroke-width="2.5" stroke-linecap="round"
							stroke-dasharray="{person.honesty_score * SCORE_CIRCUMFERENCE} {SCORE_CIRCUMFERENCE}"
							transform="rotate(-90 18 18)" />
						<text x="18" y="18" text-anchor="middle" dominant-baseline="central" class="mini-gauge-text">
							{(person.honesty_score * 100).toFixed(0)}
						</text>
					</svg>
					<span class="mini-gauge-label">Aggregate</span>
				</div>
			</div>
		{/if}

		{#if person.entity_type === 'party' && members.length > 0}
			<div class="members-section">
				<button class="members-toggle" onclick={() => membersExpanded = !membersExpanded}>
					<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="chevron" class:expanded={membersExpanded}>
						<path d="m9 18 6-6-6-6"/>
					</svg>
					{members.length} members
				</button>

				{#if membersExpanded}
					<div class="members-grid">
						{#each members as member (member.id)}
							<a href="/person/{member.id}" class="member-chip">
								{#if member.image_url}
									<img src={member.image_url} alt={member.name} class="member-avatar" />
								{:else}
									<div class="member-avatar member-avatar-placeholder">
										{member.name.charAt(0).toUpperCase()}
									</div>
								{/if}
								<span class="member-name">{member.name}</span>
								<span class="member-score" style="color: {getHonestyColor(member.honesty_score)}">
									{(member.honesty_score * 100).toFixed(0)}%
								</span>
							</a>
						{/each}
					</div>
				{/if}
			</div>
		{/if}

		{#if error}
			<div class="error-banner" style="margin-bottom: 1rem;">
				<p>{error}</p>
			</div>
		{/if}

		<div class="tabs">
			<button class="tab" class:active={activeTab === 'facts'} onclick={() => activeTab = 'facts'}>
				Facts ({totalFacts})
			</button>
			<button class="tab" class:active={activeTab === 'contradictions'} onclick={() => activeTab = 'contradictions'}>
				Contradictions ({totalContradictions})
			</button>
			<button class="tab" class:active={activeTab === 'stances'} onclick={() => activeTab = 'stances'}>
				Stances ({stances.length})
			</button>
		</div>

		<div class="tab-content">
			{#if activeTab === 'facts'}
				{#if facts.length === 0 && !loading}
					<p class="empty-state">No facts found for this person.</p>
				{:else}
					<div class="items-list">
						{#each facts as fact (fact.fact_id)}
							<div id="fact-{fact.fact_id}" class="card fact-card" class:highlighted={highlightedFactId === fact.fact_id}>
								{#if TALLY_FORM_ID}
									<button class="report-btn" title="Report this fact" aria-label="Report this fact" onclick={() => reportFact(fact)}>
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
											<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
											<line x1="12" y1="9" x2="12" y2="13"/>
											<line x1="12" y1="17" x2="12.01" y2="17"/>
										</svg>
									</button>
								{/if}
								<div class="fact-text">
									<span class="fact-id">[{fact.fact_id}]</span>
									{fact.fact_text}
								</div>
								<div class="fact-source">
									<a href={fact.source_url} target="_blank" rel="noopener noreferrer">{fact.source_url}</a>
									{#if fact.original_quote}
										<div class="quote-block">
											"{fact.original_quote.length > 200 ? fact.original_quote.substring(0, 200) + '...' : fact.original_quote}"
										</div>
									{/if}
								</div>

								{#if person?.entity_type === 'party'}
									<div class="fact-persons-section">
										<button class="persons-toggle" onclick={() => toggleFactPersons(fact.fact_id)}>
											<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="chevron" class:expanded={expandedFacts[fact.fact_id]}>
												<path d="m9 18 6-6-6-6"/>
											</svg>
											{#if factPersons[fact.fact_id]}
												{factPersons[fact.fact_id].length}+ persons
											{:else}
												Show persons
											{/if}
										</button>

										{#if expandedFacts[fact.fact_id]}
											<div class="fact-persons-grid">
												{#if factPersons[fact.fact_id]?.length > 0}
													{#each factPersons[fact.fact_id] as p (p.id)}
														<a href="/person/{p.id}" class="person-chip">
															<span class="person-chip-name">{p.name}</span>
															<span class="person-chip-score">{p.honesty_score.toFixed(2)}</span>
														</a>
													{/each}
													{#if factPersonsHasMore[fact.fact_id]}
														<button class="load-more-inline" onclick={() => loadMorePersons(fact.fact_id)}>
															Load More
														</button>
													{/if}
												{:else}
													<p class="empty-mini">No persons found</p>
												{/if}
											</div>
										{/if}
									</div>
								{/if}
							</div>
						{/each}
					</div>

					{#if hasMoreFacts}
						<div class="load-more">
							<button class="load-more-btn" onclick={loadMoreFacts} disabled={loading}>
								{loading ? 'Loading...' : 'Load More Facts'}
							</button>
						</div>
					{/if}
				{/if}
			{:else if activeTab === 'contradictions'}
				{#if contradictions.length === 0 && !loading}
					<p class="empty-state">No contradictions found for this person.</p>
				{:else}
					<div class="items-list">
						{#each contradictionBundles as bundle (bundle.key)}
							<div class="card contradiction-card">
								<div class="bundle-header">
									{#if bundle.bundle_id !== null}
										<div class="bundle-title">
											<strong>{bundle.label ?? 'Topic'}</strong>
											<span class="bundle-badge">{bundle.size} contradiction{bundle.size === 1 ? '' : 's'}</span>
										</div>
										{#if bundle.summary}
											<div class="bundle-summary">{bundle.summary}</div>
										{/if}
									{:else}
										{@const solo = bundle.items[0]}
										{@const aLabel = solo.fact_a_stance_label}
										{@const bLabel = solo.fact_b_stance_label}
										{@const missingStance = aLabel === null || bLabel === null}
										{@const crossTopic = !missingStance && solo.fact_a_stance_id !== solo.fact_b_stance_id}
										<div class="bundle-title">
											{#if missingStance}
												<strong>Standalone contradiction</strong>
												<span class="bundle-badge bundle-badge-warn" title="One or both facts are missing a stance — expected to be 0 post-migration.">Data quality</span>
											{:else if crossTopic}
												<strong>{aLabel} <span class="cross-topic-arrow">↔</span> {bLabel}</strong>
												<span class="bundle-badge bundle-badge-cross" title="The two facts contradict across different topic stances.">Cross-topic</span>
											{:else}
												<strong>{aLabel ?? 'Standalone contradiction'}</strong>
											{/if}
										</div>
									{/if}
								</div>

								{#if bundle.items.length > 1 || (bundle.bundle_id !== null && bundle.size > 1)}
									<button
										class="link-btn bundle-toggle"
										onclick={() => (expandedBundles[bundle.key] = !expandedBundles[bundle.key])}
									>
										{expandedBundles[bundle.key] ? 'Hide details' : `Show ${bundle.items.length} pair${bundle.items.length === 1 ? '' : 's'}`}
									</button>
								{/if}

								{#if bundle.items.length === 1 && bundle.bundle_id === null}
									<!-- Solo row: render its detail inline (no expander needed). -->
									{@const c = bundle.items[0]}
									<div class="contradiction-header">
										<span class="contradiction-id">#{c.id}</span>
										<span class="contradiction-date">{formatDate(c.detected_at)}</span>
										{#if TALLY_FORM_ID}
											<button class="report-btn report-btn-inline" title="Report this contradiction" aria-label="Report this contradiction" onclick={() => reportContradiction(c)}>
												<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
													<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
													<line x1="12" y1="9" x2="12" y2="13"/>
													<line x1="12" y1="17" x2="12.01" y2="17"/>
												</svg>
											</button>
										{/if}
									</div>
									<div class="contradiction-reason">{c.reason}</div>
									<div class="facts-comparison">
										<div class="comp-fact comp-a">
											<div class="comp-label">
												<strong>Statement A</strong>
												{#if c.fact_a_date}
													<span class="comp-date">{formatDateWithTime(c.fact_a_date)}</span>
												{/if}
											</div>
											<div class="comp-text"><span class="fact-id">[{c.fact_a_id}]</span> {c.fact_a_text}</div>
											<div class="comp-links">
												<button class="link-btn" onclick={() => goToFact(c.fact_a_id)}>View in Facts</button>
												{#each c.fact_a_source_urls as url, i}
													<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">Source{c.fact_a_source_urls.length > 1 ? ` ${i + 1}` : ''}</a>
												{/each}
											</div>
										</div>
										<div class="comp-fact comp-b">
											<div class="comp-label">
												<strong>Statement B</strong>
												{#if c.fact_b_date}
													<span class="comp-date">{formatDateWithTime(c.fact_b_date)}</span>
												{/if}
											</div>
											<div class="comp-text"><span class="fact-id">[{c.fact_b_id}]</span> {c.fact_b_text}</div>
											<div class="comp-links">
												<button class="link-btn" onclick={() => goToFact(c.fact_b_id)}>View in Facts</button>
												{#each c.fact_b_source_urls as url, i}
													<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">Source{c.fact_b_source_urls.length > 1 ? ` ${i + 1}` : ''}</a>
												{/each}
											</div>
										</div>
									</div>
								{:else if expandedBundles[bundle.key]}
									{#each bundle.items as c (c.id)}
										<div class="bundle-pair">
											<div class="contradiction-header">
												<span class="contradiction-id">#{c.id}</span>
												<span class="contradiction-date">{formatDate(c.detected_at)}</span>
												{#if TALLY_FORM_ID}
													<button class="report-btn report-btn-inline" title="Report this contradiction" aria-label="Report this contradiction" onclick={() => reportContradiction(c)}>
														<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
															<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
															<line x1="12" y1="9" x2="12" y2="13"/>
															<line x1="12" y1="17" x2="12.01" y2="17"/>
														</svg>
													</button>
												{/if}
											</div>
											<div class="contradiction-reason">{c.reason}</div>
											<div class="facts-comparison">
												<div class="comp-fact comp-a">
													<div class="comp-label">
														<strong>Statement A</strong>
														{#if c.fact_a_date}
															<span class="comp-date">{formatDateWithTime(c.fact_a_date)}</span>
														{/if}
													</div>
													<div class="comp-text"><span class="fact-id">[{c.fact_a_id}]</span> {c.fact_a_text}</div>
													<div class="comp-links">
														<button class="link-btn" onclick={() => goToFact(c.fact_a_id)}>View in Facts</button>
														{#each c.fact_a_source_urls as url, i}
															<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">Source{c.fact_a_source_urls.length > 1 ? ` ${i + 1}` : ''}</a>
														{/each}
													</div>
												</div>
												<div class="comp-fact comp-b">
													<div class="comp-label">
														<strong>Statement B</strong>
														{#if c.fact_b_date}
															<span class="comp-date">{formatDateWithTime(c.fact_b_date)}</span>
														{/if}
													</div>
													<div class="comp-text"><span class="fact-id">[{c.fact_b_id}]</span> {c.fact_b_text}</div>
													<div class="comp-links">
														<button class="link-btn" onclick={() => goToFact(c.fact_b_id)}>View in Facts</button>
														{#each c.fact_b_source_urls as url, i}
															<a href={url} target="_blank" rel="noopener noreferrer" class="link-btn">Source{c.fact_b_source_urls.length > 1 ? ` ${i + 1}` : ''}</a>
														{/each}
													</div>
												</div>
											</div>
										</div>
									{/each}
								{/if}
							</div>
						{/each}
					</div>

					{#if hasMoreContradictions}
						<div class="load-more">
							<button class="load-more-btn" onclick={loadMoreContradictions} disabled={loading}>
								{loading ? 'Loading...' : 'Load More Contradictions'}
							</button>
						</div>
					{/if}
				{/if}
			{:else if activeTab === 'stances'}
				{#if stances.length === 0 && !loading}
					<p class="empty-state">No stances found for this person.</p>
				{:else}
					<div class="items-list">
						{#each stances as stance (stance.id)}
							<div class="card stance-card">
								{#if TALLY_FORM_ID}
									<button class="report-btn" title="Report this stance" aria-label="Report this stance" onclick={() => reportStance(stance)}>
										<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
											<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
											<line x1="12" y1="9" x2="12" y2="13"/>
											<line x1="12" y1="17" x2="12.01" y2="17"/>
										</svg>
									</button>
								{/if}
								<div class="stance-header">
									<span class="stance-label">{stance.label}</span>
									<span class="stance-badge">{stance.fact_count} facts</span>
								</div>
								<div class="stance-summary">{stance.summary}</div>
								<div class="stance-meta">
									Updated {formatDate(stance.updated_at)}
								</div>
							</div>
						{/each}
					</div>
				{/if}
			{/if}
		</div>
	{/if}
</div>

<style>
	.detail-page {
		max-width: 1000px;
		margin: 0 auto;
	}

	.back-link {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 0.875rem;
		color: var(--text-secondary);
		margin-bottom: 1.5rem;
	}

	.back-link:hover {
		color: var(--accent);
	}

	/* Hero */
	.person-hero {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 2rem;
		margin-bottom: 2rem;
		padding: 1.5rem;
		background: var(--bg-card);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
	}

	.hero-left {
		display: flex;
		align-items: center;
		gap: 1.25rem;
		flex: 1;
		min-width: 0;
	}

	.hero-avatar {
		width: 80px;
		height: 80px;
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
	}

	.hero-avatar-placeholder {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-weight: 600;
		font-size: 2rem;
		border: 1px solid var(--border-card);
	}

	.hero-info h1 {
		font-size: 1.5rem;
		font-weight: 700;
		letter-spacing: -0.025em;
		display: flex;
		align-items: center;
		gap: 0.5rem;
		text-transform: capitalize;
	}

	.hero-label {
		color: var(--text-secondary);
		font-size: 0.875rem;
		margin-top: 0.25rem;
	}

	.badge {
		background: rgba(59, 130, 246, 0.15);
		color: var(--accent);
		font-size: 0.688rem;
		font-weight: 500;
		padding: 0.125rem 0.5rem;
		border-radius: var(--radius-full);
	}

	.hero-gauge {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.25rem;
		flex-shrink: 0;
	}

	.gauge-svg {
		width: 80px;
		height: 80px;
	}

	.gauge-text {
		font-size: 10px;
		font-weight: 700;
		fill: var(--text-primary);
	}

	.gauge-label {
		font-size: 0.688rem;
		color: var(--text-muted);
		font-weight: 500;
	}

	/* Score Breakdown */
	.score-breakdown {
		display: flex;
		justify-content: center;
		gap: 2rem;
		margin-bottom: 1.5rem;
		padding: 1rem 1.5rem;
		background: var(--bg-card);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
	}

	.mini-gauge {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.25rem;
	}

	.mini-gauge-svg {
		width: 56px;
		height: 56px;
	}

	.mini-gauge-text {
		font-size: 9px;
		font-weight: 700;
		fill: var(--text-primary);
	}

	.mini-gauge-label {
		font-size: 0.625rem;
		color: var(--text-muted);
		font-weight: 500;
	}

	/* Members */
	.members-section {
		margin-bottom: 1.5rem;
	}

	.members-toggle {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		padding: 0.625rem 1rem;
		border-radius: var(--radius-md);
		cursor: pointer;
		font-size: 0.813rem;
		font-weight: 500;
		font-family: var(--font-family);
		color: var(--text-secondary);
		width: 100%;
		text-align: left;
		transition: border-color var(--transition-fast);
	}

	.members-toggle:hover {
		border-color: var(--border-card-hover);
	}

	.chevron {
		transition: transform var(--transition-fast);
	}

	.chevron.expanded {
		transform: rotate(90deg);
	}

	.members-grid {
		margin-top: 0.5rem;
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
		gap: 0.5rem;
		max-height: 400px;
		overflow-y: auto;
		padding: 0.5rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
	}

	.member-chip {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-sm);
		color: inherit;
		font-size: 0.813rem;
		transition: border-color var(--transition-fast);
	}

	.member-chip:hover {
		border-color: var(--accent);
	}

	.member-avatar {
		width: 24px;
		height: 24px;
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
	}

	.member-avatar-placeholder {
		display: flex;
		align-items: center;
		justify-content: center;
		background: var(--bg-secondary);
		color: var(--text-muted);
		font-weight: 600;
		font-size: 0.625rem;
		border: 1px solid var(--border-card);
	}

	.member-name {
		color: var(--text-primary);
		text-transform: capitalize;
		flex: 1;
	}

	.member-score {
		font-weight: 600;
		font-size: 0.688rem;
	}

	/* Error */
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
		font-family: var(--font-family);
		cursor: pointer;
		transition: opacity var(--transition-fast);
	}

	.retry-btn:hover {
		opacity: 0.9;
	}

	/* Tabs */
	.tabs {
		display: flex;
		gap: 0;
		border-bottom: 1px solid var(--border-card);
		margin-bottom: 1.5rem;
	}

	.tab {
		background: none;
		border: none;
		padding: 0.75rem 1.5rem;
		font-size: 0.875rem;
		font-weight: 500;
		font-family: var(--font-family);
		color: var(--text-muted);
		cursor: pointer;
		border-bottom: 2px solid transparent;
		transition: color var(--transition-fast), border-color var(--transition-fast);
	}

	.tab.active {
		color: var(--text-primary);
		border-bottom-color: var(--accent);
	}

	.tab:hover:not(.active) {
		color: var(--text-secondary);
	}

	.tab-content {
		min-height: 400px;
	}

	.empty-state {
		text-align: center;
		color: var(--text-muted);
		font-style: italic;
		padding: 3rem;
	}

	/* Items List */
	.items-list {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	.card {
		background: var(--bg-card);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		padding: 1.25rem;
		transition: border-color var(--transition-fast), box-shadow var(--transition-normal);
		position: relative;
	}

	/* Report-issue affordance: a small warning icon on every fact / contradiction
	   / stance card that opens the Tally complaint popup. */
	.report-btn {
		position: absolute;
		top: 0.5rem;
		right: 0.5rem;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 24px;
		height: 24px;
		padding: 0;
		background: transparent;
		border: none;
		border-radius: 4px;
		color: var(--text-secondary);
		opacity: 0.5;
		cursor: pointer;
		transition: color var(--transition-fast), opacity var(--transition-fast), background var(--transition-fast);
	}
	.report-btn:hover,
	.report-btn:focus-visible {
		color: var(--red);
		opacity: 1;
		background: rgba(255, 255, 255, 0.05);
		outline: none;
	}
	/* Inline variant used inside contradiction-header (which has its own flex layout). */
	.report-btn-inline {
		position: static;
	}

	/* Fact Card */
	.fact-card.highlighted {
		border-color: var(--accent);
		box-shadow: 0 0 0 3px var(--accent-glow), var(--shadow-glow);
		animation: pulse-glow 1s ease-in-out;
	}

	@keyframes pulse-glow {
		0%, 100% { box-shadow: 0 0 0 3px var(--accent-glow); }
		50% { box-shadow: 0 0 0 6px var(--accent-glow); }
	}

	.fact-text {
		font-size: 0.938rem;
		line-height: 1.6;
		color: var(--text-primary);
		margin-bottom: 0.75rem;
	}

	.fact-id {
		font-size: 0.688rem;
		font-weight: 600;
		color: var(--text-muted);
		margin-right: 0.25rem;
	}

	.fact-source a {
		color: var(--accent);
		font-size: 0.813rem;
	}

	.quote-block {
		margin-top: 0.5rem;
		font-style: italic;
		color: var(--text-secondary);
		font-size: 0.813rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
		border-left: 3px solid var(--accent);
	}

	/* Fact Persons */
	.fact-persons-section {
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--border-card);
	}

	.persons-toggle {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		padding: 0.375rem 0.75rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-sm);
		cursor: pointer;
		font-size: 0.75rem;
		font-family: var(--font-family);
		color: var(--text-secondary);
		transition: border-color var(--transition-fast);
	}

	.persons-toggle:hover {
		border-color: var(--border-card-hover);
	}

	.fact-persons-grid {
		margin-top: 0.5rem;
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
		gap: 0.5rem;
	}

	.person-chip {
		display: flex;
		justify-content: space-between;
		padding: 0.5rem 0.75rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-sm);
		color: inherit;
		font-size: 0.75rem;
		transition: border-color var(--transition-fast);
	}

	.person-chip:hover {
		border-color: var(--accent);
	}

	.person-chip-name {
		color: var(--text-primary);
		text-transform: capitalize;
	}

	.person-chip-score {
		color: var(--text-muted);
	}

	.load-more-inline {
		grid-column: 1 / -1;
		padding: 0.375rem;
		background: transparent;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-sm);
		cursor: pointer;
		color: var(--accent);
		font-size: 0.75rem;
		font-family: var(--font-family);
		transition: background var(--transition-fast);
	}

	.load-more-inline:hover {
		background: var(--bg-secondary);
	}

	.empty-mini {
		grid-column: 1 / -1;
		text-align: center;
		color: var(--text-muted);
		font-size: 0.75rem;
		padding: 0.5rem;
	}

	/* Stance Card */
	.stance-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.75rem;
		gap: 0.75rem;
	}

	.stance-label {
		font-size: 0.938rem;
		font-weight: 600;
		color: var(--text-primary);
		text-transform: capitalize;
	}

	.stance-badge {
		font-size: 0.688rem;
		font-weight: 500;
		color: var(--accent);
		background: rgba(59, 130, 246, 0.12);
		padding: 0.188rem 0.625rem;
		border-radius: var(--radius-full);
		white-space: nowrap;
		flex-shrink: 0;
	}

	.stance-summary {
		font-size: 0.875rem;
		line-height: 1.6;
		color: var(--text-secondary);
		margin-bottom: 0.5rem;
	}

	.stance-meta {
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	/* Bundle (stance-grouped contradictions) */
	.bundle-header {
		margin-bottom: 0.625rem;
	}

	.bundle-title {
		display: flex;
		align-items: center;
		gap: 0.625rem;
		font-size: 0.938rem;
		color: var(--text-primary);
		margin-bottom: 0.25rem;
	}

	.bundle-badge {
		font-size: 0.688rem;
		font-weight: 600;
		color: var(--accent);
		background: rgba(59, 130, 246, 0.12);
		padding: 0.188rem 0.625rem;
		border-radius: var(--radius-full);
		white-space: nowrap;
	}

	.bundle-badge-cross {
		color: #b794f4;
		background: rgba(183, 148, 244, 0.14);
	}

	.bundle-badge-warn {
		color: #f87171;
		background: rgba(248, 113, 113, 0.14);
	}

	.cross-topic-arrow {
		color: var(--text-secondary);
		margin: 0 0.25rem;
		font-weight: 500;
	}

	.bundle-summary {
		font-size: 0.875rem;
		line-height: 1.5;
		color: var(--text-secondary);
	}

	.bundle-toggle {
		margin-bottom: 0.625rem;
	}

	.bundle-pair {
		border-top: 1px dashed var(--border-color, #2a2a2a);
		padding-top: 0.75rem;
		margin-top: 0.75rem;
	}

	.bundle-pair:first-of-type {
		border-top: none;
		padding-top: 0;
		margin-top: 0;
	}

	/* Contradiction Card */
	.contradiction-header {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}
	.contradiction-header .contradiction-date {
		margin-left: auto;
	}

	.contradiction-id {
		font-size: 0.688rem;
		font-weight: 600;
		color: var(--text-muted);
		background: var(--bg-secondary);
		padding: 0.125rem 0.5rem;
		border-radius: var(--radius-sm);
	}

	.contradiction-date {
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.contradiction-reason {
		font-size: 0.938rem;
		line-height: 1.6;
		color: var(--text-primary);
		margin-bottom: 1rem;
	}

	.facts-comparison {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.75rem;
	}

	.comp-fact {
		padding: 0.875rem;
		border-radius: var(--radius-sm);
		font-size: 0.813rem;
		line-height: 1.5;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.comp-a {
		background: rgba(239, 68, 68, 0.08);
		border: 1px solid rgba(239, 68, 68, 0.15);
	}

	.comp-b {
		background: rgba(245, 158, 11, 0.08);
		border: 1px solid rgba(245, 158, 11, 0.15);
	}

	.comp-label {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.comp-label strong {
		color: var(--text-primary);
	}

	.comp-date {
		font-size: 0.688rem;
		color: var(--text-muted);
	}

	.comp-text {
		color: var(--text-secondary);
		flex: 1;
	}

	.comp-links {
		display: flex;
		gap: 0.75rem;
		align-items: center;
		margin-top: auto;
	}

	.link-btn {
		font-size: 0.688rem;
		color: var(--accent);
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		font-family: var(--font-family);
		transition: color var(--transition-fast);
	}

	.link-btn:hover {
		color: var(--accent-hover);
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

	.load-more-btn:hover:not(:disabled) {
		background: var(--accent);
		color: white;
		box-shadow: var(--shadow-glow);
	}

	.load-more-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	/* Skeleton */
	.skeleton-hero {
		display: flex;
		align-items: center;
		gap: 1.25rem;
	}

	.skeleton-avatar-lg {
		width: 80px;
		height: 80px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.skeleton-hero-text {
		flex: 1;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.skeleton-title {
		height: 24px;
		width: 50%;
	}

	.skeleton-subtitle {
		height: 14px;
		width: 30%;
	}

	.skeleton-facts {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		margin-top: 1.5rem;
	}

	.skeleton-line-long {
		height: 14px;
		width: 90%;
		margin-bottom: 0.5rem;
	}

	.skeleton-line-short {
		height: 12px;
		width: 40%;
	}

	@media (max-width: 640px) {
		.facts-comparison {
			grid-template-columns: 1fr;
		}

		.person-hero {
			flex-direction: column;
			align-items: flex-start;
		}

		.hero-gauge {
			align-self: center;
		}
	}
</style>
