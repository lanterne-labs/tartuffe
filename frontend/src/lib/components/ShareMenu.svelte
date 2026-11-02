<script lang="ts">
	// A share button: post on X, copy the link, or the phone's own share
	// sheet. It sits inside clickable cards, so it keeps its clicks and keys.
	import { lang, tr, type Lang } from '$lib/i18n';
	import { shareURL, shareText, xIntentURL, copyText, displayName, type ShareItem } from '$lib/share';

	interface Props {
		item: ShareItem;
		// label shows the word next to the icon.
		label?: boolean;
	}

	let { item, label = false }: Props = $props();

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let open = $state(false);
	let copied = $state(false);
	let root: HTMLElement | undefined = $state();
	const canNativeShare = typeof navigator !== 'undefined' && typeof navigator.share === 'function';

	$effect(() => {
		if (!open) return;
		const onDoc = (e: MouseEvent) => {
			if (root && !root.contains(e.target as Node)) open = false;
		};
		const onKey = (e: KeyboardEvent) => {
			if (e.key === 'Escape') open = false;
		};
		document.addEventListener('click', onDoc);
		document.addEventListener('keydown', onKey);
		return () => {
			document.removeEventListener('click', onDoc);
			document.removeEventListener('keydown', onKey);
		};
	});

	async function copy() {
		copied = await copyText(shareURL(item));
		setTimeout(() => {
			copied = false;
			open = false;
		}, 1200);
	}

	async function nativeShare() {
		open = false;
		try {
			await navigator.share({ title: displayName(item.personName), text: shareText(item, currentLang), url: shareURL(item) });
		} catch (err) {
			// The user closed the sheet: nothing to do.
			if (!(err instanceof DOMException && err.name === 'AbortError')) console.error('share failed', err);
		}
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
<span class="share" bind:this={root} onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
	<button
		class="share-btn"
		class:with-label={label}
		aria-haspopup="menu"
		aria-expanded={open}
		title={tr('share.title', currentLang)}
		aria-label={tr('share.title', currentLang)}
		onclick={() => (open = !open)}
	>
		<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
			<path d="M4 12v7a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-7" />
			<polyline points="16 6 12 2 8 6" />
			<line x1="12" y1="2" x2="12" y2="15" />
		</svg>
		{#if label}<span>{tr('share.title', currentLang)}</span>{/if}
	</button>
	{#if open}
		<span class="menu" role="menu">
			<a role="menuitem" href={xIntentURL(item, currentLang)} target="_blank" rel="noopener noreferrer" onclick={() => (open = false)}>
				<svg width="14" height="14" viewBox="0 0 24 24" aria-hidden="true"><path fill="currentColor" d="M18.244 2.25h3.308l-7.227 8.26 8.502 11.24H16.17l-5.214-6.817L4.99 21.75H1.68l7.73-8.835L1.254 2.25H8.08l4.713 6.231zm-1.161 17.52h1.833L7.084 4.126H5.117z" /></svg>
				{tr('share.x', currentLang)}
			</a>
			<button role="menuitem" onclick={copy}>
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
					<path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
				</svg>
				<span aria-live="polite">{copied ? tr('share.copied', currentLang) : tr('share.copy', currentLang)}</span>
			</button>
			{#if canNativeShare}
				<button role="menuitem" onclick={nativeShare}>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
						<circle cx="18" cy="5" r="3" /><circle cx="6" cy="12" r="3" /><circle cx="18" cy="19" r="3" />
						<line x1="8.59" y1="13.51" x2="15.42" y2="17.49" /><line x1="15.41" y1="6.51" x2="8.59" y2="10.49" />
					</svg>
					{tr('share.more', currentLang)}
				</button>
			{/if}
		</span>
	{/if}
</span>

<style>
	.share {
		position: relative;
		display: inline-flex;
	}
	.share-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		min-width: 28px;
		height: 28px;
		padding: 0 0.35rem;
		justify-content: center;
		background: transparent;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.8rem;
		cursor: pointer;
	}
	.share-btn:hover,
	.share-btn[aria-expanded='true'] {
		color: var(--accent);
		border-color: var(--border-card);
	}
	.share-btn.with-label {
		border-color: var(--border-card);
		padding: 0 0.6rem;
	}
	.menu {
		position: absolute;
		top: calc(100% + 4px);
		right: 0;
		z-index: 30;
		display: flex;
		flex-direction: column;
		min-width: 12rem;
		padding: 0.3rem;
		background: var(--bg-primary, var(--bg-card));
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md, 8px);
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.18);
	}
	.menu a,
	.menu button {
		display: flex;
		align-items: center;
		gap: 0.55rem;
		padding: 0.5rem 0.6rem;
		background: none;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font: inherit;
		font-size: 0.85rem;
		text-align: left;
		text-decoration: none;
		cursor: pointer;
		white-space: nowrap;
	}
	.menu a:hover,
	.menu button:hover {
		background: var(--bg-secondary);
	}
</style>
