<script lang="ts">
	// The sign-in dialog: one section per enabled provider (GET /api/auth/providers).
	// Open it from anywhere with signInOpen.set(true).
	import { signInOpen, loadProviders, signIn } from '$lib/auth';
	import { lang, tr } from '$lib/i18n';
	import type { AuthProvider } from '$lib/types';
	import ZKPassportSignIn from './ZKPassportSignIn.svelte';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let open = $state(false);
	$effect(() => signInOpen.subscribe((v) => (open = v)));

	let providers: AuthProvider[] | null = $state(null);
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
			if (providers === null) {
				loadProviders()
					.then((p) => (providers = p))
					.catch((err) => (loadError = err instanceof Error ? err.message : String(err)));
			}
		} else if (!open && dialog.open) {
			dialog.close();
		}
	});

	function close() {
		signInOpen.set(false);
	}

	async function submitNoop(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		error = null;
		try {
			await signIn('noop', { pseudonym });
			pseudonym = '';
			close();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
	}
</script>

<dialog bind:this={dialog} class="signin" onclose={close} aria-labelledby="signin-title">
	<header>
		<h2 id="signin-title">{tr('auth.title', currentLang)}</h2>
		<button class="close" onclick={close} aria-label={tr('auth.close', currentLang)}>×</button>
	</header>

	{#if loadError}
		<p class="error">{loadError}</p>
	{:else if providers === null}
		<p class="muted">{tr('person.loading', currentLang)}</p>
	{:else if providers.length === 0}
		<p class="muted">{tr('auth.none', currentLang)}</p>
	{:else}
		{#each providers as p (p.name)}
			{#if p.name === 'zkpassport'}
				<ZKPassportSignIn config={p.config ?? {}} lang={currentLang} onDone={close} />
			{:else if p.name === 'noop'}
				<section class="provider">
					<h3>{tr('auth.noop.title', currentLang)}</h3>
					<p class="muted">{tr('auth.noop.hint', currentLang)}</p>
					<form onsubmit={submitNoop}>
						<input
							type="text"
							bind:value={pseudonym}
							maxlength="40"
							required
							placeholder={tr('auth.noop.placeholder', currentLang)}
							autocomplete="nickname"
						/>
						<button type="submit" class="primary" disabled={busy || pseudonym.trim() === ''}>
							{tr('auth.noop.submit', currentLang)}
						</button>
					</form>
				</section>
			{/if}
		{/each}
	{/if}

	{#if error}
		<p class="error">{error}</p>
	{/if}
</dialog>

<style>
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
