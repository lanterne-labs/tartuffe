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
	import { factYear } from '$lib/dates';
	import { personPath, sharePath } from '$lib/share';
	import { realtimeManager, subscribeToPersonEvents } from '$lib/realtime';
	import ShareMenu from '$lib/components/ShareMenu.svelte';
	import ReportButton from '$lib/components/ReportButton.svelte';
	import LikeButton from '$lib/components/LikeButton.svelte';
	import HintSheet from '$lib/components/HintSheet.svelte';
	import { authFetch, session } from '$lib/auth';
	import type { FeedItem, FeedPage, PersonSummary } from '$lib/types';
	import ClashCard from '$lib/components/ClashCard.svelte';
	import { loadCandidates, loadStreaks, type Streak } from '$lib/streaks';
	import { signInOpen } from '$lib/auth';
	import { displayName, clip } from '$lib/share';

	// What the feed lists: the contradictions (the default, no `kind` in the
	// URL), the facts, or both together (`kind=all`).
	type Filter = 'contradiction' | 'fact' | 'all';

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
	let filter = $derived<Filter>($page.url.searchParams.get('kind') === 'fact' ? 'fact' : $page.url.searchParams.get('kind') === 'all' ? 'all' : 'contradiction');
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
	let active = $derived([personFilter, fromDay, toDay].filter(Boolean).length);
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
			if (filter !== 'all') q.set('kind', filter);
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
		setParams({ kind: f === 'contradiction' ? '' : f });
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


	// move is a market-style move of the score: "+0,5 %", "−1,3 %".
	function move(pct: number): string {
		return new Intl.NumberFormat(currentLang === 'en' ? 'en-GB' : 'fr-FR', {
			style: 'percent',
			minimumFractionDigits: 1,
			maximumFractionDigits: 1,
			signDisplay: 'exceptZero'
		}).format(pct / 100);
	}


	// Beside the list, as on a social network: the candidates to filter by,
	// their days without a contradiction, and the contradiction the users
	// backed the most this week (one of the week drawn at random while none
	// has a +1).
	let candidates: PersonSummary[] = $state([]);
	let streaks: Streak[] = $state([]);
	let pick = $state<{ item: FeedItem; backed: boolean } | null>(null);
	async function loadSide() {
		try {
			candidates = await loadCandidates();
			streaks = await loadStreaks(candidates, currentLang);
		} catch (err) {
			console.error('failed to load the candidates of the feed', err);
		}
		try {
			const res = await fetch(`${API_BASE}/api/feed?sort=hot&kind=contradiction&limit=30`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const week = ((await res.json()) as FeedPage).items.filter((it) => it.other && Date.now() - new Date(it.at).getTime() < 7 * 86_400_000);
			const top = week.reduce<FeedItem | null>((best, it) => (it.likes > (best?.likes ?? 0) ? it : best), null);
			if (top) pick = { item: top, backed: true };
			else if (week.length > 0) pick = { item: week[Math.floor(Math.random() * week.length)], backed: false };
		} catch (err) {
			console.error('failed to load the contradiction of the week', err);
		}
	}

	// The day of an item, as the separators of the newest-first list say it.
	function dayLabel(iso: string): string {
		const d = new Date(iso);
		const days = Math.round((new Date(new Date().toDateString()).getTime() - new Date(d.toDateString()).getTime()) / 86_400_000);
		if (days <= 1) {
			const t = new Intl.RelativeTimeFormat(currentLang === 'en' ? 'en' : 'fr', { numeric: 'auto' }).format(-days, 'day');
			return t.charAt(0).toUpperCase() + t.slice(1);
		}
		const t = d.toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { weekday: 'long', day: 'numeric', month: 'long' });
		return t.charAt(0).toUpperCase() + t.slice(1);
	}

	let unsub: (() => void) | null = null;
	onMount(() => {
		loadSide();
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

<div class="feed-grid">
<aside class="side" aria-label={tr('feed.side', currentLang)}>
	{#if streaks.length > 0}
		<div class="side-card">
			<h2>{tr('streak.title', currentLang)}</h2>
			<ul class="streaks">
				{#each streaks.slice(0, 6) as s (s.person.id)}
					<li>
						<a href={personPath(s.person.slug)}>
							{#if s.person.image_url}<img src={s.person.image_url} alt="" loading="lazy" />{:else}<span class="ini">{s.person.name.charAt(0).toUpperCase()}</span>{/if}
							<span class="streak-name">{displayName(s.person.name)}</span>
							{#if s.since}
								<span class="streak-since">{s.since}</span>
							{:else}
								<span class="streak-days" class:fresh-day={s.days < 1}>{s.days}<small> {tr('streak.unit', currentLang)}</small></span>
							{/if}
						</a>
					</li>
				{/each}
			</ul>
			<a class="side-link" href="/dashboard#streaks">{tr('streak.all', currentLang)}</a>
		</div>
	{/if}
	{#if pick}
		<a class="side-card side-pick" href={sharePath('contradiction', pick.item.id, pick.item.person.slug)}>
			<h2>{tr(pick.backed ? 'feed.side.backed' : 'feed.side.pick', currentLang)}</h2>
			<p class="pick-text">{clip(whyOf(pick.item, currentLang) || pickLang(pick.item.fact.text, pick.item.fact.text_en, currentLang), 220)}</p>
			<p class="pick-who">{displayName(pick.item.person.name)}{#if pick.backed} · {pick.item.likes} +1{/if}</p>
		</a>
	{/if}
	{#if !$session}
		<div class="side-card side-signin">
			<p>{tr('feed.side.signin', currentLang)}</p>
			<button class="pill-btn pill-btn--ghost pill-btn--sm" onclick={() => signInOpen.set(true)}>{tr('auth.title', currentLang)}</button>
		</div>
	{/if}
</aside>
<div class="feed">
	<header class="feed-head">
		<div>
			<h1><span class="live-dot"></span>{tr('feed.title', currentLang)}</h1>
			<p class="feed-sub">{tr('feed.sub', currentLang)}</p>
		</div>
		<div class="head-right">
		<div class="tabs" role="tablist">
			{#each ['hot', 'new'] as s (s)}
				<button role="tab" class:on={sort === s} aria-selected={sort === s} onclick={() => setParams({ sort: s === 'new' ? 'new' : '' })}>
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

	<!-- What the feed lists, always in view: the contradictions first. -->
	{#if candidates.length > 0}
		<div class="people" role="group" aria-label={tr('feed.filter.person', currentLang)}>
			<button class:on={!personFilter} onclick={() => setParams({ person: '' })}>
				<span class="ini all"><svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 6h16M4 12h16M4 18h10" /></svg></span>
				{tr('home.filter.all', currentLang)}
			</button>
			{#each candidates as p (p.id)}
				<button class:on={personFilter === p.id} onclick={() => setParams({ person: personFilter === p.id ? '' : String(p.id) })} title={displayName(p.name)}>
					{#if p.image_url}<img src={p.image_url} alt="" loading="lazy" />{:else}<span class="ini">{p.name.charAt(0).toUpperCase()}</span>{/if}
					{displayName(p.name).split(' ').slice(-1)[0]}
				</button>
			{/each}
		</div>
	{/if}

	<div class="kinds tabs" role="tablist" aria-label={tr('feed.kinds', currentLang)}>
		{#each [['contradiction', 'feed.contradictions'], ['fact', 'feed.facts'], ['all', 'feed.all']] as [f, key] (f)}
			<button role="tab" class:on={filter === f} aria-selected={filter === f} onclick={() => setFilter(f as Filter)}>
				{tr(key, currentLang)}
			</button>
		{/each}
	</div>

	{#if showFilters}
	<div class="panel" id="feed-filters">
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
			<button class="reset" onclick={() => setParams({ person: '', from: '', to: '' })}>{tr('feed.filter.reset', currentLang)}</button>
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
			{@const link = sharePath(it.kind, it.id, it.person.slug)}
			{#if sort === 'new' && (i === 0 || dayLabel(it.at) !== dayLabel(items[i - 1].at))}
				<li class="day-sep">{dayLabel(it.at)}</li>
			{/if}
			{#if it.kind === 'contradiction' && it.other}
			<li class="clash-li"><ClashCard bind:item={items[i]} lang={currentLang} /></li>
			{:else}
			<li class="item item--{it.kind}" class:is-contradiction={it.kind === 'contradiction'}>
				<span class="kind kind--{it.kind}">
					<span class="kind-mark" aria-hidden="true">{it.kind === 'contradiction' ? '⟷' : '●'}</span>
					{tr(`feed.kind.${it.kind}`, currentLang)}
				</span>
				<a href={personPath(it.person.slug)} class="avatar-link" aria-label={it.person.name}>
					<TartuffeScore honesty={it.person.honesty_score} lang={currentLang} size="xs" image={it.person.image_url} initial={it.person.name.charAt(0).toUpperCase()} facts={it.person.fact_count} reliable={it.person.score_reliable} />
				</a>
				<div class="main">
					<div class="head">
						<a href={personPath(it.person.slug)} class="who"><span class="name">{it.person.name}</span></a>
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
						<a class="topic" href={it.stance_id ? sharePath('stance', it.stance_id, it.person.slug) : personPath(it.person.slug)}>{topic}</a>
					{/if}

					<a href={link} class="body">
						{#if it.kind === 'contradiction' && it.other}
							<!-- Why they conflict first, then the two facts it is about. -->
							{#if whyOf(it, currentLang)}
								<p class="why">{whyOf(it, currentLang)}</p>
							{/if}
							<p class="line"><span class="yr">{factYear(it.fact.date, it.fact.date_precision)}</span>{pickLang(it.fact.text, it.fact.text_en, currentLang)}</p>
							<p class="line line-b"><span class="yr">{factYear(it.other.date, it.other.date_precision)}</span>{pickLang(it.other.text, it.other.text_en, currentLang)}</p>
						{:else}
							<p class="line"><span class="yr">{factYear(it.fact.date, it.fact.date_precision)}</span>{pickLang(it.fact.text, it.fact.text_en, currentLang)}</p>
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
								personSlug: it.person.slug,
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
									a: { id: it.fact.id, text: pickLang(it.fact.text, it.fact.text_en, currentLang), date: it.fact.date, precision: it.fact.date_precision },
									b: { id: it.other.id, text: pickLang(it.other.text, it.other.text_en, currentLang), date: it.other.date, precision: it.other.date_precision },
									reason: whyOf(it, currentLang)
								}}
							/>
						{:else}
							<ReportButton kind="fact" targetId={it.id} preview={pickLang(it.fact.text, it.fact.text_en, currentLang)} />
						{/if}
					</div>
				</div>
			</li>
			{/if}
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
	/* The switch of what is listed: one track, three choices. */
	.kinds {
		display: flex;
		gap: 0.2rem;
		margin-bottom: 1rem;
		padding: 0.2rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-full);
		background: var(--bg-card);
	}
	.kind-tab {
		flex: 1 1 0;
		min-width: 0;
		padding: 0.45rem 0.5rem;
		border: none;
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--text-secondary);
		font: inherit;
		font-size: 0.85rem;
		font-weight: 600;
		cursor: pointer;
		transition: background var(--transition-fast), color var(--transition-fast);
	}
	.kind-tab:hover {
		color: var(--text-primary);
	}
	.kind-tab.active {
		background: var(--accent);
		color: var(--on-accent);
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
		.kind-tab {
			min-height: 2.5rem;
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
		margin: 0 0 0.7rem;
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
	/* ---------- The layout of a social feed: a column beside the list ---------- */
	.feed-grid {
		display: grid;
		grid-template-columns: 19rem minmax(0, 44rem);
		justify-content: center;
		gap: 2rem;
		align-items: start;
		text-align: left;
	}
	.feed {
		max-width: none;
		width: 100%;
		margin: 0;
	}
	.feed-head {
		align-items: flex-end;
	}
	.feed-head h1 {
		display: flex;
		align-items: center;
		gap: 0.8rem;
		font-size: clamp(2.2rem, 5vw, 3.4rem);
		font-weight: 800;
		letter-spacing: -0.04em;
		line-height: 1;
		text-align: left;
	}
	.feed-sub {
		margin-top: 0.5rem;
		color: var(--text-secondary);
	}
	.kinds.tabs {
		display: inline-flex;
		margin: 0 0 1rem;
		border-bottom: 1px solid var(--border-card);
	}
	/* The candidates, a row of portraits: a tap filters the feed. */
	.people {
		display: flex;
		gap: 0.9rem;
		margin: 0 0 1.1rem;
		padding: 0.3rem 0.1rem 0.5rem;
		overflow-x: auto;
		scrollbar-width: none;
	}
	.people::-webkit-scrollbar {
		display: none;
	}
	.people button {
		flex-shrink: 0;
		display: grid;
		justify-items: center;
		gap: 0.35rem;
		width: 3.6rem;
		padding: 0;
		border: 0;
		background: none;
		font: inherit;
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--text-muted);
		cursor: pointer;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.people img,
	.people .ini {
		width: 3rem;
		height: 3rem;
		border-radius: 50%;
		object-fit: cover;
		border: 2px solid transparent;
		outline: 1px solid var(--border-card-hover);
		transition: transform 0.2s var(--ease), border-color 0.2s;
	}
	.ini {
		display: grid;
		place-items: center;
		background: var(--bg-elevated);
		font-weight: 700;
		color: var(--text-primary);
	}
	.ini.all svg {
		width: 1.2rem;
		height: 1.2rem;
		fill: none;
		stroke: currentColor;
		stroke-width: 1.8;
		stroke-linecap: round;
	}
	.people button:hover img,
	.people button:hover .ini {
		transform: scale(1.06);
	}
	.people button.on {
		color: var(--text-primary);
	}
	.people button.on img,
	.people button.on .ini {
		border-color: var(--bg-primary);
		outline: 2px solid var(--accent);
	}
	.items {
		gap: 0.9rem;
	}
	.clash-li {
		list-style: none;
	}
	.day-sep {
		list-style: none;
		margin-top: 0.6rem;
		font-size: 0.8rem;
		font-weight: 700;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	/* A fact of the feed: a card that wakes under the pointer, as a contradiction. */
	.item {
		border-radius: var(--radius-lg);
		transition: border-color 0.2s, transform 0.25s var(--ease), background 0.2s;
	}
	@media (hover: hover) {
		.item:hover {
			border-color: var(--border-card-hover);
			background: var(--bg-card-hover);
			transform: translateY(-2px);
		}
	}
	/* ---------- The column ---------- */
	.side {
		position: sticky;
		top: 5.5rem;
		display: grid;
		gap: 1rem;
	}
	.side-card {
		display: grid;
		gap: 0.6rem;
		padding: 1.2rem;
		border-radius: var(--radius-lg);
		border: 1px solid var(--border-card);
		background: var(--bg-card);
		box-shadow: var(--shadow-card);
		color: var(--text-primary);
	}
	.side-card h2 {
		font-size: 1rem;
		font-weight: 700;
		letter-spacing: -0.02em;
		text-align: left;
	}
	.streaks {
		display: grid;
		gap: 0.1rem;
		list-style: none;
	}
	.streaks a {
		display: flex;
		align-items: center;
		gap: 0.7rem;
		padding: 0.4rem 0.3rem;
		border-radius: 0.6rem;
		color: var(--text-primary);
	}
	.streaks a:hover {
		background: rgba(255, 255, 255, 0.05);
	}
	.streaks img,
	.streaks .ini {
		width: 2rem;
		height: 2rem;
		border-radius: 50%;
		object-fit: cover;
		flex-shrink: 0;
		font-size: 0.75rem;
	}
	.streak-name {
		flex: 1;
		min-width: 0;
		font-size: 0.9rem;
		font-weight: 600;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.streak-days {
		font-family: var(--font-num);
		font-weight: 800;
		font-size: 1.5rem;
		line-height: 1;
		color: var(--gold);
	}
	.streak-days.fresh-day {
		color: var(--accent-hover);
	}
	.streak-days small {
		font-family: var(--font-family);
		font-size: 0.75rem;
		font-weight: 500;
		color: var(--text-muted);
	}
	.streak-since {
		font-size: 0.78rem;
		color: var(--text-muted);
		white-space: nowrap;
	}
	.side-link {
		justify-self: start;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--text-secondary);
		border-bottom: 1px solid var(--border-card-hover);
	}
	.side-link:hover {
		color: var(--text-primary);
	}
	.side-pick {
		transition: border-color 0.2s, transform 0.25s var(--ease);
	}
	.side-pick:hover {
		border-color: rgba(229, 56, 79, 0.3);
		transform: translateY(-2px);
		color: var(--text-primary);
	}
	.pick-text {
		font-size: 0.95rem;
		line-height: 1.45;
	}
	.pick-who {
		font-size: 0.82rem;
		color: var(--text-muted);
	}
	.side-signin {
		border-style: dashed;
		border-color: var(--border-card-hover);
		background: none;
		box-shadow: none;
		justify-items: start;
	}
	.side-signin p {
		font-size: 0.9rem;
		color: var(--text-secondary);
	}
	@media (max-width: 1000px) {
		.feed-grid {
			grid-template-columns: minmax(0, 44rem);
		}
		.side {
			display: none;
		}
	}
</style>
