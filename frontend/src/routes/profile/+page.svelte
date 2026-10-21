<script lang="ts">
	// The signed-in user's page: their account, and their votes per event,
	// which they can reorder or take back while the event is open.
	import { onMount, onDestroy, untrack } from 'svelte';
	import { lang, tr } from '$lib/i18n';
	import { session, signInOpen, signOut } from '$lib/auth';
	import { getMyVotes, removeVote, saveBallot, deleteAccount, onBallotChanged, BallotError } from '$lib/ballot';
	import Modal from '$lib/components/Modal.svelte';
	import VoteList from '$lib/components/VoteList.svelte';
	import { getMyReports } from '$lib/reports';
	import type { MyVotes, MyEventVotes, UserReport } from '$lib/types';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let mine: MyVotes | null = $state(null);
	let reports: UserReport[] | null = $state(null);
	let reportsError = $state(false);
	let loadError: string | null = $state(null);
	// The event whose votes are being changed (one change at a time).
	let busySlug: string | null = $state(null);
	let actionError: { slug: string; msg: string } | null = $state(null);
	let deleteOpen = $state(false);
	let deleting = $state(false);
	let deleteError: string | null = $state(null);
	let deleted = $state(false);
	let seq = 0;

	let user = $derived($session?.user ?? null);

	// The votes load again for each session (sign-in, sign-out, another account).
	let loadedFor = '';
	$effect(() => {
		const token = $session?.token ?? '';
		if (token === loadedFor) return;
		loadedFor = token;
		untrack(() => {
			if (token) {
				deleted = false;
				load();
				loadReports();
			} else {
				seq++;
				mine = null;
				reports = null;
			}
		});
	});

	async function load() {
		const n = ++seq;
		try {
			const m = await getMyVotes();
			if (n !== seq) return;
			mine = m;
			loadError = null;
		} catch (err) {
			if (n !== seq) return;
			console.error('failed to load my votes', err);
			if (!(err instanceof BallotError && err.code === 'signed_out')) loadError = tr('profile.error', currentLang);
		}
	}

	async function loadReports() {
		const n = seq;
		try {
			const r = await getMyReports();
			if (n !== seq) return;
			reports = r.reports;
			reportsError = false;
		} catch (err) {
			if (n !== seq) return;
			console.error('failed to load my reports', err);
			reportsError = true;
		}
	}

	async function change(ev: MyEventVotes, request: () => Promise<unknown>) {
		if (busySlug) return;
		busySlug = ev.event.slug;
		actionError = null;
		try {
			await request();
		} catch (err) {
			let msg = err instanceof Error ? err.message : String(err);
			if (err instanceof BallotError) {
				if (err.code === 'closed') msg = tr('vote.error.closed', currentLang);
				else if (err.code === 'signed_out') msg = tr('vote.error.signed_out', currentLang);
			}
			actionError = { slug: ev.event.slug, msg };
		} finally {
			busySlug = null;
			await load();
		}
	}

	function remove(ev: MyEventVotes, personId: number) {
		change(ev, () => removeVote(ev.event.slug, personId));
	}

	function move(ev: MyEventVotes, i: number, delta: number) {
		const ids = ev.votes.map((v) => v.person_id);
		const j = i + delta;
		if (j < 0 || j >= ids.length) return;
		[ids[i], ids[j]] = [ids[j], ids[i]];
		change(ev, () => saveBallot(ev.event.slug, ids));
	}

	async function confirmDelete() {
		deleting = true;
		deleteError = null;
		try {
			await deleteAccount();
			deleteOpen = false;
			deleted = true;
			// The session went with the account: only the local copy is left.
			session.set(null);
		} catch (err) {
			deleteError = err instanceof Error ? err.message : String(err);
		} finally {
			deleting = false;
		}
	}

	function dateText(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}

	function providerText(p: string): string {
		if (p === 'zkpassport') return tr('profile.verified', currentLang);
		if (p === 'noop') return tr('profile.test', currentLang);
		return tr('profile.provider_other', currentLang, { p });
	}

	let unwatch: (() => void) | null = null;
	onMount(() => {
		unwatch = onBallotChanged(() => {
			if ($session && !busySlug) load();
		});
	});
	onDestroy(() => unwatch?.());
</script>

<svelte:head>
	<title>{tr('profile.title', currentLang)} — tartuffe.me</title>
	<meta name="robots" content="noindex" />
</svelte:head>

<div class="profile">
	<h1>{tr('profile.title', currentLang)}</h1>

	{#if !user}
		<section class="card">
			{#if deleted}
				<p class="ok" role="status">{tr('profile.deleted', currentLang)}</p>
			{/if}
			<p class="muted">{tr('profile.sign_in_text', currentLang)}</p>
			<button class="btn btn-primary" onclick={() => signInOpen.set(true)}>{tr('auth.title', currentLang)}</button>
		</section>
	{:else}
		<section class="card" aria-labelledby="account-title">
			<h2 id="account-title">{tr('profile.account', currentLang)}</h2>
			<p class="account-name">{user.display_name || tr('auth.anonymous', currentLang)}</p>
			<p class="muted" class:warn={user.provider === 'noop'}>{providerText(user.provider)}</p>
			{#if mine?.user.consent_at}
				<p class="muted small">{tr('profile.consent_given', currentLang, { d: dateText(mine.user.consent_at) })}</p>
			{/if}
			<button class="btn" onclick={signOut}>{tr('auth.sign_out', currentLang)}</button>
		</section>

		<h2 class="section-title">{tr('profile.votes', currentLang)}</h2>
		{#if loadError}
			<div class="card">
				<p class="error">{loadError}</p>
				<button class="btn" onclick={load}>{tr('home.retry', currentLang)}</button>
			</div>
		{:else if !mine}
			<p class="muted">{tr('person.loading', currentLang)}</p>
		{:else if mine.events.length === 0}
			<p class="muted">{tr('profile.no_events', currentLang)}</p>
		{:else}
			{#each mine.events as ev (ev.event.id)}
				<section class="card event" aria-labelledby="event-{ev.event.id}">
					<div class="event-head">
						<h3 id="event-{ev.event.id}">
							<a href="/election/{ev.event.slug}">{currentLang === 'en' ? ev.event.title_en : ev.event.title_fr}</a>
						</h3>
						<span class="badge" class:badge--open={ev.open}>
							{#if !ev.open}
								{tr('profile.closed', currentLang)}
							{:else if ev.event.closes_at}
								{tr('profile.open_until', currentLang, { d: dateText(ev.event.closes_at) })}
							{:else}
								{tr('profile.open', currentLang)}
							{/if}
						</span>
					</div>
					<VoteList
						votes={ev.votes}
						max={ev.event.max_choices}
						lang={currentLang}
						editable={ev.open}
						busy={busySlug !== null}
						onmove={(i, d) => move(ev, i, d)}
						onremove={(id) => remove(ev, id)}
					/>
					{#if ev.votes.length === 0}
						<p class="muted">{tr('profile.empty', currentLang, { n: ev.event.max_choices })}</p>
					{/if}
					{#if actionError?.slug === ev.event.slug}
						<p class="error" role="alert">{actionError.msg}</p>
					{/if}
					<a href="/election/{ev.event.slug}" class="btn" class:btn-primary={ev.open && ev.votes.length < ev.event.max_choices}>
						{ev.open ? tr('profile.go_vote', currentLang) : tr('profile.go_results', currentLang)}
					</a>
				</section>
			{/each}
		{/if}

		<h2 class="section-title">{tr('profile.reports', currentLang)}</h2>
		{#if reportsError}
			<div class="card">
				<p class="error">{tr('profile.error', currentLang)}</p>
				<button class="btn" onclick={loadReports}>{tr('home.retry', currentLang)}</button>
			</div>
		{:else if !reports}
			<p class="muted">{tr('person.loading', currentLang)}</p>
		{:else if reports.length === 0}
			<p class="muted">{tr('profile.reports.none', currentLang)}</p>
		{:else}
			<ul class="reports">
				{#each reports as r (r.id)}
					<li class="card report">
						<div class="report-head">
							<span class="status status--{r.status}">{tr(`report.status.${r.status}`, currentLang)}</span>
							<a href="/person/{r.person_id}">{r.person_name}</a>
							<span class="small">{dateText(r.created_at)}</span>
						</div>
						<blockquote>{r.target_text}</blockquote>
						<p class="muted small">« {r.reason} »</p>
						{#if r.verdict_reason && r.status !== 'pending'}
							<p class="muted small"><strong>{tr('profile.reports.judge', currentLang)}</strong> {r.verdict_reason}</p>
						{/if}
					</li>
				{/each}
			</ul>
		{/if}

		<section class="card danger" aria-labelledby="danger-title">
			<h2 id="danger-title">{tr('profile.danger', currentLang)}</h2>
			<p class="muted">{tr('profile.danger_text', currentLang)}</p>
			<button class="btn btn-danger" onclick={() => (deleteOpen = true)}>{tr('profile.delete', currentLang)}</button>
		</section>
	{/if}
</div>

<Modal open={deleteOpen} title={tr('profile.delete.title', currentLang)} onclose={() => (deleteOpen = false)} closeLabel={tr('auth.close', currentLang)}>
	<p class="muted">{tr('profile.delete.text', currentLang)}</p>
	{#if deleteError}
		<p class="error" role="alert">{deleteError}</p>
	{/if}
	{#snippet actions()}
		<button class="btn" onclick={() => (deleteOpen = false)} disabled={deleting}>{tr('vote.cancel', currentLang)}</button>
		<button class="btn btn-danger" onclick={confirmDelete} disabled={deleting}>{tr('profile.delete', currentLang)}</button>
	{/snippet}
</Modal>

<style>
	.profile {
		max-width: 46rem;
		margin: 1rem auto 3rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2rem, 5vw, 2.75rem);
		font-weight: 600;
		margin: 0 0 1.25rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.4rem;
		font-weight: 600;
		margin: 0 0 0.5rem;
	}
	.section-title {
		margin: 2rem 0 0.75rem;
	}
	h3 {
		font-size: 1.05rem;
		margin: 0;
	}
	h3 a {
		color: inherit;
		text-decoration: none;
	}
	h3 a:hover {
		color: var(--accent-hover);
	}
	.card {
		padding: 1.25rem 1.5rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
	}
	.card + .card {
		margin-top: 1rem;
	}
	.event-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
		margin-bottom: 0.75rem;
	}
	.badge {
		font-size: 0.75rem;
		padding: 0.2rem 0.6rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--glass-border);
		color: var(--text-muted);
	}
	.badge--open {
		border-color: var(--green);
		color: var(--green);
	}
	.account-name {
		font-weight: 600;
		margin: 0 0 0.25rem;
	}
	.muted {
		color: var(--text-secondary);
		font-size: 0.9rem;
		line-height: 1.5;
		margin: 0 0 0.75rem;
	}
	.small {
		font-size: 0.8rem;
		color: var(--text-muted);
	}
	.warn {
		color: var(--amber);
	}
	.ok {
		color: var(--green);
		margin: 0 0 0.75rem;
	}
	.error {
		color: var(--red);
		font-size: 0.85rem;
		margin: 0 0 0.75rem;
	}
	.reports {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.report-head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 0.35rem 0.75rem;
		margin-bottom: 0.5rem;
		font-size: 0.85rem;
	}
	.report-head a {
		color: var(--text-primary);
		text-transform: capitalize;
	}
	.report blockquote {
		margin: 0 0 0.5rem;
		padding: 0.5rem 0.75rem;
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
		font-size: 0.88rem;
		line-height: 1.5;
	}
	.report .muted {
		margin-bottom: 0.35rem;
	}
	.status {
		font-size: 0.72rem;
		font-weight: 600;
		padding: 0.15rem 0.55rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--glass-border);
		color: var(--text-secondary);
	}
	.status--accepted {
		color: var(--green);
		border-color: var(--green);
	}
	.status--rejected {
		color: var(--text-muted);
	}
	.status--ambiguous,
	.status--pending {
		color: var(--amber);
		border-color: var(--amber);
	}
	.danger {
		margin-top: 2rem;
		border-color: var(--error-border);
	}
	a.btn {
		display: inline-flex;
		align-items: center;
		text-decoration: none;
	}
	@media (max-width: 640px) {
		.card {
			padding: 1rem;
		}
	}
</style>
