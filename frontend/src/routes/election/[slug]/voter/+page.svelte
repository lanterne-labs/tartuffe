<script lang="ts">
	// The vote page (isoloir, docs: the @isoloir/web README). Everything it
	// shows comes from the session (lib/vote/isoloir): the poll's setup and
	// the latest checkpoint and tally, read from the public mirror and each
	// proven in the signed log; the voter's key, ballot and receipt. The
	// candidates' cards come from the site (lib/vote/candidates). It never
	// calls our vote server. Every wait here has a visible state and a next
	// action.
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { displayName } from '$lib/share';
	import { lang, tr } from '$lib/i18n';
	import Modal from '$lib/components/Modal.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import ShareMenu from '$lib/components/ShareMenu.svelte';
	import TartuffeScore from '$lib/components/TartuffeScore.svelte';
	import VoteKeyDialog from '$lib/components/VoteKeyDialog.svelte';
	import VoteIntro from '$lib/components/vote/VoteIntro.svelte';
	import Sortable from 'sortablejs';
	import { eventPaths, sharesShown, votersShown } from '$lib/event';
	import { sessionFor, parseLink, voteKey } from '$lib/vote/isoloir';
	import { encodeComplaint } from '@isoloir/web';
	import { goto } from '$app/navigation';
	import { loadEventMeta, byName, nameKey, type Candidate } from '$lib/vote/candidates';
	import { rankedRows, byFirst, type Shown } from '$lib/vote/results';
	import { describeError, toVoteError, reportLink } from '$lib/vote/errors';
	import type { PollEvent } from '$lib/types';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));
	const vs = sessionFor($page.params.slug ?? '');
	const st = vs.state;
	const warm = vs.warm;
	const tallyAt = vs.tallyAt;

	let event = $state<PollEvent | null>(null);
	let cards = $state<Map<string, Candidate>>(new Map());
	let keyOpen = $state(false);
	// The presentation (why the passport, what leaves the phone, the secret
	// ballot, what is needed) opens on the first visit, before anything
	// else, and again from the ballot's "why the passport" link. The visit
	// is remembered in this browser; a voter who comes to send their key to
	// another device (a link in the URL) is not kept waiting by it.
	let introOpen = $state(false);
	const introKey = () => `vote-intro-seen:${slug}`;
	function closeIntro() {
		introOpen = false;
		try {
			window.localStorage.setItem(introKey(), '1');
		} catch {}
	}
	function openIntroOnce() {
		if (parseLink(window.location.hash)) return;
		try {
			if (window.localStorage.getItem(introKey()) === '1') return;
		} catch {
			return;
		}
		introOpen = true;
	}
	let keyMenuOpen = $state(false);
	let showWords = $state(false);
	let forgetOpen = $state(false);
	let slotPick = $state<number | null>(null);
	// The clock the page judges the opening and the blackouts by, every
	// 30 s: a page left open crosses them.
	let now = $state(Date.now());
	let clock: ReturnType<typeof setInterval> | null = null;
	// A device link opened here (SPEC §14): the public key of the device
	// asking for the key, where the sending stands, and the six digits the
	// other device asks the voter to compare.
	let linkCode = $state<string | null>(null);
	let linkState = $state<'ask' | 'sending' | 'sent' | 'failed'>('ask');
	let linkError = $state('');
	let linkConfirm = $state('');
	async function sendKey() {
		if (!linkCode) return;
		linkState = 'sending';
		try {
			const sent = await vs.sendKeyTo(linkCode);
			linkConfirm = sent.code;
			linkState = 'sent';
		} catch (e) {
			const ve = toVoteError(e);
			linkError = describeError(ve.code, currentLang);
			linkState = 'failed';
		}
	}
	function closeLink() {
		linkCode = null;
		history.replaceState(null, '', window.location.pathname + window.location.search);
	}
	// What went wrong, as the voter's sentence (HTML: the "report it" link)
	// and, apart, the raw detail for the curious.
	let actionError = $state('');
	let actionDetail = $state('');

	let poll = $derived($st.poll);
	let kind = $derived($st.kind);
	// The options of the poll: slot k is the k-th name, the blank last.
	let names = $derived(kind?.options ?? []);
	let BLANK = $derived(kind?.blank_slot ?? -1);
	let depth = $derived(kind?.rank_depth ?? 3);
	let candidateSlots = $derived(names.map((_, i) => i));
	let kReal = $derived(poll?.publish.mode === 'open' ? poll.publish.k_real : 1);
	let closesAt = $derived(poll ? poll.closes * 1000 : 0);
	// The page stops taking ballots 10 minutes before the close.
	let open = $derived($st.pollStatus === 'open' && now < closesAt - 10 * 60_000);
	let blackout = $derived((poll?.blackouts ?? []).some((b) => now >= b.from * 1000 && now < b.to * 1000));
	let key = $derived($st.key);
	let hasKey = $derived(!!key);
	let joined = $derived(!!key && key.position !== null);
	let ready = $derived($st.phase === 'ready');
	let ballot = $derived($st.ballot);
	let busy = $derived(ballot.state === 'drafting' || ballot.state === 'proving' || ballot.state === 'posting' || ballot.state === 'sent');
	let accepted = $derived(ballot.state === 'acked' || ballot.state === 'verified' || ballot.state === 'missing');
	let inFlight = $derived(busy || accepted);
	const rankOf = (c: { pick: number | null } | { rank: number[] } | undefined): number[] | null => (c && 'rank' in c ? c.rank : null);
	// The last ballot this key cast and the ingest took, as this device
	// knows it: what the slots show while nothing is in flight.
	let lastAccepted = $state<number[] | null>(null);
	$effect(() => {
		if (accepted) lastAccepted = rankOf(ballot.choice);
	});

	// The ballot committed: the one in flight or acknowledged, else the
	// last one accepted. The voter composes a draft on top of it and sends
	// it with the submit button: nothing goes out while they hesitate.
	let committed = $derived<number[]>(inFlight ? (rankOf(ballot.choice) ?? []) : (lastAccepted ?? []));
	let draft = $state<number[] | null>(null);
	let shown = $derived<number[]>(draft ?? committed);
	let dirty = $derived(draft !== null && JSON.stringify(draft) !== JSON.stringify(committed));
	let endsBlank = $derived(shown.length > 0 && shown.at(-1) === BLANK);
	let choosable = $derived(candidateSlots.filter((s) => !shown.includes(s)));
	// A key restored from its words, or sent by another device, knows its
	// last sequence number but not its ranking: the page says a ballot is
	// in the log.
	let loggedUnknown = $derived(!!key && $st.revote.seq !== null && !ballot.choice && !inFlight);
	let canResend = $derived(ballot.state === 'failed' && !!ballot.kept);
	let canRetry = $derived((ballot.state === 'failed' || ballot.state === 'rejected') && !ballot.kept && !!rankOf(ballot.choice));

	// A refusal the voter thinks wrong: the complaint, made from the signed
	// refusal and the ballot, is judged on its own page (and kept there).
	let contestable = $derived(ballot.state === 'rejected' && !['too_soon', 'stale_seq', 'seq_gap', 'revote_off', 'closed', 'not_open'].includes(ballot.reason ?? ''));
	async function contest() {
		try {
			const saved = await vs.need().complainOfRefusal('ballot');
			await goto(`${paths.complaint}#c=${saved.link}`);
		} catch (e) {
			failed(e);
		}
	}

	function failed(e: unknown) {
		const ve = toVoteError(e);
		const b = $st.ballot;
		const code = b.state === 'rejected' || b.state === 'failed' ? (b.reason ?? ve.code) : ve.code;
		if (code === 'too_soon') {
			// A re-vote counts only revoteGap after the last one: say when.
			const data = ve.data as { waitSeconds?: number } | undefined;
			const wait = Math.max(1, Math.ceil((data?.waitSeconds ?? $st.revoteGap) / 60));
			actionError = tr('v2.reason.too_soon', currentLang, { wait: String(wait) });
		} else actionError = describeError(code, currentLang);
		actionDetail = b.detail ?? ve.detail ?? '';
	}

	async function change(next: number[]): Promise<boolean> {
		if (busy || !ready) return false;
		actionError = '';
		actionDetail = '';
		try {
			const s = vs.need();
			// A ballot proven before and never acknowledged (the relays did
			// not take it) waits in the browser: the same ranking is sent
			// again, another one replaces it. Casting over it is refused.
			if (ballot.state === 'sent' || (ballot.state === 'failed' && ballot.kept)) {
				const kept = rankOf(ballot.choice);
				if (kept && JSON.stringify(kept) === JSON.stringify(next)) {
					await s.resendBallot();
					return true;
				}
				s.discardKeptBallot();
			}
			await s.cast({ rank: next });
			return true;
		} catch (e) {
			failed(e);
			return false;
		}
	}

	// resend posts the proven ballot no relay acknowledged; retry proves
	// the attempted ranking again.
	async function resend() {
		actionError = '';
		try {
			await vs.need().resendBallot();
		} catch (e) {
			failed(e);
		}
	}
	function retry() {
		const r = rankOf(ballot.choice);
		if (r) void change(r);
	}

	function forgetKey() {
		forgetOpen = false;
		keyMenuOpen = false;
		showWords = false;
		lastAccepted = null;
		vs.need().forgetKey();
	}

	function placed(cur: number[], choice: number, at: number | null): number[] {
		if (choice === BLANK) return [...cur.filter((x) => x !== BLANK).slice(0, at ?? cur.length), BLANK];
		const rest = cur.filter((x) => x !== choice);
		const tail = rest.at(-1) === BLANK;
		const body = tail ? rest.slice(0, -1) : rest;
		const i = at === null ? body.length : Math.min(at, body.length);
		if (at !== null && i < body.length) body[i] = choice;
		else body.splice(i, 0, choice);
		return tail && body.length < depth ? [...body, BLANK] : body;
	}

	function pick(choice: number, at: number | null) {
		const next = placed(shown, choice, at);
		if (next.length > depth) {
			actionError = tr('vote.error.full', currentLang, { n: depth });
			return;
		}
		if (JSON.stringify(next) === JSON.stringify(shown)) return;
		draft = next;
	}

	// submit sends the draft: a voter without a key gets the key dialog
	// first, and submits again once registered.
	async function submit() {
		if (draft === null) return;
		if (regPending) return;
		if (!joined) {
			keyOpen = true;
			return;
		}
		if (await change(draft)) draft = null;
	}
	// The key's copies: none is a warning on the key button.
	let keyUnprotected = $derived(!!$voteKey && keyStatusKey() === 'profile.key.unprotected');

	// A touch screen holds to drag; a mouse drags at once (the hint says which).
	const coarse = typeof window !== 'undefined' && window.matchMedia('(pointer: coarse)').matches;
	let suppressClick = false;
	function movable(id: number | undefined): boolean {
		return id !== undefined && id !== BLANK && !busy && open && shown.filter((x) => x !== BLANK).length > 1;
	}
	function sortable(node: HTMLElement) {
		const s = Sortable.create(node, {
			draggable: '.movable',
			// A finger holds first (so the page still scrolls); a mouse drags at once.
			delay: 350,
			delayOnTouchOnly: true,
			forceFallback: true,
			animation: 150,
			chosenClass: 'chosen',
			ghostClass: 'drop-target',
			fallbackClass: 'dragging',
			onMove: (e) => e.related.classList.contains('movable'),
			onChoose: () => {
				suppressClick = true;
				navigator.vibrate?.(15);
			},
			onUnchoose: () => setTimeout(() => (suppressClick = false), 0),
			onEnd: (e) => {
				const from = e.oldDraggableIndex;
				const to = e.newDraggableIndex;
				if (from === undefined || to === undefined || from === to) return;
				e.from.removeChild(e.item);
				e.from.insertBefore(e.item, e.from.children[e.oldIndex ?? from] ?? null);
				const cands = shown.filter((x) => x !== BLANK);
				const [moved] = cands.splice(from, 1);
				cands.splice(to, 0, moved);
				draft = endsBlank ? [...cands, BLANK] : cands;
			}
		});
		return { destroy: () => s.destroy() };
	}
	function openSlot(i: number) {
		if (suppressClick) return;
		slotPick = i;
	}
	function chooseFor(choice: number) {
		const at = slotPick;
		slotPick = null;
		pick(choice, at);
	}
	function remove(id: number) {
		draft = shown.filter((x) => x !== id);
	}
	// move swaps a candidate with its neighbour (the keyboard's way to
	// reorder; the drag is the finger's).
	function move(id: number, dir: -1 | 1) {
		const cands = shown.filter((x) => x !== BLANK);
		const i = cands.indexOf(id);
		const j = i + dir;
		if (i < 0 || j < 0 || j >= cands.length) return;
		[cands[i], cands[j]] = [cands[j], cands[i]];
		draft = endsBlank ? [...cands, BLANK] : cands;
	}

	function rankWord(n: number): string {
		if (currentLang === 'en') return n === 1 ? '1st' : n === 2 ? '2nd' : n === 3 ? '3rd' : `${n}th`;
		return n === 1 ? '1er' : `${n}e`;
	}
	function closesText(ms: number): string {
		return new Date(ms).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}
	// pct: « 35 % » in French (a narrow no-break space), "35%" in English.
	function pct(n: number, total: number): string {
		if (total <= 0) return currentLang === 'en' ? '0%' : '0\u202f%';
		const v = Math.round((100 * n) / total);
		return currentLang === 'en' ? `${v}%` : `${v}\u202f%`;
	}
	function ago(ms: number): string {
		const m = Math.max(0, Math.round((now - ms) / 60000));
		if (m < 1) return currentLang === 'en' ? 'just now' : 'à l’instant';
		if (m < 60) return currentLang === 'en' ? `${m} min ago` : `il y a ${m}\u00a0min`;
		const h = Math.round(m / 60);
		return currentLang === 'en' ? `${h} h ago` : `il y a ${h}\u00a0h`;
	}
	function cardOf(slot: number): Candidate | undefined {
		const n = names[slot];
		return n === undefined ? undefined : cards.get(nameKey(n));
	}
	function nameOf(slot: number): string {
		if (slot === BLANK) return tr('vote.blank.name', currentLang);
		const n = names[slot];
		if (n === undefined) return `#${slot}`;
		return displayName(cardOf(slot)?.name ?? n);
	}
	function imageOf(slot: number): string | undefined {
		return cardOf(slot)?.image_url;
	}
	function keyStatusKey(): string {
		const k = $voteKey;
		if (!k) return 'profile.key.none';
		if (k.source === 'passkey') return 'profile.key.passkey';
		if (k.backups.words) return 'profile.key.words';
		if (k.backups.linked > 0 || k.source === 'link') return 'profile.key.linked';
		return 'profile.key.unprotected';
	}

	// The results: the last tally that verified; a newer one that did not
	// is said so, and when none verified, the one that did not is shown as
	// such.
	let verdict = $derived($st.tally ?? $st.tallyUnverified?.verdict ?? null);
	let verified = $derived(!!$st.tally);
	let results = $derived(rankedRows(verdict?.results));
	let votersText = $derived(votersShown(results?.counted, currentLang));
	let winnerSlot = $derived(results?.condorcet ?? null);
	// The two readings of the same results: the ranking and the first
	// choices only (the same rows, re-sorted by their first-choice share).
	// Before the first tally, the same table at zero: the candidates are
	// there to see, the numbers come.
	let emptyResults = $derived<Shown>({
		counted: 0,
		rows: [...candidateSlots, ...(kind?.blank ? [BLANK] : [])].map((slot) => ({ slot, blank: slot === BLANK, first: 0, ranked: 0, wins: 0, rank: 1 })),
		condorcet: null
	});
	let table = $derived(results ?? emptyResults);
	// The two counts shown side by side.
	let counts = $derived([
		{ kind: 'ranking' as const, list: table.rows },
		{ kind: 'first' as const, list: byFirst(table.rows) }
	]);
	// On a phone the two counts do not fit side by side: one shows at a time,
	// chosen with the switch above them (CSS shows the switch and hides the
	// other count under 560px).
	let countShown = $state<'ranking' | 'first'>('ranking');
	// A line joins each option's row in one count to its row in the other;
	// a click on a card brings its line forward.
	let picked = $state<number | null>(null);
	let countsEl = $state<HTMLElement | null>(null);
	let rowEls = $state<Record<'ranking' | 'first', Record<number, HTMLElement>>>({ ranking: {}, first: {} });
	let links = $state<{ slot: number; d: string }[]>([]);
	function measure() {
		if (!countsEl) return;
		const box = countsEl.getBoundingClientRect();
		const out: { slot: number; d: string }[] = [];
		for (const o of table.rows) {
			const a = rowEls.ranking[o.slot]?.getBoundingClientRect();
			const b = rowEls.first[o.slot]?.getBoundingClientRect();
			if (!a || !b) continue;
			const x1 = a.right - box.left, y1 = a.top + a.height / 2 - box.top;
			const x2 = b.left - box.left, y2 = b.top + b.height / 2 - box.top;
			const m = (x1 + x2) / 2;
			out.push({ slot: o.slot, d: `M${x1} ${y1}C${m} ${y1} ${m} ${y2} ${x2} ${y2}` });
		}
		links = out;
	}
	$effect(() => {
		void counts;
		if (!countsEl) return;
		const ro = new ResizeObserver(() => measure());
		ro.observe(countsEl);
		const f = requestAnimationFrame(measure);
		return () => {
			ro.disconnect();
			cancelAnimationFrame(f);
		};
	});
	function pickRow(slot: number) {
		picked = picked === slot ? null : slot;
	}
	// The consistency score of a candidate, from its card; none for the
	// blank or a card without one.
	function scoreOf(slot: number): number | null {
		const s = slot === BLANK ? undefined : cardOf(slot)?.honesty_score;
		// The cards carry the score as the site stores it (0 to 1).
		return typeof s === 'number' && Number.isFinite(s) ? Math.round(s * 100) : null;
	}
	// The sentence under the ballot that says where the vote stands.
	let statusLine = $derived.by(() => {
		const s = ballot.state;
		const d = closesAt ? closesText(closesAt) : '';
		if (busy) return tr('v2.status.line.busy', currentLang);
		if (dirty) return tr('v2.status.line.dirty', currentLang);
		if (s === 'acked') return tr('v2.status.line.received', currentLang, { d });
		if (s === 'verified') return tr('v2.status.line.verified', currentLang, { d });
		return '';
	});
	let busyLabel = $derived(tr(ballot.state === 'drafting' || ballot.state === 'proving' ? 'v2.status.proving' : 'v2.status.sending', currentLang));
	let voted = $derived(accepted || (lastAccepted !== null && lastAccepted.length > 0));
	let registration = $derived($st.registration);
	let regPosting = $derived(registration.state === 'posting' || registration.state === 'sent');
	// The registration is on its way into the public log: the ballot waits, the
	// button says so, and the page opens the vote by itself once the log shows it.
	let regPending = $derived(regPosting || registration.state === 'acked');
	let regFailed = $derived(registration.state === 'failed' || registration.state === 'rejected' || registration.state === 'missing');
	let errorCode = $derived($st.error?.code ?? '');
	let errorDetail = $derived($st.error?.detail ?? '');

	// The prover warms up once the key is registered.
	$effect(() => {
		if (joined && $warm === 'idle') vs.warmUp();
	});

	// load reads the candidates' cards (a failure there is not the end:
	// the page shows the plain names) then starts the session, which names
	// any failure of its own with a retry.
	async function load() {
		try {
			const meta = await loadEventMeta(slug);
			event = meta.event;
			cards = byName(meta.candidates);
		} catch (e) {
			console.warn('vote: the candidates', e);
		}
		await vs.init();
		// The next ballot's proof of work, mined while the voter chooses.
		void vs.session?.startWork().catch(() => {});
	}

	onMount(async () => {
		clock = setInterval(() => (now = Date.now()), 30_000);
		openIntroOnce();
		checkLink();
		window.addEventListener('hashchange', checkLink);
		await load();
	});
	// A device link in the URL, opened here (a scanned QR code) or
	// navigated to (the hash alone changes without a reload).
	function checkLink() {
		const code = parseLink(window.location.hash);
		if (code && code !== linkCode) {
			linkCode = code;
			linkState = 'ask';
			linkError = '';
			linkConfirm = '';
		}
	}
	onDestroy(() => {
		vs.destroy();
		if (clock) clearInterval(clock);
		if (typeof window !== 'undefined') window.removeEventListener('hashchange', checkLink);
	});
</script>

<svelte:head>
	<title>{event ? `${currentLang === 'en' ? event.title_en : event.title_fr} — tartuffe.me` : 'tartuffe.me'}</title>
</svelte:head>

{#if $st.phase === 'error'}
	<div class="state error">
		<p>{@html describeError(errorCode, currentLang)}</p>
		{#if errorDetail}
			<details class="tech"><summary>{tr('v2.details', currentLang)}</summary><p>{errorDetail}</p></details>
		{/if}
		<button class="btn" onclick={load}>{tr('home.retry', currentLang)}</button>
	</div>
{:else if $st.phase === 'loading' || !poll}
	<p class="state loading"><Spinner label={tr('person.loading', currentLang)} /></p>
{:else}
	<header class="hero">
		<h1>{event ? (currentLang === 'en' ? event.title_en : event.title_fr) : slug}</h1>
	</header>

	{#if blackout}
		<div class="notice notice--legal">{tr('poll.hidden.blackout', currentLang)}</div>
	{/if}

	<!-- The presentation again, for whoever closed it: one small line above the ballot. -->
	<p class="intro-line"><button class="link-btn" onclick={() => (introOpen = true)}>{tr('v2.intro.why', currentLang)}</button></p>
	<section class="ballot" aria-labelledby="ballot-title">
		<div class="ballot-head">
			<h2 id="ballot-title">
				{tr('vote.title', currentLang)}
				{#if ballot.state === 'verified'}
					<details class="help status-tip">
						<summary class="state-icon state-icon--logged" aria-label={tr('v2.status.verified', currentLang)}>
							<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10.5" width="14" height="10" rx="2.2" /><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5" /><path class="tick" d="M9.3 15.6l1.9 1.9 3.6-3.7" /></svg>
						</summary>
						<p>{tr(ballot.late ? 'v2.status.late.help' : 'v2.status.verified.help', currentLang)}</p>
					</details>
				{:else if ballot.state === 'acked'}
					<details class="help status-tip">
						<summary class="state-icon state-icon--ok" aria-label={tr('v2.status.received', currentLang)}>
							<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>
						</summary>
						<p>{tr('v2.status.received.help', currentLang)}</p>
					</details>
				{:else if ballot.state === 'missing'}
					<details class="help status-tip">
						<summary class="state-icon state-icon--fault" aria-label={tr('v2.status.missing', currentLang)}>!</summary>
						<p>{@html tr('v2.status.missing.help', currentLang, { report: reportLink(currentLang) })}</p>
					</details>
				{:else if busy}
					<span class="state-icon" title={busyLabel}><Spinner size="sm" label={busyLabel} /></span>
				{:else if ballot.state === 'rejected' || ballot.state === 'failed'}
					<span class="state-icon state-icon--fault" title={actionError} aria-label={actionError}>!</span>
				{/if}
			</h2>
			<p class="ballot-meta">
				{#if open}{tr('poll.closes', currentLang, { d: closesText(closesAt) })}{:else}{tr('poll.closed', currentLang)}{/if}
				{#if votersText}
					<span class="dot"></span>{tr('poll.voters', currentLang, { n: votersText })}
				{/if}
			</p>
			{#if hasKey && !joined}
				{#if regPosting}
					<span class="joining"><Spinner size="sm" label={tr('v2.join.posting', currentLang)} /> {tr('v2.join.posting', currentLang)} <button class="link-btn" onclick={() => (keyOpen = true)}>{tr('v2.join.details', currentLang)}</button></span>
				{:else if registration.state === 'acked'}
					<span class="joining"><Spinner size="sm" label={tr('v2.join.acked', currentLang)} /> {tr('v2.join.acked', currentLang)}</span>
				{:else if regFailed}
					<span class="joining joining--fault"><span class="state-icon state-icon--fault" aria-hidden="true">!</span> {tr('v2.join.failed', currentLang)} <button class="btn btn-small" onclick={() => (keyOpen = true)}>{tr('v2.join.details', currentLang)}</button></span>
				{:else}
					<button class="btn btn-primary" onclick={() => (keyOpen = true)}>{tr('v2.join.idle', currentLang)}</button>
				{/if}
			{/if}
		</div>
		<!-- Without a key the ballot shows through a blur, behind the one
		     button that opens it. -->
		<div class="ballot-body" class:ballot-body--locked={!hasKey}>
		{#if !hasKey}
			<div class="unlock">
				<button class="btn btn-primary unlock-btn" onclick={() => (keyOpen = true)}>{tr('v2.key.get', currentLang)}</button>
			</div>
		{/if}
		<p class="hint">
			{tr(open ? 'vote.hint' : 'vote.closed_hint', currentLang, { n: depth })}
			{#if open && shown.filter((x) => x !== BLANK).length > 1}
				{tr(coarse ? 'vote.hint.move' : 'vote.hint.drag', currentLang)}
			{/if}
		</p>
		{#if hasKey && ($warm === 'loading' || $warm === 'failed')}
			<p class="hint muted">{tr(`v2.warm.${$warm}`, currentLang)}</p>
		{/if}
		{#if loggedUnknown}
			<p class="hint">{tr('v2.ballot.logged', currentLang)}</p>
		{/if}
		<ol class="slots" style="--n: 3" use:sortable>
			{#each [0, 1, 2] as i (i)}
				{@const id = shown[i]}
				<li class="slot" class:filled={id !== undefined} class:next={open && id === undefined && i === shown.length} class:movable={movable(id)}>
					{#if id === BLANK}
						<button class="slot-add" onclick={() => openSlot(i)} disabled={busy || !open}>
							<span class="slot-photo slot-photo--blank" aria-hidden="true"><span class="slot-rank">{i + 1}</span></span>
							<span class="slot-name slot-name--plain">{tr('vote.blank.name', currentLang)}</span>
						</button>
						{#if open}
							<button class="unpick" onclick={() => remove(BLANK)} disabled={busy} aria-label={tr('vote.remove_named', currentLang, { name: tr('vote.blank.name', currentLang) })}>×</button>
						{/if}
					{:else if id !== undefined}
						<button class="slot-add" onclick={() => openSlot(i)} disabled={busy || !open}>
							<span class="slot-photo">
								{#if imageOf(id)}
									<img src={imageOf(id)} alt="" draggable="false" />
								{:else}
									<span class="slot-initial">{nameOf(id).charAt(0).toUpperCase()}</span>
								{/if}
								<span class="slot-rank">{i + 1}</span>
							</span>
							<span class="slot-name">{nameOf(id)}</span>
						</button>
						{#if open}
							<button class="unpick" onclick={() => remove(id)} disabled={busy} aria-label={tr('vote.remove_named', currentLang, { name: nameOf(id) })}>×</button>
						{/if}
						{#if movable(id)}
							<span class="slot-move">
								<button onclick={() => move(id, -1)} disabled={busy || i === 0} aria-label={tr('vote.move_up', currentLang, { name: nameOf(id) })}>▲</button>
								<button onclick={() => move(id, 1)} disabled={busy || i >= shown.filter((x) => x !== BLANK).length - 1} aria-label={tr('vote.move_down', currentLang, { name: nameOf(id) })}>▼</button>
							</span>
						{/if}
					{:else if open && endsBlank}
						<!-- After the blank, nothing: the protocol ranks the blank last. The
						     slot says so instead of inviting a pick that would slip before it. -->
						<div class="slot-closed" aria-label={tr('vote.slot.closed', currentLang, { rank: rankWord(i + 1) })}>
							<span class="slot-photo slot-photo--empty slot-photo--closed"><svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="8" /><path d="M6.5 6.5l11 11" /></svg></span>
							<span class="slot-name slot-name--empty slot-name--closed">{tr('vote.slot.closed_short', currentLang)}</span>
						</div>
					{:else if open && i === shown.length}
						<button class="slot-add" onclick={() => (slotPick = shown.length)} disabled={busy}>
							<span class="slot-photo slot-photo--empty"><span class="slot-plus" aria-hidden="true">+</span></span>
							<span class="slot-name slot-name--empty">{tr('vote.slot.choice', currentLang, { rank: rankWord(i + 1) })}</span>
						</button>
					{:else if open}
						<!-- Locked until the slots before it are filled: the ranking has no gaps. -->
						<div class="slot-locked" aria-label={tr('vote.slot.locked', currentLang, { rank: rankWord(i + 1) })}>
							<span class="slot-photo slot-photo--empty slot-photo--locked"><svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10.5" width="14" height="10" rx="2.2" /><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5" /></svg></span>
							<span class="slot-name slot-name--empty">{tr('vote.slot.choice', currentLang, { rank: rankWord(i + 1) })}</span>
						</div>
					{:else}
						<div class="slot-photo slot-photo--empty"><span class="slot-rank">{i + 1}</span></div>
						<span class="slot-name slot-name--empty">{tr('vote.slot.choice', currentLang, { rank: rankWord(i + 1) })}</span>
					{/if}
				</li>
			{/each}
		</ol>
		{#if open && endsBlank && shown.length < depth}
			<p class="blank-note" role="status">
				<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3L2.5 20h19z" /><path d="M12 10v4.5M12 17.2v.3" /></svg>
				<span>{tr(shown.length === 1 ? 'vote.blank.closes.alone' : 'vote.blank.closes', currentLang)}</span>
			</p>
		{/if}
		{#if ballot.state === 'missing'}
			<div class="status-line status-line--fault" role="alert">
				<p>{@html tr('v2.status.missing.help', currentLang, { report: reportLink(currentLang) })}</p>
				{#if ballot.complaintEvents?.length}
					<p>
						{tr('v2.status.missing.links', currentLang)}
						{#each ballot.complaintEvents as ev, i (ev.id)}
							{#if i > 0}, {/if}
							<a href="{paths.complaint}#c={encodeComplaint(ev)}">{tr('v2.status.missing.verify', currentLang)}</a>
						{/each}
					</p>
				{/if}
			</div>
		{/if}
		{#if open}
			<div class="ballot-actions">
				{#if dirty}
					<button class="btn submit" class:btn-primary={draft !== null && draft.length > 0} class:btn-danger={draft !== null && draft.length === 0} onclick={submit} disabled={busy || regPending}>{tr(regPending ? 'v2.join.wait_button' : draft !== null && draft.length === 0 ? 'vote.withdraw_all' : 'vote.submit', currentLang)}</button>
				{:else if event && voted}
					<div class="share-big"><ShareMenu label item={{ kind: 'event', id: 0, personId: 0, personName: currentLang === 'en' ? event.title_en : event.title_fr, slug }} /></div>
				{/if}
				{#if canResend}
					<button class="btn submit btn-primary" onclick={resend} disabled={busy}>{tr('v2.ballot.resend', currentLang)}</button>
				{:else if canRetry}
					<button class="btn submit btn-primary" onclick={retry} disabled={busy}>{tr('v2.ballot.retry', currentLang)}</button>
				{/if}
			</div>
			<!-- One line under the button, its place kept: reordering moves nothing. -->
			<p class="vote-note" class:vote-note--ok={!dirty && !busy && (ballot.state === 'acked' || ballot.state === 'verified')} aria-live="polite">{statusLine}</p>
			{#if kReal > 1}<p class="vote-note vote-note--privacy">{tr('v2.privacy.batch', currentLang)}</p>{/if}
		{/if}
		{#if actionError}
			<p class="save-error" role="alert">{@html actionError}</p>
			{#if actionDetail}
				<details class="tech"><summary>{tr('v2.details', currentLang)}</summary><p>{actionDetail}</p></details>
			{/if}
			{#if contestable}
				<button class="btn btn-small btn--ghost" onclick={contest} disabled={busy}>{tr('v2.ballot.contest', currentLang)}</button>
			{/if}
		{/if}
		{#if hasKey}
			<button class="btn btn-small key-btn" class:key-btn--warn={keyUnprotected} onclick={() => (keyMenuOpen = true)}>
				<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10.5" width="14" height="10" rx="2.2" /><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5" /></svg>
				{tr('v2.key.menu', currentLang)}{#if keyUnprotected}<span class="key-warn">· {tr('v2.key.menu.unprotected', currentLang)}</span>{/if}
			</button>
		{/if}
		</div>
	</section>

	<Modal open={slotPick !== null} title={slotPick === null ? '' : tr('vote.slot.choice', currentLang, { rank: rankWord(slotPick + 1) })} onclose={() => (slotPick = null)} closeLabel={tr('auth.close', currentLang)}>
		<p class="legend">{tr('score.legend', currentLang)}</p>
		<div class="picker">
			{#each choosable as o (o)}
				<button class="pick-card" onclick={() => chooseFor(o)} disabled={busy}>
					<TartuffeScore honesty={cardOf(o)?.honesty_score} lang={currentLang} image={imageOf(o)} initial={nameOf(o).charAt(0).toUpperCase()} help={false} />
					<span class="pick-name">{nameOf(o)}</span>
				</button>
			{/each}
			{#if kind?.blank && !shown.includes(BLANK)}
				<button class="pick-card pick-card--blank" onclick={() => chooseFor(BLANK)} disabled={busy}>
					<span class="pick-initial" aria-hidden="true"></span>
					<span class="pick-name">{tr('vote.blank.name', currentLang)}</span>
				</button>
			{/if}
		</div>
	</Modal>

	<Modal open={!!linkCode} title={tr('v2.link.send.title', currentLang)} onclose={closeLink} closeLabel={tr('auth.close', currentLang)}>
		{#if !joined}
			<p class="dialog-text">{tr('v2.link.send.nokey', currentLang)}</p>
		{:else if linkState === 'sent'}
			<p class="dialog-text">{tr('v2.link.send.sent', currentLang)}</p>
			<p class="dialog-text">{tr('v2.link.send.code', currentLang)}</p>
			<p class="confirm-code">{linkConfirm}</p>
			<p class="dialog-text">{tr('v2.link.send.hint', currentLang)}</p>
		{:else}
			<p class="dialog-text">{tr('v2.link.send.text', currentLang)}</p>
			{#if linkState === 'failed'}<p class="save-error" role="alert">{linkError}</p>{/if}
		{/if}
		{#snippet actions()}
			<button class="btn" onclick={closeLink}>{tr('auth.close', currentLang)}</button>
			{#if joined && linkState !== 'sent'}
				<button class="btn btn-primary" onclick={sendKey} disabled={linkState === 'sending'}>{tr('v2.link.send.button', currentLang)}</button>
			{/if}
		{/snippet}
	</Modal>

	<Modal open={keyMenuOpen} title={tr('profile.key.title', currentLang)} onclose={() => ((keyMenuOpen = false), (showWords = false))} closeLabel={tr('auth.close', currentLang)}>
		<p class="dialog-text">{tr('profile.key.story', currentLang)}</p>
		<p class="dialog-text">{tr(keyStatusKey(), currentLang)}</p>
		{#if key?.hasWords}
			{#if showWords}
				<p class="dialog-text">{tr('v2.key.words.again', currentLang)}</p>
				<ol class="words">
					{#each (vs.session?.words() ?? '').split(' ') as w, i (i)}<li>{w}</li>{/each}
				</ol>
			{:else}
				<button class="btn btn-small" onclick={() => (showWords = true)}>{tr('v2.key.words.show', currentLang)}</button>
			{/if}
		{/if}
		<details class="lost">
			<summary>{tr('profile.key.lost.title', currentLang)}</summary>
			<p class="dialog-text">{tr('profile.key.lost.text', currentLang)}</p>
		</details>
		{#snippet actions()}
			<button class="btn btn-quiet danger" onclick={() => (forgetOpen = true)}>{tr('profile.key.forget', currentLang)}</button>
		{/snippet}
	</Modal>

	<Modal open={forgetOpen} title={tr('profile.key.forget.title', currentLang)} onclose={() => (forgetOpen = false)} closeLabel={tr('auth.close', currentLang)}>
		<p class="dialog-text">{tr(key?.backedUp ? 'profile.key.forget.text' : 'profile.key.forget.text_unprotected', currentLang)}</p>
		{#snippet actions()}
			<button class="btn" onclick={() => (forgetOpen = false)}>{tr('vote.cancel', currentLang)}</button>
			<button class="btn btn-danger" onclick={forgetKey}>{tr('profile.key.forget', currentLang)}</button>
		{/snippet}
	</Modal>

	<VoteKeyDialog open={keyOpen} lang={currentLang} session={vs} onclose={() => (keyOpen = false)} />

	{#snippet help(text: string)}
		<details class="help">
			<summary aria-label="?">?</summary>
			<p>{text}</p>
		</details>
	{/snippet}

	<section class="results" aria-labelledby="results-title">
		<h2 id="results-title">{tr('poll.results.title', currentLang)}</h2>
		<!-- One line: how many (by hundreds), how recent, checked here. -->
		{#if poll.publish.mode === 'open' && results && !blackout && (votersText || $tallyAt || verified)}
			<p class="live">
				{#if votersText}<span>{tr('v2.counter.counted', currentLang, { n: votersText })}</span>{/if}
				{#if $tallyAt}{#if votersText}<span class="dot"></span>{/if}<span>{tr('v2.results.updated.short', currentLang, { when: ago($tallyAt) })}</span>{/if}
				{#if verified}{#if votersText || $tallyAt}<span class="dot"></span>{/if}<a class="checked" href={paths.verify}>✓ {tr('v2.results.verified.short', currentLang)}</a>{/if}
			</p>
		{/if}

		{#if blackout}
			<p class="state">{tr('v2.blackout', currentLang)}</p>
		{:else}
			{#if $st.tallyUnverified}
				<div class="notice notice--legal" role="alert">{tr('v2.results.unverified', currentLang)}</div>
			{/if}
			{#if !results}
				<p class="state state--soon">{tr(kReal === 1 ? 'v2.results.none.first' : 'v2.results.none', currentLang)}</p>
			{/if}
			<!-- The two counts side by side: the ranking, and the first choices. -->
			<div class="count-switch" role="tablist" aria-label={tr('poll.results.title', currentLang)}>
				{#each counts as { kind } (kind)}
					<button role="tab" aria-selected={countShown === kind} class:on={countShown === kind} onclick={() => (countShown = kind)}>
						{tr(kind === 'ranking' ? 'poll.tab.ranking' : 'poll.tab.first.short', currentLang)}
					</button>
				{/each}
			</div>
			<div class="two-counts" bind:this={countsEl}>
				<svg class="links" aria-hidden="true">
					{#each links as l (l.slot)}
						<path d={l.d} class:on={picked === l.slot} class:off={picked !== null && picked !== l.slot} />
					{/each}
				</svg>
				{#each counts as { kind, list } (kind)}
					<div class="count" class:count--off={countShown !== kind}>
						<h3>{tr(kind === 'ranking' ? 'poll.tab.ranking' : 'poll.tab.first.short', currentLang)}</h3>
						<ol class="vote-list">
							{#each list as o, i (o.slot)}
								{@const pid = o.blank ? undefined : cardOf(o.slot)?.person_id}
								<!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
								<li
									bind:this={rowEls[kind][o.slot]}
									class="vote-row"
									class:vote-row--mine={shown.includes(o.slot)}
									class:vote-row--top={!!results && i === 0}
									class:vote-row--blank={o.blank}
									class:vote-row--picked={picked === o.slot}
									role="button"
									tabindex="0"
									aria-pressed={picked === o.slot}
									onclick={() => pickRow(o.slot)}
									onkeydown={(e) => {
										if (e.target === e.currentTarget && (e.key === 'Enter' || e.key === ' ')) {
											e.preventDefault();
											pickRow(o.slot);
										}
									}}
								>
									<span class="vote-rank">{results ? (kind === 'ranking' ? o.rank : i + 1) : '–'}</span>
									{#if o.blank}
										<span class="photo photo--empty" aria-hidden="true"></span>
									{:else}
										<TartuffeScore honesty={cardOf(o.slot)?.honesty_score} lang={currentLang} size="xs" image={imageOf(o.slot)} initial={nameOf(o.slot).charAt(0).toUpperCase()} />
									{/if}
									<span class="who-text">
										{#if pid !== undefined}
											<a class="name name--link" href={`/person/${pid}`} onclick={(e) => e.stopPropagation()}>{nameOf(o.slot)}</a>
										{:else}
											<span class="name">{nameOf(o.slot)}{#if o.blank}&nbsp;{@render help(tr('vote.blank.tip', currentLang))}{/if}</span>
										{/if}
										{#if !sharesShown(table.counted)}
											<!-- Under the floor the ranking shows alone. -->
										{:else if kind === 'ranking'}
											<span class="meta">{tr('poll.top3.short', currentLang, { r: pct(o.ranked, table.counted) })}</span>
										{:else}
											<strong class="share">{pct(o.first, table.counted)}</strong>
										{/if}
									</span>
								</li>
							{/each}
						</ol>
					</div>
				{/each}
			</div>
			<details class="explain">
				<summary>{tr('poll.explain.both', currentLang)}</summary>
				<p><strong>{tr('poll.tab.ranking', currentLang)}.</strong> {tr('poll.explain.ranking.lead', currentLang)} {tr('poll.explain.ranking.rest', currentLang)}</p>
				<p><strong>{tr('poll.tab.first.short', currentLang)}.</strong> {tr('poll.explain.first.rest.only', currentLang)} {tr('poll.explain.first.rest', currentLang)}</p>
			</details>
		{/if}
		<p class="disclaimer">{tr('poll.disclaimer', currentLang)}</p>
	</section>

	<p class="how-link"><a href={paths.how}>{tr('poll.how.title', currentLang)}</a></p>
{/if}

<!-- Outside the session's states: it opens while the mirror still loads. -->
<VoteIntro open={introOpen} lang={currentLang} howHref={paths.how} onclose={closeIntro} />

<style>
	.joining--fault {
		color: var(--red);
	}
	.link-btn {
		background: none;
		border: none;
		padding: 0;
		color: var(--text-secondary);
		text-decoration: underline;
		cursor: pointer;
		font: inherit;
	}
	.confirm-code {
		margin: 0 0 0.9rem;
		text-align: center;
		font-family: var(--font-mono, monospace);
		font-size: 2rem;
		letter-spacing: 0.3em;
		color: var(--text-primary);
	}
	.words {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 0.35rem 0.8rem;
		margin: 0 0 0.9rem;
		padding-left: 1.4rem;
		font-family: var(--font-mono, monospace);
		font-size: 0.95rem;
	}
	.lost summary {
		cursor: pointer;
		font-size: 0.9rem;
		color: var(--text-secondary);
		margin-bottom: 0.5rem;
	}
	.danger {
		color: var(--red);
	}
	.slots {
		list-style: none;
		margin: 0.75rem 0 1rem;
		padding: 0;
		display: grid;
		grid-template-columns: repeat(var(--n, 3), minmax(0, 1fr));
		gap: 0.75rem;
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
	.slot.movable {
		user-select: none;
		-webkit-user-select: none;
		-webkit-touch-callout: none;
	}
	:global(.slot.dragging) {
		opacity: 1 !important;
		cursor: grabbing;
		box-shadow: 0 14px 32px rgba(0, 0, 0, 0.45);
		transform: scale(1.05);
	}
	:global(.slot.drop-target) {
		border-color: var(--accent);
		border-style: dashed;
		background: color-mix(in oklch, var(--accent) 14%, var(--bg-card));
	}
	.slot.next {
		border-color: var(--accent);
	}
	.slot-add {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.6rem;
		width: 100%;
		padding: 0;
		border: none;
		background: none;
		color: inherit;
		font: inherit;
		cursor: pointer;
	}
	.slot:has(.slot-add):hover {
		background: color-mix(in oklch, var(--accent) 8%, transparent);
	}
	.slot-plus {
		font-size: 1.8rem;
		font-weight: 300;
		line-height: 1;
		color: var(--accent-hover);
	}
	.picker {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(7.5rem, 1fr));
		gap: 0.5rem;
		min-width: min(28rem, 80vw);
	}
	/* The dial (with its score line), then the name under it. */
	.pick-card {
		display: grid;
		grid-template-rows: auto auto;
		align-content: start;
		justify-items: center;
		align-items: start;
		gap: 0.4rem;
		padding: 0.6rem 0.3rem;
		border: 1px solid transparent;
		border-radius: var(--radius-md);
		background: transparent;
		color: var(--text-primary);
		font: inherit;
		font-size: 0.8rem;
		font-weight: 600;
		line-height: 1.2;
		text-align: center;
		cursor: pointer;
		transition: background 0.15s, border-color 0.15s;
	}
	.pick-card:hover:not(:disabled) {
		border-color: var(--accent);
		background: color-mix(in oklch, var(--accent) 12%, transparent);
	}
	.pick-card:disabled {
		opacity: 0.5;
		cursor: wait;
	}
	.pick-initial {
		width: 4.6rem;
		height: 4.6rem;
		border-radius: 50%;
		object-fit: cover;
	}
	.pick-initial {
		display: grid;
		place-items: center;
		background: var(--bg-secondary);
		font-family: var(--font-serif);
		font-size: 1.4rem;
		color: var(--text-secondary);
	}
	/* The blank vote: a plain white disc, the size and place of a dial's portrait. */
	.pick-card--blank .pick-initial {
		width: 3.85rem;
		height: 3.85rem;
		margin: 0.95rem 0 1.15rem;
		background: #fff;
		border: 1px solid var(--glass-border);
	}
	.pick-name {
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
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
		/* A square whatever the source (a 3:4 portrait kept its height and
		   ran under the name). */
		width: 100%;
		height: auto;
		aspect-ratio: 1 / 1;
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
	/* The blank vote: a white disc. */
	.slot-photo--blank {
		background: #fff;
		box-shadow: 0 0 0 3px var(--bg-card), 0 0 0 5px #fff;
	}
	.slot-locked {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.45rem;
		opacity: 0.55;
	}
	.slot-photo--locked svg {
		width: 1.5rem;
		height: 1.5rem;
		fill: none;
		stroke: var(--text-muted);
		stroke-width: 2;
		stroke-linecap: round;
	}
	/* A slot after the blank vote: closed, in amber, since nothing can follow it. */
	.slot:has(.slot-closed) {
		border-color: color-mix(in oklch, var(--amber) 55%, transparent);
		background: color-mix(in oklch, var(--amber) 6%, transparent);
	}
	.slot-closed {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.45rem;
	}
	.slot-photo--closed {
		border-color: color-mix(in oklch, var(--amber) 60%, transparent);
	}
	.slot-photo--closed svg {
		width: 1.6rem;
		height: 1.6rem;
		fill: none;
		stroke: var(--amber);
		stroke-width: 2;
		stroke-linecap: round;
	}
	.slot-name--closed {
		color: var(--amber);
		font-weight: 600;
	}
	.blank-note {
		display: flex;
		align-items: flex-start;
		gap: 0.6rem;
		margin: 0 0 0.9rem;
		padding: 0.65rem 0.85rem;
		border: 1px solid color-mix(in oklch, var(--amber) 50%, transparent);
		border-radius: var(--radius-md);
		background: color-mix(in oklch, var(--amber) 10%, var(--bg-card));
		color: var(--text-primary);
		font-size: 0.88rem;
		line-height: 1.45;
		text-align: left;
	}
	.blank-note svg {
		flex-shrink: 0;
		width: 1.2rem;
		height: 1.2rem;
		margin-top: 0.1rem;
		fill: none;
		stroke: var(--amber);
		stroke-width: 2;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	/* The blank vote: a plain white disc where the others have a dial's portrait. */
	.vote-row--blank .photo--empty {
		background: #fff;
		border: 1px solid var(--glass-border);
	}
	.slot-rank {
		position: absolute;
		left: -0.2rem;
		top: -0.2rem;
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
	.slot-move {
		position: absolute;
		left: 0.3rem;
		top: 0.3rem;
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
		opacity: 0;
		transition: opacity var(--transition-fast);
	}
	.slot:hover .slot-move,
	.slot:focus-within .slot-move {
		opacity: 1;
	}
	.slot-move button {
		width: 1.5rem;
		height: 1.3rem;
		border: none;
		border-radius: var(--radius-sm, 0.3rem);
		background: color-mix(in oklch, var(--bg-elevated, #222) 80%, transparent);
		color: var(--text-muted);
		font-size: 0.6rem;
		line-height: 1;
		cursor: pointer;
	}
	.slot-move button:hover:not(:disabled) {
		color: var(--text-primary);
	}
	.slot-move button:disabled {
		opacity: 0.35;
		cursor: default;
	}
	.unpick:hover {
		color: var(--red);
		background: color-mix(in oklch, var(--red) 12%, transparent);
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
	/* A status icon opens its text like a "?". */
	.status-tip {
		display: inline-block;
		margin-left: 0.4rem;
		vertical-align: 0.1em;
	}
	.help.status-tip summary {
		border: none;
		margin: 0;
		color: white;
	}
	.status-tip p {
		left: 0;
		transform: none;
		font-family: var(--font-family);
		font-weight: 400;
		text-align: left;
	}
	.state-icon--fault {
		background: var(--red);
		color: white;
		font-family: var(--font-family);
		font-size: 0.85rem;
		font-weight: 800;
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
	.vote-row--blank .name {
		font-style: italic;
	}
	:global(:root) {
		--poll-blue: #6f8fc9;
	}

	.state {
		text-align: center;
		color: var(--text-secondary);
		padding: 3rem 1rem;
	}
	.state--soon {
		padding: 0.25rem 1rem 1rem;
		font-size: 0.9rem;
	}
	.state.error {
		color: var(--red);
	}

	.hero {
		text-align: center;
		max-width: 46rem;
		margin: 1rem auto 1.5rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2.25rem, 6vw, 3.5rem);
		font-weight: 600;
		margin: 0.25rem 0 0.75rem;
		line-height: 1.05;
	}
	/* A results row's second line (it was the hero's rule too). */
	.meta {
		display: flex;
		justify-content: center;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin: 0;
		font-size: 0.72rem;
		color: var(--text-muted);
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
		margin: 1.2rem 0 0;
		text-align: center;
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
	.ballot-body {
		position: relative;
	}
	.ballot-body--locked > :not(.unlock) {
		filter: blur(5px);
		pointer-events: none;
		user-select: none;
	}
	.unlock {
		position: absolute;
		inset: 0;
		z-index: 1;
		display: flex;
		align-items: center;
		justify-content: center;
	}
	.intro-line {
		margin: 0 0 0.4rem;
		text-align: center;
		font-size: 0.8rem;
	}
	.intro-line .link-btn {
		color: var(--text-muted);
	}
	.intro-line .link-btn:hover {
		color: var(--text-primary);
	}
	.unlock-btn {
		padding: 0.9rem 1.6rem;
		font-size: 1.05rem;
		box-shadow: var(--shadow-glow);
	}
	.ballot {
		position: relative;
	}
	.ballot-meta {
		display: flex;
		justify-content: center;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem;
		margin: -0.2rem 0 0;
		font-size: 0.82rem;
		color: var(--text-secondary);
	}
	.ballot-head {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
		text-align: center;
	}
	.key-btn {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		margin: 1.1rem auto 0;
	}
	.key-btn svg {
		width: 1rem;
		height: 1rem;
		fill: none;
		stroke: currentColor;
		stroke-width: 2;
		stroke-linecap: round;
	}
	.key-btn--warn {
		border-color: var(--amber);
		color: var(--amber);
	}
	.key-warn {
		font-weight: 400;
	}
	.share-big :global(.share-btn) {
		height: auto;
		padding: 0.7rem 1.4rem;
		font-size: 0.95rem;
		font-weight: 600;
		color: white;
		background: var(--accent);
		border-color: var(--accent);
		border-radius: var(--radius-sm);
	}
	.share-big :global(.share-btn:hover) {
		color: white;
		background: var(--accent-hover);
		border-color: var(--accent-hover);
	}
	.legend {
		margin: -0.25rem 0 0.9rem;
		font-size: 0.8rem;
		line-height: 1.45;
		color: var(--text-secondary);
	}
	.hint {
		text-align: center;
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
	.dialog-text {
		font-size: 0.9rem;
		line-height: 1.5;
		color: var(--text-secondary);
		margin: 0 0 0.75rem;
	}
	.ballot-actions {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.6rem;
		flex-wrap: wrap;
	}
	.submit {
		padding: 0.7rem 1.4rem;
		font-size: 0.95rem;
	}
	.save-error {
		color: var(--red);
		font-size: 0.85rem;
		margin: 0.6rem 0 0;
	}
	/* The two counts side by side, each as narrow as its rows, the lines
	   between them in the gap. */
	.two-counts {
		position: relative;
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		column-gap: 3.5rem;
		max-width: 44rem;
		margin: 0.4rem auto 0.8rem;
	}
	.links {
		position: absolute;
		inset: 0;
		width: 100%;
		height: 100%;
		overflow: visible;
		pointer-events: none;
	}
	.links path {
		fill: none;
		stroke: var(--text-muted);
		stroke-width: 1;
		opacity: 0.35;
		transition: opacity var(--transition-fast), stroke var(--transition-fast);
	}
	.links path.off {
		opacity: 0.12;
	}
	.links path.on {
		stroke: var(--accent);
		stroke-width: 2;
		opacity: 1;
	}
	.vote-row[role='button'] {
		cursor: pointer;
	}
	.vote-row--picked {
		box-shadow: inset 3px 0 0 var(--accent);
		background: var(--accent-glow);
	}
	.vote-row:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: -2px;
	}
	.name--link {
		color: inherit;
		text-decoration: none;
	}
	.name--link:hover {
		color: var(--accent-hover);
		text-decoration: underline;
	}
	.count h3 {
		margin: 0 0 0.5rem;
		font-size: 0.75rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		text-align: center;
		color: var(--text-secondary);
	}
	.share {
		font-size: 1.05rem;
		font-weight: 800;
		font-variant-numeric: tabular-nums;
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
		grid-template-columns: 1.1rem auto minmax(0, 1fr);
		align-items: center;
		gap: 0.5rem;
		padding: 0.55rem 0.6rem;
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
	.who-text {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.3rem;
		min-width: 0;
	}
	.vote-row .photo {
		width: 2.4rem;
		height: 2.4rem;
		margin: 0.55rem 0.6rem 0;
		flex-shrink: 0;
	}
	.vote-row .meta {
		justify-content: flex-start;
		text-align: left;
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

	/* The switch between the two counts: shown on a phone only (the media
	   query below, which must come after this rule to win). */
	.count-switch {
		display: none;
		justify-content: center;
		gap: 0.25rem;
		width: fit-content;
		max-width: 100%;
		margin: 0.4rem auto 0.9rem;
		padding: 4px;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
		background: var(--bg-card);
	}
	.count-switch button {
		padding: 0.45rem 1rem;
		border: none;
		border-radius: var(--radius-full);
		background: none;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.85rem;
		font-weight: 600;
		white-space: nowrap;
		cursor: pointer;
	}
	.count-switch button.on {
		background: var(--accent);
		color: white;
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
		.photo {
			width: 48px;
			height: 48px;
		}
		/* One count at a time, full width, chosen with the switch; the lines
		   between the two have nothing to join. */
		.count-switch {
			display: flex;
		}
		.two-counts {
			grid-template-columns: minmax(0, 1fr);
			column-gap: 0;
		}
		.two-counts .links,
		.count--off,
		.count h3 {
			display: none;
		}
		.vote-row {
			gap: 0.6rem;
			padding: 0.6rem 0.75rem;
		}
	}
	.live {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		align-items: center;
		gap: 0.5rem;
		margin: -0.5rem 0 1.25rem;
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	.joining {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	.muted {
		color: var(--text-muted);
	}
	.checked {
		color: var(--green);
		text-decoration: none;
	}
	.how-link {
		text-align: center;
		margin: 2rem 0;
		font-size: 0.9rem;
	}
	/* The arrows to reorder are always there where there is no hover. */
	@media (hover: none) {
		.slot-move {
			opacity: 1;
		}
	}
	/* Where the vote stands, under the slots. */
	.status-line {
		margin: 0 0 0.9rem;
		padding: 0.6rem 0.8rem;
		border-radius: var(--radius-md);
		background: var(--bg-secondary);
		font-size: 0.9rem;
		line-height: 1.45;
		color: var(--text-secondary);
	}
	.status-line--fault {
		background: color-mix(in oklch, var(--red) 12%, var(--bg-card));
		color: var(--text-primary);
	}
	.status-line--fault p {
		margin: 0 0 0.4rem;
	}
	.status-line--fault p:last-child {
		margin: 0;
	}
	.vote-note {
		min-height: 2.9em;
		margin: 0.6rem 0 0;
		font-size: 0.88rem;
		line-height: 1.45;
		text-align: center;
		color: var(--text-secondary);
	}
	.vote-note--ok {
		color: var(--green);
	}
	.tech {
		margin: 0.4rem 0 0;
		font-size: 0.78rem;
		color: var(--text-muted);
	}
	.tech summary {
		cursor: pointer;
	}
	.tech p {
		margin: 0.3rem 0 0;
		font-family: var(--font-mono, monospace);
		overflow-wrap: anywhere;
	}
	.results h2,
	.explain,
	.explain summary {
		text-align: center;
	}
	.explain {
		margin: 0 0 0.75rem;
		font-size: 0.85rem;
		line-height: 1.5;
		color: var(--text-secondary);
	}
	.explain summary {
		cursor: pointer;
	}
	.explain p {
		margin: 0.4rem 0 0;
	}
	.vote-row .help summary {
		width: 1.1rem;
		height: 1.1rem;
		font-size: 0.65rem;
	}
</style>
