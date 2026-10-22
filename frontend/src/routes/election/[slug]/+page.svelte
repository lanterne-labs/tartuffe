<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import { page } from '$app/stores';
	import { API_BASE } from '$lib/config';
	import { displayName } from '$lib/share';
	import { lang, tr } from '$lib/i18n';
	import { session, signInOpen, verifyOpen, setUser } from '$lib/auth';
	import { movedNote } from '$lib/components/VerifyDialog.svelte';
	import { addVote, removeVote, saveBallot, getBallot, onBallotChanged, BallotError } from '$lib/ballot';
	import { realtimeManager, subscribeToPollEvents, clearOldEvents } from '$lib/realtime';
	import HonestyGauge from '$lib/components/HonestyGauge.svelte';
	import ScoreHelp from '$lib/components/ScoreHelp.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import VoteList from '$lib/components/VoteList.svelte';
	import { replaceState } from '$app/navigation';
	import type { PollResponse, CandidateResult, Ballot } from '$lib/types';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let signedIn = $derived($session !== null);
	// verified: the account holds a document, it may vote.
	let verified = $derived($session?.user.verified ?? false);
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

	// The user's votes as stored (person ids, first choice first). Every
	// change is saved at once and the page shows the ballot the API returns.
	let ballot: number[] = $state([]);
	let consented = $state(false);
	let busy = $state(false);
	let actionError: string | null = $state(null);
	let flash: string | null = $state(null);
	let flashTimer: ReturnType<typeof setTimeout> | null = null;
	// Each ballot request takes a number: an answer older than the last change
	// is dropped, so a slow reload never undoes a vote.
	let ballotSeq = 0;
	let ballotLoaded = $state(false);

	// The vote waiting for a step before it is cast: the sign-in, the consent,
	// or the choice of the vote to give up when every vote is used.
	let pending: number | null = $state(null);
	let consentOpen = $state(false);
	let changeOpen = $state(false);
	let withdrawOpen = $state(false);

	let event = $derived(data?.event ?? null);
	let results = $derived(data?.results ?? null);
	let maxChoices = $derived(event?.max_choices ?? 3);
	let full = $derived(ballot.length >= maxChoices);
	let byId = $derived(new Map<number, CandidateResult>((results?.candidates ?? []).map((c: CandidateResult) => [c.person_id, c])));
	let pendingName = $derived(pending !== null ? (byId.get(pending)?.name ?? '') : '');
	let ballotVotes = $derived(
		ballot.map((id) => {
			const c = byId.get(id);
			return { person_id: id, name: c?.name ?? `#${id}`, image_url: c?.image_url };
		})
	);

	// The ballot loads once per session and event, then again when another
	// tab or device may have changed it.
	let ballotKey = '';
	$effect(() => {
		const key = $session && event ? `${$session.token}:${event.id}` : '';
		if (key === ballotKey) return;
		ballotKey = key;
		untrack(() => (key ? loadBallot() : resetBallot()));
	});

	// A sign-in dialog closed without signing in drops the vote it was for.
	$effect(() =>
		signInOpen.subscribe((isOpen) => {
			if (!isOpen && !$session) untrack(() => (pending = null));
		})
	);
	// So does a verification dialog closed without verifying; once verified,
	// the vote goes on.
	$effect(() =>
		verifyOpen.subscribe((isOpen) => {
			if (isOpen) return;
			untrack(() => {
				if (!verified) pending = null;
				else if (pending !== null && ballotLoaded) pick(pending);
			});
		})
	);
	// A document that brought its votes from another account: show them.
	$effect(() =>
		movedNote.subscribe((moved) => {
			if (!moved) return;
			untrack(() => {
				movedNote.set(false);
				say(tr('verify.moved', currentLang));
				loadBallot();
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

	function showBallot(b: Ballot) {
		ballot = b.person_ids;
		consented = b.consented;
	}

	async function loadBallot() {
		const seq = ++ballotSeq;
		try {
			const b = await getBallot(slug);
			if (seq !== ballotSeq) return;
			showBallot(b);
			ballotLoaded = true;
			// The vote that asked for the sign-in goes on now.
			if (pending !== null && !consentOpen && !changeOpen) pick(pending);
		} catch (err) {
			console.error('failed to load ballot', err);
		}
	}

	function resetBallot() {
		ballotSeq++;
		ballot = [];
		consented = false;
		ballotLoaded = false;
		consentOpen = changeOpen = withdrawOpen = false;
	}

	function say(msg: string) {
		flash = msg;
		if (flashTimer) clearTimeout(flashTimer);
		flashTimer = setTimeout(() => (flash = null), 4000);
	}

	// change runs one ballot request; while it runs the vote buttons wait.
	// It tells whether the change was saved.
	async function change(request: () => Promise<Ballot>, done: string): Promise<boolean> {
		if (busy) return false;
		busy = true;
		actionError = null;
		const seq = ++ballotSeq;
		try {
			const b = await request();
			if (seq === ballotSeq) showBallot(b);
			say(done);
			load();
			return true;
		} catch (err) {
			if (!(err instanceof BallotError)) {
				actionError = err instanceof Error ? err.message : String(err);
				return false;
			}
			if (err.ballot && seq === ballotSeq) showBallot(err.ballot);
			switch (err.code) {
				case 'ballot_full':
					// Another tab used the last vote first: ask which to give up.
					changeOpen = pending !== null;
					break;
				case 'consent_required':
					consented = false;
					consentOpen = pending !== null;
					break;
				case 'verification_required':
					// The document moved to another account meanwhile.
					if ($session) setUser({ ...$session.user, verified: false });
					if (pending !== null) verifyOpen.set(true);
					break;
				case 'closed':
					actionError = tr('vote.error.closed', currentLang);
					load();
					break;
				case 'signed_out':
					actionError = tr('vote.error.signed_out', currentLang);
					break;
				case 'not_on_ballot':
					actionError = tr('vote.error.not_on_ballot', currentLang);
					break;
				default:
					actionError = err.message;
			}
			return false;
		} finally {
			busy = false;
		}
	}

	// pick is a click on "Vote" for a candidate: it signs in, asks the
	// consent, or asks which vote to give up first, as needed.
	function pick(id: number) {
		pending = id;
		if (!signedIn) {
			signInOpen.set(true);
			return;
		}
		if (!verified) {
			verifyOpen.set(true);
			return;
		}
		if (!ballotLoaded) return; // goes on once the ballot is loaded
		if (busy || ballot.includes(id)) {
			pending = null;
			return;
		}
		if (!consented) {
			consentOpen = true;
			return;
		}
		if (full) {
			changeOpen = true;
			return;
		}
		cast(id);
	}

	async function cast(id: number, replace?: number) {
		const name = byId.get(id)?.name ?? '';
		await change(() => addVote(slug, id, { replace, consent: true }), tr('vote.done.added', currentLang, { name }));
		// A refusal that needs an answer (the consent, the vote to give up)
		// keeps the vote pending for its dialog.
		if (!consentOpen && !changeOpen) pending = null;
	}

	function acceptConsent() {
		consentOpen = false;
		if (pending === null) return;
		if (full) changeOpen = true;
		else cast(pending);
	}

	function replaceWith(old: number) {
		changeOpen = false;
		if (pending !== null) cast(pending, old);
	}

	function cancelPending() {
		consentOpen = changeOpen = false;
		pending = null;
	}

	function remove(id: number) {
		const name = byId.get(id)?.name ?? '';
		change(() => removeVote(slug, id), tr('vote.done.removed', currentLang, { name }));
	}

	function move(i: number, delta: number) {
		const j = i + delta;
		if (j < 0 || j >= ballot.length) return;
		const next = [...ballot];
		[next[i], next[j]] = [next[j], next[i]];
		change(() => saveBallot(slug, next), tr('vote.done.moved', currentLang));
	}

	async function withdrawAll() {
		withdrawOpen = false;
		await change(() => saveBallot(slug, []), tr('vote.done.withdrawn', currentLang));
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
	let unwatchBallot: (() => void) | null = null;
	let refreshTimer: ReturnType<typeof setTimeout> | null = null;

	onMount(async () => {
		const v = new URL(window.location.href).searchParams.get('vue') as View | null;
		if (v && VIEWS.includes(v)) view = v;
		await load();
		unwatchBallot = onBallotChanged(() => {
			if ($session && event && !busy) loadBallot();
		});
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

	{#if results.open || ballot.length > 0}
		<section class="ballot" aria-labelledby="ballot-title">
			<div class="ballot-head">
				<h2 id="ballot-title">{tr('vote.title', currentLang)}</h2>
				{#if !signedIn}
					<button class="btn btn-primary" onclick={() => signInOpen.set(true)}>{tr('poll.ballot.sign_in', currentLang)}</button>
				{:else if !verified}
					<button class="btn btn-primary" onclick={() => verifyOpen.set(true)}>{tr('poll.ballot.verify', currentLang)}</button>
				{:else}
					<a href="/profile" class="profile-link">{tr('vote.profile_link', currentLang)}</a>
				{/if}
			</div>
			{#if results.open}
				<p class="hint">{tr('vote.hint', currentLang, { n: maxChoices })}</p>
			{:else}
				<p class="hint">{tr('vote.closed_hint', currentLang)}</p>
			{/if}

			{#if signedIn}
				{#if provider === 'noop'}
					<p class="test-account">{tr('poll.ballot.test_account', currentLang)}</p>
				{/if}
				<VoteList votes={ballotVotes} max={maxChoices} lang={currentLang} editable={results.open} {busy} onmove={move} onremove={remove} />
				{#if ballot.length === 0 && results.open}
					<p class="empty">{tr('vote.empty', currentLang)}</p>
				{/if}
				<div class="ballot-actions" aria-live="polite">
					{#if results.open && ballot.length > 0}
						<button class="btn" onclick={() => (withdrawOpen = true)} disabled={busy}>{tr('vote.withdraw_all', currentLang)}</button>
					{/if}
					{#if busy}
						<span class="status">{tr('poll.ballot.saving', currentLang)}</span>
					{:else if flash}
						<span class="status status--ok">{flash}</span>
					{/if}
				</div>
				{#if actionError}
					<p class="save-error" role="alert">{actionError}</p>
				{/if}
			{/if}
		</section>
	{/if}

	<Modal open={consentOpen} title={tr('vote.consent.title', currentLang)} onclose={cancelPending} closeLabel={tr('auth.close', currentLang)}>
		<p class="dialog-text">{tr('poll.ballot.consent', currentLang)}</p>
		{#snippet actions()}
			<button class="btn" onclick={cancelPending}>{tr('vote.cancel', currentLang)}</button>
			<button class="btn btn-primary" onclick={acceptConsent}>{tr('vote.consent.accept', currentLang, { name: pendingName })}</button>
		{/snippet}
	</Modal>

	<Modal open={changeOpen} title={tr('vote.change.title', currentLang)} onclose={cancelPending} closeLabel={tr('auth.close', currentLang)}>
		<p class="dialog-text">{tr('vote.change.text', currentLang, { n: ballot.length, name: pendingName })}</p>
		<ul class="swap-list">
			{#each ballot as id, i (id)}
				{@const c = byId.get(id)}
				<li class="swap">
					<span class="swap-rank">{i + 1}</span>
					<span class="swap-name">{c?.name ?? `#${id}`}</span>
					<button class="btn btn-danger" onclick={() => replaceWith(id)} disabled={busy}>
						{tr('vote.change.remove', currentLang)}
					</button>
				</li>
			{/each}
		</ul>
		<p class="dialog-note">{tr('vote.change.note', currentLang, { name: pendingName })}</p>
		{#snippet actions()}
			<a href="/profile" class="btn">{tr('vote.profile_link', currentLang)}</a>
			<button class="btn" onclick={cancelPending}>{tr('vote.change.keep', currentLang)}</button>
		{/snippet}
	</Modal>

	<Modal open={withdrawOpen} title={tr('vote.withdraw.title', currentLang)} onclose={() => (withdrawOpen = false)} closeLabel={tr('auth.close', currentLang)}>
		<p class="dialog-text">{tr('vote.withdraw.text', currentLang, { n: ballot.length })}</p>
		{#snippet actions()}
			<button class="btn" onclick={() => (withdrawOpen = false)}>{tr('vote.cancel', currentLang)}</button>
			<button class="btn btn-danger" onclick={withdrawAll} disabled={busy}>{tr('vote.withdraw_all', currentLang)}</button>
		{/snippet}
	</Modal>

	{#snippet pickButton(c: CandidateResult)}
		{#if results?.open}
			{@const mine = ballot.indexOf(c.person_id)}
			{#if mine >= 0}
				<button class="pick pick--mine" onclick={() => remove(c.person_id)} disabled={busy} title={tr('vote.remove_named', currentLang, { name: c.name })}>
					{tr('vote.mine', currentLang, { n: mine + 1 })} ✓
				</button>
			{:else}
				<button class="pick" class:pick--full={signedIn && full} onclick={() => pick(c.person_id)} disabled={busy}>
					{signedIn && full ? tr('vote.change_button', currentLang) : tr('vote.add', currentLang)}
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
		<ol class="vote-list">
			{#each sorted as c (c.person_id)}
				<li class="vote-row" class:vote-row--mine={ballot.includes(c.person_id)}>
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
						<HonestyGauge score={c.honesty_score} size={130} label={displayName(c.name)} />
						<span class="meta">{ordinal(c.honesty_rank)} · {tr('poll.gauge.facts', currentLang, { n: c.fact_count })} <ScoreHelp name={c.name} personId={c.person_id} /></span>
					</div>
				</div>
			{/each}
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
			<li>{tr('poll.how.privacy', currentLang)}</li>
		</ul>
	</details>
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
	.dialog-note {
		font-size: 0.8rem;
		color: var(--text-muted);
		margin: 0.75rem 0 0;
	}
	.swap-list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
	}
	.swap {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		padding: 0.45rem 0.75rem;
		background: var(--bg-card);
		border-radius: var(--radius-sm);
	}
	.swap-rank {
		width: 1.5rem;
		height: 1.5rem;
		flex-shrink: 0;
		display: grid;
		place-items: center;
		border-radius: 50%;
		background: var(--accent);
		color: white;
		font-weight: 700;
		font-size: 0.8rem;
	}
	.swap-name {
		flex: 1;
		min-width: 0;
		font-weight: 500;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	a.btn {
		text-decoration: none;
		display: inline-flex;
		align-items: center;
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
