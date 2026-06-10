<script lang="ts">
	import { onMount } from 'svelte';
	import { API_BASE } from '$lib/config';
	import { lang, tr, type Lang } from '$lib/i18n';

	interface ActivityPerson {
		id: number;
		name: string;
		image_url?: string;
		honesty_score: number;
	}

	interface ActivityEvent {
		id: number;
		topic: string;
		entity_type: string;
		entity_id: number;
		payload: Record<string, unknown>;
		created_at: string;
		person?: ActivityPerson;
	}

	interface ActivityFeed {
		items: ActivityEvent[];
		total: number;
	}

	let currentLang = $state<Lang>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	let items: ActivityEvent[] = $state([]);
	let total = $state(0);
	let loading = $state(true);
	let loadingMore = $state(false);
	let error: string | null = $state(null);
	const pageSize = 20;

	async function load(offset = 0) {
		try {
			const res = await fetch(`${API_BASE}/api/activity?limit=${pageSize}&offset=${offset}`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const data: ActivityFeed = await res.json();
			if (offset === 0) {
				items = data.items;
			} else {
				items = [...items, ...data.items];
			}
			total = data.total;
		} catch (err) {
			error = err instanceof Error ? err.message : 'An error occurred';
		}
	}

	async function loadMore() {
		loadingMore = true;
		await load(items.length);
		loadingMore = false;
	}

	onMount(async () => {
		await load();
		loading = false;
	});

	function formatDate(s: string): string {
		return new Date(s).toLocaleString([], {
			year: 'numeric',
			month: 'short',
			day: 'numeric',
			hour: '2-digit',
			minute: '2-digit'
		});
	}

	function getScoreColor(score: number): string {
		if (score >= 0.8) return 'var(--green)';
		if (score >= 0.4) return 'var(--amber)';
		return 'var(--red)';
	}

	function topicLabel(topic: string, l: Lang): string {
		if (topic === 'contradiction_added') return tr('activity.contradiction_added', l);
		if (topic === 'person_updated') return tr('activity.score_updated', l);
		return topic;
	}
</script>

<svelte:head>
	<title>{tr('nav.activity', currentLang)} - tartuffe.me</title>
	<meta property="og:title" content="Activity — tartuffe.me" />
	<meta property="og:description" content="Recent contradictions detected and honesty score updates." />
</svelte:head>

<div class="activity-page">
	<h1 class="page-title">{tr('nav.activity', currentLang)}</h1>
	<p class="page-sub">{tr('activity.subtitle', currentLang)}</p>

	{#if loading}
		<div class="feed">
			{#each Array(8) as _}
				<div class="event-row skeleton-row">
					<div class="skeleton skeleton-avatar-sm"></div>
					<div class="event-body">
						<div class="skeleton skeleton-line-long"></div>
						<div class="skeleton skeleton-line-short"></div>
					</div>
				</div>
			{/each}
		</div>
	{:else if error}
		<div class="error-banner"><p>{error}</p></div>
	{:else if items.length === 0}
		<p class="empty">{tr('activity.empty', currentLang)}</p>
	{:else}
		<div class="feed">
			{#each items as event (event.id)}
				<div class="event-row">
					{#if event.person}
						<a href="/person/{event.person.id}" class="avatar-link" tabindex="-1" aria-hidden="true">
							{#if event.person.image_url}
								<img src={event.person.image_url} alt={event.person.name} class="event-avatar" />
							{:else}
								<div class="event-avatar event-avatar-placeholder">
									{event.person.name.charAt(0).toUpperCase()}
								</div>
							{/if}
						</a>
					{:else}
						<div class="event-avatar event-avatar-placeholder">?</div>
					{/if}
					<div class="event-body">
						<div class="event-headline">
							<span class="topic-chip" class:topic-contradiction={event.topic === 'contradiction_added'} class:topic-update={event.topic === 'person_updated'}>
								{topicLabel(event.topic, currentLang)}
							</span>
							{#if event.person}
								<a href="/person/{event.person.id}" class="person-link">{event.person.name}</a>
							{/if}
							{#if event.topic === 'person_updated' && event.payload?.honesty_score != null}
								<span class="score-chip" style="color: {getScoreColor(event.payload.honesty_score as number)}">
									{((event.payload.honesty_score as number) * 100).toFixed(0)}%
								</span>
							{/if}
						</div>
						<div class="event-time">{formatDate(event.created_at)}</div>
					</div>
				</div>
			{/each}
		</div>

		{#if items.length < total}
			<div class="load-more-wrap">
				<button class="load-more-btn" onclick={loadMore} disabled={loadingMore}>
					{loadingMore ? tr('person.loading', currentLang) : tr('home.load_more', currentLang)}
				</button>
			</div>
		{/if}
	{/if}
</div>

<style>
	.activity-page {
		max-width: 680px;
		margin: 0 auto;
	}

	.page-title {
		font-size: 1.5rem;
		font-weight: 600;
		margin: 0 0 0.25rem;
		color: var(--text-primary);
	}

	.page-sub {
		font-size: 0.875rem;
		color: var(--text-muted);
		margin: 0 0 2rem;
	}

	.feed {
		display: flex;
		flex-direction: column;
		gap: 0;
	}

	.event-row {
		display: flex;
		align-items: flex-start;
		gap: 0.875rem;
		padding: 0.875rem 0;
		border-bottom: 1px solid var(--border-card);
	}

	.event-row:last-child {
		border-bottom: none;
	}

	.avatar-link {
		flex-shrink: 0;
	}

	.event-avatar {
		width: 36px;
		height: 36px;
		border-radius: var(--radius-full);
		object-fit: cover;
		flex-shrink: 0;
	}

	.event-avatar-placeholder {
		width: 36px;
		height: 36px;
		border-radius: var(--radius-full);
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		display: flex;
		align-items: center;
		justify-content: center;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-muted);
		flex-shrink: 0;
	}

	.event-body {
		flex: 1;
		min-width: 0;
	}

	.event-headline {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		font-size: 0.875rem;
	}

	.topic-chip {
		font-size: 0.7rem;
		font-weight: 600;
		letter-spacing: 0.04em;
		padding: 0.15rem 0.45rem;
		border-radius: var(--radius-full);
		text-transform: uppercase;
	}

	.topic-contradiction {
		background: rgba(194, 51, 74, 0.15);
		color: var(--red);
	}

	.topic-update {
		background: rgba(95, 165, 106, 0.15);
		color: var(--green);
	}

	.person-link {
		font-weight: 500;
		color: var(--text-primary);
	}

	.person-link:hover {
		color: var(--accent);
	}

	.score-chip {
		font-size: 0.8rem;
		font-weight: 600;
	}

	.event-time {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-top: 0.25rem;
	}

	.skeleton-row {
		padding: 0.875rem 0;
		border-bottom: 1px solid var(--border-card);
	}

	.skeleton {
		background: linear-gradient(90deg, var(--bg-card) 25%, var(--bg-secondary) 50%, var(--bg-card) 75%);
		background-size: 200% 100%;
		animation: shimmer 1.5s infinite;
		border-radius: var(--radius-sm);
	}

	@keyframes shimmer {
		0% { background-position: 200% 0; }
		100% { background-position: -200% 0; }
	}

	.skeleton-avatar-sm {
		width: 36px;
		height: 36px;
		border-radius: var(--radius-full);
		flex-shrink: 0;
	}

	.skeleton-line-long {
		height: 14px;
		width: 70%;
		margin-bottom: 6px;
	}

	.skeleton-line-short {
		height: 12px;
		width: 30%;
	}

	.empty {
		color: var(--text-muted);
		font-size: 0.875rem;
		text-align: center;
		padding: 3rem 0;
	}

	.error-banner {
		background: rgba(194, 51, 74, 0.1);
		border: 1px solid var(--red);
		border-radius: var(--radius-sm);
		padding: 1rem;
		color: var(--red);
		font-size: 0.875rem;
	}

	.load-more-wrap {
		display: flex;
		justify-content: center;
		padding: 1.5rem 0;
	}

	.load-more-btn {
		padding: 0.5rem 1.25rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--text-secondary);
		font-size: 0.875rem;
		font-family: var(--font-family);
		cursor: pointer;
		transition: all var(--transition-fast);
	}

	.load-more-btn:hover:not(:disabled) {
		border-color: var(--accent);
		color: var(--accent);
	}

	.load-more-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
</style>
