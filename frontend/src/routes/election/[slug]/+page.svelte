<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import { get } from 'svelte/store';
	import { page } from '$app/stores';
	import { API_BASE } from '$lib/config';
	import { displayName } from '$lib/share';
	import { lang, tr } from '$lib/i18n';
	import { voterKey, voterKeyOpen, protectedEnough } from '$lib/voter';
	import { cast, myBallot, onBallotChanged, BallotError, NotInWindow, type MyBallot } from '$lib/anonballot';
	import { realtimeManager, subscribeToPollEvents, clearOldEvents } from '$lib/realtime';
	import HonestyGauge from '$lib/components/HonestyGauge.svelte';
	import ScoreHelp from '$lib/components/ScoreHelp.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import { replaceState } from '$app/navigation';
	import type { PollResponse, CandidateResult } from '$lib/types';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	// hasKey: this device holds a voting key a document admitted.
	let hasKey = $derived(!!$voterKey?.registered);

	let slug = $derived($page.params.slug ?? '');
	let data = $state<PollResponse | null>(null);
	let loadError: string | null = $state(null);
	let notFound = $state(false);
	let sortBy: 'preference' | 'honesty' = $state('preference');

	// Three views: the votes only, both side by side, the honesty only. The
	// view is kept in the URL (?vue=) so a link shares it.
	type View = 'vote' | 'both' | 'honesty';
	const VIEWS: View[] = ['vote', 'both', 'honesty'];
	let view: View = $state('vote');

	function setView(v: View) {
		view = v;
		const url = new URL(window.location.href);
		if (v === 'vote') url.searchParams.delete('vue');
		else url.searchParams.set('vue', v);
		replaceState(url, {});
	}

	// The voter's ballot: up to max_choices candidates ranked from the
	// preferred one, a blank vote, or none. Each change is proved on this
	// device and cast anonymously (anonballot.ts); it counts once the next
	// batch publishes it.
	let mine = $state<MyBallot | null>(null);
	let ballot = $derived(mine?.kind === 'choices' ? mine.choices : []);
	let blank = $derived(mine?.kind === 'blank');
	let publishedBy: string | null = $state(null);
	let busy = $state(false);
	let actionError: string | null = $state(null);
	let flash: string | null = $state(null);
	let flashTimer: ReturnType<typeof setTimeout> | null = null;
	let ballotLoaded = $state(false);

	// The vote waiting for the voting key before it is cast.
	let pending: number | 'blank' | null = $state(null);
	let withdrawOpen = $state(false);

	let event = $derived(data?.event ?? null);
	let results = $derived(data?.results ?? null);
	let windows = $derived(data?.windows ?? []);
	let maxChoices = $derived(event?.max_choices ?? 3);
	let full = $derived(ballot.length >= maxChoices);
	let byId = $derived(new Map<number, CandidateResult>((results?.candidates ?? []).map((c: CandidateResult) => [c.person_id, c])));

	// The ballot loads once per voting key and event, then again when another
	// tab casts one.
	let ballotKey = '';
	$effect(() => {
		const key = $voterKey?.registered && event ? `${$voterKey.commitment}:${event.id}` : '';
		if (key === ballotKey) return;
		ballotKey = key;
		untrack(() => (key ? loadBallot() : resetBallot()));
	});

	// A key dialog closed without a key drops the vote it was for.
	$effect(() =>
		voterKeyOpen.subscribe((isOpen) => {
			if (isOpen) return;
			untrack(() => {
				if (!get(voterKey)?.registered) pending = null;
				else if (pending !== null && ballotLoaded) go(pending);
			});
		})
	);

	let sorted = $derived.by(() => {
		const list = [...(results?.candidates ?? [])];
		const name = (a: CandidateResult, b: CandidateResult) => a.name.localeCompare(b.name, 'fr');
		const by = view === 'vote' ? 'preference' : view === 'honesty' ? 'honesty' : sortBy;
		if (by === 'honesty' || results?.hidden) {
			return list.sort((a, b) => a.honesty_rank - b.honesty_rank || name(a, b));
		}
		return list.sort((a, b) => (a.preference_rank ?? 1e9) - (b.preference_rank ?? 1e9) || name(a, b));
	});

	// The same ballots counted as the one-round majority vote: first choices.
	let byMajority = $derived(
		[...(results?.candidates ?? [])].sort((a, b) => (a.majority_rank ?? 1e9) - (b.majority_rank ?? 1e9) || a.name.localeCompare(b.name, 'fr'))
	);

	// The sole winner of each count, if there is one: when they differ, the
	// page says so.
	function soleFirst(list: CandidateResult[], rank: (c: CandidateResult) => number | null): CandidateResult | null {
		const top = list.filter((c) => rank(c) === 1);
		return top.length === 1 ? top[0] : null;
	}
	let rankingWinner = $derived(results?.hidden ? null : soleFirst(results?.candidates ?? [], (c) => c.preference_rank));
	let majorityWinner = $derived(results?.hidden ? null : soleFirst(results?.candidates ?? [], (c) => c.majority_rank));
	let differ = $derived(!!rankingWinner && !!majorityWinner && rankingWinner.person_id !== majorityWinner.person_id);

	async function load() {
		try {
			const res = await fetch(`${API_BASE}/api/polls/${encodeURIComponent(slug)}`);
			if (res.status === 404) {
				notFound = true;
				return;
			}
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			data = await res.json();
			loadError = null;
		} catch (err) {
			console.error('failed to load poll', err);
			// A failed live refresh keeps the results shown.
			if (!data) loadError = tr('poll.error', currentLang);
		}
	}

	async function loadBallot() {
		const key = get(voterKey);
		if (!key?.registered || !event) return;
		try {
			const got = await myBallot(slug, event.id, key, windows);
			if (busy) return;
			mine = got.ballot;
			publishedBy = got.publishedBy ?? null;
			ballotLoaded = true;
			if (pending !== null && !get(voterKeyOpen)) go(pending);
		} catch (err) {
			console.error('failed to load ballot', err);
			ballotLoaded = true;
		}
	}

	function resetBallot() {
		mine = null;
		publishedBy = null;
		ballotLoaded = false;
		withdrawOpen = false;
	}

	function say(msg: string) {
		flash = msg;
		if (flashTimer) clearTimeout(flashTimer);
		flashTimer = setTimeout(() => (flash = null), 5000);
	}

	// change proves and casts the next ballot; while it runs the vote buttons
	// wait. It tells whether the ballot was cast.
	async function change(next: MyBallot, done: string): Promise<boolean> {
		const key = get(voterKey);
		if (busy || !key || !event) return false;
		busy = true;
		actionError = null;
		try {
			publishedBy = await cast(slug, event.id, key, windows, next);
			mine = next.kind === 'withdraw' ? null : next;
			say(done);
			return true;
		} catch (err) {
			if (err instanceof NotInWindow) {
				actionError = tr('vote.error.waiting', currentLang, { d: waitingText() });
			} else if (err instanceof BallotError && err.code === 'closed') {
				actionError = tr('vote.error.closed', currentLang);
				load();
			} else {
				actionError = err instanceof Error ? err.message : String(err);
			}
			return false;
		} finally {
			busy = false;
		}
	}

	function waitingText(): string {
		const w = get(voterKey)?.waitingUntil;
		return w ? closesText(w) : '';
	}

	// pick is a click on "Rank" for a candidate (or on the blank vote):
	// it gets a voting key first if needed.
	function pick(choice: number | 'blank') {
		pending = choice;
		if (!hasKey) {
			voterKeyOpen.set(true);
			return;
		}
		if (!ballotLoaded) return; // goes on once the ballot is loaded
		go(choice);
	}

	function go(choice: number | 'blank') {
		pending = null;
		if (busy) return;
		if (choice === 'blank') {
			if (!blank) change({ kind: 'blank', choices: [] }, tr('vote.done.blank', currentLang));
			return;
		}
		if (ballot.includes(choice)) return;
		if (full) {
			actionError = tr('vote.error.full', currentLang, { n: maxChoices });
			return;
		}
		const name = displayName(byId.get(choice)?.name ?? '');
		const next = [...ballot, choice];
		change({ kind: 'choices', choices: next }, tr('vote.done.added', currentLang, { name, rank: rankWord(next.length) }));
	}

	function remove(id: number) {
		const name = displayName(byId.get(id)?.name ?? '');
		const next = ballot.filter((x) => x !== id);
		change(next.length > 0 ? { kind: 'choices', choices: next } : { kind: 'withdraw', choices: [] }, tr('vote.done.removed', currentLang, { name }));
	}

	async function withdrawAll() {
		withdrawOpen = false;
		await change({ kind: 'withdraw', choices: [] }, tr('vote.done.withdrawn', currentLang));
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

	// The bars are on a 0–100 % scale of all the voters, blank votes
	// included.
	function barWidth(share: number | null): number {
		return share === null ? 0 : Math.round(share * 100);
	}

	function ordinal(n: number): string {
		if (currentLang === 'en') return `#${n}`;
		return n === 1 ? '1er' : `${n}e`;
	}

	// rankWord is the rank of a choice in words: 1er, 2e (1st, 2nd).
	function rankWord(n: number): string {
		if (currentLang === 'en') return n === 1 ? '1st' : n === 2 ? '2nd' : n === 3 ? '3rd' : `${n}th`;
		return n === 1 ? '1er' : `${n}e`;
	}

	function closesText(iso: string): string {
		const d = new Date(iso);
		return d.toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}

	function timeText(iso: string): string {
		return new Date(iso).toLocaleString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { weekday: 'long', hour: '2-digit', minute: '2-digit' });
	}

	let unsubscribe: (() => void) | null = null;
	let unwatchBallot: (() => void) | null = null;
	let refreshTimer: ReturnType<typeof setTimeout> | null = null;

	onMount(async () => {
		const v = new URL(window.location.href).searchParams.get('vue') as View | null;
		if (v && VIEWS.includes(v)) view = v;
		await load();
		unwatchBallot = onBallotChanged(() => {
			if (!busy) loadBallot();
		});
		realtimeManager.connect();
		if (event) {
			unsubscribe = subscribeToPollEvents(event.id, () => {
				// A batch publishes many ballots: one reload per second at most.
				if (refreshTimer) return;
				refreshTimer = setTimeout(() => {
					refreshTimer = null;
					load().then(() => loadBallot());
				}, 1000);
			});
		}
	});

	onDestroy(() => {
		unsubscribe?.();
		unwatchBallot?.();
		if (refreshTimer) clearTimeout(refreshTimer);
		if (flashTimer) clearTimeout(flashTimer);
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

<!-- The lab's temporary event: the page wears the event's colour, not the checker's. -->
<div class="event-page">

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
	<p class="event-strip">
		<span class="event-strip-tag">{tr('event.tag', currentLang)}</span>
		<span>{event.closes_at ? tr('event.strip.ends', currentLang, { d: closesText(event.closes_at) }) : tr('event.strip', currentLang)}</span>
		<a href="/lab">{tr('event.strip.why', currentLang)} →</a>
	</p>
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

	{#if results.open || mine}
		<section class="ballot" aria-labelledby="ballot-title">
			<div class="ballot-head">
				<h2 id="ballot-title">{tr('vote.title', currentLang)}</h2>
				{#if !hasKey}
					<button class="btn btn-primary" onclick={() => voterKeyOpen.set(true)}>{tr('poll.ballot.get_key', currentLang)}</button>
				{:else}
					<a href="/profile" class="profile-link">{tr('vote.profile_link', currentLang)}</a>
				{/if}
			</div>
			{#if results.open}
				<p class="hint">{tr('vote.hint', currentLang, { n: maxChoices })}</p>
			{:else}
				<p class="hint">{tr('vote.closed_hint', currentLang)}</p>
			{/if}

			{#if hasKey}
				{#if !protectedEnough($voterKey)}
					<button class="protect" onclick={() => voterKeyOpen.set(true)}>{tr('vote.protect', currentLang)}</button>
				{/if}
				{#if blank}
					<div class="blank-card">
						<span class="blank-mark" aria-hidden="true">◻</span>
						<div>
							<strong>{tr('vote.blank.mine', currentLang)}</strong>
							<span>{tr('vote.blank.counted', currentLang)}</span>
						</div>
					</div>
				{:else}
					<ol class="slots">
						{#each Array.from({ length: maxChoices }, (_, i) => i) as i (i)}
							{@const id = ballot[i]}
							{@const c = id === undefined ? undefined : byId.get(id)}
							<li class:filled={id !== undefined} class:next={id === undefined && i === ballot.length}>
								<span class="slot-num">{i + 1}</span>
								{#if id !== undefined}
									<span class="slot-name">{displayName(c?.name ?? `#${id}`)}</span>
									{#if results.open}
										<button class="unpick" onclick={() => remove(id)} disabled={busy} aria-label={tr('vote.remove_named', currentLang, { name: c?.name ?? '' })}>×</button>
									{/if}
								{:else if results.open && i === ballot.length}
									<span class="slot-empty">{tr(i === 0 ? 'vote.slot.first' : 'vote.slot.empty', currentLang)}</span>
								{/if}
							</li>
						{/each}
					</ol>
				{/if}
				<div class="ballot-actions" aria-live="polite">
					{#if results.open && !blank}
						<button class="btn" onclick={() => pick('blank')} disabled={busy}>{tr('vote.blank.button', currentLang)}</button>
					{/if}
					{#if results.open && mine}
						<button class="btn" onclick={() => (withdrawOpen = true)} disabled={busy}>{tr('vote.withdraw_all', currentLang)}</button>
					{/if}
					{#if busy}
						<span class="status">{tr('vote.proving', currentLang)}</span>
					{:else if flash}
						<span class="status status--ok">{flash}</span>
					{/if}
				</div>
				{#if publishedBy && mine && !busy}
					<p class="published">{tr('vote.published_by', currentLang, { t: timeText(publishedBy) })}</p>
				{/if}
				{#if actionError}
					<p class="save-error" role="alert">{actionError}</p>
				{/if}
			{:else if results.open}
				<button class="btn" onclick={() => pick('blank')}>{tr('vote.blank.button', currentLang)}</button>
			{/if}
		</section>
	{/if}

	<Modal open={withdrawOpen} title={tr('vote.withdraw.title', currentLang)} onclose={() => (withdrawOpen = false)} closeLabel={tr('auth.close', currentLang)}>
		<p class="dialog-text">{tr(blank ? 'vote.withdraw.text_blank' : 'vote.withdraw.text', currentLang, { n: ballot.length })}</p>
		{#snippet actions()}
			<button class="btn" onclick={() => (withdrawOpen = false)}>{tr('vote.cancel', currentLang)}</button>
			<button class="btn btn-danger" onclick={withdrawAll} disabled={busy}>{tr('vote.withdraw_all', currentLang)}</button>
		{/snippet}
	</Modal>

	{#snippet pickButton(c: CandidateResult)}
		{#if results?.open}
			{@const at = ballot.indexOf(c.person_id)}
			{#if at >= 0}
				<button class="pick pick--mine" onclick={() => remove(c.person_id)} disabled={busy} title={tr('vote.ranked_as', currentLang, { rank: rankWord(at + 1) })}>
					{rankWord(at + 1)} ×
				</button>
			{:else}
				<button class="pick" class:pick--full={hasKey && full} onclick={() => pick(c.person_id)} disabled={busy || (hasKey && full)}>
					{tr('vote.add', currentLang)}
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
			<span class="name">{displayName(c.name)}</span>
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
		{#if differ && rankingWinner && majorityWinner}
			<p class="differ">
				{tr(results.condorcet_winner === rankingWinner.person_id ? 'poll.differ' : 'poll.differ_no_condorcet', currentLang, {
					a: displayName(rankingWinner.name),
					b: displayName(majorityWinner.name)
				})}
			</p>
		{/if}
		<div class="lists" class:lists--one={!!results.hidden}>
			<section class="list">
				<h3>{tr('poll.list.ranking', currentLang)} <small>{tr('poll.list.ranking_sub', currentLang)}</small></h3>
				<ol class="vote-list">
					{#each sorted as c (c.person_id)}
						<li class="vote-row" class:vote-row--mine={ballot.includes(c.person_id)} class:vote-row--top={c.preference_rank === 1}>
							<span class="vote-rank">{c.preference_rank ?? '—'}</span>
							<div class="vote-main">
								{@render who(c)}
								{#if c.first_choice_share === null}
									<span class="meta">{tr('poll.gauge.hidden', currentLang)}</span>
								{:else}
									{#if results.condorcet_winner === c.person_id}
										<span class="winner">{tr('poll.condorcet', currentLang)}</span>
									{/if}
									<span class="meta">{tr('poll.first_ranked', currentLang, { f: pct(c.first_choice_share), r: pct(c.ranked_share) })}</span>
								{/if}
							</div>
							<div class="vote-pick">{@render pickButton(c)}</div>
						</li>
					{/each}
				</ol>
			</section>
			{#if !results.hidden}
				<section class="list">
					<h3>{tr('poll.list.majority', currentLang)} <small>{tr('poll.list.majority_sub', currentLang)}</small></h3>
					<ol class="vote-list">
						{#each byMajority as c (c.person_id)}
							<li class="vote-row" class:vote-row--mine={ballot[0] === c.person_id} class:vote-row--top={c.majority_rank === 1}>
								<span class="vote-rank">{c.majority_rank ?? '—'}</span>
								<div class="vote-main">
									{@render who(c)}
									<div class="vote-bar-line">
										<div class="bar bar--wide" aria-hidden="true">
											<span class="bar-fill--vote" style="width: {barWidth(c.first_choice_share)}%"></span>
										</div>
										<span class="value">{pct(c.first_choice_share)}</span>
									</div>
								</div>
							</li>
						{/each}
						{#if results.blank_share !== null}
							<li class="vote-row vote-row--blank" class:vote-row--mine={blank}>
								<span class="vote-rank">—</span>
								<div class="vote-main">
									<span class="who"><span class="photo photo--empty photo--blank" aria-hidden="true">◻</span><span class="name">{tr('vote.blank.name', currentLang)}</span></span>
									<div class="vote-bar-line">
										<div class="bar bar--wide" aria-hidden="true">
											<span class="bar-fill--vote bar-fill--blank" style="width: {barWidth(results.blank_share)}%"></span>
										</div>
										<span class="value">{pct(results.blank_share)}</span>
									</div>
								</div>
							</li>
						{/if}
					</ol>
				</section>
			{/if}
		</div>
		<p class="board-note">{tr('poll.note.vote', currentLang)}</p>
	{:else if view === 'honesty'}
		<div class="gauge-grid">
			{#each sorted as c (c.person_id)}
				<article class="gauge-card" class:gauge-card--mine={ballot.includes(c.person_id)}>
					<span class="gauge-rank">{ordinal(c.honesty_rank)}</span>
					{@render who(c)}
					<HonestyGauge score={c.honesty_score} size={200} label={displayName(c.name)} />
					<span class="meta">{tr('poll.gauge.facts', currentLang, { n: c.fact_count })} · {tr('poll.gauge.contradictions', currentLang, { n: c.contradiction_count })} <ScoreHelp name={c.name} personId={c.person_id} /></span>
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
				<div class="board-row" class:board-row--mine={ballot.includes(c.person_id)} role="row">
					<div class="cell cell--vote" role="cell">
						{#if c.first_choice_share === null}
							<span class="value value--muted">—</span>
							<span class="meta">{tr('poll.gauge.hidden', currentLang)}</span>
						{:else}
							<div class="value-line">
								<span class="value">{ordinal(c.preference_rank ?? 0)}</span>
							</div>
							<div class="bar bar--vote" aria-hidden="true">
								<span class="bar-fill--vote" style="width: {barWidth(c.first_choice_share)}%"></span>
							</div>
							<span class="meta">{tr('poll.first_ranked', currentLang, { f: pct(c.first_choice_share), r: pct(c.ranked_share) })}</span>
						{/if}
					</div>

					<div class="cell cell--name" role="cell">
						{@render who(c)}
						{@render pickButton(c)}
					</div>

					<div class="cell cell--honesty" role="cell">
						<HonestyGauge score={c.honesty_score} size={130} label={displayName(c.name)} />
						<span class="meta">{ordinal(c.honesty_rank)} · {tr('poll.gauge.facts', currentLang, { n: c.fact_count })} <ScoreHelp name={c.name} personId={c.person_id} /></span>
					</div>
				</div>
			{/each}
			{#if results.blank_share !== null}
				<div class="board-row board-row--blank" class:board-row--mine={blank} role="row">
					<div class="cell cell--vote" role="cell">
						<div class="value-line"><span class="value">{pct(results.blank_share)}</span></div>
						<div class="bar bar--vote" aria-hidden="true">
							<span class="bar-fill--vote bar-fill--blank" style="width: {barWidth(results.blank_share)}%"></span>
						</div>
						<span class="meta">{tr('poll.gauge.votes', currentLang, { n: results.blank ?? 0 })}</span>
					</div>
					<div class="cell cell--name" role="cell">
						<span class="who"><span class="photo photo--empty photo--blank" aria-hidden="true">◻</span><span class="name">{tr('vote.blank.name', currentLang)}</span></span>
					</div>
					<div class="cell cell--honesty" role="cell"><span class="meta">{tr('vote.blank.no_score', currentLang)}</span></div>
				</div>
			{/if}
		</div>
	{/if}

	<details class="how">
		<summary>{tr('poll.how.title', currentLang)}</summary>
		<ul>
			<li>{@html tr('poll.how.preference', currentLang)}</li>
			<li>{@html tr('poll.how.honesty', currentLang)}</li>
			<li>{@html tr('poll.how.compare', currentLang)}</li>
			<li>{tr('poll.how.facts', currentLang)}</li>
			<li>{@html tr('poll.how.verification', currentLang)}</li>
			<li>{tr('poll.how.blank', currentLang)}</li>
			<li>{@html tr('poll.how.privacy', currentLang)}</li>
		</ul>
	</details>
{/if}
</div>

<style>
	.event-page {
		--accent: var(--event-strong);
		--accent-hover: var(--event);
		--accent-glow: var(--event-glow);
	}
	.event-strip {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		align-items: center;
		gap: 0.4rem 0.9rem;
		max-width: 46rem;
		margin: 0.5rem auto 0;
		padding: 0.55rem 1rem;
		border: 1px dashed var(--event);
		border-radius: var(--radius-full);
		background: var(--event-glow);
		font-size: 0.85rem;
		text-align: center;
	}
	.event-strip-tag {
		font-size: 0.66rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		padding: 0.15rem 0.5rem;
		border-radius: var(--radius-full);
		background: var(--event-strong);
		color: white;
	}
	.event-strip a {
		color: var(--event);
	}
	.slots {
		list-style: none;
		margin: 0 0 0.75rem;
		padding: 0;
		display: grid;
		gap: 0.4rem;
	}
	.slots li {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		min-height: 2.6rem;
		padding: 0.35rem 0.6rem 0.35rem 0.4rem;
		border: 1px dashed var(--glass-border);
		border-radius: var(--radius-md);
		color: var(--text-muted);
		font-size: 0.9rem;
	}
	.slots li.filled {
		border-style: solid;
		border-color: var(--accent);
		background: var(--accent-glow);
		color: var(--text-primary);
	}
	.slots li.next {
		border-color: var(--accent);
	}
	.slot-num {
		width: 1.7rem;
		height: 1.7rem;
		flex-shrink: 0;
		display: grid;
		place-items: center;
		border-radius: 50%;
		background: var(--bg-secondary);
		font-weight: 700;
		font-size: 0.85rem;
	}
	.slots li.filled .slot-num {
		background: var(--accent);
		color: white;
	}
	.slot-name {
		flex: 1;
		min-width: 0;
		font-weight: 600;
		text-transform: capitalize;
	}
	.slot-empty {
		font-style: italic;
	}
	.unpick {
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 1.2rem;
		line-height: 1;
		cursor: pointer;
		padding: 0.1rem 0.3rem;
	}
	.unpick:hover {
		color: var(--red);
	}
	.protect {
		display: block;
		width: 100%;
		margin: 0 0 0.75rem;
		padding: 0.6rem 0.8rem;
		text-align: left;
		background: color-mix(in oklch, var(--accent) 12%, var(--bg-card));
		border: 1px solid var(--accent);
		border-radius: var(--radius-md);
		color: var(--text-primary);
		font: inherit;
		font-size: 0.88rem;
		line-height: 1.4;
		cursor: pointer;
	}
	.blank-card {
		display: flex;
		gap: 0.75rem;
		align-items: center;
		padding: 0.75rem 0.9rem;
		border: 1px dashed var(--text-secondary);
		border-radius: var(--radius-md);
		background: var(--bg-card);
	}
	.blank-card div {
		display: grid;
		gap: 0.15rem;
	}
	.blank-card span:not(.blank-mark) {
		color: var(--text-secondary);
		font-size: 0.85rem;
	}
	.blank-mark,
	.photo--blank {
		font-size: 1.2rem;
		color: var(--text-secondary);
	}
	.bar-fill--blank {
		background: var(--text-muted) !important;
	}
	.vote-row--blank .name,
	.board-row--blank .name {
		font-style: italic;
	}
	.published {
		margin: 0.5rem 0 0;
		font-size: 0.82rem;
		color: var(--text-secondary);
	}
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
	.profile-link {
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	.profile-link:hover {
		color: var(--text-primary);
	}
	.dialog-text {
		font-size: 0.9rem;
		line-height: 1.5;
		color: var(--text-secondary);
		margin: 0 0 0.75rem;
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

	.differ {
		max-width: 52rem;
		margin: 0 auto 1rem;
		padding: 0.8rem 1rem;
		border-left: 3px solid var(--accent);
		border-radius: var(--radius-sm);
		background: var(--accent-glow);
		font-size: 0.95rem;
		line-height: 1.5;
	}
	.lists {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1.25rem;
		max-width: 60rem;
		margin: 0 auto;
	}
	.lists--one {
		grid-template-columns: 1fr;
		max-width: 40rem;
	}
	.list h3 {
		margin: 0 0 0.5rem;
		font-family: var(--font-serif);
		font-size: 1.2rem;
	}
	.list h3 small {
		margin-left: 0.3rem;
		font-family: var(--font-family);
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--text-muted);
	}
	.board-note {
		max-width: 46rem;
		margin: 0.9rem auto 0;
		font-size: 0.8rem;
		line-height: 1.5;
		color: var(--text-muted);
		text-align: center;
	}
	.vote-list {
		list-style: none;
		margin: 0;
		padding: 0;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		overflow: hidden;
	}
	.vote-row {
		display: grid;
		grid-template-columns: 1.8rem minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.75rem;
		padding: 0.7rem 1rem;
		border-bottom: 1px solid var(--border-card);
	}
	.vote-row:last-child {
		border-bottom: none;
	}
	.vote-row--mine {
		background: var(--accent-glow);
	}
	.vote-row--top .vote-rank {
		color: var(--accent-hover);
	}
	.vote-rank {
		font-size: 1.2rem;
		font-weight: 700;
		color: var(--text-muted);
		text-align: center;
		font-variant-numeric: tabular-nums;
	}
	.vote-main {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		min-width: 0;
	}
	.vote-row .who {
		flex-direction: row;
		gap: 0.6rem;
		text-align: left;
		align-items: center;
	}
	.vote-row .photo {
		width: 36px;
		height: 36px;
		flex-shrink: 0;
		font-size: 1rem;
	}
	.vote-row .meta {
		justify-content: flex-start;
		text-align: left;
	}
	.winner {
		width: fit-content;
		padding: 0.1rem 0.5rem;
		border-radius: var(--radius-full);
		background: var(--accent);
		color: white;
		font-size: 0.7rem;
		font-weight: 700;
	}
	.vote-bar-line {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.vote-bar-line .value {
		min-width: 3rem;
		font-size: 1.1rem;
		text-align: right;
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
	.pick--full {
		border-style: dashed;
	}
	.pick--mine {
		background: var(--accent);
		border-color: var(--accent);
		color: white;
	}

	.how {
		max-width: 46rem;
		margin: 3rem auto 1rem;
		padding: 1.25rem 1.5rem;
		border-top: 1px solid var(--glass-border);
	}
	.how summary {
		font-family: var(--font-serif);
		font-size: 1.2rem;
		color: var(--text-secondary);
		cursor: pointer;
	}
	.how summary:hover {
		color: var(--text-primary);
	}
	.how[open] summary {
		margin-bottom: 0.75rem;
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

	@media (max-width: 760px) {
		.lists {
			grid-template-columns: 1fr;
		}
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
			gap: 0.5rem;
			padding: 0.7rem 0.75rem;
		}
		.name {
			font-size: 0.82rem;
		}
	}
</style>
