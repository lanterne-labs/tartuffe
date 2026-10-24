<script lang="ts">
	// The copies of the voting key outside this browser: a passkey (Face ID,
	// fingerprint or PIN; the password manager syncs it to the voter's other
	// devices), another device linked by QR code, the 12 recovery words. Each
	// one done is ticked. Without any, a lost device means a passport scan
	// and up to 30 days without a vote that counts.
	import Spinner from './Spinner.svelte';
	import { tr, type Lang } from '$lib/i18n';
	import { voterKey, protectWithPasskey, passkeySupported, recoveryWords, PasskeyUnsupported } from '$lib/voter';
	import LinkSend from './LinkSend.svelte';

	let { lang }: { lang: Lang } = $props();

	let busy = $state(false);
	let error: string | null = $state(null);
	let showWords = $state(false);
	let wordsSaved = $state(false);
	let linking = $state(false);

	async function passkey() {
		const k = $voterKey;
		if (!k) return;
		busy = true;
		error = null;
		try {
			await protectWithPasskey(k);
		} catch (err) {
			console.warn('passkey creation failed', err);
			error = err instanceof PasskeyUnsupported ? tr('key.passkey.unsupported', lang) : tr('key.passkey.failed', lang);
		} finally {
			busy = false;
		}
	}

	function confirmWords() {
		const k = $voterKey;
		if (!k) return;
		voterKey.set({ ...k, backups: { ...k.backups, recovery: true } });
		showWords = false;
		wordsSaved = true;
	}
</script>

{#if $voterKey}
	{@const b = $voterKey.backups}
	{#if b.passkey}
		<p class="main done">✓ {tr('key.passkey.saved', lang)}</p>
	{:else if passkeySupported()}
		<button class="main" onclick={passkey} disabled={busy}>{#if busy}<Spinner size="sm" />{:else}{tr('key.passkey.button', lang)}{/if}</button>
	{/if}
	<div class="others">
		<button class="other" class:done={b.linked > 0} onclick={() => (linking = !linking)}>
			{b.linked > 0 ? '✓ ' : ''}{tr(linking ? 'key.link.hide' : 'key.link.button', lang)}
		</button>
		<button class="other" class:done={b.recovery} onclick={() => (showWords = !showWords)}>
			{b.recovery ? '✓ ' : ''}{tr(showWords ? 'key.words.hide' : 'key.words.show', lang)}
		</button>
	</div>
	{#if linking}
		<div class="panel"><LinkSend {lang} ondone={() => (linking = false)} /></div>
	{/if}
	{#if showWords}
		<div class="panel">
			<p class="warn">{tr('key.words.warning', lang)}</p>
			<ol class="words">
				{#each recoveryWords($voterKey.secret) as w, i (i)}
					<li>{w}</li>
				{/each}
			</ol>
			{#if !b.recovery}
				<button class="confirm" onclick={confirmWords}>{tr('key.words.confirm', lang)}</button>
			{/if}
		</div>
	{/if}
	{#if wordsSaved}
		<p class="ok">{tr('key.words.saved', lang)}</p>
	{/if}
	{#if error}
		<p class="error" role="alert">{error}</p>
	{/if}
{/if}

<style>
	.main {
		display: block;
		width: 100%;
		padding: 0.9rem 1rem;
		border: none;
		border-radius: var(--radius-md);
		background: var(--accent);
		color: white;
		font: inherit;
		font-size: 1rem;
		font-weight: 700;
		text-align: center;
		cursor: pointer;
	}
	.main:disabled {
		opacity: 0.5;
	}
	.main.done {
		margin: 0;
		background: transparent;
		color: var(--green);
		border: 1px solid color-mix(in oklch, var(--green) 45%, var(--border-card));
		cursor: default;
	}
	.others {
		display: flex;
		justify-content: center;
		flex-wrap: wrap;
		gap: 0.25rem 1.25rem;
		margin-top: 0.6rem;
	}
	.other {
		background: none;
		border: none;
		padding: 0.25rem 0;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.82rem;
		text-decoration: underline;
		cursor: pointer;
	}
	.other.done {
		color: var(--green);
	}
	.panel {
		margin-top: 0.6rem;
		padding: 0.75rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		background: var(--bg-card);
	}
	.confirm {
		padding: 0.45rem 0.85rem;
		border-radius: var(--radius-sm);
		background: var(--accent);
		color: white;
		border: none;
		font: inherit;
		font-size: 0.85rem;
		font-weight: 600;
		cursor: pointer;
	}
	.words {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(7.5rem, 1fr));
		gap: 0.35rem 0.75rem;
		margin: 0.5rem 0 0.75rem;
		padding-left: 1.6rem;
		font-family: var(--font-mono, monospace);
		font-size: 0.9rem;
	}
	.warn {
		margin: 0;
		font-size: 0.82rem;
		color: var(--text-secondary);
		line-height: 1.45;
	}
	.ok {
		color: var(--green);
		font-size: 0.85rem;
		margin: 0.5rem 0 0;
	}
	.error {
		color: var(--red);
		font-size: 0.85rem;
		margin: 0.5rem 0 0;
	}
</style>
