<script lang="ts">
	// The public log of corrections: every fact or contradiction removed after
	// a report a person accepted, with the reason (GET /api/corrections). Neither the
	// reporter nor their words are shown.
	import Spinner from '$lib/components/Spinner.svelte';
	import { onMount } from 'svelte';
	import { API_BASE } from '$lib/config';
	import { lang, tr, type Lang } from '$lib/i18n';
	import type { Correction } from '$lib/types';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let items: Correction[] = $state([]);
	let next: number | undefined = $state(undefined);
	let loading = $state(true);
	let error = $state(false);

	async function load(more: boolean) {
		loading = true;
		error = false;
		try {
			const q = new URLSearchParams({ limit: '30' });
			if (more && next) q.set('before', String(next));
			const res = await fetch(`${API_BASE}/api/corrections?${q}`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			const p: { items: Correction[]; next?: number } = await res.json();
			items = more ? [...items, ...p.items] : p.items;
			next = p.next;
		} catch (err) {
			console.error('failed to load the corrections', err);
			error = true;
		} finally {
			loading = false;
		}
	}

	function day(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}

	onMount(() => load(false));
</script>

<svelte:head>
	<title>{tr('corrections.title', currentLang)} — tartuffe.me</title>
</svelte:head>

<div class="corrections">
	<h1>{tr('corrections.title', currentLang)}</h1>
	<p class="intro">{tr('corrections.intro', currentLang)} <a href="/about">{tr('score.more', currentLang)}</a></p>

	{#if items.length === 0 && loading}
		<p class="muted loading"><Spinner label={tr('person.loading', currentLang)} /></p>
	{:else if items.length === 0 && error}
		<div class="card"><p>{tr('feed.error', currentLang)}</p><button class="btn" onclick={() => load(false)}>{tr('home.retry', currentLang)}</button></div>
	{:else if items.length === 0}
		<p class="card muted">{tr('corrections.empty', currentLang)}</p>
	{:else}
		<ol class="list">
			{#each items as c (c.id)}
				<li class="card">
					<div class="head">
						<span class="kind">{tr(`corrections.kind.${c.target_kind}`, currentLang)}</span>
						<a href="/person/{c.person_id}" class="who">{c.person_name}</a>
						<time datetime={c.corrected_at}>{tr('corrections.on', currentLang, { d: day(c.corrected_at) })}</time>
					</div>
					<blockquote>{c.target_text}</blockquote>
					{#if c.verdict_reason}
						<p class="reason"><strong>{tr('corrections.reason', currentLang)} :</strong> {c.verdict_reason}</p>
					{/if}
				</li>
			{/each}
		</ol>
		{#if next}
			<div class="more"><button class="btn" onclick={() => load(true)} disabled={loading}>{tr('feed.more', currentLang)}</button></div>
		{/if}
	{/if}
</div>

<style>
	.corrections {
		max-width: 44rem;
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
		line-height: 1.55;
		margin: 0.5rem 0 1.5rem;
	}
	.list {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.8rem;
	}
	.card {
		padding: 1rem 1.15rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
	}
	.head {
		display: flex;
		flex-wrap: wrap;
		align-items: baseline;
		gap: 0.35rem 0.75rem;
		font-size: 0.82rem;
	}
	.kind {
		color: var(--green);
		font-weight: 600;
	}
	.who {
		color: var(--text-primary);
		text-transform: capitalize;
	}
	time {
		color: var(--text-muted);
	}
	blockquote {
		margin: 0.6rem 0 0;
		padding: 0.55rem 0.8rem;
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
		text-decoration: line-through;
		text-decoration-color: color-mix(in srgb, var(--text-muted) 60%, transparent);
		color: var(--text-secondary);
		line-height: 1.5;
		font-size: 0.9rem;
	}
	.reason {
		margin: 0.6rem 0 0;
		font-size: 0.86rem;
		line-height: 1.5;
	}
	.muted {
		color: var(--text-secondary);
	}
	.more {
		text-align: center;
		margin-top: 1.25rem;
	}
</style>
