<script lang="ts">
	// The vote page of tartuffe-vote/2 (docs/vote/SPEC.md). Everything it
	// shows comes from the public mirror and the mailbox (lib/vote/session):
	// the event, the setup in force, the latest checkpoint and tally, the
	// live counters; the voter's key, ballot and receipt. It never calls our
	// server.
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { displayName } from '$lib/share';
	import { lang, tr } from '$lib/i18n';
	import Modal from '$lib/components/Modal.svelte';
	import Spinner from '$lib/components/Spinner.svelte';
	import ShareMenu from '$lib/components/ShareMenu.svelte';
	import VoteKeyDialog from '$lib/components/VoteKeyDialog.svelte';
	import Sortable from 'sortablejs';
	import { eventPaths, showVoters } from '$lib/event';
	import { VoteSession, type Tally } from '$lib/vote/session';
	import { voteKey } from '$lib/vote/key';
	import { BLANK, type Ranking } from '$lib/vote/crypto';
	import { defaultSources } from '$lib/vote/sources';
	import { neventLink } from '$lib/vote/mailbox';
	import { parseLink, type LinkCode } from '$lib/vote/link';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));
	const session = new VoteSession($page.params.slug ?? '');
	const phase = session.phase;
	const setupStore = session.setup;
	const tallyStore = session.tally;
	const statsStore = session.stats;
	const ballotStore = session.ballot;
	const warm = session.warm;
	const sessionError = session.error;

	interface EventInfo {
		id: number;
		slug: string;
		title_fr: string;
		title_en: string;
		description_fr: string;
		description_en: string;
		closes_at?: string;
	}
	interface Card {
		person_id: number;
		name: string;
		image_url?: string;
		honesty_score: number;
	}
	let event = $state<EventInfo | null>(null);
	let cards = $state<Map<number, Card>>(new Map());
	let keyOpen = $state(false);
	let slotPick = $state<number | null>(null);
	let withdrawOpen = $state(false);
	// A device link opened here (SPEC §4.2): the code of the device asking
	// for the key, and where the sending stands.
	let linkCode = $state<LinkCode | null>(null);
	let linkState = $state<'ask' | 'sending' | 'sent' | 'failed'>('ask');
	let linkError = $state('');
	async function sendKey() {
		if (!linkCode) return;
		linkState = 'sending';
		try {
			await session.sendKey(linkCode);
			linkState = 'sent';
		} catch (e) {
			linkError = e instanceof Error ? e.message : String(e);
			linkState = 'failed';
		}
	}
	function closeLink() {
		linkCode = null;
		history.replaceState(null, '', window.location.pathname + window.location.search);
	}
	let actionError = $state('');

	// The options of the setup in force: slot → candidate card or blank.
	let options = $derived(($setupStore?.setup.options ?? []).filter((o) => o.kind === 'candidate' || o.kind === 'blank'));
	let bySlot = $derived(new Map(options.map((o) => [o.slot, o])));
	let mask = $derived($setupStore?.setup.mask ?? 0);
	let kReal = $derived($setupStore?.setup.k_real ?? 10);
	// The page stops taking ballots 10 minutes before the close (SPEC §9.3).
	let open = $derived($phase !== 'closed' && !!$setupStore && Date.now() < Date.parse($setupStore.setup.closes) - 10 * 60_000);
	let blackout = $derived(($setupStore?.setup.blackouts ?? []).some((b) => Date.now() >= Date.parse(b.from) && Date.now() < Date.parse(b.to)));
	let hasKey = $derived(!!$voteKey && $voteKey.slug === slug);
	let ready = $derived($phase === 'ready');
	let busy = $derived($ballotStore.state === 'proving' || $ballotStore.state === 'sending');

	// The ballot shown: the last one cast from this device (kept with the
	// key), as slots.
	let shown = $derived<number[]>($ballotStore.ranking ? $ballotStore.ranking.slots.slice(0, $ballotStore.ranking.len) : rememberedSlots());
	let endsBlank = $derived(shown.at(-1) === BLANK);
	let choosable = $derived(options.filter((o) => o.kind === 'candidate' && !shown.includes(o.slot)));

	const REMEMBER = 'vote2_ballot';
	function rememberedSlots(): number[] {
		try {
			const raw = localStorage.getItem(`${REMEMBER}:${slug}`);
			return raw ? (JSON.parse(raw) as number[]) : [];
		} catch {
			return [];
		}
	}
	function remember(slots: number[]) {
		try {
			localStorage.setItem(`${REMEMBER}:${slug}`, JSON.stringify(slots));
		} catch {}
	}

	async function change(next: number[]): Promise<boolean> {
		if (busy || !ready) return false;
		actionError = '';
		const r: Ranking = { len: next.length, slots: [next[0] ?? 0, next[1] ?? 0, next[2] ?? 0] };
		try {
			await session.cast(r);
			remember(next);
			return true;
		} catch (e) {
			const reason = $ballotStore.reason ?? (e instanceof Error ? e.message : String(e));
			actionError = tr(`v2.reason.${reason}`, currentLang) !== `v2.reason.${reason}` ? tr(`v2.reason.${reason}`, currentLang) : reason;
			return false;
		}
	}

	function placed(cur: number[], choice: number, at: number | null): number[] {
		if (choice === BLANK) return [...cur.filter((x) => x !== BLANK).slice(0, at ?? cur.length), BLANK];
		const rest = cur.filter((x) => x !== choice);
		const tail = rest.at(-1) === BLANK;
		const body = tail ? rest.slice(0, -1) : rest;
		const i = at === null ? body.length : Math.min(at, body.length);
		if (at !== null && i < body.length) body[i] = choice;
		else body.splice(i, 0, choice);
		return tail && body.length < 3 ? [...body, BLANK] : body;
	}

	function pick(choice: number, at: number | null) {
		if (!hasKey) {
			keyOpen = true;
			return;
		}
		const next = placed(shown, choice, at);
		if (next.length > 3) {
			actionError = tr('vote.error.full', currentLang, { n: 3 });
			return;
		}
		if (JSON.stringify(next) === JSON.stringify(shown)) return;
		change(next);
	}

	let suppressClick = false;
	function movable(id: number | undefined): boolean {
		return id !== undefined && id !== BLANK && !busy && open && shown.filter((x) => x !== BLANK).length > 1;
	}
	function sortable(node: HTMLElement) {
		const s = Sortable.create(node, {
			draggable: '.movable',
			delay: 350,
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
				change(endsBlank ? [...cands, BLANK] : cands);
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
		change(shown.filter((x) => x !== id));
	}
	// move swaps a candidate with its neighbour (the keyboard's way to
	// reorder; the drag is the finger's).
	function move(id: number, dir: -1 | 1) {
		const cands = shown.filter((x) => x !== BLANK);
		const i = cands.indexOf(id);
		const j = i + dir;
		if (i < 0 || j < 0 || j >= cands.length) return;
		[cands[i], cands[j]] = [cands[j], cands[i]];
		change(endsBlank ? [...cands, BLANK] : cands);
	}
	async function withdrawAll() {
		withdrawOpen = false;
		await change([]);
	}

	function rankWord(n: number): string {
		if (currentLang === 'en') return n === 1 ? '1st' : n === 2 ? '2nd' : n === 3 ? '3rd' : `${n}th`;
		return n === 1 ? '1er' : `${n}e`;
	}
	function closesText(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}
	function pct(n: number, total: number): string {
		return total > 0 ? `${Math.round((100 * n) / total)}%` : '—';
	}
	function ago(iso: string): string {
		const m = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 60000));
		if (m < 1) return currentLang === 'en' ? 'just now' : 'à l’instant';
		if (m < 60) return currentLang === 'en' ? `${m} min ago` : `il y a ${m} min`;
		const h = Math.round(m / 60);
		return currentLang === 'en' ? `${h} h ago` : `il y a ${h} h`;
	}
	function nameOf(slot: number): string {
		const o = bySlot.get(slot);
		if (!o) return `#${slot}`;
		if (o.kind === 'blank') return tr('vote.blank.name', currentLang);
		return displayName(cards.get(o.person_id ?? 0)?.name ?? o.name ?? `#${slot}`);
	}
	function imageOf(slot: number): string | undefined {
		const o = bySlot.get(slot);
		return o?.person_id ? cards.get(o.person_id)?.image_url : undefined;
	}

	let results = $derived.by(() => {
		const t: Tally | null = $tallyStore;
		if (!t?.results) return null;
		return t.results;
	});
	let winnerSlot = $derived(results?.condorcet_winner ?? null);

	onMount(async () => {
		const src = defaultSources();
		const [ev, cs] = await Promise.all([src.json<EventInfo>('event.json'), src.json<Card[]>('candidates.json')]);
		event = ev;
		cards = new Map((cs ?? []).map((c) => [c.person_id, c]));
		await session.init();
		checkLink();
		window.addEventListener('hashchange', checkLink);
	});
	// A device link in the URL, opened here (a scanned QR code) or
	// navigated to (the hash alone changes without a reload).
	function checkLink() {
		const code = parseLink(window.location.hash);
		if (code && code.pub !== linkCode?.pub) {
			linkCode = code;
			linkState = 'ask';
			linkError = '';
		}
	}
	onDestroy(() => {
		session.destroy();
		if (typeof window !== 'undefined') window.removeEventListener('hashchange', checkLink);
	});
</script>

<svelte:head>
	<title>{event ? `${currentLang === 'en' ? event.title_en : event.title_fr} — tartuffe.me` : 'tartuffe.me'}</title>
</svelte:head>

{#if $phase === 'error'}
	<div class="state error">
		<p>{$sessionError}</p>
		<button class="btn" onclick={() => session.init()}>{tr('home.retry', currentLang)}</button>
	</div>
{:else if $phase === 'loading' || !$setupStore}
	<p class="state loading"><Spinner label={tr('person.loading', currentLang)} /></p>
{:else}
	<header class="hero">
		<span class="kicker">{tr('poll.kicker', currentLang)}</span>
		<h1>{event ? (currentLang === 'en' ? event.title_en : event.title_fr) : slug}</h1>
		{#if event}<p class="lead">{currentLang === 'en' ? event.description_en : event.description_fr}</p>{/if}
		<p class="meta">
			{#if results && showVoters(results.voters)}
				<strong>{tr('poll.voters', currentLang, { n: results.voters.toLocaleString(currentLang === 'en' ? 'en-GB' : 'fr-FR') })}</strong>
				<span class="dot"></span>
			{/if}
			{#if open}
				{tr('poll.closes', currentLang, { d: closesText($setupStore.setup.closes) })}
			{:else}
				{tr('poll.closed', currentLang)}
			{/if}
		</p>
		<p class="disclaimer">{tr('poll.disclaimer', currentLang)}</p>
	</header>

	{#if $statsStore}
		<p class="live">
			<span>{tr('v2.counter.ballots', currentLang, { n: $statsStore.ballots.toLocaleString(currentLang === 'en' ? 'en-GB' : 'fr-FR') })}</span>
			<span class="dot"></span>
			<span>{tr('v2.counter.since', currentLang, { n: $statsStore.since_tally })}</span>
			<span class="dot"></span>
			<span>{tr('v2.counter.next', currentLang, { k: kReal })}</span>
		</p>
	{/if}

	{#if blackout}
		<div class="notice notice--legal">{tr('poll.hidden.blackout', currentLang)}</div>
	{/if}

	<section class="ballot" aria-labelledby="ballot-title">
		<div class="ballot-head">
			<h2 id="ballot-title">
				{tr('vote.title', currentLang)}
				{#if $ballotStore.state === 'verified'}
					<details class="help status-tip">
						<summary class="state-icon state-icon--logged" aria-label={tr('v2.status.verified', currentLang)}>
							<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="5" y="10.5" width="14" height="10" rx="2.2" /><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5" /><path class="tick" d="M9.3 15.6l1.9 1.9 3.6-3.7" /></svg>
						</summary>
						<p>{tr('v2.status.verified.help', currentLang)}</p>
					</details>
				{:else if $ballotStore.state === 'received'}
					<details class="help status-tip">
						<summary class="state-icon state-icon--ok" aria-label={tr('v2.status.received', currentLang)}>
							<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>
						</summary>
						<p>{tr('v2.status.received.help', currentLang)}</p>
					</details>
				{:else if $ballotStore.state === 'missing'}
					<details class="help status-tip">
						<summary class="state-icon state-icon--fault" aria-label={tr('v2.status.missing', currentLang)}>!</summary>
						<p>{tr('v2.status.missing.help', currentLang)}</p>
						{#if $ballotStore.complaints?.length}
							<p>
								{tr('v2.status.missing.links', currentLang)}
								{#each $ballotStore.complaints as id, i (id)}
									{#if i > 0}, {/if}
									<a href="{paths.complaint}?id={id}">{tr('v2.status.missing.verify', currentLang)}</a>
									(<a href={neventLink(id, $setupStore?.setup.relays.map((r) => r.url) ?? [])} rel="noopener">{tr('v2.status.missing.event', currentLang)}</a>)
								{/each}
							</p>
						{/if}
					</details>
				{:else if busy}
					<span class="state-icon" title={tr(`v2.status.${$ballotStore.state}`, currentLang)}><Spinner size="sm" label={tr(`v2.status.${$ballotStore.state}`, currentLang)} /></span>
				{:else if $ballotStore.state === 'rejected' || $ballotStore.state === 'failed'}
					<span class="state-icon state-icon--fault" title={actionError} aria-label={actionError}>!</span>
				{/if}
			</h2>
			{#if !hasKey}
				<button class="btn btn-primary" onclick={() => (keyOpen = true)}>{tr('v2.key.get', currentLang)}</button>
			{:else if $phase === 'joining' || $phase === 'registering'}
				<span class="joining"><Spinner size="sm" label={tr('v2.join.waiting', currentLang)} /> {tr('v2.join.waiting', currentLang)}</span>
			{/if}
		</div>
		<p class="hint">{tr(open ? 'vote.hint' : 'vote.closed_hint', currentLang, { n: 3 })}</p>
		{#if hasKey && $warm !== 'ready' && $warm !== 'idle'}
			<p class="hint muted">{tr(`v2.warm.${$warm}`, currentLang)}</p>
		{/if}
		<ol class="slots" style="--n: 3" use:sortable>
			{#each [0, 1, 2] as i (i)}
				{@const id = shown[i]}
				<li class="slot" class:filled={id !== undefined} class:next={open && id === undefined && i === shown.length} class:movable={movable(id)}>
					{#if id === BLANK}
						<button class="slot-add" onclick={() => openSlot(i)} disabled={busy || !open}>
							<span class="slot-photo slot-photo--blank" aria-hidden="true">◻<span class="slot-rank">{i + 1}</span></span>
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
					{:else if open}
						<button class="slot-add" onclick={() => (slotPick = shown.length)} disabled={busy}>
							<span class="slot-photo slot-photo--empty">
								{#if i === shown.length}<span class="slot-plus" aria-hidden="true">+</span>{:else}<span class="slot-rank">{i + 1}</span>{/if}
							</span>
							<span class="slot-name slot-name--empty">{tr('vote.slot.choice', currentLang, { rank: rankWord(i + 1) })}</span>
						</button>
					{:else}
						<div class="slot-photo slot-photo--empty"><span class="slot-rank">{i + 1}</span></div>
						<span class="slot-name slot-name--empty">{tr('vote.slot.choice', currentLang, { rank: rankWord(i + 1) })}</span>
					{/if}
				</li>
			{/each}
		</ol>
		{#if open}
			<div class="ballot-actions">
				{#if event && ($ballotStore.state === 'verified' || $ballotStore.state === 'received' || ($voteKey?.lastBallot && $voteKey.lastBallot.state !== 'missing'))}
					<ShareMenu label item={{ kind: 'event', id: 0, personId: 0, personName: currentLang === 'en' ? event.title_en : event.title_fr, slug }} />
				{/if}
				{#if shown.length === 0}
					<button class="btn btn-small" onclick={() => pick(BLANK, null)} disabled={busy}>◻ {tr('vote.blank.button', currentLang)}</button>
					{@render help(tr('vote.blank.help', currentLang))}
				{/if}
				{#if shown.length > 0}
					<button class="btn btn-small btn-quiet" onclick={() => (withdrawOpen = true)} disabled={busy}>{tr('vote.withdraw_all', currentLang)}</button>
				{/if}
			</div>
		{/if}
		{#if actionError}
			<p class="save-error" role="alert">{actionError}</p>
		{/if}
	</section>

	<Modal open={slotPick !== null} title={slotPick === null ? '' : tr('vote.slot.choice', currentLang, { rank: rankWord(slotPick + 1) })} onclose={() => (slotPick = null)} closeLabel={tr('auth.close', currentLang)}>
		<div class="picker">
			{#each choosable as o (o.slot)}
				<button class="pick-card" onclick={() => chooseFor(o.slot)} disabled={busy}>
					{#if imageOf(o.slot)}
						<img src={imageOf(o.slot)} alt="" />
					{:else}
						<span class="pick-initial" aria-hidden="true">{nameOf(o.slot).charAt(0).toUpperCase()}</span>
					{/if}
					<span>{nameOf(o.slot)}</span>
				</button>
			{/each}
			{#if !shown.includes(BLANK)}
				<button class="pick-card pick-card--blank" onclick={() => chooseFor(BLANK)} disabled={busy}>
					<span class="pick-initial" aria-hidden="true">◻</span>
					<span>{tr('vote.blank.name', currentLang)}</span>
				</button>
			{/if}
		</div>
	</Modal>

	<Modal open={withdrawOpen} title={tr('vote.withdraw.title', currentLang)} onclose={() => (withdrawOpen = false)} closeLabel={tr('auth.close', currentLang)}>
		<p class="dialog-text">{tr('vote.withdraw.text', currentLang)}</p>
		{#snippet actions()}
			<button class="btn" onclick={() => (withdrawOpen = false)}>{tr('vote.cancel', currentLang)}</button>
			<button class="btn btn-danger" onclick={withdrawAll} disabled={busy}>{tr('vote.withdraw_all', currentLang)}</button>
		{/snippet}
	</Modal>

	<Modal open={!!linkCode} title={tr('v2.link.send.title', currentLang)} onclose={closeLink} closeLabel={tr('auth.close', currentLang)}>
		{#if !hasKey || ($voteKey?.position ?? -1) < 0}
			<p class="dialog-text">{tr('v2.link.send.nokey', currentLang)}</p>
		{:else if linkState === 'sent'}
			<p class="dialog-text">{tr('v2.link.send.sent', currentLang)}</p>
		{:else}
			<p class="dialog-text">{tr('v2.link.send.text', currentLang)}</p>
			{#if linkState === 'failed'}<p class="save-error" role="alert">{linkError}</p>{/if}
		{/if}
		{#snippet actions()}
			<button class="btn" onclick={closeLink}>{tr('auth.close', currentLang)}</button>
			{#if hasKey && ($voteKey?.position ?? -1) >= 0 && linkState !== 'sent'}
				<button class="btn btn-primary" onclick={sendKey} disabled={linkState === 'sending'}>{tr('v2.link.send.button', currentLang)}</button>
			{/if}
		{/snippet}
	</Modal>

	<VoteKeyDialog open={keyOpen} lang={currentLang} {session} domain={import.meta.env.VITE_ZKPASSPORT_DOMAIN ?? window.location.hostname} devMode={import.meta.env.VITE_ZKPASSPORT_DEV === '1'} simulate={import.meta.env.VITE_VOTE_SIMULATE_PASSPORT === '1'} onclose={() => (keyOpen = false)} />

	{#snippet help(text: string)}
		<details class="help">
			<summary aria-label="?">?</summary>
			<p>{text}</p>
		</details>
	{/snippet}

	<section class="results" aria-labelledby="results-title">
		<h2 id="results-title">{tr('poll.list.ranking', currentLang)}</h2>
		{#if blackout}
			<p class="state">{tr('v2.blackout', currentLang)}</p>
		{:else if !results || !$tallyStore}
			<p class="state">{tr('v2.results.none', currentLang, { k: kReal })}</p>
		{:else}
			<p class="updated">{tr('v2.results.updated', currentLang, { when: ago($tallyStore.at) })}</p>
			<ol class="vote-list">
				{#each results.options as o (o.slot)}
					<li class="vote-row" class:vote-row--mine={shown.includes(o.slot)} class:vote-row--top={o.rank === 1} class:vote-row--blank={o.kind === 'blank'}>
						<span class="vote-rank">{o.rank}</span>
						<div class="vote-main">
							<span class="who">
								{#if imageOf(o.slot)}
									<img src={imageOf(o.slot)} alt="" class="photo" />
								{:else}
									<span class="photo photo--empty">{o.kind === 'blank' ? '◻' : nameOf(o.slot).charAt(0).toUpperCase()}</span>
								{/if}
								<span class="name">{nameOf(o.slot)}</span>
							</span>
							{#if winnerSlot === o.slot}
								<span class="winner">{tr('poll.condorcet', currentLang)}</span>
							{/if}
							<span class="meta">{tr('poll.first_ranked', currentLang, { f: pct(o.first, results.voters), r: pct(o.ranked, results.voters) })}</span>
						</div>
					</li>
				{/each}
			</ol>
		{/if}
	</section>

	<p class="how-link"><a href={paths.how}>{tr('poll.how.title', currentLang)}</a></p>
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
		grid-template-columns: repeat(auto-fill, minmax(6.5rem, 1fr));
		gap: 0.5rem;
		min-width: min(28rem, 80vw);
	}
	.pick-card {
		display: flex;
		flex-direction: column;
		align-items: center;
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
	.pick-card img,
	.pick-initial {
		width: 3.2rem;
		height: 3.2rem;
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
	.pick-card--blank {
		color: var(--text-secondary);
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
	.meta {
		font-size: 0.72rem;
		color: var(--text-muted);
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
		.vote-row {
			gap: 0.5rem;
			padding: 0.7rem 0.75rem;
		}
		.name {
			font-size: 0.82rem;
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
	.updated {
		font-size: 0.8rem;
		color: var(--text-muted);
		margin: 0 0 0.75rem;
	}
	.how-link {
		text-align: center;
		margin: 2rem 0;
		font-size: 0.9rem;
	}
</style>
