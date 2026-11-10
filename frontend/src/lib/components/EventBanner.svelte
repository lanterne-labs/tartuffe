<script lang="ts">
	// The lab's temporary event (the featured poll) as a small card in a
	// corner of the page: what it is, until when, one link. On a phone the
	// card would hide half the screen: it is a slim bar at the bottom, the
	// title, the days left and the link. Closed once, it stays closed for
	// that event in this browser. Not shown on the event's own page.
	import { page } from '$app/stores';
	import { tr, type Lang } from '$lib/i18n';
	import type { PollEvent } from '$lib/types';
	import { eventPaths } from '$lib/event';

	let { event, lang }: { event: PollEvent; lang: Lang } = $props();

	const storeKey = (id: number) => `event-banner-closed:${id}`;
	let closed = $state(true);
	$effect(() => {
		try {
			closed = window.localStorage.getItem(storeKey(event.id)) === '1';
		} catch {
			closed = false;
		}
	});

	function close() {
		closed = true;
		try {
			window.localStorage.setItem(storeKey(event.id), '1');
		} catch {}
	}

	let ends = $derived(event.closes_at ? new Date(event.closes_at) : null);
	let daysLeft = $derived(ends ? Math.max(0, Math.ceil((ends.getTime() - Date.now()) / 86_400_000)) : null);
	let onEventPage = $derived($page.url.pathname.startsWith('/election/'));

	function dateText(d: Date): string {
		return d.toLocaleDateString(lang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}
</script>

{#if !closed && !onEventPage && (!ends || ends.getTime() > Date.now())}
	<aside class="event-card" aria-label={tr('event.label', lang)}>
		<button class="close" onclick={close} aria-label={tr('auth.close', lang)}>×</button>
		<span class="tag">{tr('event.tag', lang)}</span>
		<span class="head">
			<strong>{lang === 'en' ? event.title_en : event.title_fr}</strong>
			{#if daysLeft !== null}
				<span class="ends-short">{tr('event.ends_short', lang, { n: daysLeft })}</span>
			{/if}
		</span>
		<p class="pitch">{tr('event.pitch', lang)}</p>
		{#if ends}
			<p class="ends">{tr('event.ends', lang, { d: dateText(ends), n: daysLeft ?? 0 })}</p>
		{/if}
		<a class="go" href={eventPaths(event.slug).intro} onclick={close}>{tr('event.cta', lang)} →</a>
	</aside>
{/if}

<style>
	.event-card {
		position: fixed;
		right: 1.25rem;
		bottom: 1.25rem;
		z-index: 90;
		width: min(20rem, calc(100vw - 2rem));
		display: grid;
		gap: 0.35rem;
		padding: 1rem 1.1rem 1.05rem;
		background: color-mix(in oklch, var(--event) 9%, var(--bg-secondary));
		border: 1px solid var(--event);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-card-hover);
		animation: rise 0.35s ease-out both;
	}
	@keyframes rise {
		from {
			opacity: 0;
			transform: translateY(12px);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.event-card {
			animation: none;
		}
	}
	.tag {
		justify-self: start;
		font-size: 0.68rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--event);
	}
	.head {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		min-width: 0;
	}
	strong {
		font-family: var(--font-serif);
		font-size: 1.25rem;
		line-height: 1.15;
	}
	/* The days left, short: on the bar of a phone. */
	.ends-short {
		display: none;
		flex-shrink: 0;
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--text-secondary);
	}
	p {
		margin: 0;
		font-size: 0.85rem;
		line-height: 1.45;
		color: var(--text-secondary);
	}
	.ends {
		color: var(--text-primary);
		font-weight: 600;
	}
	.go {
		justify-self: start;
		margin-top: 0.35rem;
		padding: 0.45rem 0.9rem;
		border-radius: var(--radius-full);
		background: var(--event-strong);
		color: white;
		font-size: 0.85rem;
		font-weight: 600;
		text-decoration: none;
		white-space: nowrap;
	}
	.go:hover {
		background: var(--event);
	}
	.close {
		position: absolute;
		top: 0;
		right: 0;
		display: grid;
		place-items: center;
		width: 2.25rem;
		height: 2.25rem;
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 1.3rem;
		line-height: 1;
		cursor: pointer;
	}
	.close:hover {
		color: var(--text-primary);
	}
	/* The bar of a phone: the title, the days left, the link and the cross
	   on one line, above the home indicator. */
	@media (max-width: 640px) {
		.event-card {
			right: 0;
			left: 0;
			bottom: 0;
			width: auto;
			grid-template-columns: minmax(0, 1fr) auto auto;
			align-items: center;
			gap: 0.6rem;
			padding: 0.55rem 0.75rem 0.55rem 1rem;
			padding-bottom: calc(0.55rem + env(safe-area-inset-bottom, 0px));
			border-radius: 0;
			border-width: 1px 0 0;
			background: color-mix(in oklch, var(--event) 12%, var(--bg-primary));
		}
		.tag,
		.pitch,
		.ends {
			display: none;
		}
		/* The cross is the first child (the corner of the card), so without an
		   order it would take the stretchy column: the title gets it. */
		.head {
			order: 1;
			flex-direction: column;
			align-items: flex-start;
			gap: 0;
		}
		strong {
			font-size: 1.05rem;
			line-height: 1.2;
			white-space: nowrap;
			overflow: hidden;
			text-overflow: ellipsis;
			max-width: 100%;
		}
		.ends-short {
			display: inline;
		}
		.go {
			order: 2;
			margin: 0;
			padding: 0.5rem 0.85rem;
		}
		.close {
			order: 3;
			position: static;
			width: 2.5rem;
			height: 2.5rem;
		}
	}
</style>
