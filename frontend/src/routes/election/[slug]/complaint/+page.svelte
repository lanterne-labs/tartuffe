<script lang="ts">
	// A complaint (SPEC §7.2), checked in the reader's browser: the ack our
	// server signed, the checkpoint it names (signed by the log key), and
	// whether that checkpoint's entries hold the ballot. Everything comes
	// from the relays and the mirror; nothing from our server directly.
	// Every outcome is a named verdict; a failure to read names itself and
	// offers to read again.
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';
	import { NostrMailbox, verifyEvent, neventLink, type NostrEvent } from '$lib/vote/mailbox';
	import { defaultSources } from '$lib/vote/sources';
	import { parseCheckpoint, type Setup, type Ack, type MirrorEntry } from '$lib/vote/protocol';
	import { openCheckpoint } from '$lib/vote/checkpoint';
	import { messageOf } from '$lib/vote/errors';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));
	let id = $derived($page.url.searchParams.get('id') ?? '');

	type Verdict = { kind: 'loading' | 'noid' | 'nosetup' | 'error' | 'notfound' | 'badack' | 'nocp' | 'badcp' | 'present' | 'absent'; pi?: string; n?: number; seq?: number; at?: string; detail?: string };
	let verdict = $state<Verdict>({ kind: 'loading' });
	let relays = $state<string[]>([]);
	let complaintId = $state('');

	async function check() {
		verdict = { kind: 'loading' };
		if (!/^[0-9a-f]{64}$/i.test(id)) {
			verdict = { kind: 'noid' };
			return;
		}
		try {
			const src = defaultSources();
			const setup = await src.json<Setup>('setup.json');
			if (!setup) {
				verdict = { kind: 'nosetup' };
				return;
			}
			relays = setup.setup.relays.map((r) => r.url);
			complaintId = id;
			const mail = new NostrMailbox(relays, `tartuffe-vote/2/${slug}`, setup.setup.nostr_pubkey);
			const ev = await mail.fetchEvent(id, 20_000);
			if (!ev) {
				verdict = { kind: 'notfound' };
				return;
			}
			let body: { ack?: NostrEvent; pi_hash?: string; nostr_id?: string };
			try {
				body = JSON.parse(ev.content);
			} catch {
				verdict = { kind: 'badack' };
				return;
			}
			const ackEv = body.ack;
			if (!ackEv || !verifyEvent(ackEv) || ackEv.pubkey !== setup.setup.nostr_pubkey) {
				verdict = { kind: 'badack' };
				return;
			}
			let ack: Ack;
			try {
				ack = JSON.parse(ackEv.content);
			} catch {
				verdict = { kind: 'badack' };
				return;
			}
			const pi = ack.pi_hash ?? body.pi_hash ?? '';
			const n = ack.checkpoint;
			const note = await src.text(`checkpoints/${n}.txt`);
			if (!note) {
				verdict = { kind: 'nocp', pi, n };
				return;
			}
			const key = (import.meta.env.VITE_VOTE_LOG_KEY as string | undefined) || (await src.text('log-key.txt'))?.trim() || '';
			if (key && !openCheckpoint(note, key)) {
				verdict = { kind: 'badcp', pi, n };
				return;
			}
			const info = parseCheckpoint(note);
			if (!info) {
				verdict = { kind: 'badcp', pi, n };
				return;
			}
			// The entries of that checkpoint: in the files of its hour and the
			// hours around it, read at the checkpoint's own mirror commit.
			const at = new Date(info.time * 1000);
			for (const dt of [0, -3_600_000, 3_600_000]) {
				const hour = new Date(at.getTime() + dt).toISOString().slice(0, 13);
				const list = await src.json<MirrorEntry[]>(`entries/${hour}.json`, info.mirror);
				const found = list?.find((e) => e.hash === pi && e.checkpoint === n);
				if (found) {
					verdict = { kind: 'present', pi, n, seq: found.log_seq };
					return;
				}
			}
			verdict = { kind: 'absent', pi, n, at: at.toLocaleString(currentLang === 'fr' ? 'fr-FR' : 'en-GB', { timeZone: 'Europe/Paris' }) };
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
