<script lang="ts">
	import Spinner from '$lib/components/Spinner.svelte';
	import { sourceDomain } from '$lib/sources';
	import FactVersions from '$lib/components/FactVersions.svelte';
	import { page } from '$app/stores';
	import { onMount, onDestroy } from 'svelte';
	import type { PersonSummary, FactWithSource, ContradictionGroup, PaginatedResponse, StanceDocument, HistoryPoint, Restatement } from '$lib/types';
	import { realtimeManager, subscribeToPersonEvents, clearOldEvents } from '$lib/realtime';
	import { API_BASE } from '$lib/config';
	import { lang, tr, pickLang, type Lang } from '$lib/i18n';
	import TartuffeScore from '$lib/components/TartuffeScore.svelte';
	import ScoreCompare from '$lib/components/ScoreCompare.svelte';
	import ContradictionGroupCard from '$lib/components/ContradictionGroup.svelte';
	import HistoryChart from '$lib/components/HistoryChart.svelte';
	import ShareMenu from '$lib/components/ShareMenu.svelte';
	import ReportButton from '$lib/components/ReportButton.svelte';
	import type { ShareTarget } from '$lib/types';
	import { tick } from 'svelte';

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
		archive_url?: string | null;
		restatements?: Restatement[];
	}
	let stanceFacts: Record<number, StanceFact[]> = $state({});
	let stanceFactsLoading: Record<number, boolean> = $state({});
	// Which tab is active inside each expanded stance card. Defaults to 'contradictions'
	// (matches the user's request) and switches to 'facts' on tab click. Per-stance so
	// different cards can be on different tabs at once.
	type StanceTab = 'contradictions' | 'facts';
	let stanceTab: Record<number, StanceTab> = $state({});

	function pickFactText(f: StanceFact | FactWithSource | Restatement): string {
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
	let historyPoints: HistoryPoint[] = $state([]);
	let historyError: string | null = $state(null);
	let loading = $state(true);
	let error: string | null = $state(null);
	let membersExpanded = $state(false);

	// Stance-first profile state.
	let stanceSort: StanceSort = $state('worst');
	let expandedStances: Record<number, boolean> = $state({});
	let stanceContradictions: Record<number, ContradictionGroup[]> = $state({});
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

	// The item a shared link points at (?stance=, ?c=, ?fact=), highlighted
	// for a moment once its stance is open.
	let highlightContradiction: number | null = $state(null);
	let highlightStance: number | null = $state(null);

	function flash(elementId: string, clear: () => void) {
		tick().then(() => {
			const el = document.getElementById(elementId);
			el?.scrollIntoView({ behavior: 'smooth', block: 'center' });
			setTimeout(clear, 2500);
		});
	}

	async function openStance(stanceId: number, tab: StanceTab = 'contradictions') {
		if (!stances.some((s) => s.id === stanceId)) return false;
		stanceTab[stanceId] = tab;
		if (!expandedStances[stanceId]) await toggleStance(stanceId);
		if (tab === 'facts' && stanceFacts[stanceId] === undefined) await loadStanceFacts(stanceId);
		return true;
	}

	// goToFact opens the facts of the stance that holds the fact (inStance
	// when it does, else the fact's own stance of this person) and shows it.
	async function goToFact(factId: number, inStance?: number) {
		let stanceId = inStance;
		if (stanceId !== undefined) {
			if (stanceFacts[stanceId] === undefined) await loadStanceFacts(stanceId);
			if (!stanceFacts[stanceId]?.some((f) => f.fact_id === factId)) stanceId = undefined;
		}
		if (stanceId === undefined) {
			const t = await shareTarget('fact', factId);
			stanceId = t?.stance_id ?? undefined;
		}
		if (stanceId === undefined || !(await openStance(stanceId, 'facts'))) return;
		highlightedFactId = factId;
		flash(`fact-${factId}`, () => (highlightedFactId = null));
	}

	async function shareTarget(kind: 'stance' | 'contradiction' | 'fact', id: number): Promise<ShareTarget | null> {
		try {
			const res = await fetch(`${API_BASE}/api/share/${kind}/${id}?person=${personId}`);
			if (!res.ok) return null;
			const t: ShareTarget = await res.json();
			return t.person_id === personId ? t : null;
		} catch (err) {
			console.error('failed to resolve the shared item', err);
			return null;
		}
	}

	// openPointed opens what the link of the page points at.
	async function openPointed() {
		const q = $page.url.searchParams;
		const num = (k: string) => (/^\d+$/.test(q.get(k) ?? '') ? Number(q.get(k)) : null);
		const c = num('c'), f = num('fact'), st = num('stance');
		if (c !== null) {
			const t = await shareTarget('contradiction', c);
			if (t?.stance_id && (await openStance(t.stance_id))) {
				// The contradiction may sit past the first few shown.
				const items = stanceContradictions[t.stance_id] ?? [];
				const at = items.findIndex((g) => g.contradicting_facts.some((x) => x.contradiction_id === c));
				if (at >= (stanceContradictionsShown[t.stance_id] ?? INITIAL_CONTRADICTIONS_SHOWN)) stanceContradictionsShown[t.stance_id] = at + 1;
				highlightContradiction = c;
				flash(`c-${c}`, () => (highlightContradiction = null));
			}
		} else if (f !== null) {
			await goToFact(f);
		} else if (st !== null && (await openStance(st))) {
			highlightStance = st;
			flash(`stance-${st}`, () => (highlightStance = null));
		}
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
			const items: ContradictionGroup[] = await response.json();
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

	// The candidates, to place this one among them (only on a candidate's page).
	let candidates: PersonSummary[] = $state([]);
	async function loadCandidates() {
		try {
			const response = await fetch(`${API_BASE}/api/persons?candidate=true&limit=100`);
			if (!response.ok) throw new Error(`HTTP ${response.status}`);
			candidates = (await response.json()) ?? [];
		} catch (err) {
			console.error('failed to load the candidates', err);
		}
	}

	async function loadData() {
		loading = true;
		error = null;

		// Load only what's needed for the headline view: the person summary (with stance/
		// contradiction counts for the summary line) and the stance list. Facts are lazy-
		// loaded when the user opens the facts disclosure; per-stance contradictions are
		// lazy-loaded when a stance card is expanded.
		await Promise.all([loadPerson(), loadStances(), loadHistory()]);

		if (person?.entity_type === 'party') {
			await loadMembers();
		}
		if (person?.candidate) loadCandidates();

		loading = false;
		await openPointed();
	}

	async function loadHistory() {
		historyError = null;
		try {
			const response = await fetch(`${API_BASE}/api/person/${personId}/history`);
			if (!response.ok) throw new Error(`HTTP ${response.status}`);
			historyPoints = (await response.json()) ?? [];
		} catch (err) {
			historyError = err instanceof Error ? err.message : 'Failed to load history';
		}
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



	// A label as a sentence: its first letter upper case.
	function sentence(s: string | null | undefined): string {
		return s ? s.charAt(0).toUpperCase() + s.slice(1) : '';
	}

	function formatDate(dateString: string): string {
		return new Date(dateString).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'short', year: 'numeric' });
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

	function handleRealtimeEvent(_event: any) {
		// Refresh the headline view (person counts + stance scores). Stance contradictions
		// and the facts list are reloaded lazily on next expand to avoid clobbering the
		// user's current open panels mid-interaction.
		loadPerson();
		loadStances();
		loadHistory();
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
	<title>{person?.name ?? tr('person.loading', currentLang)} — tartuffe.me</title>
</svelte:head>

<div class="detail-page">
	<a href="/dashboard" class="back-link">
		<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
			<path d="m15 18-6-6 6-6"/>
		</svg>
		{tr('nav.dashboard', currentLang)}
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
				<TartuffeScore honesty={person.candidate ? person.honesty_score : null} lang={currentLang} size="lg" image={person.image_url} initial={person.name.charAt(0).toUpperCase()} facts={person.fact_count} />
				<div class="hero-info">
					<h1>
						{person.name}
						{#if person.entity_type === 'party'}
							<span class="badge">{tr('person.party_badge', currentLang)}</span>
						{/if}
					</h1>
					{#if person.candidate}
					<p class="hero-summary">
						{person.stance_count} {tr(person.stance_count === 1 ? 'count.stance' : 'count.stances', currentLang)}
						<span class="dot">·</span>
						{person.contradiction_count} {tr(person.contradiction_count === 1 ? 'count.contradiction' : 'count.contradictions', currentLang)} {tr('count.over', currentLang)} {person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)}
						<span class="dot">·</span>
						<a href="/feed?person={person.id}" class="feed-link">{tr('feed.person', currentLang)} →</a>
					</p>
					<p class="hero-note">{tr('person.ai_note', currentLang)} <a href="/about">{tr('person.ai_note_link', currentLang)}</a></p>
					{:else}
						<p class="hero-summary">{person.fact_count} {tr(person.fact_count === 1 ? 'count.fact' : 'count.facts', currentLang)}</p>
						<p class="hero-note">{tr('person.no_detection_note', currentLang)}</p>
					{/if}
				</div>
			</div>
			<div class="hero-gauge">
				<ShareMenu label item={{ kind: 'person', id: person.id, personId: person.id, personName: person.name, score: person.candidate ? person.honesty_score : undefined }} />
			</div>
		</div>

		{#if person.candidate && person.entity_type !== 'human' && person.members_score != null}
			<div class="score-breakdown">
				<TartuffeScore honesty={person.own_score ?? person.honesty_score} lang={currentLang} size="md" caption={tr('person.own_score', currentLang)} />
				<TartuffeScore honesty={person.members_score} lang={currentLang} size="md" caption={tr('person.members_score', currentLang)} />
				<TartuffeScore honesty={person.honesty_score} lang={currentLang} size="md" caption={tr('person.aggregate', currentLang)} />
			</div>
		{/if}

		{#if person.entity_type === 'party' && members.length > 0}
			<div class="members-section">
				<button class="members-toggle" onclick={() => membersExpanded = !membersExpanded}>
					<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" class="chevron" class:expanded={membersExpanded}>
						<path d="m9 18 6-6-6-6"/>
					</svg>
					{members.length} {tr('score.members', currentLang).toLowerCase()}
				</button>

				{#if membersExpanded}
					<div class="members-grid">
						{#each members as member (member.id)}
							<a href="/person/{member.id}" class="member-chip">
								<TartuffeScore honesty={member.honesty_score} lang={currentLang} size="xs" image={member.image_url} initial={member.name.charAt(0).toUpperCase()} />
								<span class="member-name">{member.name}</span>
							</a>
						{/each}
					</div>
				{/if}
			</div>
		{/if}

		<section class="history-section">
			<h2 class="section-heading">{tr('person.history', currentLang)}</h2>
			{#if historyError}
				<p class="empty-state">{tr('person.history.error', currentLang)} ({historyError})</p>
			{:else if historyPoints.length > 0}
				<HistoryChart
					points={historyPoints}
					lang={currentLang}
					scoreLabel={tr(person.entity_type === 'human' ? 'person.history.score' : 'person.history.own_score', currentLang)}
				/>
			{:else}
				<p class="empty-state">{tr('person.history.empty', currentLang)}</p>
			{/if}
		</section>

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
						{@const topic = pickLang(stance.label, stance.label_en, currentLang)}
						<div class="card stance-card" class:open={isOpen} class:highlight={highlightStance === stance.id} id="stance-{stance.id}">
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
											<span class="stance-label">{sentence(pickLang(stance.label, stance.label_en, currentLang))}</span>
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
											<ShareMenu item={{ kind: 'stance', id: stance.id, personId: person.id, personName: person.name, topic }} />
										</div>
									</div>
									<div class="stance-gauge">
										<TartuffeScore honesty={stance.score} lang={currentLang} />
									</div>
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
											<p class="empty-mini loading"><Spinner label={tr('person.loading', currentLang)} /></p>
										{:else if items.length > 0}
											{#each items.slice(0, shown) as g (g.base_fact_id)}
												<ContradictionGroupCard
													{g}
													person={{ id: person.id, name: person.name }}
													{topic}
													onGoToFact={(id) => goToFact(id, stance.id)}
													highlight={highlightContradiction}
												/>
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
											<p class="empty-mini loading"><Spinner label={tr('person.loading', currentLang)} /></p>
										{:else if (stanceFacts[stance.id]?.length ?? 0) > 0}
											<div class="stance-facts-list">
												{#each stanceFacts[stance.id] as f (f.fact_id)}
													<div class="stance-fact-row" id="fact-{f.fact_id}" class:highlight={highlightedFactId === f.fact_id}>
														<div class="stance-fact-text">
															<span class="fact-id">[{f.fact_id}]</span>
															{pickFactText(f)}
														</div>
														{#if f.source_url}
															<details class="source-dropdown" onclick={(e) => e.stopPropagation()}>
																<summary title={f.source_url}>{sourceDomain(f.source_url)}</summary>
																<div class="source-dropdown-menu">
																	<a href={f.source_url} target="_blank" rel="noopener noreferrer">{tr('person.source.original', currentLang)}</a>
																	{#if f.archive_url}
																		<a class="archive-link" href={f.archive_url} target="_blank" rel="noopener noreferrer">{tr('person.source.archive', currentLang)}</a>
																	{/if}
																</div>
															</details>
														{/if}
														{#if f.original_quote}
															<div class="quote-block">"{f.original_quote.length > 200 ? f.original_quote.slice(0, 200) + '…' : f.original_quote}"</div>
														{/if}
														<FactVersions sourceUrls={[f.source_url]} restatements={f.restatements ?? []} lang={currentLang} />
														<div class="fact-actions">
															<ShareMenu item={{ kind: 'fact', id: f.fact_id, personId: person.id, personName: person.name, topic, text: pickFactText(f) }} />
															<ReportButton kind="fact" targetId={f.fact_id} preview={f.fact_text} />
														</div>
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

		{#if person.candidate && candidates.length > 1}
			<section class="compare-section">
				<h2>{tr('score.compare.title', currentLang)}</h2>
				<ScoreCompare people={candidates} lang={currentLang} focus={person.id} />
			</section>
		{/if}

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
		flex-direction: column;
		align-items: center;
		justify-content: center;
		gap: 1rem;
		margin-bottom: 2rem;
		padding: 1.5rem;
		background: var(--bg-card);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
	}

	/* The dial with the portrait, the name and the summary under it. */
	.hero-left {
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
		gap: 0.75rem;
		min-width: 0;
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

		.hero-note {
		color: var(--text-secondary);
		font-size: 0.8125rem;
		margin-top: 0.5rem;
		max-width: 60ch;
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
		width: min(20rem, 100%);
	}

	/* Score Breakdown */
	.score-breakdown {
		display: flex;
		justify-content: center;
		gap: 1.25rem;
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

	.member-name {
		color: var(--text-primary);
		text-transform: capitalize;
		flex: 1;
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

	.compare-section {
		margin: 2rem 0 1.5rem;
	}
	.compare-section h2 {
		text-align: center;
		font-size: 1.15rem;
		margin-bottom: 0.9rem;
	}

	/* Stances section: the headline content of the profile. */
	.history-section {
		margin-bottom: 1.5rem;
		padding: 1rem 1.25rem 0.75rem;
		background: var(--bg-card);
		backdrop-filter: var(--glass-blur);
		-webkit-backdrop-filter: var(--glass-blur);
		border: 1px solid var(--border-card);
		border-radius: 12px;
	}

	.history-section .section-heading {
		margin: 0 0 0.75rem;
	}

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
	.source-dropdown {
		font-size: 0.75rem;
		margin-bottom: 0.3rem;
	}
	.source-dropdown > summary {
		cursor: pointer;
		color: var(--accent);
		list-style: none;
		width: fit-content;
	}
	.source-dropdown > summary::after {
		content: ' ▾';
		opacity: 0.6;
	}
	.source-dropdown[open] > summary::after {
		content: ' ▴';
	}
	.source-dropdown-menu {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		padding: 0.3rem 0 0 0.5rem;
	}
	.source-dropdown-menu a {
		color: var(--accent);
		word-break: break-all;
	}
	.source-dropdown-menu .archive-link {
		opacity: 0.8;
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

	@keyframes pulse-glow {
		0%, 100% { box-shadow: 0 0 0 3px var(--accent-glow); }
		50% { box-shadow: 0 0 0 6px var(--accent-glow); }
	}

	.fact-id {
		font-size: 0.688rem;
		font-weight: 600;
		color: var(--text-muted);
		margin-right: 0.25rem;
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

	.hero-gauge {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.6rem;
	}
	.stance-gauge {
		display: flex;
		align-items: flex-start;
		gap: 0.3rem;
		flex-shrink: 0;
		width: 8.5rem;
	}
	.fact-actions {
		display: flex;
		justify-content: flex-end;
		gap: 0.25rem;
		margin-top: 0.35rem;
	}
	.stance-card.highlight,
	.stance-fact-row.highlight {
		box-shadow: 0 0 0 3px var(--accent-glow);
		border-color: var(--accent);
		transition: box-shadow 0.6s ease;
	}
	.feed-link {
		color: var(--accent-hover);
		white-space: nowrap;
	}
</style>
