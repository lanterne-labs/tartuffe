<script lang="ts">
	// Getting a voting key (SPEC §4): the secret (a passkey where the
	// platform offers PRF, else 12 words confirmed before anything else),
	// then the passport scan, then the registration posted to the mailbox
	// and acknowledged. Also the way back in with 12 words.
	import Modal from './Modal.svelte';
	import Steps, { type StepState } from './Steps.svelte';
	import VotePassport from './VotePassport.svelte';
	import { tr, type Lang } from '$lib/i18n';
	import type { VoteSession } from '$lib/vote/session';
	import type { VoteKey } from '$lib/vote/key';
	import { prfSupported } from '$lib/vote/key';
	import { voteKey } from '$lib/vote/key';

	interface Props {
		open: boolean;
		lang: Lang;
		session: VoteSession;
		domain: string;
		devMode?: boolean;
		onclose: () => void;
	}
	let { open, lang, session, domain, devMode = false, onclose }: Props = $props();

	type Step = 'choose' | 'words' | 'confirm' | 'passport' | 'posting' | 'done' | 'restore';
	let step: Step = $state('choose');
	let key: VoteKey | null = $state(null);
	let words = $state('');
	let confirmIdx = $state<[number, number, number]>([2, 6, 10]);
	let confirmInput = $state(['', '', '']);
	let error = $state('');
	let busy = $state(false);
	let restoreWords = $state('');
	let request = $derived(key ? session.registrationRequest(key) : null);
	let canPasskey = $derived(prfSupported());

	function pickPositions(): [number, number, number] {
		const s = new Set<number>();
		while (s.size < 3) s.add(Math.floor(Math.random() * 12));
		return [...s].sort((a, b) => a - b) as [number, number, number];
	}

	async function make(prefer: 'passkey' | 'words') {
		busy = true;
		error = '';
		try {
			const got = await session.newKey(prefer);
			key = got.key;
			if (got.words) {
				words = got.words;
				confirmIdx = pickPositions();
				confirmInput = ['', '', ''];
				step = 'words';
			} else {
				step = 'passport';
			}
		} catch (e) {
			error = e instanceof Error ? e.message : String(e);
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
		if (key) key.backups.words = true;
		step = 'passport';
	}

	async function onProof({ proofs, result, at }: { proofs: unknown; result: unknown; at: Date }) {
		if (!key) return;
		step = 'posting';
		const uid = String((result as { uniqueIdentifier?: string })?.uniqueIdentifier ?? '');
		await session.register(key, uid, { proofs, result }, at);
		step = 'done';
	}

	async function restore() {
		busy = true;
		error = '';
		try {
			await session.restoreFromWords(restoreWords);
			step = 'done';
		} catch (e) {
			error = e instanceof Error && e.message === 'not registered' ? tr('v2.key.restore.notfound', lang) : e instanceof Error ? e.message : String(e);
		} finally {
			busy = false;
		}
	}

	function close() {
		onclose();
		setTimeout(() => {
			if (!$voteKey) step = 'choose';
		}, 300);
	}

	const posting = $derived.by((): { label: string; state: StepState }[] => {
		const s: Step = step;
		return [
			{ label: tr('zk.flow.check', lang), state: 'ok' },
			{ label: tr('v2.key.posting', lang), state: s === 'posting' ? 'running' : s === 'done' ? 'ok' : 'todo' }
		];
	});
</script>

<Modal {open} title={tr('v2.key.title', lang)} {onclose} closeLabel={tr('auth.close', lang)}>
	{#if step === 'choose'}
		<p class="text">{tr('v2.key.intro', lang)}</p>
		<div class="choices">
			{#if canPasskey}
				<button class="btn btn-primary" onclick={() => make('passkey')} disabled={busy}>{tr('v2.key.passkey', lang)}</button>
			{/if}
			<button class="btn" class:btn-primary={!canPasskey} onclick={() => make('words')} disabled={busy}>{tr('v2.key.words', lang)}</button>
			<button class="link" onclick={() => (step = 'restore')}>{tr('v2.key.restore', lang)}</button>
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
		<VotePassport {lang} {domain} scope={request.scope} customData={request.customData} {devMode} onSuccess={onProof} />
	{:else if step === 'posting' || step === 'done'}
		<div class="steps-box"><Steps steps={posting} /></div>
		{#if step === 'done'}
			<p class="text">{tr('v2.key.joined', lang)}</p>
			<button class="btn btn-primary" onclick={close}>{tr('auth.close', lang)}</button>
		{/if}
	{:else if step === 'restore'}
		<p class="text">{tr('v2.key.restore', lang)}</p>
		<textarea rows="3" bind:value={restoreWords} placeholder={tr('v2.key.restore.placeholder', lang)} autocapitalize="none"></textarea>
		<div class="row">
			<button class="btn" onclick={() => (step = 'choose')}>{tr('key.back', lang)}</button>
			<button class="btn btn-primary" onclick={restore} disabled={busy || !restoreWords.trim()}>{tr('v2.key.continue', lang)}</button>
		</div>
	{/if}
	{#if error}
		<p class="error" role="alert">{error}</p>
	{/if}
</Modal>

<style>
	.text {
		margin: 0 0 0.9rem;
		line-height: 1.55;
		color: var(--text-secondary);
	}
	.warn {
		color: var(--amber);
	}
	.choices {
		display: grid;
		gap: 0.6rem;
	}
	.link {
		background: none;
		border: none;
		color: var(--text-secondary);
		text-decoration: underline;
		cursor: pointer;
		font-size: 0.85rem;
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
	.error {
		margin: 0.75rem 0 0;
		color: var(--red);
		font-size: 0.9rem;
	}
</style>
