<script lang="ts">
	// The voter's page: their voting key first (no account needed), the copies
	// that keep it safe and the devices that share it, then what this device
	// remembers of their ballots; then their account, if they have one, for
	// the reports and the +1s.
	import Spinner from '$lib/components/Spinner.svelte';
	import { eventPaths } from '$lib/event';
	import { onMount, untrack } from 'svelte';
	import { lang, tr } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import { personPath } from '$lib/share';
	import { session, signInOpen, signOut, setDisplayName, authFetch } from '$lib/auth';
	import { voteKey, forgetKey as forgetVoteKey, pollOf } from '$lib/vote/isoloir';
	import Modal from '$lib/components/Modal.svelte';
	import { getMyReports } from '$lib/reports';
	import type { PollEvent, UserReport } from '$lib/types';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let events: PollEvent[] | null = $state(null);
	let eventsError = $state(false);
	let reports: UserReport[] | null = $state(null);
	let reportsError = $state(false);
	let deleteOpen = $state(false);
	let deleting = $state(false);
	let deleteError: string | null = $state(null);
	let deleted = $state(false);
	let forgetOpen = $state(false);
	let seq = 0;

	let user = $derived($session?.user ?? null);
	let key = $derived($voteKey);

	// The reports load again for each session (sign-in, sign-out, another account).
	let loadedFor = '';
	$effect(() => {
		const token = $session?.token ?? '';
		if (token === loadedFor) return;
		loadedFor = token;
		untrack(() => {
			if (token) {
				deleted = false;
				loadReports();
			} else {
				seq++;
				reports = null;
			}
		});
	});

	async function loadEvents() {
		try {
			const res = await fetch(`${API_BASE}/api/polls`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			events = await res.json();
			eventsError = false;
		} catch (err) {
			console.error('failed to load polls', err);
			eventsError = true;
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

	async function confirmDelete() {
		deleting = true;
		deleteError = null;
		try {
			const res = await authFetch('/api/auth/me', { method: 'DELETE' });
			if (!res.ok && res.status !== 401) throw new Error(`HTTP ${res.status}`);
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

	function forgetKey() {
		forgetOpen = false;
		forgetVoteKey();
	}

	function dateText(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}

	function isOpen(e: PollEvent): boolean {
		const now = Date.now();
		return new Date(e.opens_at).getTime() <= now && (!e.closes_at || now < new Date(e.closes_at).getTime());
	}

	function ballotText(e: PollEvent): string {
		// The last ranking this device cast in the event's poll.
		const id = pollOf(e.slug);
		const slots = (id !== null ? key?.choices[String(id)] : null) ?? [];
		if (slots.length === 0) return tr('profile.ballot.none', currentLang);
		return tr(slots.length === 1 ? 'profile.ballot.one' : 'profile.ballot.many', currentLang, { n: slots.length });
	}

	// The pseudonym, shown to the user alone; editing it in place.
	let editingName = $state(false);
	let newName = $state('');
	let nameError: string | null = $state(null);
	async function saveName(event: SubmitEvent) {
		event.preventDefault();
		nameError = null;
		try {
			await setDisplayName(newName.trim());
			editingName = false;
		} catch (err) {
			nameError = err instanceof Error ? err.message : String(err);
		}
	}

	function providerText(p: string): string {
		if (p === 'google') return tr('profile.google', currentLang);
		if (p === 'noop') return tr('profile.test', currentLang);
		return tr('profile.provider_other', currentLang, { p });
	}

	onMount(loadEvents);
</script>

<svelte:head>
	<title>{tr('profile.title', currentLang)} — tartuffe.me</title>
	<meta name="robots" content="noindex" />
</svelte:head>

<div class="profile">
	<h1>{tr('profile.title', currentLang)}</h1>

	<section class="card key" aria-labelledby="key-title">
		<h2 id="key-title">{tr('profile.key.title', currentLang)}</h2>
		{#if !key || !key.registered}
			<p class="muted">{tr('profile.key.none', currentLang)}</p>
			{#if events?.find((e) => e.featured)}
				<a class="btn btn-primary" href={eventPaths(events.find((e) => e.featured)!.slug).vote}>{tr('v2.key.get', currentLang)}</a>
			{/if}
		{:else}
			<p class="ok">{tr('profile.key.ready', currentLang)}</p>
			<p class="muted">{tr('profile.key.story', currentLang)}</p>
			<p class="muted">{tr(key.source === 'passkey' ? 'profile.key.passkey' : key.backups.words ? 'profile.key.words' : key.backups.linked > 0 || key.source === 'link' ? 'profile.key.linked' : 'profile.key.unprotected', currentLang)}</p>
			<details class="lost">
				<summary>{tr('profile.key.lost.title', currentLang)}</summary>
				<p class="muted">{tr('profile.key.lost.text', currentLang)}</p>
			</details>
			<button class="link danger-link" onclick={() => (forgetOpen = true)}>{tr('profile.key.forget', currentLang)}</button>
		{/if}
	</section>

	<h2 class="section-title">{tr('profile.votes', currentLang)}</h2>
	<p class="muted here">{tr('profile.votes.here', currentLang)}</p>
	{#if eventsError}
		<div class="card">
			<p class="error">{tr('profile.error', currentLang)}</p>
			<button class="btn" onclick={loadEvents}>{tr('home.retry', currentLang)}</button>
		</div>
	{:else if !events}
		<p class="muted loading"><Spinner label={tr('person.loading', currentLang)} /></p>
	{:else if events.length === 0}
		<p class="muted">{tr('profile.no_events', currentLang)}</p>
	{:else}
		{#each events as ev (ev.id)}
			<section class="card event" aria-labelledby="event-{ev.id}">
				<div class="event-head">
					<h3 id="event-{ev.id}">
						<a href={eventPaths(ev.slug).intro}>{currentLang === 'en' ? ev.title_en : ev.title_fr} →</a>
					</h3>
					<span class="badge" class:badge--open={isOpen(ev)}>
						{#if !isOpen(ev)}
							{tr('profile.closed', currentLang)}
						{:else if ev.closes_at}
							{tr('profile.open_until', currentLang, { d: dateText(ev.closes_at) })}
						{:else}
							{tr('profile.open', currentLang)}
						{/if}
					</span>
				</div>
				<p class="muted">{key && key.registered ? ballotText(ev) : tr('profile.ballot.no_key', currentLang)}</p>
				<a href={eventPaths(ev.slug).vote} class="btn" class:btn-primary={isOpen(ev)}>
					{isOpen(ev) ? tr('profile.go_vote', currentLang) : tr('profile.go_results', currentLang)}
				</a>
			</section>
		{/each}
	{/if}

	<h2 class="section-title">{tr('profile.account', currentLang)}</h2>
	{#if !user}
		<section class="card">
			{#if deleted}
				<p class="ok" role="status">{tr('profile.deleted', currentLang)}</p>
			{/if}
			<p class="muted">{tr('profile.sign_in_text', currentLang)}</p>
			<button class="btn" onclick={() => signInOpen.set(true)}>{tr('auth.title', currentLang)}</button>
		</section>
	{:else}
		<section class="card" aria-label={tr('profile.account', currentLang)}>
			{#if editingName}
				<form class="name-form" onsubmit={saveName}>
					<input type="text" bind:value={newName} maxlength="40" required placeholder={tr('auth.name.placeholder', currentLang)} autocomplete="nickname" />
					<button type="submit" class="btn btn-primary" disabled={newName.trim() === ''}>{tr('auth.name.submit', currentLang)}</button>
					<button type="button" class="btn" onclick={() => (editingName = false)}>{tr('vote.cancel', currentLang)}</button>
				</form>
				{#if nameError}<p class="error">{nameError}</p>{/if}
			{:else}
				<p class="account-name">
					{user.display_name || tr('auth.anonymous', currentLang)}
					<button class="link" onclick={() => { newName = user.display_name; editingName = true; }}>{tr('profile.rename', currentLang)}</button>
				</p>
			{/if}
			<p class="muted small">{tr('auth.name.hint', currentLang)}</p>
			<p class="muted" class:warn={user.provider === 'noop'}>{providerText(user.provider)}</p>
			<p class="muted small">{tr('profile.account.apart', currentLang)}</p>
			<button class="btn" onclick={signOut}>{tr('auth.sign_out', currentLang)}</button>
		</section>

		<h2 class="section-title">{tr('profile.reports', currentLang)}</h2>
		{#if reportsError}
			<div class="card">
				<p class="error">{tr('profile.error', currentLang)}</p>
				<button class="btn" onclick={loadReports}>{tr('home.retry', currentLang)}</button>
			</div>
		{:else if !reports}
			<p class="muted loading"><Spinner label={tr('person.loading', currentLang)} /></p>
		{:else if reports.length === 0}
			<p class="muted">{tr('profile.reports.none', currentLang)}</p>
		{:else}
			<ul class="reports">
				{#each reports as r (r.id)}
					<li class="card report">
						<div class="report-head">
							<span class="status status--{r.status}">{tr(`report.status.${r.status}`, currentLang)}</span>
							<a href={personPath(r.person_slug ?? r.person_id)}>{r.person_name}</a>
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

<Modal open={forgetOpen} title={tr('profile.key.forget.title', currentLang)} onclose={() => (forgetOpen = false)} closeLabel={tr('auth.close', currentLang)}>
	<p class="muted">{tr(key?.source === 'passkey' || key?.backups.words ? 'profile.key.forget.text' : 'profile.key.forget.text_unprotected', currentLang)}</p>
	{#snippet actions()}
		<button class="btn" onclick={() => (forgetOpen = false)}>{tr('vote.cancel', currentLang)}</button>
		<button class="btn btn-danger" onclick={forgetKey}>{tr('profile.key.forget', currentLang)}</button>
	{/snippet}
</Modal>

<style>
	.profile {
		max-width: 46rem;
		margin: 1rem auto 3rem;
		text-align: center;
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
	.here {
		margin: -0.4rem 0 0.75rem;
		font-size: 0.85rem;
	}
	h3 {
		font-size: 1.05rem;
		margin: 0;
	}
	/* The event's title is a link to its page: it reads as one. */
	h3 a {
		color: var(--accent);
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
		justify-content: center;
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
	.link {
		margin-left: 0.5rem;
		padding: 0;
		border: none;
		background: none;
		color: var(--accent-hover);
		font: inherit;
		font-size: 0.85rem;
		font-weight: 400;
		cursor: pointer;
	}
	.name-form {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: 0.5rem;
		margin-bottom: 0.4rem;
	}
	.name-form input {
		flex: 1 1 12rem;
		min-width: 0;
		padding: 0.5rem 0.7rem;
		background: var(--bg-secondary);
		color: var(--text-primary);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-md);
		font: inherit;
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
		font-size: 0.78rem;
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
	.status--review,
	.status--pending {
		color: var(--amber);
		border-color: var(--amber);
	}
	.key h2 {
		margin-bottom: 0.75rem;
	}
	.notice {
		padding: 0.75rem;
		border-left: 3px solid var(--accent);
		background: var(--bg-secondary);
		margin: 0 0 0.75rem;
		line-height: 1.5;
		font-size: 0.9rem;
	}
	.alarm {
		padding: 0.7rem 0.85rem;
		margin: 0 0 0.75rem;
		border: 1px solid var(--amber);
		border-radius: var(--radius-md);
		color: var(--text-primary);
		background: color-mix(in oklch, var(--amber) 12%, var(--bg-card));
		font-size: 0.9rem;
		line-height: 1.5;
	}
	.lost {
		margin-top: 1rem;
		font-size: 0.9rem;
	}
	.lost summary {
		cursor: pointer;
		color: var(--text-secondary);
	}
	.lost p {
		margin-top: 0.5rem;
	}
	.danger-link {
		display: block;
		margin: 1rem 0 0;
		color: var(--red);
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
	/* A finger needs the text links and the summary 40px tall. */
	@media (pointer: coarse) {
		.link,
		.report-head a,
		h3 a {
			display: inline-flex;
			align-items: center;
			min-height: 2.5rem;
			padding: 0 0.4rem;
		}
		.danger-link {
			margin: 0.5rem 0 0;
		}
		.lost summary {
			padding: 0.6rem 0;
		}
	}
</style>
