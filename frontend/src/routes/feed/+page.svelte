<script lang="ts">
	// The feed: every new contradiction and fact, newest first, in an endless
	// scroll (GET /api/feed). A new contradiction while the page is open shows
	// a banner to load it.
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { API_BASE } from '$lib/config';
	import { lang, tr, pickLang, type Lang } from '$lib/i18n';
	import { sourceDomain } from '$lib/sources';
	import { sharePath } from '$lib/share';
	import { realtimeManager, subscribeToPersonEvents } from '$lib/realtime';
	import ShareMenu from '$lib/components/ShareMenu.svelte';
	import ReportButton from '$lib/components/ReportButton.svelte';
	import type { FeedItem, FeedPage } from '$lib/types';

	type Filter = '' | 'contradiction' | 'fact';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let filter = $derived(($page.url.searchParams.get('kind') ?? '') as Filter);
	let items: FeedItem[] = $state([]);
	let next: string | undefined = $state(undefined);
	let loading = $state(false);
	let error = $state(false);
	let ended = $state(false);
	let fresh = $state(0);
	let sentinel: HTMLElement | undefined = $state();
	// A load for an older filter is dropped.
	let seq = 0;

	async function load(reset: boolean) {
		if (loading && !reset) return;
		const n = ++seq;
		loading = true;
		error = false;
		try {
			const q = new URLSearchParams({ limit: '20' });
			if (filter) q.set('kind', filter);
			if (!reset && next) q.set('after', next);
			const res = await fetch(`${API_BASE}/api/feed?${q}`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const p: FeedPage = await res.json();
			if (n !== seq) return;
			items = reset ? p.items : [...items, ...p.items];
			next = p.next;
			ended = !p.next;
			if (reset) fresh = 0;
		} catch (err) {
			if (n !== seq) return;
			console.error('failed to load the feed', err);
			error = true;
		} finally {
			if (n === seq) loading = false;
		}
	}

	// The filter is in the URL: a change reloads from the top.
	let loadedFilter: Filter | null = null;
	$effect(() => {
		const f = filter;
		if (f === loadedFilter) return;
		loadedFilter = f;
		next = undefined;
		ended = false;
		load(true);
	});

	function setFilter(f: Filter) {
		const url = new URL($page.url);
		if (f) url.searchParams.set('kind', f);
		else url.searchParams.delete('kind');
		goto(url, { replaceState: true, noScroll: true, keepFocus: true });
	}

	// The next page loads when the end of the list comes near.
	$effect(() => {
		if (!sentinel) return;
		const io = new IntersectionObserver(
			(entries) => {
				if (entries.some((e) => e.isIntersecting) && next && !loading && !error) load(false);
			},
			{ rootMargin: '600px 0px' }
		);
		io.observe(sentinel);
		return () => io.disconnect();
	});

	function showFresh() {
		window.scrollTo({ top: 0, behavior: 'smooth' });
		load(true);
	}

	function ago(iso: string): string {
		const rtf = new Intl.RelativeTimeFormat(currentLang === 'en' ? 'en' : 'fr', { numeric: 'auto' });
		const s = (new Date(iso).getTime() - Date.now()) / 1000;
		const steps: [number, Intl.RelativeTimeFormatUnit][] = [[60, 'second'], [60, 'minute'], [24, 'hour'], [7, 'day'], [4.35, 'week'], [12, 'month']];
		let v = s;
		for (const [n, unit] of steps) {
			if (Math.abs(v) < n) return rtf.format(Math.round(v), unit);
			v /= n;
		}
		return rtf.format(Math.round(v), 'year');
	}

	function day(iso: string | null | undefined): string {
		if (!iso) return '';
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'short', year: 'numeric' });
	}

	function scoreColor(s: number): string {
		if (s >= 0.8) return 'var(--green)';
		if (s >= 0.4) return 'var(--amber)';
		return 'var(--red)';
	}

	let unsub: (() => void) | null = null;
	onMount(() => {
		realtimeManager.connect();
		unsub = subscribeToPersonEvents(-1, (e) => {
			if (e.type === 'contradiction_added' && filter !== 'fact') fresh++;
		});
	});
	onDestroy(() => {
		unsub?.();
		realtimeManager.disconnect();
	});
</script>

<svelte:head>
	<title>{tr('feed.title', currentLang)} — tartuffe.me</title>
</svelte:head>

<div class="feed">
	<header class="feed-head">
		<h1>{tr('feed.title', currentLang)}</h1>
		<p class="intro">{tr('feed.intro', currentLang)}</p>
		<div class="tabs" role="tablist">
			{#each [['', 'feed.all'], ['contradiction', 'feed.contradictions'], ['fact', 'feed.facts']] as [f, key] (f)}
				<button role="tab" class="tab" class:active={filter === f} aria-selected={filter === f} onclick={() => setFilter(f as Filter)}>
					{tr(key, currentLang)}
				</button>
			{/each}
		</div>
	</header>

	{#if fresh > 0}
		<button class="fresh" onclick={showFresh}>{tr('feed.new', currentLang, { n: fresh })}</button>
	{/if}

	{#if items.length === 0 && loading}
		{#each Array(4) as _}
			<div class="card skeleton-card"><div class="sk sk-head"></div><div class="sk sk-line"></div><div class="sk sk-line short"></div></div>
		{/each}
	{:else if items.length === 0 && error}
		<div class="card state"><p>{tr('feed.error', currentLang)}</p><button class="btn" onclick={() => load(true)}>{tr('home.retry', currentLang)}</button></div>
	{:else if items.length === 0}
		<p class="state muted">{tr('feed.empty', currentLang)}</p>
	{/if}

	<ol class="items">
		{#each items as it (it.kind + it.id)}
			{@const topic = pickLang(it.stance_label, it.stance_label_en, currentLang)}
			{@const link = sharePath(it.kind, it.id, it.person.id)}
			<li class="card item" class:is-contradiction={it.kind === 'contradiction'}>
				<div class="item-head">
					<a href="/person/{it.person.id}" class="who">
						{#if it.person.image_url}
							<img src={it.person.image_url} alt="" class="avatar" loading="lazy" />
						{:else}
							<span class="avatar avatar-ph">{it.person.name.charAt(0).toUpperCase()}</span>
						{/if}
						<span class="who-text">
							<span class="name">{it.person.name}</span>
							<span class="meta">
								<span class="score" style="color: {scoreColor(it.person.honesty_score)}">{Math.round(it.person.honesty_score * 100)}/100</span>
								<span aria-hidden="true">·</span>
								<time datetime={it.at} title={new Date(it.at).toLocaleString()}>{ago(it.at)}</time>
							</span>
						</span>
					</a>
					<span class="kind" class:kind-c={it.kind === 'contradiction'}>{tr(it.kind === 'contradiction' ? 'feed.contradiction' : 'feed.fact', currentLang)}</span>
				</div>
				{#if topic}
					<a class="topic" href={it.stance_id ? `/person/${it.person.id}?stance=${it.stance_id}` : `/person/${it.person.id}`}>{topic}</a>
				{/if}

				{#if it.kind === 'contradiction' && it.other}
					<div class="pair">
						<blockquote>
							{#if it.fact.date}<span class="when">{day(it.fact.date)}</span>{/if}
							{pickLang(it.fact.text, it.fact.text_en, currentLang)}
						</blockquote>
						<span class="then">{tr('feed.then', currentLang)}</span>
						<blockquote>
							{#if it.other.date}<span class="when">{day(it.other.date)}</span>{/if}
							{pickLang(it.other.text, it.other.text_en, currentLang)}
						</blockquote>
					</div>
					{#if it.reason}
						<p class="why"><strong>{tr('feed.why', currentLang)} :</strong> {pickLang(it.reason, it.reason_en, currentLang)}</p>
					{/if}
					<div class="sources">
						{#each [...it.fact.source_urls.slice(0, 2), ...it.other.source_urls.slice(0, 2)] as url (url)}
							<a href={url} target="_blank" rel="noopener noreferrer" title={url}>{sourceDomain(url)}</a>
						{/each}
					</div>
				{:else}
					<p class="fact-text">{pickLang(it.fact.text, it.fact.text_en, currentLang)}</p>
					{#if it.fact.quote}
						<blockquote class="quote">« {it.fact.quote.length > 280 ? it.fact.quote.slice(0, 280) + '…' : it.fact.quote} »</blockquote>
					{/if}
					<div class="sources">
						{#each it.fact.source_urls.slice(0, 3) as url (url)}
							<a href={url} target="_blank" rel="noopener noreferrer" title={url}>{sourceDomain(url)}</a>
						{/each}
					</div>
				{/if}

				<div class="actions">
					<a href={link} class="open">{tr('feed.open', currentLang)} →</a>
					<span class="spacer"></span>
					<ShareMenu
						item={{
							kind: it.kind,
							id: it.id,
							personId: it.person.id,
							personName: it.person.name,
							topic,
							text: pickLang(it.fact.text, it.fact.text_en, currentLang),
							other: it.other ? pickLang(it.other.text, it.other.text_en, currentLang) : undefined
						}}
					/>
					<ReportButton
						kind={it.kind}
						targetId={it.id}
						preview={it.other ? `${it.fact.text}\n⟷\n${it.other.text}` : it.fact.text}
					/>
				</div>
			</li>
		{/each}
	</ol>

	<div bind:this={sentinel} class="sentinel"></div>
	{#if items.length > 0}
		{#if loading}
			<p class="state muted">{tr('person.loading', currentLang)}</p>
		{:else if error}
			<div class="state"><p class="muted">{tr('feed.error', currentLang)}</p><button class="btn" onclick={() => load(false)}>{tr('home.retry', currentLang)}</button></div>
		{:else if ended}
			<p class="state muted">{tr('feed.end', currentLang)}</p>
		{:else}
			<div class="state"><button class="btn" onclick={() => load(false)}>{tr('feed.more', currentLang)}</button></div>
		{/if}
	{/if}
</div>

<style>
	.feed {
		max-width: 40rem;
		margin: 1rem auto 3rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2rem, 5vw, 2.75rem);
		font-weight: 600;
		margin: 0;
	}
	.intro {
		color: var(--text-secondary);
		line-height: 1.5;
		margin: 0.4rem 0 1rem;
	}
	.feed-head {
		margin-bottom: 1rem;
	}
	.tabs {
		display: flex;
		gap: 0.4rem;
		flex-wrap: wrap;
	}
	.tab {
		padding: 0.4rem 0.9rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.85rem;
		cursor: pointer;
	}
	.tab.active {
		background: var(--accent);
		border-color: var(--accent);
		color: #fff;
	}
	.fresh {
		position: sticky;
		top: 4.5rem;
		z-index: 20;
		display: block;
		margin: 0 auto 1rem;
		padding: 0.5rem 1rem;
		border-radius: var(--radius-full);
		border: none;
		background: var(--accent);
		color: #fff;
		font: inherit;
		font-size: 0.85rem;
		cursor: pointer;
		box-shadow: var(--shadow-card);
	}
	.items {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.9rem;
	}
	.card {
		padding: 1rem 1.15rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
	}
	.item.is-contradiction {
		border-left: 3px solid var(--red);
	}
	.item-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
	}
	.who {
		display: flex;
		align-items: center;
		gap: 0.65rem;
		min-width: 0;
		color: inherit;
		text-decoration: none;
	}
	.who:hover .name {
		color: var(--accent-hover);
	}
	.avatar {
		width: 40px;
		height: 40px;
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
	}
	.avatar-ph {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		background: var(--bg-secondary);
		color: var(--text-secondary);
		font-weight: 600;
	}
	.who-text {
		display: flex;
		flex-direction: column;
		min-width: 0;
	}
	.name {
		font-weight: 600;
		text-transform: capitalize;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.meta {
		display: flex;
		gap: 0.35rem;
		font-size: 0.78rem;
		color: var(--text-muted);
	}
	.score {
		font-weight: 600;
	}
	.kind {
		flex-shrink: 0;
		font-size: 0.7rem;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--text-muted);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
		padding: 0.15rem 0.55rem;
	}
	.kind-c {
		color: var(--red);
		border-color: var(--error-border);
	}
	.topic {
		display: inline-block;
		margin-top: 0.65rem;
		font-size: 0.78rem;
		color: var(--text-secondary);
		background: var(--bg-secondary);
		border-radius: var(--radius-full);
		padding: 0.15rem 0.6rem;
		text-decoration: none;
	}
	.topic:hover {
		color: var(--text-primary);
	}
	.pair {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
		margin-top: 0.75rem;
	}
	blockquote {
		margin: 0;
		padding: 0.6rem 0.8rem;
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
		line-height: 1.5;
		font-size: 0.92rem;
	}
	.when {
		display: block;
		font-size: 0.72rem;
		color: var(--text-muted);
		margin-bottom: 0.15rem;
	}
	.then {
		align-self: center;
		font-size: 0.72rem;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--red);
	}
	.why {
		margin: 0.6rem 0 0;
		font-size: 0.85rem;
		color: var(--text-secondary);
		line-height: 1.5;
	}
	.fact-text {
		margin: 0.7rem 0 0;
		line-height: 1.55;
	}
	.quote {
		margin-top: 0.5rem;
		font-style: italic;
		color: var(--text-secondary);
		font-size: 0.85rem;
	}
	.sources {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem;
		margin-top: 0.6rem;
	}
	.sources a {
		font-size: 0.75rem;
		color: var(--text-secondary);
		border: 1px solid var(--border-card-hover);
		border-radius: var(--radius-sm);
		padding: 0.15rem 0.45rem;
		text-decoration: none;
	}
	.sources a:hover {
		color: var(--accent-hover);
	}
	.actions {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		margin-top: 0.75rem;
		padding-top: 0.6rem;
		border-top: 1px solid var(--border-card);
	}
	.open {
		font-size: 0.82rem;
		color: var(--accent-hover);
		text-decoration: none;
	}
	.spacer {
		flex: 1;
	}
	.state {
		text-align: center;
		margin: 1.5rem 0;
	}
	.muted {
		color: var(--text-secondary);
	}
	.sentinel {
		height: 1px;
	}
	.skeleton-card {
		margin-bottom: 0.9rem;
	}
	.sk {
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
		animation: pulse 1.4s ease-in-out infinite;
	}
	.sk-head {
		height: 40px;
		width: 50%;
		margin-bottom: 0.75rem;
	}
	.sk-line {
		height: 0.9rem;
		margin-top: 0.5rem;
	}
	.sk-line.short {
		width: 60%;
	}
	@keyframes pulse {
		50% {
			opacity: 0.5;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.sk {
			animation: none;
		}
	}
	@media (max-width: 560px) {
		.card {
			padding: 0.85rem 0.9rem;
		}
	}
</style>
