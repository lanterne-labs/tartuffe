<script lang="ts">
	// Gets this device a voting key: from another device of the voter (a
	// passkey, a device link, the 12 words), or, the first time, by scanning
	// the passport, which admits a new key (POST /api/voters/register). Then
	// it asks to protect the key, so that a lost device does not lose the
	// vote. Open it with voterKeyOpen.set(true).
	import Spinner from './Spinner.svelte';
	import { get } from 'svelte/store';
	import { loadProviders, session } from '$lib/auth';
	import { lang, tr } from '$lib/i18n';
	import type { AuthProvider } from '$lib/types';
	import {
		voterKey,
		voterKeyOpen,
		newKey,
		register,
		restoreFromPasskey,
		passkeySupported,
		adoptSecret,
		secretFromWords,
		protectedEnough,
		PasskeyUnsupported,
		type VoterKey
	} from '$lib/voter';
	import ZKPassportVerify from './ZKPassportVerify.svelte';
	import ProtectKey from './ProtectKey.svelte';
	import LinkReceive from './LinkReceive.svelte';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let open = $state(false);
	$effect(() => voterKeyOpen.subscribe((v) => (open = v)));

	type Step = 'choose' | 'scan' | 'link' | 'words' | 'protect' | 'done';
	let step: Step = $state('choose');
	let all = $state<AuthProvider[] | null>(null);
	let providers = $derived(all?.filter((p) => p.role === 'verification' || p.role === 'test') ?? null);
	let loadError: string | null = $state(null);
	let pseudonym = $state('');
	let words = $state('');
	let busy = $state(false);
	let error: string | null = $state(null);
	let waitingUntil: string | null = $state(null);
	let dialog: HTMLDialogElement | undefined = $state();
	// The key being registered: kept until the passport admits it.
	let pending: VoterKey | null = null;

	$effect(() => {
		if (!dialog) return;
		if (open && !dialog.open) {
			dialog.showModal();
			error = null;
			waitingUntil = null;
			const k = get(voterKey);
			step = k?.registered ? (protectedEnough(k) ? 'done' : 'protect') : 'choose';
			if (all === null) {
				loadProviders()
					.then((p) => (all = p))
					.catch((err) => (loadError = err instanceof Error ? err.message : String(err)));
			}
		} else if (!open && dialog.open) {
			dialog.close();
		}
	});

	function close() {
		voterKeyOpen.set(false);
	}

	function keyToRegister(): VoterKey {
		const k = get(voterKey);
		if (k && !k.registered) return k;
		pending ??= newKey();
		return pending;
	}

	async function registered(provider: string, payload: Record<string, unknown>) {
		const key = keyToRegister();
		const reg = await register(provider, key, payload);
		pending = null;
		if (reg.status === 'replaced') waitingUntil = reg.from.map((w) => w.start).sort()[0];
		step = 'protect';
	}

	async function submitNoop(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		error = null;
		try {
			await registered('noop', { pseudonym });
			pseudonym = '';
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}

	async function usePasskey() {
		busy = true;
		error = null;
		try {
			await restoreFromPasskey();
			step = 'done';
		} catch (err) {
			error = err instanceof PasskeyUnsupported ? tr('key.passkey.unsupported', currentLang) : tr('key.passkey.failed', currentLang);
			console.warn('passkey restore failed', err);
		} finally {
			busy = false;
		}
	}

	function submitWords(event: SubmitEvent) {
		event.preventDefault();
		const secret = secretFromWords(words);
		if (!secret) {
			error = tr('key.words.invalid', currentLang);
			return;
		}
		adoptSecret(secret, { recovery: true });
		words = '';
		step = 'done';
	}

	function linked() {
		step = 'done';
	}

	function dateText(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long' });
	}
</script>

<dialog bind:this={dialog} class="signin" onclose={close} aria-labelledby="key-title">
	<header>
		<h2 id="key-title">{tr(step === 'protect' ? 'key.protect.title' : 'key.title', currentLang)}</h2>
		<button class="close" onclick={close} aria-label={tr('auth.close', currentLang)}>×</button>
	</header>

	{#if step === 'choose'}
		<p class="lead">{tr('key.lead', currentLang)}</p>
		<section class="provider">
			<h3>{tr('key.first.title', currentLang)}</h3>
			<p class="muted">{tr('key.first.text', currentLang)}</p>
			<button class="primary" onclick={() => (step = 'scan')}>{tr('key.first.button', currentLang)}</button>
		</section>
		<section class="provider">
			<h3>{tr('key.other.title', currentLang)}</h3>
			<p class="muted">{tr('key.other.text', currentLang)}</p>
			<div class="stack">
				{#if passkeySupported()}
					<button class="secondary" onclick={usePasskey} disabled={busy}>{tr('key.other.passkey', currentLang)}</button>
				{/if}
				<button class="secondary" onclick={() => (step = 'link')}>{tr('key.other.link', currentLang)}</button>
				<button class="secondary" onclick={() => (step = 'words')}>{tr('key.other.words', currentLang)}</button>
			</div>
			<p class="muted small">{tr('key.other.lost', currentLang)}</p>
		</section>
	{:else if step === 'scan'}
		{#if loadError}
			<p class="error">{loadError}</p>
		{:else if providers === null}
			<p class="muted loading"><Spinner label={tr('person.loading', currentLang)} /></p>
		{:else if providers.length === 0}
			<p class="muted">{tr('auth.none', currentLang)}</p>
		{:else}
			{#each providers as p (p.name)}
				{#if p.name === 'zkpassport'}
					<ZKPassportVerify config={p.config ?? {}} lang={currentLang} commitment={keyToRegister().commitment} onProof={(payload) => registered('zkpassport', payload)} />
				{:else if p.name === 'noop'}
					<section class="provider">
						<h3>{tr('verify.noop.title', currentLang)}</h3>
						<p class="muted">{tr('key.noop.hint', currentLang)}</p>
						<form onsubmit={submitNoop}>
							<input type="text" bind:value={pseudonym} maxlength="40" required placeholder={tr('verify.noop.placeholder', currentLang)} />
							<button type="submit" class="primary" disabled={busy || pseudonym.trim() === ''}>{tr('verify.noop.submit', currentLang)}</button>
						</form>
					</section>
				{/if}
			{/each}
		{/if}
		<button class="link" onclick={() => (step = 'choose')}>{tr('key.back', currentLang)}</button>
	{:else if step === 'link'}
		<LinkReceive lang={currentLang} ondone={linked} />
		<button class="link" onclick={() => (step = 'choose')}>{tr('key.back', currentLang)}</button>
	{:else if step === 'words'}
		<form class="words" onsubmit={submitWords}>
			<label for="key-words">{tr('key.words.label', currentLang)}</label>
			<textarea id="key-words" bind:value={words} rows="3" autocomplete="off" autocapitalize="off" spellcheck="false"></textarea>
			<button type="submit" class="primary" disabled={words.trim().split(/\s+/).length < 12}>{tr('key.words.submit', currentLang)}</button>
		</form>
		<button class="link" onclick={() => (step = 'choose')}>{tr('key.back', currentLang)}</button>
	{:else if step === 'protect'}
		{#if waitingUntil}
			<p class="notice">{tr('key.waiting', currentLang, { d: dateText(waitingUntil) })}</p>
		{/if}
		<ProtectKey lang={currentLang} />
		<button class="link" onclick={close}>{tr($voterKey && protectedEnough($voterKey) ? 'key.protect.done' : 'key.protect.later', currentLang)}</button>
	{:else}
		<p class="ok">{tr($session ? 'key.ready.account' : 'key.ready', currentLang)}</p>
		<button class="primary" onclick={close}>{tr('key.continue', currentLang)}</button>
	{/if}

	{#if error}
		<p class="error" role="alert">{error}</p>
	{/if}
</dialog>

<style>
	.lead {
		margin: 0 0 1rem;
		color: var(--text-secondary);
		line-height: 1.5;
	}
	.signin {
		margin: auto;
		max-height: calc(100vh - 2rem);
		overflow-y: auto;
		width: min(460px, calc(100vw - 2rem));
		padding: 1.25rem 1.5rem 1.5rem;
		background: var(--bg-secondary);
		color: var(--text-primary);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-card-hover);
	}
	.signin::backdrop {
		background: rgba(0, 0, 0, 0.6);
		backdrop-filter: blur(3px);
	}
	header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 0.75rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.5rem;
		font-weight: 600;
		margin: 0;
	}
	h3 {
		font-size: 0.95rem;
		margin: 0 0 0.25rem;
	}
	.close {
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 1.5rem;
		cursor: pointer;
		line-height: 1;
	}
	.close:hover {
		color: var(--text-primary);
	}
	.provider {
		padding: 1rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		background: var(--bg-card);
	}
	.signin :global(section + section) {
		margin-top: 0.75rem;
	}
	.muted {
		color: var(--text-secondary);
		font-size: 0.85rem;
		line-height: 1.45;
		margin: 0 0 0.75rem;
	}
	.small {
		font-size: 0.8rem;
		margin: 0.75rem 0 0;
	}
	.stack {
		display: grid;
		gap: 0.5rem;
	}
	form {
		display: flex;
		gap: 0.5rem;
	}
	.words {
		flex-direction: column;
	}
	label {
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	input,
	textarea {
		flex: 1;
		min-width: 0;
		padding: 0.55rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font: inherit;
		font-size: 0.9rem;
	}
	input:focus,
	textarea:focus {
		outline: none;
		border-color: var(--accent);
	}
	.primary,
	.secondary {
		padding: 0.55rem 1rem;
		border-radius: var(--radius-sm);
		font: inherit;
		font-weight: 600;
		cursor: pointer;
	}
	.primary {
		background: var(--accent);
		color: white;
		border: none;
	}
	.primary:hover:not(:disabled) {
		background: var(--accent-hover);
	}
	.secondary {
		background: transparent;
		color: var(--text-primary);
		border: 1px solid var(--glass-border);
		text-align: left;
	}
	.secondary:hover:not(:disabled) {
		border-color: var(--accent);
	}
	.primary:disabled,
	.secondary:disabled {
		opacity: 0.5;
		cursor: default;
	}
	.link {
		display: block;
		margin: 0.9rem auto 0;
		background: none;
		border: none;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.85rem;
		text-decoration: underline;
		cursor: pointer;
	}
	.ok {
		color: var(--green);
		margin: 0 0 0.75rem;
		line-height: 1.5;
	}
	.notice {
		padding: 0.75rem;
		border-left: 3px solid var(--accent);
		background: var(--bg-card);
		margin: 0 0 0.75rem;
		line-height: 1.5;
		font-size: 0.9rem;
	}
	.error {
		color: var(--red);
		font-size: 0.85rem;
		margin: 0.75rem 0 0;
	}
</style>
