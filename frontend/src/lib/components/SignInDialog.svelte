<script lang="ts">
	// The sign-in dialog: one section per enabled account provider (GET
	// /api/auth/providers): Google, and the test account where noop is on.
	// Open it from anywhere with signInOpen.set(true). Verifying the account
	// for a vote is another dialog (VerifyDialog).
	import Spinner from './Spinner.svelte';
	import { signInOpen, loadProviders, signIn, session, setDisplayName } from '$lib/auth';
	import { signInWithGoogle, GoogleClosed } from '$lib/google';
	import { lang, tr } from '$lib/i18n';
	import { CONTACT_EMAIL, mailto } from '$lib/contact';
	import BrandMark from './BrandMark.svelte';
	import type { AuthProvider } from '$lib/types';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let open = $state(false);
	$effect(() => signInOpen.subscribe((v) => (open = v)));

	let all = $state<AuthProvider[] | null>(null);
	let providers = $derived(all?.filter((p) => p.role === 'account' || p.role === 'test') ?? null);
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
					.then((p) => (all = p))
					.catch((err) => (loadError = err instanceof Error ? err.message : String(err)));
			}
		} else if (!open && dialog.open) {
			dialog.close();
		}
	});

	function close() {
		naming = false;
		signInOpen.set(false);
	}

	// A click on the backdrop closes the dialog, as in Modal. The dialog has
	// its own padding, so a click there is the dialog's target too: only a
	// point outside its box counts.
	function onBackdrop(e: MouseEvent) {
		if (!dialog || e.target !== dialog) return;
		const r = dialog.getBoundingClientRect();
		if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) close();
	}

	// naming: the Google account has no pseudonym yet (Google gives none):
	// the dialog asks for one before it closes.
	let naming = $state(false);
	let chosen = $state('');

	async function google(p: AuthProvider) {
		busy = true;
		error = null;
		try {
			await signInWithGoogle(String(p.config?.client_id ?? ''));
			if ($session && !$session.user.display_name) naming = true;
			else close();
		} catch (err) {
			if (!(err instanceof GoogleClosed)) {
				console.error('Google sign-in failed', err);
				error = err instanceof Error ? err.message : String(err);
			}
		} finally {
			busy = false;
		}
	}

	async function submitName(event: SubmitEvent) {
		event.preventDefault();
		busy = true;
		error = null;
		try {
			await setDisplayName(chosen.trim());
			chosen = '';
			close();
		} catch (err) {
			error = err instanceof Error ? err.message : String(err);
		} finally {
			busy = false;
		}
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

<dialog bind:this={dialog} class="signin" onclose={close} onclick={onBackdrop} aria-labelledby="signin-title">
	<header>
		<BrandMark size="nav" />
		<button class="close" onclick={close} aria-label={tr('auth.close', currentLang)}>×</button>
	</header>
	<h2 id="signin-title">{tr('auth.title', currentLang)}</h2>
	{#if !naming}
		<p class="intro">{tr('auth.intro', currentLang)}</p>
	{/if}

	{#if naming}
		<section class="provider">
			<h3>{tr('auth.name.title', currentLang)}</h3>
			<p class="muted">{tr('auth.name.hint', currentLang)}</p>
			<form onsubmit={submitName}>
				<input type="text" bind:value={chosen} maxlength="40" required placeholder={tr('auth.name.placeholder', currentLang)} autocomplete="nickname" />
				<button type="submit" class="primary" disabled={busy || chosen.trim() === ''}>{tr('auth.name.submit', currentLang)}</button>
			</form>
		</section>
	{:else if loadError}
		<p class="error">{loadError}</p>
	{:else if providers === null}
		<p class="muted loading"><Spinner label={tr('person.loading', currentLang)} /></p>
	{:else if providers.length === 0}
		<p class="muted">{tr('auth.none', currentLang)} <a href={mailto(currentLang === 'fr' ? 'Signalement' : 'Report')}>{CONTACT_EMAIL}</a></p>
	{:else}
		{#each providers as p (p.name)}
			{#if p.name === 'google'}
				<section class="provider">
					<button class="google" onclick={() => google(p)} disabled={busy}>
						<svg viewBox="0 0 48 48" aria-hidden="true"
							><path fill="#FFC107" d="M43.6 20.5H42V20H24v8h11.3C33.7 32.7 29.2 36 24 36c-6.6 0-12-5.4-12-12s5.4-12 12-12c3.1 0 5.8 1.2 7.9 3.1l5.7-5.7C34 6.1 29.3 4 24 4 12.9 4 4 12.9 4 24s8.9 20 20 20 20-8.9 20-20c0-1.3-.1-2.4-.4-3.5z" /><path
								fill="#FF3D00"
								d="M6.3 14.7l6.6 4.8C14.7 15.1 19 12 24 12c3.1 0 5.8 1.2 7.9 3.1l5.7-5.7C34 6.1 29.3 4 24 4 16.3 4 9.7 8.3 6.3 14.7z"
							/><path fill="#4CAF50" d="M24 44c5.2 0 9.9-2 13.4-5.2l-6.2-5.2C29.2 35.1 26.7 36 24 36c-5.2 0-9.6-3.3-11.3-7.9l-6.5 5C9.5 39.6 16.2 44 24 44z" /><path
								fill="#1976D2"
								d="M43.6 20.5H42V20H24v8h11.3c-.8 2.2-2.2 4.2-4.1 5.6l6.2 5.2C36.9 39.2 44 34 44 24c0-1.3-.1-2.4-.4-3.5z"
							/></svg
						>
						{tr('auth.google.button', currentLang)}
					</button>
					<ul class="points">
						{#each ['like', 'report', 'google', 'pseudo'] as k (k)}
							<li><span aria-hidden="true">✓</span>{tr(`auth.point.${k}`, currentLang)}</li>
						{/each}
					</ul>
					<a class="detail" href="/about/privacy" onclick={close}>{tr('auth.detail', currentLang)}</a>
				</section>
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
	.google {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.6rem;
		width: 100%;
		padding: 0.7rem 1rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--border-card-hover);
		background: #fff;
		color: #1f1f1f;
		font: inherit;
		font-weight: 600;
		cursor: pointer;
	}
	.google:hover {
		background: #f2f2f2;
	}
	.google:disabled {
		opacity: 0.6;
		cursor: wait;
	}
	.google svg {
		width: 20px;
		height: 20px;
	}
	.signin {
		margin: auto;
		/* dvh: the height left under the browser's bars on a phone. */
		max-height: calc(100vh - 2rem);
		max-height: calc(100dvh - 2rem);
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
		font-family: var(--font-display);
		font-size: 1.5rem;
		font-weight: 600;
		margin: 0;
	}
	h3 {
		font-size: 0.95rem;
		margin: 0 0 0.25rem;
	}
	/* A 44px square under the finger; the negative margins keep the header
	   as tall as its title and the cross in the corner. */
	.close {
		display: grid;
		place-items: center;
		width: 2.75rem;
		height: 2.75rem;
		margin: -0.5rem -0.75rem -0.5rem 0;
		padding: 0;
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
		/* Under 16px iOS zooms into the field. */
		font-size: 1rem;
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
	/* The new look of the dialog: the mask, what an account is for, what
	   Google does not send. */
	.signin {
		border-radius: 1.5rem;
		border: 1px solid var(--border-card-hover);
		background: var(--bg-secondary);
		box-shadow: 0 40px 80px -20px rgba(0, 0, 0, 0.8);
	}
	.signin header {
		align-items: flex-start;
	}
	.signin h2 {
		font-family: var(--font-family);
		font-size: 1.8rem;
		font-weight: 760;
		letter-spacing: -0.035em;
		line-height: 1.1;
	}
	.intro {
		margin: 0.5rem 0 1.1rem;
		font-size: 0.95rem;
		line-height: 1.55;
		color: var(--text-secondary);
	}
	.google {
		width: 100%;
		min-height: 3rem;
		border-radius: var(--radius-full);
		font-weight: 650;
	}
	.points {
		display: grid;
		gap: 0.45rem;
		margin: 1rem 0 0.8rem;
		padding: 1rem;
		list-style: none;
		border-radius: 1rem;
		background: rgba(255, 255, 255, 0.03);
		font-size: 0.88rem;
		color: var(--text-secondary);
	}
	.points li {
		display: flex;
		gap: 0.5rem;
	}
	.points span {
		color: var(--green);
	}
	.detail {
		display: block;
		text-align: center;
		font-size: 0.84rem;
		color: var(--text-muted);
	}
	.detail:hover {
		color: var(--text-primary);
	}
</style>