<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import { get } from 'svelte/store';
	import { page } from '$app/stores';
	import { API_BASE } from '$lib/config';
	import { displayName } from '$lib/share';
	import { lang, tr } from '$lib/i18n';
	import { voterKey, voterKeyOpen, protectedEnough } from '$lib/voter';
	import { cast, myBallot, onBallotChanged, BallotError, NotInWindow, type MyBallot, type Check, type Via } from '$lib/anonballot';
	import { realtimeManager, subscribeToPollEvents, clearOldEvents } from '$lib/realtime';
	import HonestyGauge from '$lib/components/HonestyGauge.svelte';
	import ScoreHelp from '$lib/components/ScoreHelp.svelte';
	import Modal from '$lib/components/Modal.svelte';
	import { replaceState } from '$app/navigation';
	import { eventPaths, showVoters } from '$lib/event';
	import { recount, BLANK, type Step, type StepId } from '$lib/recount';
	import type { PollResponse, CandidateResult } from '$lib/types';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	// hasKey: this device holds a voting key a document admitted.
	let hasKey = $derived(!!$voterKey?.registered);

	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));
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
	// endsBlank: the ranking ends with the blank vote (the others: no).
	let endsBlank = $derived(ballot.at(-1) === BLANK);
	let publishedBy: string | null = $state(null);
	let busy = $state(false);
	let actionError: string | null = $state(null);
	let ballotLoaded = $state(false);

	// The vote waiting for the voting key before it is cast.
	let pending: number | 'blank' | null = $state(null);
	let withdrawOpen = $state(false);

	let event = $derived(data?.event ?? null);
	let results = $derived(data?.results ?? null);
	let windows = $derived(data?.windows ?? []);
	let via = $derived<Via>({ relays: data?.relays ?? [], publishedBy: data?.published_by ?? '' });
	// check: what this browser found of the ballot in the public log.
	let check = $state<Check | null>(null);
	// status is the icon on the ballot's title: proving and sending, received
	// (waiting for the batch that publishes it), on the public log as cast
	// (checked by this browser), or a fault.
	let status = $derived.by(() => {
		if (busy) return 'busy';
		if (check?.state === 'fault') return 'fault';
		if (!mine) return null;
		if (check?.state === 'included' && !publishedBy) return 'logged';
		return 'received';
	});

	// The voter's own check, step by step (the log signed, never rewritten,
	// the ballot in it as cast), and anyone's recount of the results.
	type MineStep = 'signed' | 'history' | 'mine';
	let mineSteps = $state<Record<MineStep, 'running' | 'ok' | 'failed' | 'waiting'> | null>(null);
	let recountSteps = $state<Step[] | null>(null);
	let recounting = $state(false);

	async function verifyMine() {
		const key = get(voterKey);
		if (!key?.registered || !event) return;
		mineSteps = { signed: 'running', history: 'running', mine: 'running' };
		try {
			const got = await myBallot(slug, event.id, key, windows, via);
			check = got.check ?? null;
			publishedBy = got.publishedBy ?? null;
			const f = check?.state === 'fault' ? check.fault.kind : null;
			const signedOk = f !== 'bad_signature' && f !== 'key_changed';
			const historyOk = signedOk && f !== 'forked';
			mineSteps = {
				signed: signedOk ? 'ok' : 'failed',
				history: !signedOk ? 'failed' : historyOk ? 'ok' : 'failed',
				mine: !historyOk ? 'failed' : f ? 'failed' : check?.state === 'included' && !publishedBy ? 'ok' : 'waiting'
			};
		} catch (err) {
			console.error('vote check failed', err);
			mineSteps = { signed: 'failed', history: 'failed', mine: 'failed' };
		}
	}

	async function verifyResults() {
		if (!event || !results || recounting) return;
		recounting = true;
		const steps: Step[] = (['signed', 'tree', 'proofs', 'count', 'shown'] as StepId[]).map((id) => ({ id, state: 'running' as const }));
		recountSteps = steps;
		try {
			await recount(event.id, results, (s) => {
				recountSteps = (recountSteps ?? steps).map((x) => (x.id === s.id ? s : x));
			});
		} catch (err) {
			console.error('recount failed', err);
			recountSteps = (recountSteps ?? steps).map((x) => (x.state === 'running' ? { ...x, state: 'failed' } : x));
		} finally {
			recountSteps = (recountSteps ?? steps).map((x) => (x.state === 'running' ? { ...x, state: 'skipped' } : x));
			recounting = false;
		}
	}

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
	// blankAt: where the blank vote's line goes in the ranked list, by its
	// rank (null while hidden).
	let blankAt = $derived.by(() => {
		const r = results?.blank_rank;
		if (results?.hidden || r === null || r === undefined) return null;
		const i = sorted.findIndex((c) => (c.preference_rank ?? 1e9) > r);
		return i < 0 ? sorted.length : i;
	});

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
			const got = await myBallot(slug, event.id, key, windows, via);
			if (busy) return;
			mine = got.ballot;
			publishedBy = got.publishedBy ?? null;
			check = got.check ?? null;
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
		check = null;
		ballotLoaded = false;
		withdrawOpen = false;
	}

	// change proves and casts the next ballot; while it runs the vote buttons
	// wait. It tells whether the ballot was cast.
	async function change(next: MyBallot): Promise<boolean> {
		const key = get(voterKey);
		if (busy || !key || !event) return false;
		busy = true;
		actionError = null;
		try {
			publishedBy = await cast(slug, event.id, key, windows, next, via);
			mine = next.kind === 'withdraw' ? null : next;
			check = mine ? { state: 'waiting' } : null;
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

	// saveEvidence downloads what proves the server's fault: the signed
	// checkpoints and the entry, for anyone to check.
	function saveEvidence(c: Check | null) {
		if (c?.state !== 'fault') return;
		const text = `tartuffe.me vote log fault: ${c.fault.kind}\n${c.fault.detail}\nfound by this browser at ${new Date().toISOString()}\n\n${c.fault.evidence}\n`;
		const a = document.createElement('a');
		a.href = URL.createObjectURL(new Blob([text], { type: 'text/plain' }));
		a.download = 'tartuffe-vote-log-proof.txt';
		a.click();
		URL.revokeObjectURL(a.href);
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
			// Alone, a blank ballot; after candidates, it ends the ranking.
			if (ballot.length === 0) {
				if (!blank) change({ kind: 'blank', choices: [] });
			} else if (!endsBlank) {
				if (full) actionError = tr('vote.error.full', currentLang, { n: maxChoices });
				else change({ kind: 'choices', choices: [...ballot, BLANK] });
			}
			return;
		}
		if (ballot.includes(choice)) return;
		if (full) {
			actionError = tr('vote.error.full', currentLang, { n: maxChoices });
			return;
		}
		// A candidate goes before the blank vote that ends the ranking.
		const next = endsBlank ? [...ballot.slice(0, -1), choice, BLANK] : [...ballot, choice];
		change({ kind: 'choices', choices: next });
	}

	function remove(id: number) {
		const next = ballot.filter((x) => x !== id);
		if (next.length === 1 && next[0] === BLANK) change({ kind: 'blank', choices: [] });
		else change(next.length > 0 ? { kind: 'choices', choices: next } : { kind: 'withdraw', choices: [] });
	}

	async function withdrawAll() {
		withdrawOpen = false;
		await change({ kind: 'withdraw', choices: [] });
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
			{#if showVoters(results.voters)}
				<strong>{tr('poll.voters', currentLang, { n: results.voters.toLocaleString(currentLang === 'en' ? 'en-GB' : 'fr-FR') })}</strong>
				<span class="dot"></span>
			{/if}
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
				{tr(results.voters === 0 ? 'poll.hidden.none' : 'poll.hidden.too_few_voters', currentLang)}
			{/if}
		</div>
	{/if}

	{#if results.open || mine}
		<section class="ballot" aria-labelledby="ballot-title">
			<div class="ballot-head">
				<h2 id="ballot-title">
					{tr('vote.title', currentLang)}
					{#if status === 'logged'}
						<a href="{paths.how}#journal" class="state-icon state-icon--logged" title={tr('vote.status.logged', currentLang)} aria-label={tr('vote.status.logged', currentLang)}>
							<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10.5" width="14" height="10" rx="2.2" /><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5" /><path class="tick" d="M9.3 15.6l1.9 1.9 3.6-3.7" /></svg>
						</a>
					{:else if status === 'received'}
						<span class="state-icon state-icon--ok" title={tr('vote.status.received', currentLang)} aria-label={tr('vote.status.received', currentLang)}>
							<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>
						</span>
					{:else if status === 'busy'}
						<span class="state-icon state-icon--spin" role="status" title={tr('vote.status.busy', currentLang)} aria-label={tr('vote.status.busy', currentLang)}></span>
					{:else if status === 'fault'}
						<span class="state-icon state-icon--fault" title={tr('vote.log.fault', currentLang)} aria-label={tr('vote.log.fault', currentLang)}>!</span>
					{/if}
				</h2>
				{#if !hasKey}
					<button class="btn btn-primary" onclick={() => voterKeyOpen.set(true)}>{tr('poll.ballot.get_key', currentLang)}</button>
				{:else}
					<a href="/profile" class="profile-link">{tr('vote.profile_link', currentLang)}</a>
				{/if}
			</div>
			<p class="hint">{tr(results.open ? 'vote.hint' : 'vote.closed_hint', currentLang, { n: maxChoices })}</p>

			{#if hasKey}
				{#if !protectedEnough($voterKey)}
					<button class="protect" onclick={() => voterKeyOpen.set(true)}>{tr('vote.protect', currentLang)}</button>
				{/if}
				{#if blank}
					<div class="slots slots--blank">
						<div class="slot filled">
							<div class="slot-photo slot-photo--blank" aria-hidden="true">◻</div>
							<span class="slot-name slot-name--plain">{tr('vote.blank.name', currentLang)}</span>
						</div>
					</div>
				{:else}
					<ol class="slots" style="--n: {maxChoices}">
						{#each Array.from({ length: maxChoices }, (_, i) => i) as i (i)}
							{@const id = ballot[i]}
							{@const c = id === undefined ? undefined : byId.get(id)}
							<li class="slot" class:filled={id !== undefined} class:closed={id === undefined && endsBlank} class:next={results.open && !endsBlank && id === undefined && i === ballot.length}>
								{#if id === BLANK}
									<div class="slot-photo slot-photo--blank" aria-hidden="true">◻<span class="slot-rank">{i + 1}</span></div>
									<span class="slot-name slot-name--plain">{tr('vote.blank.rest', currentLang)}</span>
									{#if results.open}
										<button class="unpick" onclick={() => remove(BLANK)} disabled={busy} aria-label={tr('vote.remove_named', currentLang, { name: tr('vote.blank.name', currentLang) })}>×</button>
									{/if}
								{:else if id !== undefined}
									<div class="slot-photo">
										{#if c?.image_url}
											<img src={c.image_url} alt="" />
										{:else}
											<span class="slot-initial">{(c?.name ?? '?').charAt(0).toUpperCase()}</span>
										{/if}
										<span class="slot-rank">{i + 1}</span>
									</div>
									<span class="slot-name">{displayName(c?.name ?? `#${id}`)}</span>
									{#if results.open}
										<button class="unpick" onclick={() => remove(id)} disabled={busy} aria-label={tr('vote.remove_named', currentLang, { name: c?.name ?? '' })}>×</button>
									{/if}
								{:else if !endsBlank}
									<div class="slot-photo slot-photo--empty"><span class="slot-rank">{i + 1}</span></div>
									<span class="slot-name slot-name--empty">{tr('vote.slot.choice', currentLang, { rank: rankWord(i + 1) })}</span>
								{/if}
							</li>
						{/each}
					</ol>
				{/if}
				{#if results.open}
					<div class="ballot-actions">
						{#if !blank && !endsBlank && (ballot.length === 0 || !full)}
							<button class="btn btn-small" onclick={() => pick('blank')} disabled={busy}>◻ {tr(ballot.length === 0 ? 'vote.blank.button' : 'vote.blank.after', currentLang)}</button>
							{@render help(tr('vote.blank.help', currentLang))}
						{/if}
						{#if mine}
							<button class="btn btn-small btn-quiet" onclick={() => (withdrawOpen = true)} disabled={busy}>{tr('vote.withdraw_all', currentLang)}</button>
						{/if}
					</div>
				{/if}
				{#if mine}
					<div class="verify">
						<button class="btn btn-small" onclick={verifyMine} disabled={busy}>🔍 {tr('vote.verify', currentLang)}</button>
						{@render help(tr('vote.verify.help', currentLang))}
					</div>
					{#if mineSteps}
						<ul class="steps">
							{#each ['signed', 'history', 'mine'] as const as k (k)}
								<li class="step step--{mineSteps[k]}">{@render stepIcon(mineSteps[k])}<span>{tr(`vote.verify.${k}`, currentLang)}</span></li>
							{/each}
						</ul>
					{/if}
				{/if}
				{#if check?.state === 'fault'}
					<div class="log-fault" role="alert">
						<strong>{tr('vote.log.fault', currentLang)}</strong>
						<span>{tr(`vote.log.fault.${check.fault.kind}`, currentLang)}</span>
						<button class="btn" onclick={() => saveEvidence(check)}>{tr('vote.log.evidence', currentLang)}</button>
					</div>
				{/if}
				{#if actionError}
					<p class="save-error" role="alert">{actionError}</p>
				{/if}
			{:else if results.open}
				<div class="ballot-actions">
					<button class="btn btn-small" onclick={() => pick('blank')}>◻ {tr('vote.blank.button', currentLang)}</button>
				</div>
			{/if}
		</section>
	{/if}

	<Modal open={withdrawOpen} title={tr('vote.withdraw.title', currentLang)} onclose={() => (withdrawOpen = false)} closeLabel={tr('auth.close', currentLang)}>
		<p class="dialog-text">{tr(blank ? 'vote.withdraw.text_blank' : 'vote.withdraw.text', currentLang)}</p>
		{#snippet actions()}
			<button class="btn" onclick={() => (withdrawOpen = false)}>{tr('vote.cancel', currentLang)}</button>
			<button class="btn btn-danger" onclick={withdrawAll} disabled={busy}>{tr('vote.withdraw_all', currentLang)}</button>
		{/snippet}
	</Modal>

	{#snippet stepIcon(state: string)}
		{#if state === 'running'}
			<span class="state-icon state-icon--spin state-icon--sm" aria-hidden="true"></span>
		{:else if state === 'ok'}
			<span class="state-icon state-icon--ok state-icon--sm" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg></span>
		{:else if state === 'failed'}
			<span class="state-icon state-icon--fault state-icon--sm" aria-hidden="true">!</span>
		{:else}
			<span class="state-icon state-icon--wait state-icon--sm" aria-hidden="true">…</span>
		{/if}
	{/snippet}

	{#snippet help(text: string)}
		<details class="help">
			<summary aria-label="?">?</summary>
			<p>{text}</p>
		</details>
	{/snippet}

	{#snippet blankRankedRow()}
		<li class="vote-row vote-row--blank" class:vote-row--mine={blank || endsBlank} class:vote-row--top={results?.blank_rank === 1}>
			<span class="vote-rank">{results?.blank_rank ?? '—'}</span>
			<div class="vote-main">
				<span class="who"><span class="photo photo--empty photo--blank" aria-hidden="true">◻</span><span class="name">{tr('vote.blank.name', currentLang)}</span></span>
				{#if results?.condorcet_winner === BLANK}
					<span class="winner">{tr('poll.condorcet', currentLang)}</span>
				{/if}
				<span class="meta">{tr('poll.first_ranked', currentLang, { f: pct(results?.blank_share ?? null), r: pct(results?.blank_ranked_share ?? null) })}</span>
			</div>
		</li>
	{/snippet}

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
					{#each sorted as c, i (c.person_id)}
						{#if blankAt === i}{@render blankRankedRow()}{/if}
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
					{#if blankAt === sorted.length}{@render blankRankedRow()}{/if}
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
		<div class="verify verify--results">
			<button class="btn btn-small" onclick={verifyResults} disabled={recounting}>🔍 {tr('poll.verify', currentLang)}</button>
			{@render help(tr('poll.verify.help', currentLang))}
		</div>
		{#if recountSteps}
			<ul class="steps steps--results">
				{#each recountSteps as st (st.id)}
					<li class="step step--{st.state}">{@render stepIcon(st.state)}<span>{tr(`poll.verify.${st.id}`, currentLang)}</span></li>
				{/each}
			</ul>
		{/if}
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
			<li>{@html tr('poll.how.verification', currentLang, { href: paths.how + '#cle' })}</li>
			<li>{tr('poll.how.blank', currentLang)}</li>
			<li>{@html tr('poll.how.privacy', currentLang, { href: paths.how + '#journal' })}</li>
		</ul>
	</details>
{/if}

<style>
	.slots {
		list-style: none;
		margin: 0.75rem 0 1rem;
		padding: 0;
		display: grid;
		grid-template-columns: repeat(var(--n, 3), minmax(0, 1fr));
		gap: 0.75rem;
	}
	.slots--blank {
		grid-template-columns: minmax(0, 12rem);
		justify-content: center;
	}
	.slot {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.6rem;
		padding: 1rem 0.5rem 0.85rem;
		border: 1px dashed var(--glass-border);
		border-radius: var(--radius-lg);
		text-align: center;
		transition: border-color 0.2s, background 0.2s;
	}
	.slot.filled {
		border: 1px solid color-mix(in oklch, var(--accent) 55%, var(--border-card));
		background: linear-gradient(180deg, color-mix(in oklch, var(--accent) 14%, var(--bg-card)), var(--bg-card));
	}
	.slot.next {
		border-color: var(--accent);
	}
	.slot.closed {
		border-color: transparent;
		background: repeating-linear-gradient(135deg, transparent 0 8px, color-mix(in oklch, var(--text-muted) 10%, transparent) 8px 9px);
	}
	.slot-photo {
		position: relative;
		width: 4.75rem;
		height: 4.75rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: var(--bg-secondary);
		box-shadow: 0 0 0 3px var(--bg-card), 0 0 0 5px var(--accent);
	}
	.slot-photo img {
		width: 100%;
		height: 100%;
		border-radius: 50%;
		object-fit: cover;
	}
	.slot-initial {
		font-family: var(--font-serif);
		font-size: 1.9rem;
		font-weight: 600;
		color: var(--text-secondary);
	}
	.slot-photo--empty {
		background: transparent;
		box-shadow: none;
		border: 2px dashed var(--glass-border);
	}
	.slot.next .slot-photo--empty {
		border-color: var(--accent);
	}
	.slot-photo--blank {
		font-size: 2rem;
		color: var(--text-secondary);
		box-shadow: 0 0 0 3px var(--bg-card), 0 0 0 5px var(--text-muted);
	}
	.slot-rank {
		position: absolute;
		left: -0.2rem;
		bottom: -0.2rem;
		width: 1.6rem;
		height: 1.6rem;
		display: grid;
		place-items: center;
		border-radius: 50%;
		background: var(--accent);
		color: white;
		font-weight: 700;
		font-size: 0.85rem;
		box-shadow: 0 0 0 2px var(--bg-card);
	}
	.slot-photo--empty .slot-rank {
		position: static;
		background: transparent;
		color: var(--text-muted);
		box-shadow: none;
		font-size: 1.1rem;
	}
	.slot.next .slot-photo--empty .slot-rank {
		color: var(--accent-hover);
	}
	.slot-name {
		font-weight: 600;
		font-size: 0.95rem;
		line-height: 1.2;
		text-transform: capitalize;
		overflow-wrap: anywhere;
	}
	.slot-name--plain {
		text-transform: none;
	}
	.slot-name--empty {
		font-weight: 400;
		font-size: 0.82rem;
		color: var(--text-muted);
		text-transform: none;
	}
	.unpick {
		position: absolute;
		top: 0.3rem;
		right: 0.3rem;
		width: 1.6rem;
		height: 1.6rem;
		border-radius: 50%;
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 1.15rem;
		line-height: 1;
		cursor: pointer;
	}
	.unpick:hover {
		color: var(--red);
		background: color-mix(in oklch, var(--red) 12%, transparent);
	}
	.protect {
		display: block;
		width: 100%;
		margin: 0.5rem 0 0;
		padding: 0.55rem 0.8rem;
		text-align: center;
		background: color-mix(in oklch, var(--accent) 12%, var(--bg-card));
		border: 1px solid var(--accent);
		border-radius: var(--radius-md);
		color: var(--text-primary);
		font: inherit;
		font-size: 0.88rem;
		font-weight: 600;
		cursor: pointer;
	}
	.state-icon {
		display: inline-grid;
		place-items: center;
		width: 1.35rem;
		height: 1.35rem;
		margin-left: 0.4rem;
		vertical-align: 0.1em;
		border-radius: 50%;
	}
	.state-icon--ok {
		background: var(--green);
	}
	.state-icon--ok svg {
		width: 0.95rem;
		height: 0.95rem;
		fill: none;
		stroke: white;
		stroke-width: 3;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.state-icon--logged {
		width: 1.6rem;
		height: 1.6rem;
		background: var(--green);
	}
	.state-icon--logged svg {
		width: 1.15rem;
		height: 1.15rem;
		fill: none;
		stroke: white;
		stroke-width: 2;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.state-icon--logged svg .tick {
		stroke-width: 2.2;
	}
	.state-icon--sm {
		width: 1.1rem;
		height: 1.1rem;
		margin: 0;
		flex-shrink: 0;
	}
	.state-icon--sm svg {
		width: 0.75rem;
		height: 0.75rem;
	}
	.state-icon--sm.state-icon--fault {
		font-size: 0.7rem;
	}
	.state-icon--wait {
		background: var(--bg-secondary);
		color: var(--text-muted);
		font-size: 0.7rem;
		line-height: 1;
	}
	.verify {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		margin-top: 0.75rem;
	}
	.verify--results {
		justify-content: center;
		margin: 1.25rem auto 0;
	}
	.help {
		position: relative;
	}
	.help summary {
		list-style: none;
		width: 1.3rem;
		height: 1.3rem;
		display: grid;
		place-items: center;
		border-radius: 50%;
		border: 1px solid var(--glass-border);
		color: var(--text-secondary);
		font-size: 0.75rem;
		font-weight: 700;
		cursor: pointer;
	}
	.help summary::-webkit-details-marker {
		display: none;
	}
	.help p {
		position: absolute;
		z-index: 20;
		top: 1.7rem;
		left: 50%;
		transform: translateX(-50%);
		width: min(17rem, 80vw);
		margin: 0;
		padding: 0.6rem 0.75rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-md);
		background: var(--bg-secondary);
		box-shadow: var(--shadow-card-hover);
		font-size: 0.8rem;
		line-height: 1.45;
		color: var(--text-secondary);
	}
	.steps {
		list-style: none;
		margin: 0.6rem 0 0;
		padding: 0;
		display: grid;
		gap: 0.35rem;
	}
	.steps--results {
		width: fit-content;
		margin: 0.75rem auto 0;
	}
	.step {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	.step--ok {
		color: var(--text-primary);
	}
	.step--failed {
		color: var(--red);
	}
	.state-icon--spin {
		border: 2.5px solid color-mix(in oklch, var(--text-muted) 40%, transparent);
		border-top-color: var(--accent-hover);
		animation: spin 0.8s linear infinite;
	}
	.state-icon--fault {
		background: var(--red);
		color: white;
		font-family: var(--font-sans, inherit);
		font-size: 0.85rem;
		font-weight: 800;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.state-icon--spin {
			animation-duration: 2.4s;
		}
	}
	.btn-small {
		padding: 0.4rem 0.8rem;
		font-size: 0.85rem;
	}
	.btn-quiet {
		border-color: transparent;
		background: transparent;
		color: var(--text-secondary);
	}
	.bar-fill--blank {
		background: var(--text-muted) !important;
	}
	.vote-row--blank .name,
	.board-row--blank .name {
		font-style: italic;
	}
	.log-fault {
		display: grid;
		gap: 0.4rem;
		justify-items: start;
		margin: 0.75rem 0 0;
		padding: 0.75rem 0.9rem;
		border: 1px solid var(--red);
		border-radius: var(--radius-md);
		background: color-mix(in oklch, var(--red) 10%, var(--bg-card));
		font-size: 0.88rem;
		line-height: 1.45;
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
		.slots {
			gap: 0.45rem;
		}
		.slot {
			padding: 0.8rem 0.25rem 0.7rem;
		}
		.slot-photo {
			width: 3.6rem;
			height: 3.6rem;
		}
		.slot-name {
			font-size: 0.82rem;
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
