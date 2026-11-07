<script lang="ts">
	// The +1 of a signed-in user on a fact or a contradiction, in the feed
	// and on the person page (PUT / DELETE /api/likes/{kind}/{id}); the hot
	// feed ranks by them. A signed-out click opens the sign-in, then gives
	// the +1. The count and the mark are bound when the parent keeps them
	// (the feed), else the button keeps its own after a click.
	import { lang, tr, type Lang } from '$lib/i18n';
	import { authFetch, session, signInOpen } from '$lib/auth';
	import type { ReportKind } from '$lib/types';

	interface Props {
		kind: ReportKind;
		targetId: number;
		likes: number;
		liked: boolean;
	}

	let { kind, targetId, likes = $bindable(), liked = $bindable() }: Props = $props();

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let sending = $state(false);
	let waitingSignIn = $state(false);

	$effect(() => {
		if (waitingSignIn && $session && !$signInOpen) {
			waitingSignIn = false;
			if (!liked) toggle();
		}
	});
	$effect(() => {
		if (waitingSignIn && !$signInOpen && !$session) waitingSignIn = false;
	});

	async function toggle() {
		if (sending) return;
		if (!$session) {
			waitingSignIn = true;
			signInOpen.set(true);
			return;
		}
		// The count changes at once and goes back if the call fails.
		const was = { likes, liked };
		liked = !liked;
		likes += liked ? 1 : -1;
		sending = true;
		try {
			const res = await authFetch(`/api/likes/${kind}/${targetId}`, { method: liked ? 'PUT' : 'DELETE' });
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const s: { likes: number; liked: boolean } = await res.json();
			likes = s.likes;
			liked = s.liked;
		} catch (err) {
			console.error('failed to record the like', err);
			likes = was.likes;
			liked = was.liked;
		} finally {
			sending = false;
		}
	}
</script>

<button
	class="like"
	class:on={liked}
	aria-pressed={liked}
	title={tr(liked ? 'like.undo' : 'like.do', currentLang)}
	onclick={(e) => {
		e.preventDefault();
		e.stopPropagation();
		toggle();
	}}
>
	<span class="plus">+1</span>
	{#if likes > 0}<span class="n">{likes}</span>{/if}
</button>

<style>
	.like {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		height: 28px;
		padding: 0 0.65rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.8rem;
		font-weight: 600;
		cursor: pointer;
		transition:
			background 0.15s,
			color 0.15s,
			border-color 0.15s;
	}
	.like:hover {
		color: var(--text-primary);
		border-color: var(--border-card-hover);
	}
	.like.on {
		background: var(--accent);
		border-color: var(--accent);
		color: #fff;
	}
	.like:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}
	.n {
		padding-left: 0.4rem;
		border-left: 1px solid currentColor;
		font-variant-numeric: tabular-nums;
		opacity: 0.85;
	}
</style>
