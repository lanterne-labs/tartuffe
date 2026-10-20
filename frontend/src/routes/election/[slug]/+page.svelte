<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import { page } from '$app/stores';
	import { API_BASE } from '$lib/config';
	import { lang, tr } from '$lib/i18n';
	import { session, signInOpen, authFetch, errorText } from '$lib/auth';
	import { realtimeManager, subscribeToPollEvents, clearOldEvents } from '$lib/realtime';
	import RingGauge from '$lib/components/RingGauge.svelte';
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
		if (sortBy === 'honesty' || results?.hidden) {
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

	function rankText(kind: 'preference' | 'honesty', n: number): string {
		return n === 1 ? tr(`poll.rank.${kind}_first`, currentLang) : tr(`poll.rank.${kind}`, currentLang, { n });
	}

	function closesText(iso: string): string {
		const d = new Date(iso);
		return d.toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}

	let unsubscribe: (() => void) | null = null;
	let refreshTimer: ReturnType<typeof setTimeout> | null = null;

	onMount(async () => {
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

	<div class="toolbar">
		<span class="toolbar-label">{tr('poll.sort', currentLang)}</span>
		<div class="pills" role="group" aria-label={tr('poll.sort', currentLang)}>
			<button class="pill" class:active={sortBy === 'preference'} aria-pressed={sortBy === 'preference'} onclick={() => (sortBy = 'preference')} disabled={!!results.hidden}>
				{tr('poll.sort.preference', currentLang)}
			</button>
			<button class="pill" class:active={sortBy === 'honesty' || !!results.hidden} aria-pressed={sortBy === 'honesty' || !!results.hidden} onclick={() => (sortBy = 'honesty')}>
				{tr('poll.sort.honesty', currentLang)}
			</button>
		</div>
	</div>

	{#if sorted.length === 0}
		<p class="state">{tr('poll.no_candidates', currentLang)}</p>
	{:else}
		<div class="grid">
			{#each sorted as c (c.person_id)}
				{@const mine = choice.indexOf(c.person_id)}
				<article class="cand" class:cand--mine={mine >= 0}>
					<a class="cand-head" href="/person/{c.person_id}">
						{#if c.image_url}
							<img src={c.image_url} alt={c.name} class="avatar" />
						{:else}
							<div class="avatar avatar-placeholder">{c.name.charAt(0).toUpperCase()}</div>
						{/if}
						<div class="cand-id">
							<h3>{c.name}</h3>
							<p class="ranks">
								{#if c.preference_rank !== null}<span>{rankText('preference', c.preference_rank)}</span><span class="sep">·</span>{/if}<span>{rankText('honesty', c.honesty_rank)}</span>
							</p>
						</div>
					</a>

					<div class="gauges">
						<RingGauge
							value={c.first_choice_share}
							text={pct(c.first_choice_share)}
							color="var(--poll-blue)"
							caption={tr('poll.gauge.preference', currentLang)}
							sub={c.first_choices === null ? tr('poll.gauge.hidden', currentLang) : tr('poll.gauge.votes', currentLang, { n: c.first_choices })}
						/>
						<RingGauge
							value={c.honesty_score}
							text={String(Math.round(c.honesty_score * 100))}
							color={honestyColor(c.honesty_score)}
							caption={tr('poll.gauge.honesty', currentLang)}
							sub={tr('poll.gauge.facts', currentLang, { n: c.fact_count })}
						/>
					</div>

					{#if c.chosen_share !== null}
						<p class="topn">{tr('poll.top_n', currentLang, { k: maxChoices, p: pct(c.chosen_share) })}</p>
					{/if}

					{#if results.open}
						{#if mine >= 0}
							<button class="btn btn-block btn-mine" onclick={() => remove(c.person_id)}>{tr('poll.ballot.added', currentLang, { n: mine + 1 })} ✓</button>
						{:else}
							<button class="btn btn-block" onclick={() => pick(c.person_id)} disabled={signedIn && choice.length >= maxChoices}>
								{signedIn && choice.length >= maxChoices ? tr('poll.ballot.full', currentLang) : tr('poll.ballot.add', currentLang)}
							</button>
						{/if}
					{/if}
				</article>
			{/each}
		</div>
	{/if}

	<section class="how">
		<h2>{tr('poll.how.title', currentLang)}</h2>
		<ul>
			<li>{@html tr('poll.how.preference', currentLang)}</li>
			<li>{@html tr('poll.how.honesty', currentLang)}</li>
			<li>{@html tr('poll.how.compare', currentLang)}</li>
			<li>{tr('poll.how.facts', currentLang)}</li>
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
	.btn-block {
		width: 100%;
		margin-top: auto;
	}
	.btn-mine {
		background: var(--accent-glow);
		border-color: var(--accent);
	}

	.toolbar {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 1rem;
		flex-wrap: wrap;
	}
	.toolbar-label {
		font-size: 0.8rem;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.08em;
	}
	.pills {
		display: inline-flex;
		gap: 2px;
		padding: 3px;
		background: var(--bg-card);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
	}
	.pill {
		padding: 0.35rem 0.9rem;
		border: none;
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--text-secondary);
		font-size: 0.85rem;
		font-weight: 600;
		cursor: pointer;
	}
	.pill.active {
		background: var(--accent);
		color: white;
	}
	.pill:disabled {
		opacity: 0.4;
		cursor: default;
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(270px, 1fr));
		gap: 1rem;
	}
	.cand {
		display: flex;
		flex-direction: column;
		gap: 1rem;
		padding: 1.1rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		transition: border-color var(--transition-normal);
	}
	.cand:hover {
		border-color: var(--border-card-hover);
	}
	.cand--mine {
		border-color: var(--accent);
	}
	.cand-head {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		color: inherit;
	}
	.cand-head:hover h3 {
		color: var(--accent-hover);
	}
	.avatar {
		width: 48px;
		height: 48px;
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
	}
	.avatar-placeholder {
		display: grid;
		place-items: center;
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-weight: 700;
		font-size: 1.2rem;
	}
	.cand-id {
		min-width: 0;
	}
	.cand h3 {
		font-size: 1.05rem;
		margin: 0;
		transition: color var(--transition-fast);
	}
	.ranks {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin: 0.15rem 0 0;
	}
	.ranks .sep {
		margin: 0 0.35rem;
	}
	.gauges {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 0.5rem;
	}
	.topn {
		font-size: 0.75rem;
		color: var(--text-muted);
		text-align: center;
		margin: -0.4rem 0 0;
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
		.grid {
			grid-template-columns: 1fr;
		}
	}
</style>
