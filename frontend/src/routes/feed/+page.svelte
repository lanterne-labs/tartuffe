<script lang="ts">
	// The feed: every new contradiction and fact in an endless scroll (GET
	// /api/feed), "à la une" (ranked by the users' +1 and age, the default) or
	// newest first, filtered by kind, person and the dates of the statements,
	// all kept in the URL so a filtered feed can be shared. A new
	// contradiction while the page is open shows a banner to load it.
	import Spinner from '$lib/components/Spinner.svelte';
	import TartuffeScore from '$lib/components/TartuffeScore.svelte';
	import { onMount, onDestroy } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { API_BASE } from '$lib/config';
	import { lang, tr, pickLang, whyOf, type Lang } from '$lib/i18n';
	import { sourceDomain } from '$lib/sources';
	import { sharePath } from '$lib/share';
	import { realtimeManager, subscribeToPersonEvents } from '$lib/realtime';
	import ShareMenu from '$lib/components/ShareMenu.svelte';
	import ReportButton from '$lib/components/ReportButton.svelte';
	import LikeButton from '$lib/components/LikeButton.svelte';
	import HintSheet from '$lib/components/HintSheet.svelte';
	import { authFetch, session } from '$lib/auth';
	import type { FeedItem, FeedPage, PersonSummary } from '$lib/types';

	type Filter = '' | 'contradiction' | 'fact';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	// A touch screen has no hover: the title of the score move opens in a
	// sheet on a tap instead. Read after mount: the page is server-rendered.
	let touch = $state(false);
	$effect(() => {
		touch = window.matchMedia('(hover: none)').matches;
	});
	let effectHint = $state<FeedItem | null>(null);

	const isDay = (v: string | null) => (v && /^\d{4}-\d{2}-\d{2}$/.test(v) ? v : '');
	let filter = $derived(($page.url.searchParams.get('kind') ?? '') as Filter);
	let sort = $derived<'hot' | 'new'>($page.url.searchParams.get('sort') === 'new' ? 'new' : 'hot');
	let personFilter = $derived(/^\d+$/.test($page.url.searchParams.get('person') ?? '') ? Number($page.url.searchParams.get('person')) : 0);
	let fromDay = $derived(isDay($page.url.searchParams.get('from')));
	let toDay = $derived(isDay($page.url.searchParams.get('to')));
	// What the list is loaded for: a change reloads it from the top, as does
	// a sign-in or sign-out (the viewer's +1 are marked).
	let token = $derived($session?.token ?? '');
	let filterKey = $derived(`${sort}|${filter}|${personFilter}|${fromDay}|${toDay}|${token}`);
	let filtered = $derived(!!(personFilter || fromDay || toDay));
	// The filters hide behind an icon; its badge counts the active ones.
	let active = $derived([filter, personFilter, fromDay, toDay].filter(Boolean).length);
	let showFilters = $state(false);

	// The person picker: the chosen person's name, and the matches of a search.
	let personName = $state('');
	let query = $state('');
	let matches: PersonSummary[] = $state([]);
	let searchTimer: ReturnType<typeof setTimeout> | undefined;
	$effect(() => {
		const id = personFilter;
		if (!id) {
			personName = '';
			return;
		}
		if (items.find((it) => it.person.id === id)) {
			personName = items.find((it) => it.person.id === id)!.person.name;
			return;
		}
		fetch(`${API_BASE}/api/person/${id}`)
			.then((r) => (r.ok ? r.json() : null))
			.then((p: PersonSummary | null) => {
				if (personFilter === id) personName = p?.name ?? `#${id}`;
			})
			.catch((err) => console.error('failed to load the person', err));
	});

	function onSearch() {
		clearTimeout(searchTimer);
		const q = query.trim();
		if (q.length < 2) {
			matches = [];
			return;
		}
		searchTimer = setTimeout(async () => {
			try {
				const res = await fetch(`${API_BASE}/api/persons?search=${encodeURIComponent(q)}&limit=8`);
				if (!res.ok) throw new Error(`HTTP ${res.status}`);
				const found: PersonSummary[] = await res.json();
				if (query.trim() === q) matches = found ?? [];
			} catch (err) {
				console.error('person search failed', err);
			}
		}, 200);
	}

	function setParams(changes: Record<string, string>) {
		const url = new URL($page.url);
		for (const [k, v] of Object.entries(changes)) {
			if (v) url.searchParams.set(k, v);
			else url.searchParams.delete(k);
		}
		goto(url, { replaceState: true, noScroll: true, keepFocus: true });
	}

	function pickPerson(p: PersonSummary) {
		personName = p.name;
		query = '';
		matches = [];
		setParams({ person: String(p.id) });
	}
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
			const q = new URLSearchParams({ limit: '20', sort });
			if (filter) q.set('kind', filter);
			if (personFilter) q.set('person', String(personFilter));
			if (fromDay) q.set('from', fromDay);
			if (toDay) q.set('to', toDay);
			if (!reset && next) q.set('after', next);
			const res = await authFetch(`/api/feed?${q}`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const p: FeedPage = await res.json();
			if (n !== seq) return;
			// The hot order moves between two pages: an item seen is not shown twice.
			const seen = new Set(reset ? [] : items.map((it) => it.kind + it.id));
			const add = p.items.filter((it) => !seen.has(it.kind + it.id));
			items = reset ? add : [...items, ...add];
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
	let loadedFilter: string | null = null;
	$effect(() => {
		const f = filterKey;
		if (f === loadedFilter) return;
		loadedFilter = f;
		next = undefined;
		ended = false;
		load(true);
	});

	function setFilter(f: Filter) {
		setParams({ kind: f });
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
		// The short style, not the narrow one: in French the narrow "-2 h" reads
		// as a negative number, the short "il y a 2 h" as a time.
		const rtf = new Intl.RelativeTimeFormat(currentLang === 'en' ? 'en' : 'fr', { numeric: 'auto', style: 'short' });
		const s = (new Date(iso).getTime() - Date.now()) / 1000;
		const steps: [number, Intl.RelativeTimeFormatUnit][] = [[60, 'second'], [60, 'minute'], [24, 'hour'], [7, 'day'], [4.35, 'week'], [12, 'month']];
		let v = s;
		for (const [n, unit] of steps) {
			if (Math.abs(v) < n) return rtf.format(Math.round(v), unit);
			v /= n;
		}
		return rtf.format(Math.round(v), 'year');
	}

	function year(iso: string | null | undefined): string {
		return iso ? String(new Date(iso).getFullYear()) : '';
	}

	// move is a market-style move of the score: "+0,5 %", "−1,3 %".
	function move(pct: number): string {
		return new Intl.NumberFormat(currentLang === 'en' ? 'en-GB' : 'fr-FR', {
			style: 'percent',
			minimumFractionDigits: 1,
			maximumFractionDigits: 1,
			signDisplay: 'exceptZero'
		}).format(pct / 100);
	}


	let unsub: (() => void) | null = null;
	onMount(() => {
		realtimeManager.connect();
		unsub = subscribeToPersonEvents(-1, (e) => {
			if (e.type === 'contradiction_added' && filter !== 'fact' && (!personFilter || e.personId === personFilter)) fresh++;
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
		<div class="head-right">
		<div class="sorts" role="tablist">
			{#each ['hot', 'new'] as s (s)}
				<button role="tab" class="sort" class:active={sort === s} aria-selected={sort === s} onclick={() => setParams({ sort: s === 'new' ? 'new' : '' })}>
					{tr(`feed.sort.${s}`, currentLang)}
				</button>
			{/each}
		</div>
		<button
			class="filter-btn"
			class:on={showFilters || active > 0}
			aria-expanded={showFilters}
			aria-controls="feed-filters"
			aria-label={tr('feed.filter.open', currentLang)}
			title={tr('feed.filter.open', currentLang)}
			onclick={() => (showFilters = !showFilters)}
		>
			<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M7 12h10M10 18h4" /></svg>
			{#if active > 0}<span class="badge">{active}</span>{/if}
		</button>
		</div>
	</header>

	{#if showFilters}
	<div class="panel" id="feed-filters">
	<div class="tabs" role="tablist">
		{#each [['', 'feed.all'], ['contradiction', 'feed.contradictions'], ['fact', 'feed.facts']] as [f, key] (f)}
			<button role="tab" class="tab" class:active={filter === f} aria-selected={filter === f} onclick={() => setFilter(f as Filter)}>
				{tr(key, currentLang)}
			</button>
		{/each}
	</div>

	<div class="filters">
		<div class="picker">
			{#if personFilter}
				<span class="chip">
					<span class="chip-name">{personName}</span>
					<button onclick={() => setParams({ person: '' })} aria-label={tr('feed.filter.clear_person', currentLang)}>×</button>
				</span>
			{:else}
				<input
					type="search"
					bind:value={query}
					oninput={onSearch}
					onkeydown={(e) => {
						if (e.key === 'Enter' && matches[0]) pickPerson(matches[0]);
						if (e.key === 'Escape') matches = [];
					}}
					placeholder={tr('feed.filter.person', currentLang)}
					aria-label={tr('feed.filter.person', currentLang)}
					autocomplete="off"
				/>
				{#if matches.length > 0}
					<ul class="matches" role="listbox">
						{#each matches as m (m.id)}
							<li><button role="option" aria-selected="false" onclick={() => pickPerson(m)}>{m.name}</button></li>
						{/each}
					</ul>
				{/if}
			{/if}
		</div>
		<label class="day">
			<span>{tr('feed.filter.from', currentLang)}</span>
			<input type="date" value={fromDay} max={toDay || undefined} onchange={(e) => setParams({ from: (e.currentTarget as HTMLInputElement).value })} />
		</label>
		<label class="day">
			<span>{tr('feed.filter.to', currentLang)}</span>
			<input type="date" value={toDay} min={fromDay || undefined} onchange={(e) => setParams({ to: (e.currentTarget as HTMLInputElement).value })} />
		</label>
		{#if active > 0}
			<button class="reset" onclick={() => setParams({ kind: '', person: '', from: '', to: '' })}>{tr('feed.filter.reset', currentLang)}</button>
		{/if}
	</div>
	</div>
	{/if}

	{#if fresh > 0}
		<button class="fresh" onclick={showFresh}>{tr('feed.new', currentLang, { n: fresh })}</button>
	{/if}

	{#if items.length === 0 && loading}
		{#each Array(5) as _}
			<div class="item skeleton-card"><div class="sk sk-head"></div><div class="sk sk-line"></div></div>
		{/each}
	{:else if items.length === 0 && error}
		<div class="state"><p>{tr('feed.error', currentLang)}</p><button class="btn" onclick={() => load(true)}>{tr('home.retry', currentLang)}</button></div>
	{:else if items.length === 0}
		<p class="state muted">{tr(filtered ? 'feed.empty_filtered' : sort === 'hot' ? 'feed.hot_empty' : 'feed.empty', currentLang)}</p>
		{#if sort === 'hot'}
			<div class="state"><button class="btn" onclick={() => setParams({ sort: 'new' })}>{tr('feed.see_new', currentLang)}</button></div>
		{/if}
	{/if}

	<ol class="items">
		{#each items as it, i (it.kind + it.id)}
			{@const topic = pickLang(it.stance_label, it.stance_label_en, currentLang)}
			{@const link = sharePath(it.kind, it.id, it.person.id)}
			<li class="item item--{it.kind}" class:is-contradiction={it.kind === 'contradiction'}>
				<span class="kind kind--{it.kind}">
					<span class="kind-mark" aria-hidden="true">{it.kind === 'contradiction' ? '⟷' : '●'}</span>
					{tr(`feed.kind.${it.kind}`, currentLang)}
				</span>
				<a href="/person/{it.person.id}" class="avatar-link" aria-label={it.person.name}>
					<TartuffeScore honesty={it.person.honesty_score} lang={currentLang} size="xs" image={it.person.image_url} initial={it.person.name.charAt(0).toUpperCase()} facts={it.person.fact_count} reliable={it.person.score_reliable} covered={it.person.covered_facts} />
				</a>
				<div class="main">
					<div class="head">
						<a href="/person/{it.person.id}" class="who"><span class="name">{it.person.name}</span></a>
						{#if it.score_effect != null}
							<!-- On a touch screen the move is a button: its title opens in a
							     sheet. The element is a button exactly when it has the click
							     handler, which the check cannot see. -->
							<!-- svelte-ignore a11y_no_static_element_interactions -->
							<svelte:element
								this={touch ? 'button' : 'span'}
								type={touch ? 'button' : undefined}
								class="ticker"
								class:down={it.score_effect < 0}
								title={tr('feed.effect', currentLang, { name: it.person.name })}
								aria-label={`${tr('feed.effect', currentLang, { name: it.person.name })} : ${move(-it.score_effect)}`}
								onclick={touch ? () => (effectHint = it) : undefined}
							>
								<span aria-hidden="true">{it.score_effect < 0 ? '▲' : '▼'}</span>
								{move(-it.score_effect)}
							</svelte:element>
						{/if}
						<time datetime={it.at} title={new Date(it.at).toLocaleString()}>{ago(it.at)}</time>
					</div>
					{#if topic}
						<a class="topic" href={it.stance_id ? `/person/${it.person.id}?stance=${it.stance_id}` : `/person/${it.person.id}`}>{topic}</a>
					{/if}

					<a href={link} class="body">
						{#if it.kind === 'contradiction' && it.other}
							<p class="line"><span class="yr">{year(it.fact.date)}</span>{pickLang(it.fact.text, it.fact.text_en, currentLang)}</p>
							<p class="line line-b"><span class="yr">{year(it.other.date)}</span>{pickLang(it.other.text, it.other.text_en, currentLang)}</p>
							{#if whyOf(it, currentLang)}
								<p class="why">{whyOf(it, currentLang)}</p>
							{/if}
						{:else}
							<p class="line"><span class="yr">{year(it.fact.date)}</span>{pickLang(it.fact.text, it.fact.text_en, currentLang)}</p>
						{/if}
					</a>

					<div class="foot">
						{#each (it.other ? [it.fact.source_urls[0], it.other.source_urls[0]] : [it.fact.source_urls[0]]).filter(Boolean) as url, i (i)}
							<a href={url} class="src" target="_blank" rel="noopener noreferrer" title={url}>{sourceDomain(url)}</a>
						{/each}
						<span class="spacer"></span>
						<LikeButton kind={it.kind} targetId={it.id} bind:likes={items[i].likes} bind:liked={items[i].liked} />
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
						{#if it.other}
							<ReportButton
								kind="contradiction"
								targetId={it.id}
								pair={{
									a: { id: it.fact.id, text: pickLang(it.fact.text, it.fact.text_en, currentLang), date: it.fact.date },
									b: { id: it.other.id, text: pickLang(it.other.text, it.other.text_en, currentLang), date: it.other.date },
									reason: whyOf(it, currentLang)
								}}
							/>
						{:else}
							<ReportButton kind="fact" targetId={it.id} preview={pickLang(it.fact.text, it.fact.text_en, currentLang)} />
						{/if}
					</div>
				</div>
			</li>
		{/each}
	</ol>

	{#if touch && effectHint && effectHint.score_effect != null}
		<HintSheet open={true} onclose={() => (effectHint = null)}>
			{tr('feed.effect', currentLang, { name: effectHint.person.name })} : <strong>{move(-effectHint.score_effect)}</strong>
		</HintSheet>
	{/if}

	<div bind:this={sentinel} class="sentinel"></div>
	{#if items.length > 0}
		{#if loading}
			<p class="state muted loading"><Spinner label={tr('person.loading', currentLang)} /></p>
		{:else if error}
			<div class="state"><button class="btn" onclick={() => load(false)}>{tr('home.retry', currentLang)}</button></div>
		{:else if ended && sort === 'hot'}
			<p class="state muted">{tr('feed.hot_end', currentLang)}</p>
			<div class="state"><button class="btn" onclick={() => { window.scrollTo({ top: 0 }); setParams({ sort: 'new' }); }}>{tr('feed.see_new', currentLang)}</button></div>
		{:else if ended}
			<p class="state muted">{tr('feed.end', currentLang)}</p>
		{:else}
			<div class="state"><button class="btn" onclick={() => load(false)}>{tr('feed.more', currentLang)}</button></div>
		{/if}
	{/if}
</div>

<style>
	.feed {
		max-width: 38rem;
		margin: 0.5rem auto 3rem;
	}
	.feed-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
		margin-bottom: 0.75rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: 2rem;
		font-weight: 600;
		margin: 0;
	}
	.head-right {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.filter-btn {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 38px;
		height: 38px;
		border-radius: 50%;
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-secondary);
		cursor: pointer;
	}
	.filter-btn:hover,
	.filter-btn.on {
		color: var(--text-primary);
		border-color: var(--border-card-hover);
	}
	.filter-btn svg {
		width: 18px;
		height: 18px;
		fill: none;
		stroke: currentColor;
		stroke-width: 2;
		stroke-linecap: round;
	}
	.badge {
		position: absolute;
		top: -3px;
		right: -3px;
		min-width: 16px;
		height: 16px;
		padding: 0 4px;
		border-radius: var(--radius-full);
		background: var(--accent);
		color: #fff;
		font-size: 0.72rem;
		font-weight: 700;
		line-height: 16px;
	}
	.panel {
		margin-bottom: 1rem;
		padding: 0.9rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
	}
	.panel .filters {
		margin-bottom: 0;
	}
	.sorts {
		display: flex;
		padding: 3px;
		border-radius: var(--radius-full);
		background: var(--bg-secondary);
		border: 1px solid var(--glass-border);
	}
	.sort {
		padding: 0.35rem 0.95rem;
		border-radius: var(--radius-full);
		border: none;
		background: transparent;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.85rem;
		font-weight: 600;
		cursor: pointer;
	}
	.sort.active {
		background: var(--bg-card);
		color: var(--text-primary);
		box-shadow: 0 1px 4px rgba(0, 0, 0, 0.3);
	}
	.tabs {
		display: flex;
		flex-wrap: wrap;
		gap: 0.3rem;
		margin-bottom: 0.6rem;
	}
	.tab {
		padding: 0.3rem 0.75rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.8rem;
		cursor: pointer;
	}
	.tab.active {
		background: var(--accent);
		border-color: var(--accent);
		color: #fff;
	}
	.filters {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.4rem 0.6rem;
		margin-bottom: 0.75rem;
		font-size: 0.8rem;
	}
	.picker {
		position: relative;
		flex: 1 1 12rem;
		min-width: 0;
	}
	.picker input,
	.day input {
		width: 100%;
		box-sizing: border-box;
		height: 30px;
		padding: 0 0.6rem;
		background: var(--bg-secondary);
		color: var(--text-primary);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
		font: inherit;
		color-scheme: dark;
	}
	.picker input:focus,
	.day input:focus {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.matches {
		position: absolute;
		top: calc(100% + 4px);
		left: 0;
		right: 0;
		z-index: 30;
		list-style: none;
		margin: 0;
		padding: 0.25rem;
		background: var(--bg-primary);
		border: 1px solid var(--border-card-hover);
		border-radius: var(--radius-md);
		box-shadow: 0 8px 24px rgba(0, 0, 0, 0.3);
	}
	.matches button {
		display: block;
		width: 100%;
		padding: 0.4rem 0.6rem;
		background: none;
		border: none;
		border-radius: var(--radius-sm);
		color: var(--text-primary);
		font: inherit;
		text-align: left;
		text-transform: capitalize;
		cursor: pointer;
	}
	.matches button:hover {
		background: var(--bg-secondary);
	}
	.chip {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		max-width: 100%;
		height: 30px;
		padding: 0 0.3rem 0 0.7rem;
		background: var(--accent);
		color: #fff;
		border-radius: var(--radius-full);
	}
	.chip-name {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		text-transform: capitalize;
	}
	.chip button {
		width: 22px;
		height: 22px;
		border: none;
		border-radius: 50%;
		background: rgba(255, 255, 255, 0.2);
		color: #fff;
		font-size: 0.9rem;
		line-height: 1;
		cursor: pointer;
	}
	.day {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		color: var(--text-muted);
	}
	.day input {
		width: 9.5rem;
	}
	.reset {
		background: none;
		border: none;
		color: var(--accent-hover);
		font: inherit;
		cursor: pointer;
		padding: 0.2rem;
	}
	/* A finger needs more than a mouse: every control of the head and of
	   the filter panel is at least 40px high. */
	@media (pointer: coarse) {
		.sort {
			min-height: 2.5rem;
		}
		.filter-btn {
			width: 2.5rem;
			height: 2.5rem;
		}
		.tab {
			min-height: 2.5rem;
			padding: 0.3rem 0.9rem;
		}
		.picker input,
		.day input,
		.chip {
			height: 2.5rem;
		}
		.chip {
			padding-right: 0;
		}
		.chip button {
			width: 2.5rem;
			height: 2.5rem;
		}
		.matches button {
			min-height: 2.5rem;
		}
		.reset {
			min-height: 2.5rem;
			padding: 0.2rem 0.6rem;
		}
	}
	.fresh {
		position: sticky;
		top: 4.5rem;
		z-index: 20;
		display: block;
		margin: 0 auto 0.75rem;
		padding: 0.4rem 0.9rem;
		border-radius: var(--radius-full);
		border: none;
		background: var(--accent);
		color: #fff;
		font: inherit;
		font-size: 0.8rem;
		cursor: pointer;
		box-shadow: var(--shadow-card);
	}
	.items {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.item {
		display: grid;
		grid-template-columns: 44px minmax(0, 1fr);
		gap: 0.85rem;
		padding: 1.1rem 1.2rem 0.8rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-left: 3px solid var(--kind-c);
		border-radius: var(--radius-lg);
		--kind-c: var(--text-muted);
	}
	.item--contradiction {
		--kind-c: var(--red);
	}
	/* What the card is, across its top: a contradiction or a fact. */
	.kind {
		grid-column: 1 / -1;
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		margin: -0.2rem 0 -0.35rem;
		color: var(--kind-c);
		font-size: 0.72rem;
		font-weight: 700;
		letter-spacing: 0.09em;
		text-transform: uppercase;
	}
	.kind-mark {
		font-size: 0.85rem;
		letter-spacing: 0;
	}
	.kind--fact .kind-mark {
		font-size: 0.55rem;
	}
	.avatar-link {
		position: relative;
		align-self: start;
	}
	.main {
		min-width: 0;
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 0.2rem 0.5rem;
		min-width: 0;
		font-size: 0.9rem;
	}
	.who {
		min-width: 0;
		overflow: hidden;
		color: var(--text-primary);
		text-decoration: none;
	}
	.who:hover .name {
		color: var(--accent-hover);
	}
	.name {
		font-weight: 600;
		text-transform: capitalize;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.ticker {
		position: relative;
		flex-shrink: 0;
		display: inline-flex;
		align-items: center;
		gap: 0.2rem;
		padding: 0;
		border: none;
		background: none;
		font-family: inherit;
		font-size: 0.75rem;
		font-weight: 700;
		font-variant-numeric: tabular-nums;
		color: var(--green);
	}
	.ticker.down {
		color: var(--red);
	}
	.ticker span {
		font-size: 0.72rem;
	}
	/* A finger: the move answers to an area of 40px around it. */
	@media (pointer: coarse) {
		.ticker::after {
			content: '';
			position: absolute;
			inset: -0.7rem -0.4rem;
		}
	}
	.topic {
		display: block;
		margin-top: 0.15rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		font-size: 0.8rem;
		color: var(--text-muted);
		text-decoration: none;
	}
	.topic:hover {
		color: var(--text-primary);
	}
	time {
		margin-left: auto;
		flex-shrink: 0;
		color: var(--text-muted);
		font-size: 0.75rem;
	}
	.body {
		display: block;
		margin-top: 0.75rem;
		color: var(--text-primary);
		text-decoration: none;
	}
	.line {
		margin: 0;
		font-size: 0.98rem;
		line-height: 1.6;
		display: -webkit-box;
		-webkit-line-clamp: 3;
		line-clamp: 3;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}
	.line-b {
		margin-top: 0.7rem;
		padding-top: 0.7rem;
		border-top: 1px dashed var(--error-border);
	}
	/* Why the two lines conflict, in full: it is what makes the pair readable. */
	.why {
		margin: 0.7rem 0 0;
		padding: 0.55rem 0.75rem;
		border-left: 2px solid var(--error-border);
		background: var(--error-bg);
		border-radius: 0 var(--radius-md) var(--radius-md) 0;
		font-size: 0.88rem;
		line-height: 1.55;
		color: var(--text-secondary);
	}
	.yr {
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--text-muted);
		margin-right: 0.4rem;
	}
	.yr:empty {
		display: none;
	}
	.foot {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		margin-top: 0.7rem;
		min-width: 0;
	}
	.src {
		font-size: 0.75rem;
		color: var(--text-muted);
		text-decoration: none;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		max-width: 11rem;
	}
	.src + .src::before {
		content: '·';
		margin-right: 0.3rem;
	}
	.src:hover {
		color: var(--accent-hover);
	}
	.spacer {
		flex: 1;
	}
	.state {
		text-align: center;
		margin: 1.25rem 0;
	}
	.muted {
		color: var(--text-secondary);
	}
	.sentinel {
		height: 1px;
	}
	.skeleton-card {
		margin-bottom: 0.5rem;
	}
	@media (max-width: 480px) {
		/* The column is the width of the small dial (TartuffeScore xs): its
		   value does not wrap, so a narrower column spills it into the text. */
		.item {
			grid-template-columns: 3.6rem minmax(0, 1fr);
			gap: 0.7rem;
			padding: 0.95rem 0.9rem 0.7rem;
		}
	}
	.sk {
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
		animation: pulse 1.4s ease-in-out infinite;
	}
	.sk-head {
		height: 0.9rem;
		width: 45%;
		margin-bottom: 0.5rem;
	}
	.sk-line {
		height: 2.2rem;
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
</style>
