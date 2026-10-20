<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import { page } from '$app/stores';
	import { API_BASE } from '$lib/config';
	import { lang, tr } from '$lib/i18n';
	import { session, signInOpen, authFetch, errorText } from '$lib/auth';
	import { realtimeManager, subscribeToPollEvents, clearOldEvents } from '$lib/realtime';
	import HonestyGauge from '$lib/components/HonestyGauge.svelte';
	import { replaceState } from '$app/navigation';
	import type { PollResponse, CandidateResult, Ballot } from '$lib/types';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let signedIn = $derived($session !== null);
	let provider = $derived($session?.user.provider ?? null);

	let slug = $derived($page.params.slug ?? '');
	let data = $state<PollResponse | null>(null);
	let loadError: string | null = $state(null);
	let notFound = $state(false);
	let sortBy: 'preference' | 'honesty' = $state('preference');

	// Three views: the votes only, both side by side, the honesty only. The
	// view is kept in the URL (?vue=) so a link shares it.
	type View = 'vote' | 'both' | 'honesty';
	const VIEWS: View[] = ['vote', 'both', 'honesty'];
	let view: View = $state('both');

	function setView(v: View) {
		view = v;
		const url = new URL(window.location.href);
		if (v === 'both') url.searchParams.delete('vue');
		else url.searchParams.set('vue', v);
		replaceState(url, {});
	}

	// The ballot being edited (person ids, first choice first) and the saved one.
	let choice: number[] = $state([]);
	let saved: number[] = $state([]);
	let consented = $state(false);
	let consent = $state(false);
	let saving = $state(false);
	let saveError: string | null = $state(null);
	let justSaved = $state(false);

	let event = $derived(data?.event ?? null);
	let results = $derived(data?.results ?? null);
	let maxChoices = $derived(event?.max_choices ?? 3);
	let dirty = $derived(choice.length !== saved.length || choice.some((id, i) => saved[i] !== id));
	let byId = $derived(new Map<number, CandidateResult>((results?.candidates ?? []).map((c: CandidateResult) => [c.person_id, c])));

	// The ballot loads once per session and event: the live refreshes of the
	// results must not overwrite choices not saved yet.
	let ballotKey = '';
	$effect(() => {
		const key = $session && event ? `${$session.token}:${event.id}` : '';
		if (key === ballotKey) return;
		ballotKey = key;
		untrack(() => (key ? loadBallot() : resetBallot()));
	});

	let sorted = $derived.by(() => {
		const list = [...(results?.candidates ?? [])];
		const name = (a: CandidateResult, b: CandidateResult) => a.name.localeCompare(b.name, 'fr');
		const by = view === 'vote' ? 'preference' : view === 'honesty' ? 'honesty' : sortBy;
		if (by === 'honesty' || results?.hidden) {
			return list.sort((a, b) => a.honesty_rank - b.honesty_rank || name(a, b));
		}
		return list.sort((a, b) => (a.preference_rank ?? 1e9) - (b.preference_rank ?? 1e9) || name(a, b));
	});

	async function load() {
		loadError = null;
		try {
			const res = await fetch(`${API_BASE}/api/polls/${encodeURIComponent(slug)}`);
			if (res.status === 404) {
				notFound = true;
				return;
			}
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			data = await res.json();
		} catch (err) {
			console.error('failed to load poll', err);
			loadError = tr('poll.error', currentLang);
		}
	}

	async function loadBallot() {
		try {
			const res = await authFetch(`/api/polls/${encodeURIComponent(slug)}/ballot`);
			if (!res.ok) throw new Error(await errorText(res));
			const b: Ballot = await res.json();
			saved = b.person_ids;
			choice = [...b.person_ids];
			consented = b.consented;
		} catch (err) {
			console.error('failed to load ballot', err);
		}
	}

	function resetBallot() {
		choice = [];
		saved = [];
		consented = false;
		consent = false;
	}

	function pick(id: number) {
		if (!signedIn) {
			signInOpen.set(true);
			return;
		}
		if (choice.includes(id) || choice.length >= maxChoices) return;
		choice = [...choice, id];
		justSaved = false;
	}

	function remove(id: number) {
		choice = choice.filter((x) => x !== id);
		justSaved = false;
	}

	function move(i: number, delta: number) {
		const j = i + delta;
		if (j < 0 || j >= choice.length) return;
		const next = [...choice];
		[next[i], next[j]] = [next[j], next[i]];
		choice = next;
		justSaved = false;
	}

	async function save(ids: number[]) {
		saving = true;
		saveError = null;
		try {
			const res = await authFetch(`/api/polls/${encodeURIComponent(slug)}/ballot`, {
				method: 'POST',
				body: JSON.stringify({ person_ids: ids, consent: consented || consent })
			});
			if (!res.ok) throw new Error(await errorText(res));
			const b: Ballot = await res.json();
			saved = b.person_ids;
			choice = [...b.person_ids];
			consented = b.consented;
			justSaved = true;
			await load();
		} catch (err) {
			saveError = err instanceof Error ? err.message : String(err);
		} finally {
			saving = false;
		}
	}

	// The honesty colour follows the score: red at 20 and below, green at 80
	// and above, through amber between.
	function honestyColor(score: number): string {
		const p = Math.round(Math.max(0, Math.min(1, (score - 0.2) / 0.6)) * 100);
		return `color-mix(in oklch, var(--green) ${p}%, var(--red))`;
	}

	function pct(v: number | null): string {
		return v === null ? '—' : `${Math.round(v * 100)}%`;
	}

	// The vote bars are drawn relative to the leader: with many candidates the
	// shares are small, and a bar on a 0–100 % scale would be nearly empty for all.
	let leaderShare = $derived(Math.max(0, ...(results?.candidates ?? []).map((c) => c.first_choice_share ?? 0)));

	function barWidth(share: number | null, leader: number): number {
		if (share === null || leader <= 0) return 0;
		return Math.round((share / leader) * 100);
	}

	function ordinal(n: number): string {
		if (currentLang === 'en') return `#${n}`;
		return n === 1 ? '1er' : `${n}e`;
	}

	function closesText(iso: string): string {
		const d = new Date(iso);
		return d.toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}

	let unsubscribe: (() => void) | null = null;
	let refreshTimer: ReturnType<typeof setTimeout> | null = null;

	onMount(async () => {
		const v = new URL(window.location.href).searchParams.get('vue') as View | null;
		if (v && VIEWS.includes(v)) view = v;
		await load();
		realtimeManager.connect();
		if (event) {
			unsubscribe = subscribeToPollEvents(event.id, () => {
				// Ballots can come in bursts: one reload per second at most.
				if (refreshTimer) return;
				refreshTimer = setTimeout(() => {
					refreshTimer = null;
					load();
				}, 1000);
			});
		}
	});

	onDestroy(() => {
		unsubscribe?.();
		if (refreshTimer) clearTimeout(refreshTimer);
		realtimeManager.disconnect();
		clearOldEvents();
	});
</script>

<svelte:head>
	<title>{event ? `${currentLang === 'en' ? event.title_en : event.title_fr} — tartuffe.me` : 'tartuffe.me'}</title>
	{#if event}
		<meta name="description" content={currentLang === 'en' ? event.description_en : event.description_fr} />
	{/if}
</svelte:head>

{#if notFound}
	<p class="state">{tr('poll.not_found', currentLang)}</p>
{:else if loadError}
	<div class="state error">
		<p>{loadError}</p>
		<button class="btn" onclick={load}>{tr('home.retry', currentLang)}</button>
	</div>
{:else if !event || !results}
	<p class="state">{tr('person.loading', currentLang)}</p>
{:else}
	<header class="hero">
		<span class="kicker">{tr('poll.kicker', currentLang)}</span>
		<h1>{currentLang === 'en' ? event.title_en : event.title_fr}</h1>
		<p class="lead">{currentLang === 'en' ? event.description_en : event.description_fr}</p>
		<p class="meta">
			<strong>{results.voters === 1 ? tr('poll.voter', currentLang) : tr('poll.voters', currentLang, { n: results.voters })}</strong>
			<span class="dot"></span>
			{#if results.open && event.closes_at}
				{tr('poll.closes', currentLang, { d: closesText(event.closes_at) })}
			{:else if !results.open}
				{tr('poll.closed', currentLang)}
			{/if}
		</p>
		<p class="disclaimer">{tr('poll.disclaimer', currentLang)}</p>
	</header>

	{#if results.hidden}
		<div class="notice" class:notice--legal={results.hidden === 'blackout'}>
			{#if results.hidden === 'blackout'}
				{tr('poll.hidden.blackout', currentLang)}
			{:else}
				{tr('poll.hidden.too_few_voters', currentLang, { min: event.min_voters, n: results.voters })}
			{/if}
		</div>
	{/if}

	{#if results.open}
		<section class="ballot" aria-labelledby="ballot-title">
			<div class="ballot-head">
				<h2 id="ballot-title">{tr('poll.ballot.title', currentLang)}</h2>
				{#if !signedIn}
					<button class="btn btn-primary" onclick={() => signInOpen.set(true)}>{tr('poll.ballot.sign_in', currentLang)}</button>
				{/if}
			</div>
			<p class="hint">{tr('poll.ballot.hint', currentLang, { n: maxChoices })}</p>

			{#if signedIn}
				{#if provider === 'noop'}
					<p class="test-account">{tr('poll.ballot.test_account', currentLang)}</p>
				{/if}
				{#if choice.length === 0}
					<p class="empty">{tr('poll.ballot.empty', currentLang)}</p>
				{:else}
					<ol class="choice-list">
						{#each choice as id, i (id)}
							{@const c = byId.get(id)}
							<li class="choice">
								<span class="choice-rank">{i + 1}</span>
								<span class="choice-name">{c?.name ?? id}</span>
								<span class="choice-actions">
									<button class="icon" onclick={() => move(i, -1)} disabled={i === 0} aria-label={tr('poll.ballot.up', currentLang)} title={tr('poll.ballot.up', currentLang)}>↑</button>
									<button class="icon" onclick={() => move(i, 1)} disabled={i === choice.length - 1} aria-label={tr('poll.ballot.down', currentLang)} title={tr('poll.ballot.down', currentLang)}>↓</button>
									<button class="icon" onclick={() => remove(id)} aria-label={tr('poll.ballot.remove', currentLang)} title={tr('poll.ballot.remove', currentLang)}>×</button>
								</span>
							</li>
						{/each}
					</ol>
				{/if}

				{#if !consented}
					<label class="consent">
						<input type="checkbox" bind:checked={consent} />
						<span>{tr('poll.ballot.consent', currentLang)}</span>
					</label>
				{/if}

				<div class="ballot-actions">
					<button class="btn btn-primary" onclick={() => save(choice)} disabled={saving || !dirty || choice.length === 0 || (!consented && !consent)}>
						{saving ? tr('poll.ballot.saving', currentLang) : tr('poll.ballot.save', currentLang)}
					</button>
					{#if saved.length > 0}
						<button class="btn" onclick={() => save([])} disabled={saving}>{tr('poll.ballot.withdraw', currentLang)}</button>
					{/if}
					{#if dirty && saved.length > 0}
						<span class="status">{tr('poll.ballot.unsaved', currentLang)}</span>
					{:else if justSaved && saved.length > 0}
						<span class="status status--ok">{tr('poll.ballot.saved', currentLang)}</span>
					{/if}
				</div>
				{#if saveError}
					<p class="save-error">{saveError}</p>
				{/if}
			{/if}
		</section>
	{/if}

	{#snippet pickButton(c: CandidateResult)}
		{#if results?.open}
			{@const mine = choice.indexOf(c.person_id)}
			{#if mine >= 0}
				<button class="pick pick--mine" onclick={() => remove(c.person_id)}>{tr('poll.ballot.added', currentLang, { n: mine + 1 })} ✓</button>
			{:else}
				<button class="pick" onclick={() => pick(c.person_id)} disabled={signedIn && choice.length >= maxChoices}>
					{signedIn && choice.length >= maxChoices ? tr('poll.ballot.full', currentLang) : tr('poll.ballot.add', currentLang)}
				</button>
			{/if}
		{/if}
	{/snippet}

	{#snippet who(c: CandidateResult)}
		<a href="/person/{c.person_id}" class="who">
			{#if c.image_url}
				<img src={c.image_url} alt="" class="photo" />
			{:else}
				<span class="photo photo--empty">{c.name.charAt(0).toUpperCase()}</span>
			{/if}
			<span class="name">{c.name}</span>
		</a>
	{/snippet}

	<div class="views" role="tablist" aria-label={tr('poll.view', currentLang)}>
		{#each ['vote', 'both', 'honesty'] as const as v}
			<button class="view-tab" class:active={view === v} role="tab" aria-selected={view === v} onclick={() => setView(v)}>
				{tr(`poll.view.${v}`, currentLang)}
			</button>
		{/each}
	</div>

	{#if sorted.length === 0}
		<p class="state">{tr('poll.no_candidates', currentLang)}</p>
	{:else if view === 'vote'}
		<ol class="vote-list">
			{#each sorted as c (c.person_id)}
				<li class="vote-row" class:vote-row--mine={choice.includes(c.person_id)}>
					<span class="vote-rank">{c.preference_rank ?? '—'}</span>
					{@render who(c)}
					<div class="vote-bar">
						{#if c.first_choice_share === null}
							<span class="value value--muted">—</span>
							<span class="meta">{tr('poll.gauge.hidden', currentLang)}</span>
						{:else}
							<div class="vote-bar-line">
								<div class="bar bar--wide" aria-hidden="true">
									<span class="bar-fill--vote" style="width: {barWidth(c.first_choice_share, leaderShare)}%"></span>
								</div>
								<span class="value">{pct(c.first_choice_share)}</span>
							</div>
							<span class="meta">{tr('poll.gauge.votes', currentLang, { n: c.first_choices ?? 0 })} · {tr('poll.top_n_short', currentLang, { k: maxChoices, p: pct(c.chosen_share) })}</span>
						{/if}
					</div>
					<div class="vote-pick">{@render pickButton(c)}</div>
				</li>
			{/each}
		</ol>
		<p class="board-note">{tr('poll.note.vote', currentLang)}</p>
	{:else if view === 'honesty'}
		<div class="gauge-grid">
			{#each sorted as c (c.person_id)}
				<article class="gauge-card" class:gauge-card--mine={choice.includes(c.person_id)}>
					<span class="gauge-rank">{ordinal(c.honesty_rank)}</span>
					{@render who(c)}
					<HonestyGauge score={c.honesty_score} size={200} label={c.name} />
					<span class="meta">{tr('poll.gauge.facts', currentLang, { n: c.fact_count })} · {tr('poll.gauge.contradictions', currentLang, { n: c.contradiction_count })}</span>
					{@render pickButton(c)}
				</article>
			{/each}
		</div>
		<p class="board-note">{tr('poll.note.honesty', currentLang)}</p>
	{:else}
		<div class="board" role="table" aria-label={currentLang === 'en' ? event.title_en : event.title_fr}>
			<div class="board-head" role="row">
				<button
					class="col-head col-head--vote"
					class:active={sortBy === 'preference' && !results.hidden}
					onclick={() => (sortBy = 'preference')}
					disabled={!!results.hidden}
					role="columnheader"
					aria-sort={sortBy === 'preference' && !results.hidden ? 'descending' : 'none'}
				>
					{tr('poll.col.vote', currentLang)}{#if sortBy === 'preference' && !results.hidden} ↓{/if}
				</button>
				<span class="col-head col-head--name" role="columnheader">{tr('poll.col.candidate', currentLang)}</span>
				<button
					class="col-head col-head--honesty"
					class:active={sortBy === 'honesty' || !!results.hidden}
					onclick={() => (sortBy = 'honesty')}
					role="columnheader"
					aria-sort={sortBy === 'honesty' || results.hidden ? 'descending' : 'none'}
				>
					{tr('poll.col.honesty', currentLang)}{#if sortBy === 'honesty' || results.hidden} ↓{/if}
				</button>
			</div>

			{#each sorted as c (c.person_id)}
				<div class="board-row" class:board-row--mine={choice.includes(c.person_id)} role="row">
					<div class="cell cell--vote" role="cell">
						{#if c.first_choice_share === null}
							<span class="value value--muted">—</span>
							<span class="meta">{tr('poll.gauge.hidden', currentLang)}</span>
						{:else}
							<div class="value-line">
								<span class="rank">{ordinal(c.preference_rank ?? 0)}</span>
								<span class="value">{pct(c.first_choice_share)}</span>
							</div>
							<div class="bar bar--vote" aria-hidden="true">
								<span class="bar-fill--vote" style="width: {barWidth(c.first_choice_share, leaderShare)}%"></span>
							</div>
							<span class="meta">{tr('poll.top_n_short', currentLang, { k: maxChoices, p: pct(c.chosen_share) })}</span>
						{/if}
					</div>

					<div class="cell cell--name" role="cell">
						{@render who(c)}
						{@render pickButton(c)}
					</div>

					<div class="cell cell--honesty" role="cell">
						<HonestyGauge score={c.honesty_score} size={130} label={c.name} />
						<span class="meta">{ordinal(c.honesty_rank)} · {tr('poll.gauge.facts', currentLang, { n: c.fact_count })}</span>
					</div>
				</div>
			{/each}
		</div>
		<p class="board-note">{tr('poll.board_note', currentLang)}</p>
	{/if}

	<section class="how">
		<h2>{tr('poll.how.title', currentLang)}</h2>
		<ul>
			<li>{@html tr('poll.how.preference', currentLang)}</li>
			<li>{@html tr('poll.how.honesty', currentLang)}</li>
			<li>{@html tr('poll.how.compare', currentLang)}</li>
			<li>{tr('poll.how.facts', currentLang)}</li>
			<li>{@html tr('poll.how.verification', currentLang)}</li>
			<li>{tr('poll.how.privacy', currentLang)}</li>
		</ul>
	</section>
{/if}

<style>
	:global(:root) {
		--poll-blue: #6f8fc9;
	}

	.state {
		text-align: center;
		color: var(--text-secondary);
		padding: 3rem 1rem;
	}
	.state.error {
		color: var(--red);
	}

	.hero {
		text-align: center;
		max-width: 46rem;
		margin: 1rem auto 2rem;
	}
	.kicker {
		display: inline-block;
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.12em;
		text-transform: uppercase;
		color: var(--accent-hover);
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2.25rem, 6vw, 3.5rem);
		font-weight: 600;
		margin: 0.25rem 0 0.75rem;
		line-height: 1.05;
	}
	.lead {
		font-size: 1.1rem;
		color: var(--text-secondary);
		line-height: 1.5;
		margin: 0 0 1rem;
	}
	.meta {
		display: flex;
		justify-content: center;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem;
		font-size: 0.9rem;
		color: var(--text-secondary);
		margin: 0;
	}
	.dot {
		width: 4px;
		height: 4px;
		border-radius: 50%;
		background: var(--text-muted);
	}
	.disclaimer {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin: 0.75rem 0 0;
	}

	.notice {
		max-width: 46rem;
		margin: 0 auto 1.5rem;
		padding: 0.75rem 1rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-md);
		background: var(--bg-card);
		text-align: center;
		font-size: 0.9rem;
		color: var(--text-secondary);
	}
	.notice--legal {
		border-color: var(--amber);
		color: var(--text-primary);
	}

	.ballot {
		max-width: 46rem;
		margin: 0 auto 2rem;
		padding: 1.25rem 1.5rem;
		background: var(--bg-card);
		border: 1px solid var(--accent);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-glow);
	}
	.ballot-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 1rem;
		flex-wrap: wrap;
	}
	.ballot h2 {
		font-family: var(--font-serif);
		font-size: 1.5rem;
		margin: 0;
	}
	.hint {
		font-size: 0.85rem;
		color: var(--text-secondary);
		margin: 0.5rem 0 0.75rem;
		line-height: 1.45;
	}
	.test-account {
		font-size: 0.75rem;
		color: var(--amber);
		margin: 0 0 0.75rem;
	}
	.empty {
		font-size: 0.9rem;
		color: var(--text-muted);
		margin: 0 0 0.75rem;
	}
	.choice-list {
		list-style: none;
		margin: 0 0 0.75rem;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
	}
	.choice {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
	}
	.choice-rank {
		width: 1.6rem;
		height: 1.6rem;
		flex-shrink: 0;
		display: grid;
		place-items: center;
		border-radius: 50%;
		background: var(--accent);
		color: white;
		font-weight: 700;
		font-size: 0.85rem;
	}
	.choice-name {
		flex: 1;
		min-width: 0;
		font-weight: 500;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.choice-actions {
		display: inline-flex;
		gap: 0.25rem;
	}
	.icon {
		width: 1.9rem;
		height: 1.9rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-sm);
		background: transparent;
		color: var(--text-secondary);
		cursor: pointer;
	}
	.icon:hover:not(:disabled) {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}
	.icon:disabled {
		opacity: 0.3;
		cursor: default;
	}
	.consent {
		display: flex;
		gap: 0.6rem;
		align-items: flex-start;
		font-size: 0.8rem;
		line-height: 1.45;
		color: var(--text-secondary);
		margin: 0 0 0.9rem;
		cursor: pointer;
	}
	.consent input {
		margin-top: 0.2rem;
		accent-color: var(--accent);
	}
	.ballot-actions {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		flex-wrap: wrap;
	}
	.status {
		font-size: 0.8rem;
		color: var(--amber);
	}
	.status--ok {
		color: var(--green);
	}
	.save-error {
		color: var(--red);
		font-size: 0.85rem;
		margin: 0.6rem 0 0;
	}

	.btn {
		padding: 0.5rem 1rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-sm);
		background: var(--bg-secondary);
		color: var(--text-primary);
		font-size: 0.85rem;
		font-weight: 600;
		cursor: pointer;
		transition: all var(--transition-fast);
	}
	.btn:hover:not(:disabled) {
		border-color: var(--text-muted);
	}
	.btn:disabled {
		opacity: 0.45;
		cursor: default;
	}
	.btn-primary {
		background: var(--accent);
		border-color: var(--accent);
		color: white;
	}
	.btn-primary:hover:not(:disabled) {
		background: var(--accent-hover);
		border-color: var(--accent-hover);
	}

	.views {
		display: flex;
		justify-content: center;
		gap: 3px;
		width: fit-content;
		margin: 0 auto 1.25rem;
		padding: 4px;
		background: var(--bg-card);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
	}
	.view-tab {
		padding: 0.45rem 1.1rem;
		border: none;
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--text-secondary);
		font-size: 0.85rem;
		font-weight: 600;
		cursor: pointer;
		transition: all var(--transition-fast);
	}
	.view-tab:hover {
		color: var(--text-primary);
	}
	.view-tab.active {
		background: var(--accent);
		color: white;
	}

	.vote-list {
		list-style: none;
		max-width: 52rem;
		margin: 0 auto;
		padding: 0;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		overflow: hidden;
	}
	.vote-row {
		display: grid;
		grid-template-columns: 2.5rem 12rem 1fr auto;
		align-items: center;
		gap: 1rem;
		padding: 0.9rem 1.25rem;
		border-bottom: 1px solid var(--border-card);
	}
	.vote-row:last-child {
		border-bottom: none;
	}
	.vote-row--mine {
		background: var(--accent-glow);
	}
	.vote-rank {
		font-size: 1.25rem;
		font-weight: 700;
		color: var(--text-muted);
		text-align: center;
		font-variant-numeric: tabular-nums;
	}
	.vote-row .who {
		flex-direction: row;
		gap: 0.6rem;
		text-align: left;
	}
	.vote-row .photo {
		width: 44px;
		height: 44px;
		flex-shrink: 0;
	}
	.vote-bar {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		min-width: 0;
	}
	.vote-bar-line {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.vote-bar .value {
		min-width: 3.5rem;
		text-align: right;
	}
	.vote-bar .meta {
		text-align: left;
	}

	.gauge-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(230px, 1fr));
		gap: 1rem;
		max-width: 60rem;
		margin: 0 auto;
	}
	.gauge-card {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.6rem;
		padding: 1.25rem 1rem 1rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
	}
	.gauge-card--mine {
		border-color: var(--accent);
	}
	.gauge-rank {
		position: absolute;
		top: 0.75rem;
		left: 0.9rem;
		font-size: 0.8rem;
		font-weight: 700;
		color: var(--text-muted);
	}

	.board {
		max-width: 60rem;
		margin: 0 auto;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		overflow: hidden;
	}
	.board-head,
	.board-row {
		display: grid;
		grid-template-columns: 1fr minmax(9rem, 14rem) 1fr;
		align-items: center;
	}
	.board-head {
		border-bottom: 1px solid var(--glass-border);
		background: var(--bg-secondary);
	}
	.col-head {
		padding: 0.8rem 1.25rem;
		background: none;
		border: none;
		font: inherit;
		font-size: 0.72rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	button.col-head {
		cursor: pointer;
		transition: color var(--transition-fast);
	}
	button.col-head:hover:not(:disabled),
	.col-head.active {
		color: var(--text-primary);
	}
	button.col-head:disabled {
		cursor: default;
	}
	.col-head--vote {
		text-align: right;
	}
	.col-head--name {
		text-align: center;
	}
	.col-head--honesty {
		text-align: left;
	}

	.board-row {
		border-bottom: 1px solid var(--border-card);
		transition: background var(--transition-fast);
	}
	.board-row:last-child {
		border-bottom: none;
	}
	.board-row:hover {
		background: var(--bg-card-hover);
	}
	.board-row--mine {
		background: var(--accent-glow);
	}
	.board-row--mine:hover {
		background: var(--accent-glow);
	}

	.cell {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		padding: 1rem 1.25rem;
		min-width: 0;
	}
	/* The bars grow outwards from the central column. */
	.cell--vote {
		align-items: flex-end;
		text-align: right;
	}
	.cell--honesty {
		align-items: flex-start;
	}
	.value-line {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
	}
	.value {
		font-size: 1.5rem;
		font-weight: 700;
		line-height: 1;
		font-variant-numeric: tabular-nums;
		color: var(--text-primary);
	}
	.value--muted {
		color: var(--text-muted);
	}
	.rank {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-muted);
	}
	.bar {
		display: flex;
		width: 100%;
		max-width: 16rem;
		height: 8px;
		border-radius: var(--radius-full);
		background: var(--bg-secondary);
		overflow: hidden;
	}
	.bar span {
		display: block;
		height: 100%;
		border-radius: var(--radius-full);
		transition: width var(--transition-slow);
	}
	.bar--vote {
		justify-content: flex-end;
	}
	.bar--wide {
		max-width: none;
		flex: 1;
		height: 12px;
	}
	.bar-fill--vote {
		background: var(--poll-blue);
	}
	.meta {
		font-size: 0.72rem;
		color: var(--text-muted);
	}

	.cell--name {
		align-items: center;
		text-align: center;
		gap: 0.5rem;
		border-left: 1px solid var(--border-card);
		border-right: 1px solid var(--border-card);
	}
	.who {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.45rem;
		color: var(--text-primary);
	}
	.who:hover .name {
		color: var(--accent-hover);
	}
	.photo {
		width: 64px;
		height: 64px;
		border-radius: 50%;
		object-fit: cover;
		border: 2px solid var(--glass-border);
	}
	.photo--empty {
		display: grid;
		place-items: center;
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-weight: 700;
		font-size: 1.4rem;
	}
	.name {
		font-weight: 600;
		font-size: 0.95rem;
		line-height: 1.25;
		transition: color var(--transition-fast);
	}
	.pick {
		padding: 0.25rem 0.8rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--text-secondary);
		font-size: 0.75rem;
		font-weight: 600;
		cursor: pointer;
		transition: all var(--transition-fast);
	}
	.pick:hover:not(:disabled) {
		border-color: var(--accent);
		color: var(--text-primary);
	}
	.pick:disabled {
		opacity: 0.4;
		cursor: default;
	}
	.pick--mine {
		background: var(--accent);
		border-color: var(--accent);
		color: white;
	}
	.board-note {
		max-width: 60rem;
		margin: 0.6rem auto 0;
		font-size: 0.75rem;
		color: var(--text-muted);
		text-align: center;
	}

	.how {
		max-width: 46rem;
		margin: 3rem auto 1rem;
		padding: 1.25rem 1.5rem;
		border-top: 1px solid var(--glass-border);
	}
	.how h2 {
		font-family: var(--font-serif);
		font-size: 1.4rem;
		margin: 0 0 0.75rem;
	}
	.how ul {
		margin: 0;
		padding-left: 1.1rem;
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		color: var(--text-secondary);
		font-size: 0.9rem;
		line-height: 1.5;
	}
	.how :global(strong) {
		color: var(--text-primary);
	}

	@media (max-width: 560px) {
		.ballot {
			padding: 1rem;
		}
		.board-head,
		.board-row {
			grid-template-columns: 1fr 7.5rem 1fr;
		}
		.cell,
		.col-head {
			padding-left: 0.6rem;
			padding-right: 0.6rem;
		}
		.value {
			font-size: 1.15rem;
		}
		.photo {
			width: 48px;
			height: 48px;
		}
		.vote-row {
			grid-template-columns: 1.6rem 1fr;
			gap: 0.4rem 0.75rem;
			padding: 0.8rem 0.9rem;
		}
		.vote-bar {
			grid-column: 1 / -1;
		}
		.vote-pick {
			grid-column: 1 / -1;
			justify-self: start;
		}
		.name {
			font-size: 0.82rem;
		}
	}
</style>
