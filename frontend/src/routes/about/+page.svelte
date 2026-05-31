<script lang="ts">
	import { onMount } from 'svelte';
	import { API_BASE } from '$lib/config';
	import { lang, tr } from '$lib/i18n';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => {
		const unsub = lang.subscribe((v) => (currentLang = v));
		return unsub;
	});

	interface SiteStats {
		person_count: number;
		fact_count: number;
		contradiction_count: number;
		last_fact_at: string | null;
		last_contradiction_at: string | null;
	}

	let stats: SiteStats | null = $state(null);

	function lastUpdated(s: SiteStats): string {
		const candidates = [s.last_contradiction_at, s.last_fact_at].filter(Boolean) as string[];
		if (candidates.length === 0) return tr('about.stats.never', currentLang);
		const latest = candidates.map((d) => new Date(d)).sort((a, b) => b.getTime() - a.getTime())[0];
		return latest.toLocaleDateString(currentLang === 'fr' ? 'fr-FR' : 'en-GB', {
			year: 'numeric',
			month: 'long',
			day: 'numeric'
		});
	}

	onMount(async () => {
		try {
			const res = await fetch(`${API_BASE}/api/stats`);
			if (res.ok) stats = await res.json();
		} catch {
			// Stats are non-critical; page works without them.
		}
	});
</script>

<svelte:head>
	<title>tartuffe.me — {tr('about.title', currentLang)}</title>
</svelte:head>

<div class="about">
	<header class="about-header">
		<h1>{tr('about.title', currentLang)}</h1>
		<p class="subtitle">{tr('about.subtitle', currentLang)}</p>
	</header>

	{#if stats}
		<section class="stats-row">
			<div class="stat-card">
				<div class="stat-value">{stats.person_count}</div>
				<div class="stat-label">{tr('about.stats.persons', currentLang)}</div>
			</div>
			<div class="stat-card">
				<div class="stat-value">{stats.fact_count.toLocaleString()}</div>
				<div class="stat-label">{tr('about.stats.facts', currentLang)}</div>
			</div>
			<div class="stat-card">
				<div class="stat-value">{stats.contradiction_count}</div>
				<div class="stat-label">{tr('about.stats.contradictions', currentLang)}</div>
			</div>
			<div class="stat-card stat-card--wide">
				<div class="stat-value stat-value--small">{lastUpdated(stats)}</div>
				<div class="stat-label">{tr('about.stats.last_updated', currentLang)}</div>
			</div>
		</section>
	{/if}

	<section class="section">
		<h2>{tr('about.scoring.title', currentLang)}</h2>
		<p>{tr('about.scoring.intro', currentLang)}</p>

		<div class="formula-block">
			<div class="formula-label">{tr('about.scoring.formula_label', currentLang)}</div>
			<code class="formula">
				score = exp(−1.5 · bundles / max(facts, 75) − 0.03 · bundles − 0.05 · ln(1 + extras))
			</code>
		</div>

		<h3>{tr('about.scoring.terms.title', currentLang)}</h3>
		<ul class="terms-list">
			<li>
				<code>bundles</code> — {tr('about.scoring.term.bundles', currentLang)}
			</li>
			<li>
				<code>facts</code> — {tr('about.scoring.term.facts', currentLang, { n: 75 })}
			</li>
			<li>
				<code>extras</code> — {tr('about.scoring.term.extras', currentLang)}
			</li>
		</ul>
		<p class="note">{tr('about.scoring.note', currentLang)}</p>
	</section>

	<section class="section">
		<h2>{tr('about.detection.title', currentLang)}</h2>
		<p>{tr('about.detection.body', currentLang)}</p>
	</section>

	<section class="section">
		<h2>{tr('about.sources.title', currentLang)}</h2>
		<div class="source-list">
			<div class="source-item">
				<div class="source-name">{tr('about.sources.news', currentLang)}</div>
				<p>{tr('about.sources.news.body', currentLang)}</p>
			</div>
			<div class="source-item">
				<div class="source-name">{tr('about.sources.parliament', currentLang)}</div>
				<p>{tr('about.sources.parliament.body', currentLang)}</p>
			</div>
			<div class="source-item">
				<div class="source-name">{tr('about.sources.twitter', currentLang)}</div>
				<p>{tr('about.sources.twitter.body', currentLang)}</p>
			</div>
		</div>
	</section>

	<section class="section">
		<h2>{tr('about.opendata.title', currentLang)}</h2>
		<p>{tr('about.opendata.body', currentLang)}</p>
	</section>
</div>

<style>
	.about {
		max-width: 760px;
		margin: 0 auto;
	}

	.about-header {
		margin-bottom: 2.5rem;
		padding-bottom: 1.25rem;
		border-bottom: 1px solid var(--border-card);
		position: relative;
	}

	.about-header::after {
		content: '';
		position: absolute;
		left: 0;
		bottom: -1px;
		width: 3rem;
		height: 1px;
		background: var(--accent);
	}

	.about-header h1 {
		font-family: var(--font-serif);
		font-size: 2rem;
		font-weight: 600;
		letter-spacing: 0.005em;
		line-height: 1.2;
		margin-bottom: 0.5rem;
	}

	.subtitle {
		color: var(--text-secondary);
		font-size: 0.9375rem;
		font-style: italic;
	}

	/* Stats row */
	.stats-row {
		display: grid;
		grid-template-columns: repeat(4, 1fr);
		gap: 1rem;
		margin-bottom: 2.5rem;
	}

	@media (max-width: 640px) {
		.stats-row {
			grid-template-columns: 1fr 1fr;
		}
	}

	.stat-card {
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		padding: 1.25rem;
		text-align: center;
	}

	.stat-value {
		font-size: 1.75rem;
		font-weight: 700;
		color: var(--accent);
		line-height: 1.1;
	}

	.stat-value--small {
		font-size: 1rem;
		font-weight: 600;
	}

	.stat-label {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-top: 0.375rem;
	}

	/* Sections */
	.section {
		margin-bottom: 2.5rem;
	}

	.section h2 {
		font-size: 1.25rem;
		font-weight: 700;
		letter-spacing: -0.01em;
		margin-bottom: 0.75rem;
		color: var(--text-primary);
	}

	.section h3 {
		font-size: 1rem;
		font-weight: 600;
		margin-top: 1.25rem;
		margin-bottom: 0.5rem;
		color: var(--text-primary);
	}

	.section p {
		color: var(--text-secondary);
		font-size: 0.9375rem;
		line-height: 1.65;
	}

	/* Formula */
	.formula-block {
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		padding: 1rem 1.25rem;
		margin: 1rem 0;
	}

	.formula-label {
		font-size: 0.75rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 0.06em;
		color: var(--text-muted);
		margin-bottom: 0.5rem;
	}

	.formula {
		display: block;
		font-family: var(--font-mono, 'ui-monospace', 'Cascadia Code', monospace);
		font-size: 0.875rem;
		color: var(--text-primary);
		white-space: pre-wrap;
		word-break: break-all;
	}

	/* Terms list */
	.terms-list {
		list-style: none;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0;
	}

	.terms-list li {
		font-size: 0.9375rem;
		color: var(--text-secondary);
		line-height: 1.5;
	}

	.terms-list code {
		font-family: var(--font-mono, 'ui-monospace', monospace);
		font-size: 0.8125rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border-card);
		border-radius: 3px;
		padding: 0.1em 0.35em;
		color: var(--accent);
	}

	.note {
		margin-top: 0.875rem;
		font-size: 0.875rem !important;
		color: var(--text-muted) !important;
		font-style: italic;
	}

	/* Sources */
	.source-list {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.source-item {
		padding: 1rem 1.25rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
	}

	.source-name {
		font-weight: 600;
		font-size: 0.9375rem;
		color: var(--text-primary);
		margin-bottom: 0.375rem;
	}

	.source-item p {
		margin: 0;
		font-size: 0.875rem;
		color: var(--text-secondary);
		line-height: 1.55;
	}
</style>
