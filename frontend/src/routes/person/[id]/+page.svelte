<script lang="ts">
	import { page } from '$app/stores';
	import { onMount, onDestroy } from 'svelte';
	import type { PersonSummary, FactWithSource, ContradictionWithFacts } from '$lib/types';
	import { realtimeManager, subscribeToPersonEvents, clearOldEvents } from '$lib/realtime';
	import { API_BASE } from '$lib/config';

	interface MemberInfo {
		id: number;
		name: string;
		entity_type: string;
		honesty_score: number;
		image_url?: string;
	}

	let person: PersonSummary | null = null;
	let facts: FactWithSource[] = [];
	let contradictions: ContradictionWithFacts[] = [];
	let members: MemberInfo[] = [];
	let loading = true;
	let error: string | null = null;
	let activeTab = 'facts';
	let membersExpanded = false;

	// Pagination state
	let factsPage = 0;
	let contradictionsPage = 0;
	let factsPageSize = 25;
	let contradictionsPageSize = 25;
	let hasMoreFacts = true;
	let hasMoreContradictions = true;

	// Fact persons state (for party facts dropdown)
	let expandedFacts: Record<number, boolean> = {};
	let factPersons: Record<number, PersonSummary[]> = {};
	let factPersonsPage: Record<number, number> = {};
	let factPersonsHasMore: Record<number, boolean> = {};
	const personsPageSize = 10;

	// Highlighted fact state (for linking from contradictions)
	let highlightedFactId: number | null = null;

	async function goToFact(factId: number) {
		// Switch to facts tab
		activeTab = 'facts';

		// Check if fact is already loaded
		const factExists = facts.some(f => f.fact_id === factId);

		if (!factExists) {
			// Load all facts to find this one (reset pagination)
			await loadFacts(0);
		}

		// Set highlighted fact
		highlightedFactId = factId;

		// Wait for DOM update then scroll to fact
		setTimeout(() => {
			const element = document.getElementById(`fact-${factId}`);
			if (element) {
				element.scrollIntoView({ behavior: 'smooth', block: 'center' });
			}

			// Remove highlight after animation
			setTimeout(() => {
				highlightedFactId = null;
			}, 2000);
		}, 100);
	}

	$: personId = Number($page.params.id);

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

			const data = await response.json();
			facts = page === 0 ? data : [...facts, ...data];
			hasMoreFacts = data.length === factsPageSize;
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

			const data = await response.json();
			contradictions = page === 0 ? data : [...contradictions, ...data];
			hasMoreContradictions = data.length === contradictionsPageSize;
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
			console.error('Failed to load members:', err);
			members = [];
		}
	}

	async function loadData() {
		loading = true;
		error = null;

		await Promise.all([
			loadPerson(),
			loadFacts(),
			loadContradictions()
		]);

		// Load members if this is a party
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
			const response = await fetch(
				`${API_BASE}/api/facts/${factId}/persons?limit=${personsPageSize}&offset=${offset}`
			);

			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}

			const data = await response.json();

			// Initialize or append data
			if (page === 0) {
				factPersons[factId] = data;
			} else {
				factPersons[factId] = [...(factPersons[factId] || []), ...data];
			}

			// Update pagination state
			factPersonsPage[factId] = page;
			factPersonsHasMore[factId] = data.length === personsPageSize;

			// Trigger reactivity
			factPersons = factPersons;
		} catch (err) {
			console.error('Failed to load persons for fact:', err);
		}
	}

	async function toggleFactPersons(factId: number) {
		const isCurrentlyExpanded = expandedFacts[factId];

		if (!isCurrentlyExpanded) {
			// Expanding - fetch data if not already loaded
			if (!factPersons[factId]) {
				await loadFactPersons(factId, 0);
			}
		}

		// Toggle state
		expandedFacts[factId] = !isCurrentlyExpanded;
		expandedFacts = expandedFacts; // Trigger reactivity
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
		if (score >= 0.8) return '#22c55e'; // green
		if (score >= 0.4) return '#f59e0b'; // amber
		return '#ef4444'; // red
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

	function handleRealtimeEvent(event: any) {
		console.log('Person page realtime event:', event);

		// Refresh person data and current tab data
		loadPerson();

		if (activeTab === 'facts') {
			loadFacts(0); // Reload facts from the beginning
		} else if (activeTab === 'contradictions') {
			loadContradictions(0); // Reload contradictions from the beginning
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

<div class="container">
	<div class="header">
		<a href="/" class="back-link">← Back to all persons</a>
		{#if loading}
			<h1>Loading...</h1>
		{:else if person}
			<div class="person-header">
				<div class="person-title-row">
					{#if person.image_url}
						<img src={person.image_url} alt={person.name} class="avatar avatar-96" />
					{:else}
						<div class="avatar avatar-96 avatar-placeholder">
							{person.name.charAt(0).toUpperCase()}
						</div>
					{/if}
					<h1>
						{person.name}
						{#if person.entity_type === 'party'}
							<span class="party-badge">Party</span>
						{/if}
					</h1>
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

				{#if person.entity_type === 'party' && members.length > 0}
					<div class="members-section">
						<button
							class="members-toggle"
							on:click={() => membersExpanded = !membersExpanded}
						>
							<span class="toggle-icon">{membersExpanded ? '▼' : '▶'}</span>
							{members.length} members
						</button>

						{#if membersExpanded}
							<div class="members-list">
								{#each members as member (member.id)}
									<a href="/person/{member.id}" class="member-item">
										{#if member.image_url}
											<img src={member.image_url} alt={member.name} class="avatar avatar-24" />
										{:else}
											<div class="avatar avatar-24 avatar-placeholder avatar-placeholder-sm">
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
			</div>
		{:else if error}
			<div class="error">
				<h1>Error</h1>
				<p>{error}</p>
				<button on:click={loadData}>Retry</button>
			</div>
		{/if}
	</div>

	{#if person}
		<div class="tabs">
			<button
				class="tab-button {activeTab === 'facts' ? 'active' : ''}"
				on:click={() => activeTab = 'facts'}
			>
				Facts ({facts.length}{hasMoreFacts ? '+' : ''})
			</button>
			<button
				class="tab-button {activeTab === 'contradictions' ? 'active' : ''}"
				on:click={() => activeTab = 'contradictions'}
			>
				Contradictions ({contradictions.length}{hasMoreContradictions ? '+' : ''})
			</button>
		</div>

		<div class="tab-content">
			{#if activeTab === 'facts'}
				<div class="facts-section">
					{#if facts.length === 0 && !loading}
						<p class="empty-state">No facts found for this person.</p>
					{:else}
						<div class="facts-list">
							{#each facts as fact (fact.fact_id)}
								<div id="fact-{fact.fact_id}" class="fact-card" class:highlighted={highlightedFactId === fact.fact_id}>
									<div class="fact-text"><span class="fact-id">[{fact.fact_id}]</span> {fact.fact_text}</div>
									<div class="fact-source">
										<a href="{fact.source_url}" target="_blank" rel="noopener noreferrer">
											{fact.source_url}
										</a>
										{#if fact.original_quote}
											<div class="quote-preview">
												"{fact.original_quote.length > 200 ? fact.original_quote.substring(0, 200) + '...' : fact.original_quote}"
											</div>
										{/if}
									</div>

									{#if person?.entity_type === 'party'}
										<div class="fact-persons-section">
											<button
												class="persons-toggle"
												on:click={() => toggleFactPersons(fact.fact_id)}
											>
												<span class="toggle-icon">
													{expandedFacts[fact.fact_id] ? '▼' : '▶'}
												</span>
												{#if factPersons[fact.fact_id]}
													Show {factPersons[fact.fact_id].length}+ persons
												{:else}
													Show persons
												{/if}
											</button>

											{#if expandedFacts[fact.fact_id]}
												<div class="persons-list">
													{#if factPersons[fact.fact_id]?.length > 0}
														<div class="persons-grid">
															{#each factPersons[fact.fact_id] as p (p.id)}
																<a href="/person/{p.id}" class="person-item">
																	<div class="person-name">{p.name}</div>
																	<div class="person-score">
																		Score: {p.honesty_score.toFixed(2)}
																	</div>
																</a>
															{/each}
														</div>

														{#if factPersonsHasMore[fact.fact_id]}
															<button
																class="load-more-persons"
																on:click={() => loadMorePersons(fact.fact_id)}
															>
																Load More Persons
															</button>
														{/if}
													{:else}
														<div class="no-persons">No persons found</div>
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
								<button on:click={loadMoreFacts} disabled={loading}>
									{loading ? 'Loading...' : 'Load More Facts'}
								</button>
							</div>
						{/if}
					{/if}
				</div>
			{:else if activeTab === 'contradictions'}
				<div class="contradictions-section">
					{#if contradictions.length === 0 && !loading}
						<p class="empty-state">No contradictions found for this person.</p>
					{:else}
						<div class="contradictions-list">
							{#each contradictions as contradiction (contradiction.id)}
								<div class="contradiction-card">
									<div class="contradiction-header">
										<span class="contradiction-id">#{contradiction.id}</span>
									</div>
									<div class="contradiction-reason">
										<strong>Contradiction:</strong> {contradiction.reason}
									</div>
									<div class="contradiction-date">
										Detected on {formatDate(contradiction.detected_at)}
									</div>
									<div class="facts-comparison">
										<div class="fact-a">
											<div class="fact-header">
												<strong>Statement A</strong>
												{#if contradiction.fact_a_date}
													<span class="fact-date">{formatDateWithTime(contradiction.fact_a_date)}</span>
												{/if}
											</div>
											<div class="fact-content"><span class="fact-id">[{contradiction.fact_a_id}]</span> {contradiction.fact_a_text}</div>
											<div class="fact-links">
												<button class="fact-link-btn" on:click={() => goToFact(contradiction.fact_a_id)}>
													View in Facts
												</button>
												{#if contradiction.fact_a_source_url}
													<a href={contradiction.fact_a_source_url} target="_blank" rel="noopener noreferrer" class="fact-source-link">
														View source
													</a>
												{/if}
											</div>
										</div>
										<div class="fact-b">
											<div class="fact-header">
												<strong>Statement B</strong>
												{#if contradiction.fact_b_date}
													<span class="fact-date">{formatDateWithTime(contradiction.fact_b_date)}</span>
												{/if}
											</div>
											<div class="fact-content"><span class="fact-id">[{contradiction.fact_b_id}]</span> {contradiction.fact_b_text}</div>
											<div class="fact-links">
												<button class="fact-link-btn" on:click={() => goToFact(contradiction.fact_b_id)}>
													View in Facts
												</button>
												{#if contradiction.fact_b_source_url}
													<a href={contradiction.fact_b_source_url} target="_blank" rel="noopener noreferrer" class="fact-source-link">
														View source
													</a>
												{/if}
											</div>
										</div>
									</div>
								</div>
							{/each}
						</div>

						{#if hasMoreContradictions}
							<div class="load-more">
								<button on:click={loadMoreContradictions} disabled={loading}>
									{loading ? 'Loading...' : 'Load More Contradictions'}
								</button>
							</div>
						{/if}
					{/if}
				</div>
			{/if}
		</div>
	{/if}
</div>

<style>
	.container {
		max-width: 1000px;
		margin: 0 auto;
		padding: 2rem;
		font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
	}

	.header {
		margin-bottom: 2rem;
	}

	.back-link {
		color: #3b82f6;
		text-decoration: none;
		font-size: 0.875rem;
		margin-bottom: 1rem;
		display: inline-block;
	}

	.back-link:hover {
		text-decoration: underline;
	}

	.person-title-row {
		display: flex;
		align-items: center;
		gap: 1.5rem;
		margin-bottom: 1rem;
	}

	.avatar {
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
	}

	.avatar-96 {
		width: 96px;
		height: 96px;
	}

	.avatar-24 {
		width: 24px;
		height: 24px;
	}

	.avatar-placeholder {
		display: flex;
		align-items: center;
		justify-content: center;
		background: #e5e7eb;
		color: #6b7280;
		font-weight: 600;
		font-size: 2rem;
	}

	.avatar-placeholder-sm {
		font-size: 0.75rem;
	}

	.person-header h1 {
		margin: 0;
		color: #1f2937;
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.party-badge {
		background: #dbeafe;
		color: #1e40af;
		font-size: 0.75rem;
		font-weight: 500;
		padding: 0.25rem 0.5rem;
		border-radius: 0.25rem;
	}

	.honesty-score {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		max-width: 300px;
	}

	.score-bar {
		height: 12px;
		background: #e5e7eb;
		border-radius: 6px;
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

	.error {
		background: #fee2e2;
		border: 1px solid #fecaca;
		border-radius: 0.5rem;
		padding: 1rem;
	}

	.error h1 {
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

	.tabs {
		display: flex;
		border-bottom: 1px solid #e5e7eb;
		margin-bottom: 2rem;
	}

	.tab-button {
		background: none;
		border: none;
		padding: 1rem 2rem;
		font-size: 1rem;
		font-weight: 500;
		color: #6b7280;
		cursor: pointer;
		border-bottom: 2px solid transparent;
		transition: all 0.2s;
	}

	.tab-button.active {
		color: #1f2937;
		border-bottom-color: #3b82f6;
	}

	.tab-button:hover:not(.active) {
		color: #374151;
	}

	.tab-content {
		min-height: 400px;
	}

	.empty-state {
		text-align: center;
		color: #6b7280;
		font-style: italic;
		padding: 3rem;
	}

	.facts-list, .contradictions-list {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.fact-card, .contradiction-card {
		background: white;
		border: 1px solid #e5e7eb;
		border-radius: 0.5rem;
		padding: 1.5rem;
		box-shadow: 0 1px 3px rgba(0, 0, 0, 0.1);
		transition: background-color 0.3s, border-color 0.3s, box-shadow 0.3s;
	}

	.fact-card.highlighted {
		background: #fef3c7;
		border-color: #f59e0b;
		box-shadow: 0 0 0 3px rgba(245, 158, 11, 0.3);
		animation: pulse-highlight 1s ease-in-out;
	}

	@keyframes pulse-highlight {
		0%, 100% { box-shadow: 0 0 0 3px rgba(245, 158, 11, 0.3); }
		50% { box-shadow: 0 0 0 6px rgba(245, 158, 11, 0.2); }
	}

	.fact-text {
		font-size: 1rem;
		line-height: 1.6;
		color: #1f2937;
		margin-bottom: 1rem;
	}

	.fact-id {
		font-size: 0.75rem;
		font-weight: 600;
		color: #6b7280;
		margin-right: 0.25rem;
	}

	.fact-source a {
		color: #3b82f6;
		text-decoration: none;
		font-size: 0.875rem;
	}

	.fact-source a:hover {
		text-decoration: underline;
	}

	.quote-preview {
		margin-top: 0.5rem;
		font-style: italic;
		color: #6b7280;
		font-size: 0.875rem;
		padding: 0.5rem;
		background: #f9fafb;
		border-radius: 0.25rem;
		border-left: 3px solid #3b82f6;
	}

	.contradiction-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.5rem;
	}

	.contradiction-id {
		font-size: 0.75rem;
		font-weight: 600;
		color: #6b7280;
		background: #f3f4f6;
		padding: 0.25rem 0.5rem;
		border-radius: 0.25rem;
	}

	.contradiction-reason {
		font-size: 1rem;
		line-height: 1.6;
		color: #1f2937;
		margin-bottom: 0.5rem;
	}

	.contradiction-date {
		font-size: 0.875rem;
		color: #6b7280;
		margin-bottom: 1rem;
	}

	.facts-comparison {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
	}

	.fact-a, .fact-b {
		padding: 1rem;
		border-radius: 0.25rem;
		font-size: 0.875rem;
		line-height: 1.5;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.fact-a {
		background: #fef2f2;
		border: 1px solid #fecaca;
	}

	.fact-b {
		background: #fef3c7;
		border: 1px solid #fde68a;
	}

	.fact-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem;
	}

	.fact-date {
		font-size: 0.75rem;
		color: #6b7280;
		font-weight: normal;
	}

	.fact-content {
		flex: 1;
	}

	.fact-links {
		display: flex;
		gap: 1rem;
		align-items: center;
		margin-top: auto;
	}

	.fact-link-btn {
		font-size: 0.75rem;
		color: #3b82f6;
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		text-decoration: none;
	}

	.fact-link-btn:hover {
		text-decoration: underline;
	}

	.fact-source-link {
		font-size: 0.75rem;
		color: #3b82f6;
		text-decoration: none;
		margin-top: auto;
	}

	.fact-source-link:hover {
		text-decoration: underline;
	}

	.load-more {
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

	.load-more button:hover:not(:disabled) {
		background: #2563eb;
	}

	.load-more button:disabled {
		background: #9ca3af;
		cursor: not-allowed;
	}

	/* Members section styles */
	.members-section {
		margin-top: 1.5rem;
	}

	.members-toggle {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		background: #f3f4f6;
		border: 1px solid #e5e7eb;
		padding: 0.75rem 1rem;
		border-radius: 0.5rem;
		cursor: pointer;
		font-size: 0.875rem;
		font-weight: 500;
		color: #374151;
		transition: background-color 0.2s;
		width: 100%;
		text-align: left;
	}

	.members-toggle:hover {
		background: #e5e7eb;
	}

	.toggle-icon {
		font-size: 0.75rem;
		color: #6b7280;
	}

	.members-list {
		margin-top: 0.5rem;
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
		gap: 0.5rem;
		max-height: 400px;
		overflow-y: auto;
		padding: 0.5rem;
		background: #f9fafb;
		border: 1px solid #e5e7eb;
		border-radius: 0.5rem;
	}

	.member-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.5rem 0.75rem;
		background: white;
		border: 1px solid #e5e7eb;
		border-radius: 0.375rem;
		text-decoration: none;
		color: inherit;
		font-size: 0.875rem;
		transition: background-color 0.2s, border-color 0.2s;
	}

	.member-item:hover {
		background: #f3f4f6;
		border-color: #3b82f6;
	}

	.member-name {
		color: #1f2937;
		text-transform: capitalize;
		flex: 1;
	}

	.member-score {
		font-weight: 600;
		font-size: 0.75rem;
	}

	/* Fact persons section styles */
	.fact-persons-section {
		margin-top: 1rem;
		padding-top: 1rem;
		border-top: 1px solid #e5e7eb;
	}

	.persons-toggle {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		padding: 0.5rem 1rem;
		background: #f9fafb;
		border: 1px solid #e5e7eb;
		border-radius: 0.375rem;
		cursor: pointer;
		font-size: 0.875rem;
		color: #374151;
		transition: all 0.2s;
		width: auto;
	}

	.persons-toggle:hover {
		background: #f3f4f6;
		border-color: #d1d5db;
	}

	.persons-list {
		margin-top: 0.75rem;
		padding: 1rem;
		background: #f9fafb;
		border-radius: 0.375rem;
	}

	.persons-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
		gap: 0.75rem;
		margin-bottom: 1rem;
	}

	.person-item {
		padding: 0.75rem;
		background: white;
		border: 1px solid #e5e7eb;
		border-radius: 0.375rem;
		text-decoration: none;
		color: inherit;
		transition: all 0.2s;
	}

	.person-item:hover {
		border-color: #3b82f6;
		box-shadow: 0 2px 8px rgba(59, 130, 246, 0.1);
	}

	.person-name {
		font-weight: 500;
		color: #1f2937;
		margin-bottom: 0.25rem;
		text-transform: capitalize;
	}

	.person-score {
		font-size: 0.75rem;
		color: #6b7280;
	}

	.load-more-persons {
		width: 100%;
		padding: 0.5rem;
		background: white;
		border: 1px solid #e5e7eb;
		border-radius: 0.375rem;
		cursor: pointer;
		color: #3b82f6;
		font-size: 0.875rem;
		transition: background-color 0.2s;
	}

	.load-more-persons:hover {
		background: #f9fafb;
	}

	.no-persons {
		padding: 1rem;
		text-align: center;
		color: #6b7280;
		font-size: 0.875rem;
	}
</style>