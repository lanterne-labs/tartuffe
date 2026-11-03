<script lang="ts">
	// Getting a voting key (isoloir SPEC §6, §14): the passport scan first
	// (it binds a registration key drawn before any voter key), then the
	// key, kept with a synced passkey (Face ID, the fingerprint) or with 12
	// words, three of them typed back, then the registration posted and
	// acknowledged. Also the ways back in: 12 words, the passkey, a link
	// from another device. Every wait names where it stands and every
	// failure names the next step; the dialog can be closed and reopened at
	// any point, it resumes from the session's state.
	import Modal from './Modal.svelte';
	import Steps, { type StepState } from './Steps.svelte';
	import Spinner from './Spinner.svelte';
	import VotePassport from './VotePassport.svelte';
	import { tr, type Lang } from '$lib/i18n';
	import { prfCapability, type LinkReceiver, type PassportProof, type RegistrationRequest, type SessionState } from '@isoloir/web';
	import { linkURL, type VoteSession } from '$lib/vote/isoloir';
	import { describeError, toVoteError } from '$lib/vote/errors';
	import { untrack } from 'svelte';

	interface Props {
		open: boolean;
		lang: Lang;
		session: VoteSession;
		onclose: () => void;
	}
	let { open, lang, session, onclose }: Props = $props();

	// The session's state, followed from its store.
	let st = $state<SessionState | null>(null);
	$effect(() => session.state.subscribe((v) => (st = v)));
	let offer = $state<string | null>(null);
	$effect(() => session.offer.subscribe((v) => (offer = v)));
	let registration = $derived(st?.registration ?? { state: 'none' as const });
	let key = $derived(st?.key ?? null);

	// The steps of a first registration: the passport scan, then the key
	// (kept with a passkey, or with the 12 words typed back), then the post.
	// A key that exists comes back to 'passport' only for a new scan.
	// 'existing' is the panel of the ways back in for a voter who already
	// has a key: the passkey, the 12 words, a link from another device.
	type Step = 'passport' | 'existing' | 'keep' | 'skip' | 'words' | 'confirm' | 'posting' | 'failed' | 'pending' | 'done' | 'restore' | 'link' | 'linked' | 'restored';
	let step: Step = $state('passport');
	let words = $state('');
	let confirmIdx = $state<number[]>([3, 7, 11]);
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
	// The passport request: the registration key's binding, drawn on
	// entering the step and kept until the registration is posted.
	let request = $state<RegistrationRequest | null>(null);
	$effect(() => {
		if (step !== 'passport' || !open) return;
		untrack(() => {
			try {
				request = session.need().registrationRequest();
				// The proof of work the ledger asks of every message, mined in the
				// background while the voter scans the passport.
				void session.need().startWork().catch(() => {});
			} catch (e) {
				fail(e);
			}
		});
	});
	let canPasskey = $state(typeof window !== 'undefined' && 'PublicKeyCredential' in window);
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
	// mirror: a registration refused or missing from its checkpoint, or
	// the position found.
	$effect(() => {
		const r = registration;
		const k = key;
		untrack(() => {
			if (!open) return;
			if ((r.state === 'failed' || r.state === 'rejected' || r.state === 'missing') && (step === 'pending' || step === 'posting')) step = 'failed';
			else if (k && k.position !== null && (step === 'pending' || step === 'posting')) step = 'done';
		});
	});

	function arrive() {
		error = '';
		errorDetail = '';
		const s = session.session;
		const k = key;
		if (!s) return;
		if (!k) {
			step = s.scanReady() ? 'keep' : 'passport';
			return;
		}
		if (k.position !== null) {
			step = 'done';
			return;
		}
		const r = registration;
		if (r.state === 'posting') step = 'posting';
		else if (r.state === 'failed' || r.state === 'rejected' || r.state === 'missing') step = 'failed';
		else if (r.state === 'acked' || r.state === 'sent') step = 'pending';
		else if (!k.backedUp && k.hasWords) showWords(s.words() ?? '');
		else if (s.scanReady()) void post();
		else step = 'passport';
	}

	function showWords(w: string) {
		words = w;
		confirmIdx = session.need().wordChallenge();
		confirmInput = confirmIdx.map(() => '');
		step = 'words';
	}

	function fail(e: unknown) {
		const ve = toVoteError(e);
		error = describeError(ve.code, lang);
		errorDetail = ve.detail;
	}

	// make creates the key after the scan: with 12 words, they are shown
	// and three of them typed back first; with Face ID or the fingerprint
	// (a synced passkey), the registration is posted at once.
	async function make(prefer: 'passkey' | 'words') {
		busy = true;
		error = '';
		errorDetail = '';
		try {
			const s = session.need();
			if (prefer === 'words') showWords(s.createKeyFromWords());
			else {
				await s.createKeyWithPasskey();
				await post();
			}
		} catch (e) {
			fail(e);
			// A platform without PRF offers the words alone from now on.
			const code = toVoteError(e).code;
			if (code === 'passkey_noprf' || code === 'passkey_unsupported') canPasskey = false;
		} finally {
			busy = false;
		}
	}

	// skip registers without a backup, after the warning: the key (one of
	// 12 words, readable later from the profile) stays on this device alone.
	async function skip() {
		busy = true;
		error = '';
		errorDetail = '';
		try {
			const s = session.need();
			if (!key) s.createKeyFromWords();
			s.declineBackup();
			await post();
		} catch (e) {
			fail(e);
		} finally {
			busy = false;
		}
	}

	function checkWords() {
		const c = session.need().confirmWords(confirmIdx, confirmInput);
		if (!c.ok) {
			error = tr('v2.key.words.bad', lang);
			errorDetail = '';
			return;
		}
		error = '';
		if (session.need().scanReady()) void post();
		else step = 'passport';
	}

	// post registers the key with the proof of the scan; the session's
	// registration state carries the progress and the outcome, this dialog
	// shows them. A proof past its validity sends the voter back to the
	// scan, with the same registration key.
	async function post() {
		step = 'posting';
		error = '';
		errorDetail = '';
		try {
			await session.need().submitRegistration();
			step = key?.position !== null && key?.position !== undefined ? 'done' : 'pending';
		} catch (e) {
			fail(e);
			step = toVoteError(e).code === 'stale_proof' ? 'passport' : 'failed';
		}
	}

	// onProof takes the passport's answer: before any key, the scan is
	// kept and the key comes next; for a key scanning again (a stale
	// proof), the registration is posted at once.
	async function onProof(p: PassportProof) {
		error = '';
		try {
			await session.need().scanPassport(async () => p);
		} catch (e) {
			fail(e);
			return;
		}
		if (!key) step = 'keep';
		else if (!key.backedUp && key.hasWords) showWords(session.need().words() ?? '');
		else await post();
	}

	async function resend() {
		step = 'posting';
		error = '';
		try {
			await session.need().resendRegistration();
			step = key?.position !== null && key?.position !== undefined ? 'done' : 'pending';
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
			await session.need().restoreFromWords(restoreWords);
			step = 'restored';
		} catch (e) {
			const ve = toVoteError(e);
			error = ve.code === 'not_registered' ? tr('v2.key.restore.notfound', lang) : describeError(ve.code, lang);
			errorDetail = ve.detail;
		} finally {
			busy = false;
		}
	}

	async function restorePasskey() {
		busy = true;
		error = '';
		try {
			await session.need().restoreWithPasskey();
			step = 'restored';
		} catch (e) {
			const ve = toVoteError(e);
			error = ve.code === 'not_registered' ? tr('v2.key.restore.passkey.notfound', lang) : describeError(ve.code, lang);
			errorDetail = ve.detail;
		} finally {
			busy = false;
		}
	}

	// receiveLink shows the code of this device and waits for the key; a
	// key that arrives is adopted only once the voter confirms its code.
	async function receiveLink() {
		error = '';
		try {
			const got = session.receiveLink();
			receiver = got;
			linkUrl = linkURL(window.location.origin + window.location.pathname, got.publicKey);
			linkCopied = false;
			step = 'link';
			const QRCode = await import('qrcode');
			linkQr = await QRCode.toDataURL(linkUrl, { margin: 1, width: 240 });
			await got.done;
			receiver = null;
			step = 'linked';
		} catch (e) {
			receiver = null;
			const ve = toVoteError(e);
			if (ve.code === 'stopped') return;
			error = ve.code === 'not_registered' ? tr('v2.key.restore.notfound', lang) : describeError(ve.code, lang);
			errorDetail = ve.detail;
			if (step === 'link') step = 'passport';
		}
	}

	// answer says whether the code matches; a wrong one waits for another key.
	function answer(ok: boolean) {
		session.offer.set(null);
		receiver?.answer(ok);
	}

	function stopReceiving() {
		receiver?.stop();
		receiver = null;
	}

	async function copyLink() {
		try {
			await navigator.clipboard.writeText(linkUrl);
			linkCopied = true;
		} catch (e) {
			console.warn('vote: copying the link', e);
		}
	}

	function back() {
		stopReceiving();
		error = '';
		errorDetail = '';
		step = 'passport';
	}

	function close() {
		stopReceiving();
		clearSuccess();
		onclose();
	}

	// The success state closes the dialog by itself after 3 s: long enough
	// to read one line and see the check, short enough not to be in the
	// way (the page shows the same state in its header).
	let successTimer: ReturnType<typeof setTimeout> | null = null;
	function clearSuccess() {
		if (successTimer) clearTimeout(successTimer);
		successTimer = null;
	}
	$effect(() => {
		const success = open && (step === 'done' || step === 'pending');
		untrack(() => {
			if (success && !successTimer) successTimer = setTimeout(close, 3000);
			else if (!success) clearSuccess();
		});
	});

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
		return tr('v2.key.posting.box', lang, { n: p.n, of: p.of, box: p.channel.replace(/^(wss?|https?):\/\//, ''), stage: tr(p.stage === 'posting' ? 'v2.key.posting.send' : 'v2.key.posting.wait', lang) });
	});

	// failedCode: the code of what stopped the registration (an ingest's
	// reason as reg:<reason>, a missing registration as reg_missing).
	let failedCode = $derived.by(() => {
		const r = registration;
		if (r.state === 'rejected') return `reg:${r.reason ?? 'rejected'}`;
		if (r.state === 'missing') return 'reg_missing';
		if (r.state === 'failed') return r.reason ?? 'unknown';
		return '';
	});
	let failedText = $derived(error || (failedCode ? describeError(failedCode, lang) : ''));
	let failedStale = $derived(registration.state === 'failed' && !!registration.stale);
	let failedDetail = $derived(registration.state === 'failed' ? (registration.detail ?? '') : errorDetail);
	// A registration kept on the device can go again (the same body).
	let canResend = $derived(registration.state === 'failed' || registration.state === 'missing');
</script>

{#snippet ways()}
	<!-- The ways back in for a voter who already has a key. -->
	<div class="choices">
		{#if canPasskey}
			<button class="btn btn-way" onclick={restorePasskey} disabled={busy}>{tr('v2.key.passkey.restore', lang)}</button>
		{/if}
		<button class="btn btn-way" onclick={() => (step = 'restore')} disabled={busy}>{tr('v2.key.restore', lang)}</button>
		<button class="btn btn-way" onclick={receiveLink} disabled={busy}>{tr('v2.key.link', lang)}</button>
	</div>
{/snippet}

<Modal {open} title={tr('v2.key.title', lang)} onclose={close} closeLabel={tr('auth.close', lang)}>
	<div class="centered">
	{#if step === 'passport'}
		{#if key}
			<p class="text small">{tr('v2.key.pending.idle', lang)}</p>
		{/if}
		{#if request}
			<VotePassport {lang} {request} serverNow={() => session.need().serverNow()} {onProof} />
		{/if}
		{#if !key}
			<button class="btn btn-way existing" onclick={() => (step = 'existing')} disabled={busy}>{tr('v2.key.existing', lang)}</button>
		{/if}
	{:else if step === 'existing'}
		<p class="text"><strong>{tr('v2.key.existing.title', lang)}</strong></p>
		{@render ways()}
		<div class="row back-row">
			<button class="btn" onclick={back}>{tr('key.back', lang)}</button>
		</div>
	{:else if step === 'keep'}
		<p class="text"><strong>{tr('v2.key.keep.title', lang)}</strong> {tr(canPasskey ? 'v2.key.keep.text' : 'v2.key.keep.text.words', lang)}</p>
		<div class="choices">
			{#if canPasskey}
				<button class="btn btn-primary btn-way" onclick={() => make('passkey')} disabled={busy}>{tr('v2.key.passkey', lang)}</button>
				<button class="btn btn-way" onclick={() => make('words')} disabled={busy}>{tr('v2.key.words', lang)}</button>
			{:else}
				<button class="btn btn-primary btn-way" onclick={() => make('words')} disabled={busy}>{tr('v2.key.words', lang)}</button>
				<p class="text small hint">{tr('v2.key.words.hint', lang)}</p>
			{/if}
		</div>
		<button class="skip" onclick={() => (step = 'skip')} disabled={busy}>{tr('v2.key.skip', lang)}</button>
	{:else if step === 'skip'}
		<p class="text"><strong>{tr('v2.key.skip.title', lang)}</strong></p>
		<p class="text warn">{tr('v2.key.skip.text', lang)}</p>
		<div class="row">
			<button class="btn" onclick={() => (step = 'keep')} disabled={busy}>{tr('key.back', lang)}</button>
			<button class="btn btn-primary" onclick={skip} disabled={busy}>{tr('v2.key.skip.confirm', lang)}</button>
		</div>
	{:else if step === 'words'}
		<p class="text"><strong>{tr('v2.key.words.title', lang)}</strong></p>
		<ol class="words">
			{#each words.split(' ') as w, i (i)}<li>{w}</li>{/each}
		</ol>
		<p class="text warn">{tr('v2.key.words.warning', lang)}</p>
		<button class="btn btn-primary" onclick={() => (step = 'confirm')}>{tr('v2.key.continue', lang)}</button>
	{:else if step === 'confirm'}
		<p class="text">{tr('v2.key.words.confirm', lang, { a: confirmIdx[0], b: confirmIdx[1], c: confirmIdx[2] })}</p>
		<div class="confirm">
			{#each confirmIdx as idx, k (idx)}
				<label>
					<span>{idx}</span>
					<input type="text" autocomplete="off" autocapitalize="none" bind:value={confirmInput[k]} />
				</label>
			{/each}
		</div>
		<div class="row">
			<button class="btn" onclick={() => (step = 'words')}>{tr('key.back', lang)}</button>
			<button class="btn btn-primary" onclick={checkWords}>{tr('v2.key.continue', lang)}</button>
		</div>
	{:else if step === 'done' || step === 'pending'}
		<!-- The check draws itself, then the dialog closes on its own: the
		     page carries on from there. -->
		<div class="success" role="status">
			<svg class="check" viewBox="0 0 52 52" aria-hidden="true">
				<circle cx="26" cy="26" r="24" />
				<path d="M14 27l8 8 16-16" />
			</svg>
			<p class="text">{tr(step === 'pending' && registration.state === 'acked' ? 'v2.key.pending.acked' : 'v2.key.joined', lang)}</p>
		</div>
	{:else if step === 'posting' || step === 'failed'}
		<div class="steps-box"><Steps steps={posting} /></div>
		{#if step === 'posting'}
			<p class="text small">{tr('v2.key.posting.simple', lang)}</p>
			{#if progressText}
				<details class="tech"><summary>{tr('v2.details', lang)}</summary><p>{progressText}</p></details>
			{/if}
		{:else}
			<p class="text"><strong>{tr('v2.key.failed.title', lang)}</strong></p>
			<p class="error" role="alert">{@html failedText}</p>
			{#if failedDetail}
				<details class="tech"><summary>{tr('v2.details', lang)}</summary><p>{failedDetail}</p></details>
			{/if}
			{#if failedCode === 'reg:uid_used' || !key}
				{@render ways()}
			{:else if failedCode === 'reg:closed'}
				<div class="choices"><button class="btn" onclick={close}>{tr('auth.close', lang)}</button></div>
			{:else}
				<div class="choices">
					{#if !failedStale && canResend}
						<button class="btn btn-primary" onclick={resend} disabled={busy}>{tr('v2.key.resend', lang)}</button>
					{/if}
					<button class="btn" class:btn-primary={failedStale || !canResend} onclick={rescan} disabled={busy}>{tr('v2.key.rescan', lang)}</button>
				</div>
			{/if}
		{/if}
	{:else if step === 'link'}
		{#if offer}
			<p class="text"><strong>{tr('v2.key.link.confirm', lang, { code: offer })}</strong></p>
			<p class="code">{offer}</p>
			<div class="row">
				<button class="btn" onclick={() => answer(false)}>{tr('v2.key.link.no', lang)}</button>
				<button class="btn btn-primary" onclick={() => answer(true)}>{tr('v2.key.link.yes', lang)}</button>
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
			<button class="btn" onclick={back}>{tr('key.back', lang)}</button>
			<button class="btn btn-primary" onclick={restore} disabled={busy || !restoreWords.trim()}>{tr('v2.key.continue', lang)}</button>
		</div>
	{/if}
	{#if error && step !== 'failed'}
		<p class="error" role="alert">{@html error}</p>
		{#if errorDetail}
			<details class="tech"><summary>{tr('v2.details', lang)}</summary><p>{errorDetail}</p></details>
		{/if}
	{/if}
	</div>
</Modal>

<style>
	/* Everything in the dialog reads centred: the texts, the rows of
	   buttons, the words. */
	.centered {
		text-align: center;
	}
	.centered .row {
		justify-content: center;
	}
	.text {
		margin: 0 0 0.9rem;
		line-height: 1.55;
		color: var(--text-secondary);
	}
	.success {
		display: grid;
		justify-items: center;
		gap: 0.9rem;
		padding: 0.75rem 0 0.25rem;
	}
	.success .text {
		margin: 0;
	}
	.check {
		width: 76px;
		height: 76px;
	}
	.check circle {
		fill: none;
		stroke: var(--green);
		stroke-width: 2.5;
		stroke-dasharray: 151;
		stroke-dashoffset: 151;
		animation: draw 0.5s ease-out forwards;
	}
	.check path {
		fill: none;
		stroke: var(--green);
		stroke-width: 3.5;
		stroke-linecap: round;
		stroke-linejoin: round;
		stroke-dasharray: 36;
		stroke-dashoffset: 36;
		animation: draw 0.35s ease-out 0.45s forwards;
	}
	@keyframes draw {
		to {
			stroke-dashoffset: 0;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.check circle,
		.check path {
			animation: none;
			stroke-dashoffset: 0;
		}
	}
	.hint {
		margin: -0.2rem 0 0.2rem;
		color: var(--text-muted);
	}
	/* The ways back in and the button to them: full width, the size of
	   the primary action. */
	.btn-way {
		width: 100%;
		padding: 0.7rem 1rem;
		font-size: 0.95rem;
		text-align: center;
	}
	.existing {
		margin-top: 0.9rem;
	}
	/* The way past the backup: a link under the choices. */
	.skip {
		display: block;
		margin: 0.9rem auto 0;
		background: none;
		border: none;
		color: var(--text-secondary);
		text-decoration: underline;
		text-underline-offset: 0.15em;
		font-size: 0.85rem;
		cursor: pointer;
	}
	.skip:disabled {
		opacity: 0.45;
		cursor: default;
	}
	.back-row {
		margin-top: 0.9rem;
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
