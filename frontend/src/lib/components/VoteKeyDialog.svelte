<script lang="ts">
	// Getting a voting key (SPEC §4): the secret (a passkey where the
	// platform offers PRF, else 12 words confirmed before anything else),
	// then the passport scan, then the registration posted to the mailbox
	// and acknowledged. Also the ways back in: 12 words, Face ID or a
	// passkey, a link from another device. Every wait names where it
	// stands and every failure names the next step; the dialog can be
	// closed and reopened at any point, it resumes from the session's
	// state.
	import Modal from './Modal.svelte';
	import Steps, { type StepState } from './Steps.svelte';
	import Spinner from './Spinner.svelte';
	import VotePassport from './VotePassport.svelte';
	import { tr, type Lang } from '$lib/i18n';
	import type { VoteSession, LinkReceiver, RegistrationStatus } from '$lib/vote/session';
	import { prfSupported, prfCapability, voteKey } from '$lib/vote/key';
	import { describeError, toVoteError } from '$lib/vote/errors';
	import { serverNow } from '$lib/vote/mailbox';
	import { writable } from 'svelte/store';
	import { untrack } from 'svelte';

	interface Props {
		open: boolean;
		lang: Lang;
		session: VoteSession;
		domain: string;
		devMode?: boolean;
		simulate?: boolean;
		onclose: () => void;
	}
	let { open, lang, session, domain, devMode = false, simulate = false, onclose }: Props = $props();

	// The registration's state, followed from the session's store.
	let registration = $state<RegistrationStatus>({ state: 'idle' });
	$effect(() => session.registration.subscribe((v) => (registration = v)));

	type Step = 'choose' | 'words' | 'confirm' | 'passport' | 'posting' | 'failed' | 'pending' | 'done' | 'restore' | 'link' | 'linked' | 'restored';
	let step: Step = $state('choose');
	let words = $state('');
	let confirmIdx = $state<[number, number, number]>([2, 6, 10]);
	let confirmInput = $state(['', '', '']);
	// What went wrong: the voter's sentence (HTML, the "report it" link)
	// and the raw detail, shown apart.
	let error = $state('');
	let errorDetail = $state('');
	let busy = $state(false);
	let restoreWords = $state('');
	let linkUrl = $state('');
	let linkQr = $state('');
	let linkCopied = $state(false);
	let receiver: LinkReceiver | null = null;
	let offer = writable<string | null>(null);
	let key = $derived($voteKey && $voteKey.slug === session.slug ? $voteKey : null);
	let request = $derived(key ? session.registrationRequest(key) : null);
	let canPasskey = $state(prfSupported());
	$effect(() => {
		void prfCapability().then((c) => {
			if (c === false) canPasskey = false;
		});
	});

	// On opening, the step follows the key: none, one waiting for its
	// scan, its acknowledgement, its checkpoint, or one that failed.
	$effect(() => {
		if (open) untrack(arrive);
		else untrack(stopReceiving);
	});

	// While it waits, the dialog follows what the session learns from the
	// polls: a registration that failed after its acknowledgement (the
	// checkpoint came without the leaf), or the position found.
	$effect(() => {
		const r = registration;
		const k = key;
		untrack(() => {
			if (!open) return;
			if (r.state === 'failed' && (step === 'pending' || step === 'posting')) step = 'failed';
			else if (k && k.position >= 0 && step === 'pending') step = 'done';
		});
	});

	function arrive() {
		error = '';
		const k = key;
		if (!k) {
			step = 'choose';
			return;
		}
		if (k.position >= 0) {
			step = 'done';
			return;
		}
		const r = registration;
		if (r.state === 'posting') step = 'posting';
		else if (r.state === 'failed') step = 'failed';
		else if (r.state === 'acked' || k.registration?.checkpoint !== undefined) step = 'pending';
		else if (!k.prf && !k.backups.words && k.words) {
			words = k.words;
			confirmIdx = pickPositions();
			confirmInput = ['', '', ''];
			step = 'words';
		} else step = 'passport';
	}

	function pickPositions(): [number, number, number] {
		const s = new Set<number>();
		while (s.size < 3) s.add(Math.floor(Math.random() * 12));
		return [...s].sort((a, b) => a - b) as [number, number, number];
	}

	function fail(e: unknown) {
		const ve = toVoteError(e);
		error = describeError(ve.code, ve.detail, lang);
		errorDetail = ve.detail;
	}

	// make creates the key: with 12 words, they are shown and confirmed
	// first; with Face ID or the fingerprint, the passport comes at once.
	async function make(prefer: 'passkey' | 'words') {
		busy = true;
		error = '';
		errorDetail = '';
		try {
			const got = await session.newKey(prefer);
			if (got.words) {
				words = got.words;
				confirmIdx = pickPositions();
				confirmInput = ['', '', ''];
				step = 'words';
			} else {
				step = 'passport';
			}
		} catch (e) {
			fail(e);
		} finally {
			busy = false;
		}
	}

	function checkWords() {
		const list = words.split(' ');
		const ok = confirmIdx.every((i, k) => confirmInput[k].trim().toLowerCase() === list[i]);
		if (!ok) {
			error = tr('v2.key.words.bad', lang);
			return;
		}
		error = '';
		session.confirmWords();
		step = 'passport';
	}

	// onProof posts the registration; the session's registration store
	// carries the progress and the outcome, this dialog shows them.
	async function onProof({ proofs, result }: { proofs: unknown; result: unknown; at: Date }) {
		const k = key;
		if (!k) return;
		step = 'posting';
		error = '';
		const uid = String((result as { uniqueIdentifier?: string })?.uniqueIdentifier ?? '');
		try {
			// The proof's date on the server's clock (SPEC §4 step 4).
			await session.register(k, uid, { proofs, result }, new Date(serverNow()));
			step = 'done';
		} catch (e) {
			fail(e);
			step = 'failed';
		}
	}

	async function resend() {
		step = 'posting';
		error = '';
		try {
			await session.resendRegistration();
			step = 'done';
		} catch (e) {
			fail(e);
			step = toVoteError(e).code === 'stale_proof' ? 'passport' : 'failed';
		}
	}

	function rescan() {
		error = '';
		step = 'passport';
	}

	async function restore() {
		busy = true;
		error = '';
		try {
			await session.restoreFromWords(restoreWords);
			step = 'restored';
		} catch (e) {
			const ve = toVoteError(e);
			error = ve.code === 'not_registered' ? tr('v2.key.restore.notfound', lang) : describeError(ve.code, ve.detail, lang);
		} finally {
			busy = false;
		}
	}

	async function restorePasskey() {
		busy = true;
		error = '';
		try {
			await session.restoreFromPasskey();
			step = 'restored';
		} catch (e) {
			const ve = toVoteError(e);
			error = ve.code === 'not_registered' ? tr('v2.key.restore.passkey.notfound', lang) : describeError(ve.code, ve.detail, lang);
		} finally {
			busy = false;
		}
	}

	// receiveLink shows the code of this device and waits for the key; a
	// key that arrives is adopted only once the voter confirms its code.
	async function receiveLink() {
		error = '';
		try {
			const got = session.receiveLink(window.location.origin + window.location.pathname);
			receiver = got;
			offer = got.offer;
			linkUrl = got.url;
			linkCopied = false;
			step = 'link';
			const QRCode = await import('qrcode');
			linkQr = await QRCode.toDataURL(got.url, { margin: 1, width: 240 });
			await got.done;
			receiver = null;
			step = 'linked';
		} catch (e) {
			receiver = null;
			const ve = toVoteError(e);
			if (ve.code === 'stopped') return;
			error = ve.code === 'not_registered' ? tr('v2.key.restore.notfound', lang) : describeError(ve.code, ve.detail, lang);
			if (step === 'link') step = 'choose';
		}
	}

	function stopReceiving() {
		receiver?.stop();
		receiver = null;
	}

	async function copyLink() {
		try {
			await navigator.clipboard.writeText(linkUrl);
			linkCopied = true;
		} catch {}
	}

	function back() {
		stopReceiving();
		error = '';
		errorDetail = '';
		step = 'choose';
	}

	function close() {
		stopReceiving();
		onclose();
	}

	const posting = $derived.by((): { label: string; state: StepState }[] => {
		const s: Step = step;
		return [
			{ label: tr('zk.flow.check', lang), state: 'ok' },
			{ label: tr('v2.key.posting', lang), state: s === 'posting' ? 'running' : s === 'done' || s === 'pending' ? 'ok' : s === 'failed' ? 'failed' : 'todo' }
		];
	});

	let progressText = $derived.by(() => {
		const p = registration.progress;
		if (!p) return '';
		return tr('v2.key.posting.box', lang, { n: p.n, of: p.of, box: p.box.replace(/^wss?:\/\//, ''), stage: tr(p.stage === 'posting' ? 'v2.key.posting.send' : 'v2.key.posting.wait', lang) });
	});

	let failedText = $derived.by(() => {
		const r = registration;
		if (r.state !== 'failed') return error;
		return error || describeError(r.code ?? 'unknown', r.detail ?? '', lang);
	});
	let failedCode = $derived(registration.state === 'failed' ? (registration.code ?? '') : '');
	let failedStale = $derived(registration.state === 'failed' && !!registration.stale);
	let failedDetail = $derived(registration.state === 'failed' ? (registration.detail ?? '') : errorDetail);
</script>

<Modal {open} title={tr('v2.key.title', lang)} onclose={close} closeLabel={tr('auth.close', lang)}>
	{#if step === 'choose'}
		<p class="text">{tr('v2.key.intro', lang)}</p>
		<div class="choices">
			{#if canPasskey}
				<button class="btn btn-primary" onclick={() => make('passkey')} disabled={busy}>{tr('v2.key.passkey', lang)}</button>
			{:else}
				<button class="btn btn-primary" onclick={() => make('words')} disabled={busy}>{tr('v2.key.words.primary', lang)}</button>
			{/if}
			<details class="other">
				<summary>{tr('v2.key.other', lang)}</summary>
				<div class="choices">
					{#if canPasskey}
						<button class="btn" onclick={() => make('words')} disabled={busy}>{tr('v2.key.words', lang)}</button>
						<button class="btn" onclick={restorePasskey} disabled={busy}>{tr('v2.key.passkey.restore', lang)}</button>
					{/if}
					<button class="btn" onclick={() => (step = 'restore')} disabled={busy}>{tr('v2.key.restore', lang)}</button>
					<button class="btn" onclick={receiveLink} disabled={busy}>{tr('v2.key.link', lang)}</button>
				</div>
			</details>
		</div>
	{:else if step === 'words'}
		<p class="text"><strong>{tr('v2.key.words.title', lang)}</strong></p>
		<ol class="words">
			{#each words.split(' ') as w, i (i)}<li>{w}</li>{/each}
		</ol>
		<p class="text warn">{tr('v2.key.words.warning', lang)}</p>
		<button class="btn btn-primary" onclick={() => (step = 'confirm')}>{tr('v2.key.continue', lang)}</button>
	{:else if step === 'confirm'}
		<p class="text">{tr('v2.key.words.confirm', lang, { a: confirmIdx[0] + 1, b: confirmIdx[1] + 1, c: confirmIdx[2] + 1 })}</p>
		<div class="confirm">
			{#each confirmIdx as idx, k (idx)}
				<label>
					<span>{idx + 1}</span>
					<input type="text" autocomplete="off" autocapitalize="none" bind:value={confirmInput[k]} />
				</label>
			{/each}
		</div>
		<div class="row">
			<button class="btn" onclick={() => (step = 'words')}>{tr('key.back', lang)}</button>
			<button class="btn btn-primary" onclick={checkWords}>{tr('v2.key.continue', lang)}</button>
		</div>
	{:else if step === 'passport' && request}
		<VotePassport {lang} {domain} scope={request.scope} customData={request.customData} {devMode} {simulate} onSuccess={onProof} />
	{:else if step === 'posting' || step === 'done' || step === 'pending' || step === 'failed'}
		<div class="steps-box"><Steps steps={posting} /></div>
		{#if step === 'posting'}
			<p class="text small">{tr('v2.key.posting.simple', lang)}</p>
			{#if progressText}
				<details class="tech"><summary>{tr('v2.details', lang)}</summary><p>{progressText}</p></details>
			{/if}
		{:else if step === 'done' || step === 'pending'}
			<p class="text">{tr(step === 'pending' && registration.state === 'acked' ? 'v2.key.pending.acked' : 'v2.key.joined', lang)}</p>
			{#if key?.prf}
				<p class="text small">{tr('v2.key.passkey.note', lang)}</p>
			{/if}
			<button class="btn btn-primary" onclick={close}>{tr('auth.close', lang)}</button>
		{:else}
			<p class="text"><strong>{tr('v2.key.failed.title', lang)}</strong></p>
			<p class="error" role="alert">{@html failedText}</p>
			{#if failedDetail}
				<details class="tech"><summary>{tr('v2.details', lang)}</summary><p>{failedDetail}</p></details>
			{/if}
			<div class="choices">
				{#if failedCode === 'reg:uid_used' || !key}
					{#if canPasskey}
						<button class="btn" onclick={restorePasskey} disabled={busy}>{tr('v2.key.passkey.restore', lang)}</button>
					{/if}
					<button class="btn" onclick={() => (step = 'restore')} disabled={busy}>{tr('v2.key.restore', lang)}</button>
					<button class="btn" onclick={receiveLink} disabled={busy}>{tr('v2.key.link', lang)}</button>
				{:else if failedCode === 'reg:closed'}
					<button class="btn" onclick={close}>{tr('auth.close', lang)}</button>
				{:else}
					{#if !failedStale && key.registration?.raw}
						<button class="btn btn-primary" onclick={resend} disabled={busy}>{tr('v2.key.resend', lang)}</button>
					{/if}
					<button class="btn" class:btn-primary={failedStale || !key.registration?.raw} onclick={rescan} disabled={busy}>{tr('v2.key.rescan', lang)}</button>
				{/if}
			</div>
		{/if}
	{:else if step === 'link'}
		{#if $offer}
			<p class="text"><strong>{tr('v2.key.link.confirm', lang, { code: $offer })}</strong></p>
			<p class="code">{$offer}</p>
			<div class="row">
				<button class="btn" onclick={() => receiver?.answer(false)}>{tr('v2.key.link.no', lang)}</button>
				<button class="btn btn-primary" onclick={() => receiver?.answer(true)}>{tr('v2.key.link.yes', lang)}</button>
			</div>
		{:else}
			<p class="text">{tr('v2.key.link.show', lang)}</p>
			{#if linkQr}
				<img class="qr" src={linkQr} alt={tr('v2.key.link.qr_alt', lang)} width="240" height="240" />
			{/if}
			<p class="url"><code>{linkUrl}</code></p>
			<div class="row">
				<button class="btn" onclick={back}>{tr('key.back', lang)}</button>
				<button class="btn" onclick={copyLink}>{tr(linkCopied ? 'v2.key.link.copied' : 'v2.key.link.copy', lang)}</button>
			</div>
			<p class="text waiting"><Spinner size="sm" label={tr('v2.key.link.waiting', lang)} /> {tr('v2.key.link.waiting', lang)}</p>
		{/if}
	{:else if step === 'linked' || step === 'restored'}
		<p class="text">{tr(step === 'linked' ? 'v2.key.link.done' : 'v2.key.restored', lang)}</p>
		<button class="btn btn-primary" onclick={close}>{tr('auth.close', lang)}</button>
	{:else if step === 'restore'}
		<p class="text">{tr('v2.key.restore.hint', lang)}</p>
		<textarea rows="3" bind:value={restoreWords} placeholder={tr('v2.key.restore.placeholder', lang)} autocapitalize="none"></textarea>
		<div class="row">
			<button class="btn" onclick={() => (step = 'choose')}>{tr('key.back', lang)}</button>
			<button class="btn btn-primary" onclick={restore} disabled={busy || !restoreWords.trim()}>{tr('v2.key.continue', lang)}</button>
		</div>
	{/if}
	{#if error && step !== 'failed'}
		<p class="error" role="alert">{@html error}</p>
		{#if errorDetail}
			<details class="tech"><summary>{tr('v2.details', lang)}</summary><p>{errorDetail}</p></details>
		{/if}
	{/if}
</Modal>

<style>
	.text {
		margin: 0 0 0.9rem;
		line-height: 1.55;
		color: var(--text-secondary);
	}
	.other {
		margin-top: 0.3rem;
	}
	.other summary {
		cursor: pointer;
		font-size: 0.85rem;
		color: var(--text-secondary);
		text-decoration: underline;
		text-underline-offset: 0.15em;
	}
	.other .choices {
		margin-top: 0.6rem;
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
	.small {
		font-size: 0.82rem;
	}
	.warn {
		color: var(--amber);
	}
	.choices {
		display: grid;
		gap: 0.6rem;
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
	.confirm {
		display: grid;
		grid-template-columns: repeat(3, 1fr);
		gap: 0.6rem;
		margin-bottom: 0.9rem;
	}
	.confirm label {
		display: grid;
		gap: 0.25rem;
		font-size: 0.8rem;
		color: var(--text-muted);
	}
	.confirm input,
	textarea {
		width: 100%;
		padding: 0.5rem 0.6rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-md);
		background: var(--bg-card);
		color: var(--text-primary);
		font-size: 1rem;
	}
	textarea {
		margin-bottom: 0.9rem;
		font-family: var(--font-mono, monospace);
	}
	.row {
		display: flex;
		gap: 0.6rem;
		justify-content: flex-end;
	}
	.steps-box {
		margin: 0.5rem 0 1rem;
	}
	.qr {
		display: block;
		margin: 0 auto 0.9rem;
		border-radius: var(--radius-md);
		background: white;
	}
	.url {
		margin: 0 0 0.9rem;
		font-size: 0.72rem;
		word-break: break-all;
		color: var(--text-muted);
	}
	.code {
		margin: 0 0 0.9rem;
		text-align: center;
		font-family: var(--font-mono, monospace);
		font-size: 2rem;
		letter-spacing: 0.3em;
		color: var(--text-primary);
	}
	.waiting {
		margin-top: 0.9rem;
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.error {
		margin: 0.75rem 0 0;
		color: var(--red);
		font-size: 0.9rem;
	}
</style>
