<script lang="ts">
	// A complaint (isoloir SPEC §11.3), checked in the reader's browser: the
	// ack the ingest signed, the checkpoint it names (signed by the log key
	// pinned in this app), and whether that checkpoint's entries hold the
	// body. Everything comes from the relays and the mirror; nothing from
	// our server directly. Every outcome is a named verdict; a failure to
	// read names itself and offers to read again.
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';
	import { Mirror, WebSocketTransport, IsoloirError, type LedgerSetup, type NostrEvent } from '@isoloir/web';
	import { core, mirrorSources, settingsProblem, neventLink, LOG_KEY } from '$lib/vote/isoloir';
	import { messageOf } from '$lib/vote/errors';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let paths = $derived(eventPaths($page.params.slug ?? ''));
	let id = $derived($page.url.searchParams.get('id') ?? '');

	type Verdict = { kind: 'loading' | 'noid' | 'nosetup' | 'error' | 'notfound' | 'badack' | 'nocp' | 'badcp' | 'present' | 'absent'; pi?: string; n?: number; seq?: number; at?: string; detail?: string };
	let verdict = $state<Verdict>({ kind: 'loading' });
	let relays = $state<string[]>([]);
	let complaintId = $state('');

	const FETCH_TIMEOUT = 20_000;

	// fetchEvent reads an event by id from the relays, the first that has it.
	function fetchEvent(urls: string[], eventId: string): Promise<NostrEvent | null> {
		const t = new WebSocketTransport();
		return new Promise((resolve) => {
			let left = urls.length;
			const stops: (() => void)[] = [];
			const finish = (e: NostrEvent | null) => {
				clearTimeout(timer);
				stops.splice(0).forEach((s) => s());
				resolve(e);
			};
			const timer = setTimeout(() => finish(null), FETCH_TIMEOUT);
			if (left === 0) finish(null);
			for (const url of urls) {
				stops.push(
					t.subscribe(url, { ids: [eventId] }, (e) => e.id === eventId && finish(e), {
						onEose: () => --left === 0 && finish(null),
						onDown: () => --left === 0 && finish(null)
					})
				);
			}
		});
	}

	async function check() {
		verdict = { kind: 'loading' };
		if (!/^[0-9a-f]{64}$/i.test(id)) {
			verdict = { kind: 'noid' };
			return;
		}
		try {
			if (settingsProblem($page.params.slug ?? '')) {
				verdict = { kind: 'nosetup' };
				return;
			}
			const c = await core();
			const mirror = new Mirror(c, mirrorSources(), LOG_KEY);
			const ledger = await mirror.setup<LedgerSetup>('setup/ledger.json', 'ledger');
			if (!ledger) {
				verdict = { kind: 'nosetup' };
				return;
			}
			relays = ledger.body.channels.filter((ch) => ch.type === 'nostr').map((ch) => ch.url);
			complaintId = id;
			const ev = await fetchEvent(relays, id);
			if (!ev || !c.verifyEvent(ev)) {
				verdict = { kind: 'notfound' };
				return;
			}
			// The complaint: {v, ack: <the ack event>, body: <the body, as JSON>}.
			let ackHash = '';
			let n = 0;
			try {
				const body = JSON.parse(ev.content) as { ack?: NostrEvent; body?: unknown };
				if (!body.ack || body.body === undefined) throw new Error('no ack or body');
				const ack = c.readAck(body.ack, ledger.body.ingest);
				if (ack.outcome !== 'accepted' || ack.checkpoint === undefined) throw new Error('not an acceptance naming a checkpoint');
				if (c.bodyHash(c.canonical(body.body)) !== ack.hash) throw new Error('the ack is of another body');
				ackHash = ack.hash;
				n = ack.checkpoint;
			} catch (e) {
				verdict = { kind: 'badack', detail: messageOf(e) };
				return;
			}
			let cp;
			try {
				cp = await mirror.checkpoint(n);
			} catch (e) {
				if (e instanceof IsoloirError && e.code === 'log_key') {
					verdict = { kind: 'badcp', pi: ackHash, n, detail: e.detail };
					return;
				}
				throw e;
			}
			if (!cp) {
				verdict = { kind: 'nocp', pi: ackHash, n };
				return;
			}
			const entries = await mirror.entries(n, cp.mirror ?? '');
			if (entries === null) {
				verdict = { kind: 'nocp', pi: ackHash, n };
				return;
			}
			const found = entries.find((e) => c.bodyHash(e.body) === ackHash);
			if (found) {
				verdict = { kind: 'present', pi: ackHash, n, seq: found.seq };
				return;
			}
			const at = new Date(cp.time * 1000);
			verdict = { kind: 'absent', pi: ackHash, n, at: at.toLocaleString(currentLang === 'fr' ? 'fr-FR' : 'en-GB', { timeZone: 'Europe/Paris' }) };
		} catch (e) {
			console.error('complaint', e);
			verdict = { kind: 'error', detail: messageOf(e) };
		}
	}

	onMount(check);

	const short = (s: string) => (s.length > 18 ? s.slice(0, 10) + '…' + s.slice(-6) : s);
</script>

<svelte:head>
	<title>{tr('v2.complaint.title', currentLang)}</title>
</svelte:head>

<article class="complaint">
	<h1>{tr('v2.complaint.title', currentLang)}</h1>
	<p class="lead">{tr('v2.complaint.lead', currentLang)}</p>
	{#if verdict.kind === 'loading'}
		<p class="muted">{tr('v2.complaint.loading', currentLang)}</p>
	{:else if verdict.kind === 'noid'}
		<p class="verdict verdict--none">{tr('v2.complaint.noid', currentLang)}</p>
	{:else if verdict.kind === 'nosetup'}
		<p class="verdict verdict--none">{tr('v2.complaint.nosetup', currentLang)}</p>
		<button class="btn" onclick={check}>{tr('home.retry', currentLang)}</button>
	{:else if verdict.kind === 'error'}
		<p class="verdict verdict--none" role="alert">{tr('v2.complaint.error', currentLang)}</p>
		{#if verdict.detail}
			<details class="tech"><summary>{tr('v2.details', currentLang)}</summary><p>{verdict.detail}</p></details>
		{/if}
		<button class="btn" onclick={check}>{tr('home.retry', currentLang)}</button>
	{:else if verdict.kind === 'notfound'}
		<p class="verdict verdict--none">{tr('v2.complaint.notfound', currentLang)}</p>
		<button class="btn" onclick={check}>{tr('home.retry', currentLang)}</button>
	{:else if verdict.kind === 'badack'}
		<p class="verdict verdict--none">{tr('v2.complaint.badack', currentLang)}</p>
		{#if verdict.detail}
			<details class="tech"><summary>{tr('v2.details', currentLang)}</summary><p>{verdict.detail}</p></details>
		{/if}
	{:else}
		<p>{tr('v2.complaint.ack', currentLang, { pi: short(verdict.pi ?? ''), n: verdict.n ?? 0 })}</p>
		{#if verdict.kind === 'nocp'}
			<p class="verdict verdict--none">{tr('v2.complaint.nocp', currentLang, { n: verdict.n ?? 0 })}</p>
			<button class="btn" onclick={check}>{tr('home.retry', currentLang)}</button>
		{:else if verdict.kind === 'badcp'}
			<p class="verdict verdict--bad">{tr('v2.complaint.badcp', currentLang, { n: verdict.n ?? 0 })}</p>
		{:else if verdict.kind === 'present'}
			<p class="verdict verdict--ok">{tr('v2.complaint.present', currentLang, { n: verdict.n ?? 0, seq: verdict.seq ?? 0 })}</p>
		{:else}
			<p class="verdict verdict--bad">{tr('v2.complaint.absent', currentLang, { n: verdict.n ?? 0, at: verdict.at ?? '' })}</p>
		{/if}
		<p class="muted">{tr('v2.complaint.verified', currentLang)}</p>
	{/if}
	{#if complaintId}
		<p class="links">
			<a href={neventLink(complaintId, relays)} rel="noopener">{tr('v2.complaint.post', currentLang)}</a> · <a href={paths.how}>{tr('event.menu.how', currentLang)}</a>
		</p>
	{/if}
</article>

<style>
	.complaint {
		max-width: 40rem;
		margin: 0 auto;
		padding: 1.5rem 1rem 3rem;
	}
	.tech {
		margin: 0 0 0.9rem;
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
	.lead {
		color: var(--muted, #9aa);
	}
	.verdict {
		padding: 0.9rem 1rem;
		border-radius: 0.6rem;
		border: 1px solid var(--border, #334);
		font-weight: 600;
	}
	.verdict--ok {
		border-color: #2e7d4f;
	}
	.verdict--bad {
		border-color: #c0392b;
	}
	.verdict--none {
		opacity: 0.8;
	}
	.links {
		margin-top: 2rem;
	}
</style>
