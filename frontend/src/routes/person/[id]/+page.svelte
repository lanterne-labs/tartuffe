<script lang="ts">
	import { page } from '$app/stores';
	import { onMount, onDestroy } from 'svelte';
	import type { PersonSummary, FactWithSource, BundledContradiction, PaginatedResponse, StanceDocument } from '$lib/types';
	import { realtimeManager, subscribeToPersonEvents, clearOldEvents } from '$lib/realtime';
	import { API_BASE, TALLY_FORM_ID } from '$lib/config';
	import { lang, tr, pickLang, type Lang } from '$lib/i18n';
	import ScoreGauge from '$lib/components/ScoreGauge.svelte';
	import BundledContradictionCard from '$lib/components/BundledContradictionCard.svelte';

	let currentLang = $state<Lang>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	// Per-stance Facts cache, fetched lazily when the user opens the Facts tab on a stance.
	interface StanceFact {
		fact_id: number;
		fact_text: string;
		fact_text_en?: string;
		lang?: string;
		canonical_verb?: string;
		source_url: string;
		original_quote?: string;
	}
	let stanceFacts: Record<number, StanceFact[]> = $state({});
	let stanceFactsLoading: Record<number, boolean> = $state({});
	// Which tab is active inside each expanded stance card. Defaults to 'contradictions'
	// (matches the user's request) and switches to 'facts' on tab click. Per-stance so
	// different cards can be on different tabs at once.
	type StanceTab = 'contradictions' | 'facts';
	let stanceTab: Record<number, StanceTab> = $state({});

	function pickFactText(f: StanceFact | FactWithSource): string {
		return pickLang(f.fact_text, f.fact_text_en, currentLang);
	}

	interface MemberInfo {
		id: number;
		name: string;
		entity_type: string;
		honesty_score: number;
		image_url?: string;
	}

	type StanceSort = 'worst' | 'recent' | 'most_facts';

	let person: PersonSummary | null = $state(null);
	let facts: FactWithSource[] = $state([]);
	let members: MemberInfo[] = $state([]);
	let stances: StanceDocument[] = $state([]);
	let loading = $state(true);
	let error: string | null = $state(null);
	let membersExpanded = $state(false);

	// Stance-first profile state.
	let stanceSort: StanceSort = $state('worst');
	let expandedStances: Record<number, boolean> = $state({});
	let stanceContradictions: Record<number, BundledContradiction[]> = $state({});
	let stanceLoading: Record<number, boolean> = $state({});
	// How many contradictions to render per expanded stance. Starts small so a stance with
	// 30+ paraphrase pairs doesn't dump the whole list at once; "Show N more" / "Show all"
	// buttons grow it from there.
	let stanceContradictionsShown: Record<number, number> = $state({});
	const INITIAL_CONTRADICTIONS_SHOWN = 3;
	const CONTRADICTIONS_MORE_STEP = 5;

	// Facts disclosure (de-emphasized — full list hidden behind a toggle).
	let factsExpanded = $state(false);
	let factsPage = $state(0);
	let factsPageSize = 25;
	let hasMoreFacts = $state(true);
	let expandedFacts: Record<number, boolean> = $state({});

	// Stable client-side sort. Worst-first is the default — surfaces the highest-signal
	// stances at the top — but the user can pivot to most-recent or most-active.
	let sortedStances = $derived.by(() => {
		const arr = [...stances];
		switch (stanceSort) {
			case 'worst':
				arr.sort((a, b) => a.score - b.score || b.fact_count - a.fact_count);
				break;
			case 'recent':
				arr.sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
				break;
			case 'most_facts':
				arr.sort((a, b) => b.fact_count - a.fact_count || a.score - b.score);
				break;
		}
		return arr;
	});
	let factPersons: Record<number, PersonSummary[]> = $state({});
	let factPersonsPage: Record<number, number> = $state({});
	let factPersonsHasMore: Record<number, boolean> = $state({});
	const personsPageSize = 10;

	let highlightedFactId: number | null = $state(null);

	let personId = $derived(Number($page.params.id));

	async function loadStanceFacts(stanceId: number) {
		stanceFactsLoading[stanceId] = true;
		try {
			const response = await fetch(`${API_BASE}/api/stances/${stanceId}/facts`);
			if (!response.ok) throw new Error(`HTTP ${response.status}`);
			stanceFacts[stanceId] = (await response.json()) ?? [];
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		} finally {
			stanceFactsLoading[stanceId] = false;
		}
	}

	function setStanceTab(stanceId: number, tab: StanceTab) {
		stanceTab[stanceId] = tab;
		if (tab === 'facts' && stanceFacts[stanceId] === undefined) {
			loadStanceFacts(stanceId);
		}
	}

	async function goToFact(factId: number) {
		factsExpanded = true;

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
			hasMoreFacts = facts.length < data.total;
			factsPage = page;
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		}
	}

	async function loadStanceContradictions(stanceId: number) {
		stanceLoading[stanceId] = true;
		try {
			const response = await fetch(`${API_BASE}/api/stances/${stanceId}/contradictions`);
			if (!response.ok) {
				throw new Error(`HTTP error! status: ${response.status}`);
			}
			const items: BundledContradiction[] = await response.json();
			stanceContradictions[stanceId] = items;
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		} finally {
			stanceLoading[stanceId] = false;
		}
	}

	async function toggleStance(stanceId: number) {
		const wasOpen = expandedStances[stanceId];
		expandedStances[stanceId] = !wasOpen;
		if (!wasOpen) {
			if (stanceContradictionsShown[stanceId] === undefined) {
				stanceContradictionsShown[stanceId] = INITIAL_CONTRADICTIONS_SHOWN;
			}
			if (stanceContradictions[stanceId] === undefined) {
				await loadStanceContradictions(stanceId);
			}
		}
	}

	function onStanceCardKeydown(e: KeyboardEvent, stanceId: number) {
		if (e.key === 'Enter' || e.key === ' ') {
			e.preventDefault();
			toggleStance(stanceId);
		}
	}

	function showMoreInStance(stanceId: number, total: number) {
		const current = stanceContradictionsShown[stanceId] ?? INITIAL_CONTRADICTIONS_SHOWN;
		stanceContradictionsShown[stanceId] = Math.min(total, current + CONTRADICTIONS_MORE_STEP);
	}

	function showAllInStance(stanceId: number, total: number) {
		stanceContradictionsShown[stanceId] = total;
	}

	function toggleFacts() {
		factsExpanded = !factsExpanded;
		if (factsExpanded && facts.length === 0 && hasMoreFacts) {
			loadFacts(0);
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

		// Load only what's needed for the headline view: the person summary (with stance/
		// contradiction counts for the summary line) and the stance list. Facts are lazy-
		// loaded when the user opens the facts disclosure; per-stance contradictions are
		// lazy-loaded when a stance card is expanded.
		await Promise.all([loadPerson(), loadStances()]);

		if (person?.entity_type === 'party') {
			await loadMembers();
		}

		loading = false;
	}

	function loadMoreFacts() {
		loadFacts(factsPage + 1);
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

	function getHonestyLabel(score: number, l: Lang): string {
		if (score >= 0.8) return tr('person.honest', l);
		if (score >= 0.4) return tr('person.mixed', l);
		return tr('person.dishonest', l);
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
		const previewSrc = pickFactText(fact);
		const preview = previewSrc.length > 80 ? previewSrc.slice(0, 80) + '…' : previewSrc;
		openTally({
			target_kind: 'fact',
			fact_id: fact.fact_id,
			contradiction_id: '',
			stance_id: '',
			target_label: `Fact #${fact.fact_id}: ${preview}`,
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
		// Refresh the headline view (person counts + stance scores). Stance contradictions
		// and the facts list are reloaded lazily on next expand to avoid clobbering the
		// user's current open panels mid-interaction.
		loadPerson();
		loadStances();
		// Drop any cached per-stance contradictions so the next expand re-fetches.
		stanceContradictions = {};
		if (factsExpanded) {
			loadFacts(0);
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
			<button class="retry-btn" onclick={loadData}>{tr('person.retry', currentLang)}</button>
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
							<span class="badge">{tr('person.party_badge', currentLang)}</span>
						{/if}
					</h1>
					<p class="hero-label">{getHonestyLabel(person.honesty_score, currentLang)} — {(person.honesty_score * 100).toFixed(1)}%</p>
					<p class="hero-summary">
						{person.stance_count} {tr(person.stance_count === 1 ? 'count.stance' : 'count.stances', currentLang)}
						<span class="dot">·</span>
						{person.contradiction_count} {tr(person.contradiction_count === 1 ? 'count.contradiction' : 'count.contradictions', currentLang)} {tr('count.over', currentLang)} {person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)}
					</p>
				</div>
			</div>
			<div class="hero-gauge">
				<ScoreGauge score={person.honesty_score} size={80} showLabel label={tr('person.score', currentLang)} />
			</div>
		</div>

		{#if person.entity_type !== 'human' && person.members_score != null}
			<div class="score-breakdown">
				<ScoreGauge score={person.own_score ?? person.honesty_score} size={56} showLabel label="Own Score" />
				<ScoreGauge score={person.members_score} size={56} showLabel label="Members Score" />
				<ScoreGauge score={person.honesty_score} size={56} showLabel label="Aggregate" />
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

		<!-- Stances are now the headline content. Each card carries a per-stance honesty
		     gauge, computed with the same formula as the global score, scoped to the stance's
		     facts and bundled contradictions. Expanding a card lazy-loads the contradictions
		     within that stance — that's where the contradictions live now (no separate tab). -->
		<section class="stances-section">
			<div class="stances-toolbar">
				<h2 class="section-heading">{tr('person.stances', currentLang)}</h2>
				{#if stances.length > 1}
					<div class="sort-control">
						<label for="stance-sort">{tr('person.sort_by', currentLang)}</label>
						<select id="stance-sort" bind:value={stanceSort}>
							<option value="worst">{tr('person.sort.worst', currentLang)}</option>
							<option value="recent">{tr('person.sort.recent', currentLang)}</option>
							<option value="most_facts">{tr('person.sort.most_facts', currentLang)}</option>
						</select>
					</div>
				{/if}
			</div>

			{#if stances.length === 0 && !loading}
				<p class="empty-state">{tr('person.no_stances', currentLang)}</p>
			{:else}
				<div class="items-list">
					{#each sortedStances as stance (stance.id)}
						{@const hasContradictions = stance.contradiction_count > 0}
						{@const isOpen = !!expandedStances[stance.id]}
						{@const items = stanceContradictions[stance.id] ?? []}
						{@const shown = stanceContradictionsShown[stance.id] ?? INITIAL_CONTRADICTIONS_SHOWN}
						{@const remaining = Math.max(0, items.length - shown)}
						<div class="card stance-card" class:open={isOpen}>
							<!-- The whole header is one click target — tap anywhere on the card top to
							     toggle. Interactive children inside (the report button) stop propagation
							     so they don't also flip the toggle. The contradictions panel below sits
							     outside this clickable area, so clicking sources/links inside it never
							     toggles either. The Svelte a11y check can't tell that role+tabindex are
							     paired conditionally, so it warns even though the runtime behaviour is
							     correct (tabindex is only set when role="button" is also set). -->
							<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
							<div
								class="stance-clickable"
								role="button"
								tabindex={0}
								aria-expanded={isOpen}
								onclick={() => toggleStance(stance.id)}
								onkeydown={(e) => onStanceCardKeydown(e, stance.id)}
							>
								<div class="stance-top">
									<div class="stance-main">
										<div class="stance-label-row">
											<span class="stance-label">{pickLang(stance.label, stance.label_en, currentLang)}</span>
											<svg class="stance-chevron" class:expanded={isOpen} width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round">
												<path d="m6 9 6 6 6-6"/>
											</svg>
										</div>
										{#if stance.summary}
											<div class="stance-summary">{pickLang(stance.summary, stance.summary_en, currentLang)}</div>
										{/if}
										<div class="stance-meta">
											<span>{stance.fact_count} {tr(stance.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)}</span>
											<span class="dot">·</span>
											<span>{stance.contradiction_count} {tr(stance.contradiction_count === 1 ? 'count.contradiction' : 'count.contradictions', currentLang)}</span>
											<span class="dot">·</span>
											<span class="muted">{tr('person.updated', currentLang)} {formatDate(stance.updated_at)}</span>
											{#if !hasContradictions}
												<span class="no-contradictions-chip">{tr('person.no_contradictions_chip', currentLang)}</span>
											{/if}
											{#if TALLY_FORM_ID}
												<button
													class="meta-report-btn"
													title="Report this stance"
													aria-label="Report this stance"
													onclick={(e) => { e.stopPropagation(); reportStance(stance); }}
												>
													<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
														<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/>
														<line x1="12" y1="9" x2="12" y2="13"/>
														<line x1="12" y1="17" x2="12.01" y2="17"/>
													</svg>
												</button>
											{/if}
										</div>
									</div>
									<ScoreGauge score={stance.score} size={56} />
								</div>
							</div>

							{#if isOpen}
								{@const tab = stanceTab[stance.id] ?? 'contradictions'}
								<div class="stance-tabs" role="tablist">
									<button
										class="stance-tab"
										class:active={tab === 'contradictions'}
										role="tab"
										aria-selected={tab === 'contradictions'}
										onclick={(e) => { e.stopPropagation(); setStanceTab(stance.id, 'contradictions'); }}
									>{tr('person.tab.contradictions', currentLang)} {#if stance.contradiction_count > 0}<span class="tab-count">{stance.contradiction_count}</span>{/if}</button>
									<button
										class="stance-tab"
										class:active={tab === 'facts'}
										role="tab"
										aria-selected={tab === 'facts'}
										onclick={(e) => { e.stopPropagation(); setStanceTab(stance.id, 'facts'); }}
									>{tr('person.tab.facts', currentLang)} <span class="tab-count">{stance.fact_count}</span></button>
								</div>
								<div class="stance-tab-body" role="tabpanel">
									{#if tab === 'contradictions'}
										{#if stanceLoading[stance.id]}
											<p class="empty-mini">{tr('person.loading', currentLang)}</p>
										{:else if items.length > 0}
											{#each items.slice(0, shown) as b, i (b.bundle_id ?? `solo-${i}`)}
												<BundledContradictionCard {b} onGoToFact={goToFact} />
											{/each}
											{#if remaining > 0}
												<div class="stance-more-row">
													<button class="link-btn" onclick={(e) => { e.stopPropagation(); showMoreInStance(stance.id, items.length); }}>
														{tr('person.show_n_more', currentLang, { n: Math.min(remaining, CONTRADICTIONS_MORE_STEP) })}
													</button>
													{#if remaining > CONTRADICTIONS_MORE_STEP}
														<button class="link-btn link-btn-muted" onclick={(e) => { e.stopPropagation(); showAllInStance(stance.id, items.length); }}>
															{tr('person.show_all_n', currentLang, { n: items.length })}
														</button>
													{/if}
												</div>
											{/if}
										{:else}
											<p class="empty-mini">{tr('person.tab.no_contradictions', currentLang)}</p>
										{/if}
									{:else}
										{#if stanceFactsLoading[stance.id]}
											<p class="empty-mini">{tr('person.loading', currentLang)}</p>
										{:else if (stanceFacts[stance.id]?.length ?? 0) > 0}
											<div class="stance-facts-list">
												{#each stanceFacts[stance.id] as f (f.fact_id)}
													<div class="stance-fact-row">
														<div class="stance-fact-text">
															<span class="fact-id">[{f.fact_id}]</span>
															{pickFactText(f)}
														</div>
														{#if f.source_url}
															<a class="stance-fact-source" href={f.source_url} target="_blank" rel="noopener noreferrer" onclick={(e) => e.stopPropagation()}>{f.source_url}</a>
														{/if}
														{#if f.original_quote}
															<div class="quote-block">"{f.original_quote.length > 200 ? f.original_quote.slice(0, 200) + '…' : f.original_quote}"</div>
														{/if}
													</div>
												{/each}
											</div>
										{:else}
											<p class="empty-mini">{tr('person.tab.no_facts', currentLang)}</p>
										{/if}
									{/if}
								</div>
							{/if}
						</div>
					{/each}
				</div>
			{/if}
		</section>

		<!-- Facts disclosure removed: facts now live inside each stance card under a
		     "Facts" tab next to "Contradictions". See stance card markup above. -->

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

	.hero-summary {
		color: var(--text-muted);
		font-size: 0.813rem;
		margin-top: 0.25rem;
	}

	.dot {
		margin: 0 0.25rem;
		color: var(--text-muted);
	}

	.muted {
		color: var(--text-muted);
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
		flex-shrink: 0;
	}

	.lang-toggle {
		display: inline-flex;
		gap: 2px;
		padding: 2px;
		background: var(--bg-card);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
		flex-shrink: 0;
		margin-left: 0.5rem;
	}
	.lang-pill {
		background: transparent;
		border: none;
		padding: 0.25rem 0.6rem;
		border-radius: var(--radius-full);
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		cursor: pointer;
		transition: all var(--transition-normal);
	}
	.lang-pill:hover {
		color: var(--text-primary);
	}
	.lang-pill.active {
		background: var(--accent);
		color: white;
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

	.empty-state {
		text-align: center;
		color: var(--text-muted);
		font-style: italic;
		padding: 3rem;
	}

	/* Stances section: the headline content of the profile. */
	.stances-section {
		margin-bottom: 1.5rem;
	}

	.stances-toolbar {
		display: flex;
		align-items: baseline;
		justify-content: space-between;
		gap: 1rem;
		margin-bottom: 0.75rem;
	}

	.section-heading {
		font-size: 1.063rem;
		font-weight: 600;
		color: var(--text-primary);
		letter-spacing: -0.01em;
	}

	.sort-control {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.75rem;
		color: var(--text-muted);
	}

	.sort-control select {
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		color: var(--text-primary);
		font-family: var(--font-family);
		font-size: 0.75rem;
		padding: 0.25rem 0.5rem;
		border-radius: var(--radius-sm);
		cursor: pointer;
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

	/* Report-issue affordance: small warning icon used on fact cards. Stance cards
	   place their report button inside the meta line (no absolute positioning) so it
	   doesn't collide with the per-stance gauge. */
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

	.meta-report-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 0;
		margin-left: auto;
		background: transparent;
		border: none;
		color: var(--text-muted);
		opacity: 0.6;
		cursor: pointer;
		transition: color var(--transition-fast), opacity var(--transition-fast);
	}
	.meta-report-btn:hover {
		color: var(--red);
		opacity: 1;
	}

	/* Stance card */
	.stance-card {
		padding: 0;
		overflow: hidden;
	}

	.stance-card.open {
		border-color: var(--border-card-hover, var(--accent));
		box-shadow: 0 0 0 1px var(--accent-glow, transparent);
	}

	/* Whole-card click target: clicking anywhere in this region toggles the stance.
	   Children that should not toggle (the report button) call stopPropagation. */
	.stance-clickable {
		display: block;
		padding: 1rem 1.25rem;
		cursor: pointer;
		transition: background var(--transition-fast);
	}

	.stance-clickable:hover {
		background: rgba(255, 255, 255, 0.02);
	}

	.stance-clickable:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: -2px;
	}

	.stance-clickable.disabled {
		cursor: default;
	}

	.stance-clickable.disabled:hover {
		background: transparent;
	}

	.stance-top {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 1rem;
	}

	.stance-main {
		flex: 1;
		min-width: 0;
	}

	.stance-label-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.stance-label {
		font-size: 0.938rem;
		font-weight: 600;
		color: var(--text-primary);
		text-transform: capitalize;
	}

	.stance-chevron {
		flex-shrink: 0;
		color: var(--text-muted);
		transition: transform var(--transition-fast), color var(--transition-fast);
	}

	.stance-chevron.expanded {
		transform: rotate(180deg);
		color: var(--text-secondary);
	}

	.stance-summary {
		font-size: 0.875rem;
		line-height: 1.55;
		color: var(--text-secondary);
		margin-top: 0.25rem;
	}

	.stance-meta {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-top: 0.5rem;
	}

	.stance-tabs {
		display: flex;
		gap: 0.25rem;
		padding: 0.5rem 1.25rem 0 1.25rem;
		border-top: 1px solid var(--glass-border);
		background: var(--bg-card);
	}
	.stance-tab {
		background: transparent;
		border: none;
		border-bottom: 2px solid transparent;
		padding: 0.5rem 0.9rem;
		font-size: 0.85rem;
		font-weight: 500;
		color: var(--text-muted);
		cursor: pointer;
		transition: color var(--transition-normal), border-color var(--transition-normal);
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
	}
	.stance-tab:hover {
		color: var(--text-primary);
	}
	.stance-tab.active {
		color: var(--accent);
		border-bottom-color: var(--accent);
	}
	.tab-count {
		display: inline-block;
		min-width: 1.25rem;
		padding: 0.05rem 0.4rem;
		text-align: center;
		font-size: 0.7rem;
		font-weight: 600;
		background: var(--bg-elevated);
		color: var(--text-muted);
		border-radius: var(--radius-full);
	}
	.stance-tab.active .tab-count {
		background: var(--accent);
		color: white;
	}
	.stance-tab-body {
		padding: 0.75rem 1.25rem 1rem 1.25rem;
		background: var(--bg-card);
		border-bottom-left-radius: var(--radius-md);
		border-bottom-right-radius: var(--radius-md);
	}
	.stance-facts-list {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}
	.stance-fact-row {
		padding: 0.6rem 0.75rem;
		background: var(--bg-elevated);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-sm);
	}
	.stance-fact-text {
		font-size: 0.9rem;
		line-height: 1.5;
		color: var(--text-primary);
		margin-bottom: 0.3rem;
	}
	.stance-fact-source {
		display: block;
		font-size: 0.75rem;
		color: var(--accent);
		word-break: break-all;
		margin-bottom: 0.3rem;
	}

	.stance-contradictions {
		margin: 0 1.25rem 1rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--border-card);
	}

	.stance-more-row {
		display: flex;
		gap: 1rem;
		align-items: center;
		padding-top: 0.5rem;
		border-top: 1px dashed var(--border-card);
	}

	.link-btn-muted {
		color: var(--text-muted);
	}

	.link-btn-muted:hover {
		color: var(--text-secondary);
	}

	.no-contradictions-chip {
		display: inline-block;
		padding: 0.125rem 0.5rem;
		font-size: 0.625rem;
		color: var(--green);
		background: rgba(34, 197, 94, 0.1);
		border-radius: var(--radius-full);
	}

	/* Facts disclosure (de-emphasized — stances are the headline). */
	.facts-disclosure {
		margin-top: 1.5rem;
		padding-top: 1rem;
		border-top: 1px solid var(--border-card);
	}

	.facts-disclosure-toggle {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		background: transparent;
		border: none;
		padding: 0.25rem 0;
		font-family: var(--font-family);
		font-size: 0.813rem;
		color: var(--text-muted);
		cursor: pointer;
		transition: color var(--transition-fast);
	}

	.facts-disclosure-toggle:hover {
		color: var(--text-primary);
	}

	.facts-list {
		margin-top: 0.75rem;
	}

	/* Fact Card (used inside the facts disclosure) */
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
		text-align: center;
		color: var(--text-muted);
		font-size: 0.75rem;
		padding: 0.5rem;
	}

	.link-btn {
		font-size: 0.75rem;
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
		.person-hero {
			flex-direction: column;
			align-items: flex-start;
		}

		.hero-gauge {
			align-self: center;
		}

		.stance-top {
			flex-direction: column-reverse;
			align-items: flex-start;
		}
	}
</style>
