<script lang="ts" module>
	import { writable } from 'svelte/store';
	// movedNote: the document came from another account, with its votes.
	export const movedNote = writable(false);
</script>

<script lang="ts">
	// The identity verification dialog: a signed-in account proves one
	// document to vote, once (POST /api/me/verify/{provider}). One section per
	// enabled verification provider: ZKPassport, and the test document where
	// noop is on. Open it with verifyOpen.set(true).
	import { verifyOpen, loadProviders, verify, VerifyError, pendingMove, confirmMove } from '$lib/auth';
	import { lang, tr } from '$lib/i18n';
	import type { AuthProvider } from '$lib/types';
	import ZKPassportVerify from './ZKPassportVerify.svelte';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let open = $state(false);
	$effect(() => verifyOpen.subscribe((v) => (open = v)));

	let all = $state<AuthProvider[] | null>(null);
	let providers = $derived(all?.filter((p) => p.role === 'verification' || p.role === 'test') ?? null);
	let loadError: string | null = $state(null);
	let pseudonym = $state('');
	let busy = $state(false);
	let error: string | null = $state(null);
	let dialog: HTMLDialogElement | undefined = $state();

	$effect(() => {
		if (!dialog) return;
		if (open && !dialog.open) {
			dialog.showModal();
			error = null;
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
		pendingMove.set(null);
		verifyOpen.set(false);
	}

	async function move() {
		busy = true;
		error = null;
		try {
			const { moved } = await confirmMove();
			done(moved);
		} catch (err) {
			error = err instanceof VerifyError && err.code === 'no_move' ? tr('verify.move_expired', currentLang) : err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}

	function done(moved: boolean) {
		if (moved) movedNote.set(true);
		close();
	}

	async function submitNoop(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		error = null;
		try {
			const { moved } = await verify('noop', { pseudonym });
			pseudonym = '';
			done(moved);
		} catch (err) {
			if (err instanceof VerifyError && err.code === 'document_elsewhere') return;
			error = err instanceof VerifyError && err.code === 'other_document' ? tr('verify.other_document', currentLang) : err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<dialog bind:this={dialog} class="signin" onclose={close} aria-labelledby="verify-title">
	<header>
		<h2 id="verify-title">{tr('verify.title', currentLang)}</h2>
		<button class="close" onclick={close} aria-label={tr('auth.close', currentLang)}>×</button>
	</header>
	{#if $pendingMove}
		<section class="provider move">
			<h3>{tr('verify.move.title', currentLang)}</h3>
			<p>{tr('verify.move.text', currentLang)}</p>
			<p class="muted">{tr('verify.move.note', currentLang)}</p>
			<div class="move-actions">
				<button class="primary" onclick={move} disabled={busy}>{tr('verify.move.confirm', currentLang)}</button>
				<button class="secondary" onclick={close} disabled={busy}>{tr('verify.move.cancel', currentLang)}</button>
			</div>
		</section>
	{:else}
	<p class="lead">{tr('verify.lead', currentLang)}</p>

	{#if loadError}
		<p class="error">{loadError}</p>
	{:else if providers === null}
		<p class="muted">{tr('person.loading', currentLang)}</p>
	{:else if providers.length === 0}
		<p class="muted">{tr('auth.none', currentLang)}</p>
	{:else}
		{#each providers as p (p.name)}
			{#if p.name === 'zkpassport'}
				<ZKPassportVerify config={p.config ?? {}} lang={currentLang} onDone={done} />
			{:else if p.name === 'noop'}
				<section class="provider">
					<h3>{tr('verify.noop.title', currentLang)}</h3>
					<p class="muted">{tr('verify.noop.hint', currentLang)}</p>
					<form onsubmit={submitNoop}>
						<input type="text" bind:value={pseudonym} maxlength="40" required placeholder={tr('verify.noop.placeholder', currentLang)} />
						<button type="submit" class="primary" disabled={busy || pseudonym.trim() === ''}>
							{tr('verify.noop.submit', currentLang)}
						</button>
					</form>
				</section>
			{/if}
		{/each}
	{/if}
	{/if}

	{#if error}
		<p class="error">{error}</p>
	{/if}
</dialog>

<style>
	.move p {
		margin: 0 0 0.75rem;
		line-height: 1.5;
	}
	.move-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
	.secondary {
		padding: 0.55rem 1rem;
		border-radius: var(--radius-md);
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-secondary);
		font: inherit;
		cursor: pointer;
	}
	.lead {
		margin: 0 0 1rem;
		color: var(--text-secondary);
		line-height: 1.5;
	}
	.signin {
		margin: auto;
		max-height: calc(100vh - 2rem);
		overflow-y: auto;
		width: min(440px, calc(100vw - 2rem));
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
	form {
		display: flex;
		gap: 0.5rem;
	}
	input {
		flex: 1;
		min-width: 0;
		padding: 0.55rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font-size: 0.9rem;
	}
	input:focus {
		outline: none;
		border-color: var(--accent);
	}
	.primary {
		padding: 0.55rem 1rem;
		background: var(--accent);
		color: white;
		border: none;
		border-radius: var(--radius-sm);
		font-weight: 600;
		cursor: pointer;
	}
	.primary:hover:not(:disabled) {
		background: var(--accent-hover);
	}
	.primary:disabled {
		opacity: 0.5;
		cursor: default;
	}
	.error {
		color: var(--red);
		font-size: 0.85rem;
		margin: 0.75rem 0 0;
	}
</style>
