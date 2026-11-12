<script lang="ts">
	// A contradiction as a card: who and on what, the two facts face to face
	// (the older first, each with the date of the statement at its precision
	// and its source), the lightning between them, why they conflict, then
	// the +1, the share and the report. The feed, the home page and its
	// featured card (`feature`) draw it. The texts are the facts as the site
	// words them: never set as the person's own words.
	import TartuffeScore from './TartuffeScore.svelte';
	import LikeButton from './LikeButton.svelte';
	import ShareMenu from './ShareMenu.svelte';
	import ReportButton from './ReportButton.svelte';
	import { tr, pickLang, whyOf, type Lang } from '$lib/i18n';
	import { sourceDomain } from '$lib/sources';
	import { factYear, statementDateText } from '$lib/dates';
	import { personPath, sharePath, displayName } from '$lib/share';
	import type { FeedItem, FeedFactSide } from '$lib/types';

	interface Props {
		item: FeedItem;
		lang: Lang;
		/** The featured card of the home page: larger, in the curtain's glow. */
		feature?: boolean;
		/** The +1, share and report under the card. */
		actions?: boolean;
	}
	let { item = $bindable(), lang, feature = false, actions = true }: Props = $props();

	// The older fact first, as a contradiction reads.
	let sides = $derived.by(() => {
		const [x, y] = [item.fact, item.other ?? item.fact];
		return (x.date ?? '') <= (y.date ?? '') ? [x, y] : [y, x];
	});
	let topic = $derived(pickLang(item.stance_label, item.stance_label_en, lang));
	let link = $derived(sharePath('contradiction', item.id, item.person.slug));
	let why = $derived(whyOf(item, lang));
	// The score's move, as a market shows one: the Tartuffe-Score rises when
	// the honesty score falls.
	let rise = $derived(item.score_effect != null && item.score_effect < 0 ? Math.max(1, Math.round(-item.score_effect)) : 0);

	function dateOf(f: FeedFactSide): string {
		return statementDateText(f.date, f.date_precision, lang) || factYear(f.date, f.date_precision);
	}
	function textOf(f: FeedFactSide): string {
		return pickLang(f.text, f.text_en, lang);
	}
	function ago(iso: string): string {
		const rtf = new Intl.RelativeTimeFormat(lang === 'en' ? 'en' : 'fr', { numeric: 'auto', style: 'short' });
		const steps: [number, Intl.RelativeTimeFormatUnit][] = [[60, 'second'], [60, 'minute'], [24, 'hour'], [7, 'day'], [4.35, 'week'], [12, 'month']];
		let v = (new Date(iso).getTime() - Date.now()) / 1000;
		for (const [n, unit] of steps) {
			if (Math.abs(v) < n) return rtf.format(Math.round(v), unit);
			v /= n;
		}
		return rtf.format(Math.round(v), 'year');
	}
</script>

<article class="clash" class:feature>
	<header class="who">
		<a class="who-l" href={personPath(item.person.slug)}>
			<TartuffeScore honesty={item.person.honesty_score} {lang} size="xs" named={false} help={false} image={item.person.image_url} initial={item.person.name.charAt(0).toUpperCase()} facts={item.person.fact_count} reliable={item.person.score_reliable} />
			<span class="who-name">
				<strong>{displayName(item.person.name)}</strong>
				<span class="who-meta">
					{#if topic}<b>{topic}</b>{/if}
					<time datetime={item.at} title={new Date(item.at).toLocaleString()}>{ago(item.at)}</time>
				</span>
			</span>
		</a>
		{#if rise > 0}
			<span class="rise" title={tr('feed.effect', lang, { name: displayName(item.person.name) })}>
				<span aria-hidden="true">▲</span> +{rise}
			</span>
		{/if}
	</header>

	<a href={link} class="body">
		{#each sides as f, i (f.id)}
			{#if i === 1}
				<div class="bolt" aria-hidden="true"><svg viewBox="0 0 24 24"><path d="M13 2 4 14h7l-1 8 9-12h-7z" /></svg></div>
			{/if}
			<div class="side" class:later={i === 1}>
				<span class="side-date">{dateOf(f)}{#if f.source_urls?.[0]}<span>{sourceDomain(f.source_urls[0])}</span>{/if}</span>
				<p>{textOf(f)}</p>
			</div>
		{/each}
		{#if why}
			<p class="why">{why}</p>
		{/if}
	</a>

	{#if actions}
		<footer class="actions">
			<LikeButton kind="contradiction" targetId={item.id} bind:likes={item.likes} bind:liked={item.liked} />
			<ShareMenu
				item={{
					kind: 'contradiction',
					id: item.id,
					personSlug: item.person.slug,
					personName: item.person.name,
					topic,
					text: textOf(item.fact),
					other: item.other ? textOf(item.other) : undefined
				}}
			/>
			{#if item.other}
				<ReportButton
					kind="contradiction"
					targetId={item.id}
					pair={{
						a: { id: item.fact.id, text: textOf(item.fact), date: item.fact.date, precision: item.fact.date_precision },
						b: { id: item.other.id, text: textOf(item.other), date: item.other.date, precision: item.other.date_precision },
						reason: why
					}}
				/>
			{/if}
			<a class="push" href={link}>{tr('clash.sources', lang)}</a>
		</footer>
	{/if}
</article>

<style>
	.clash {
		display: grid;
		gap: 0.9rem;
		padding: 1.15rem 1.2rem 0.8rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-card);
		text-align: left;
		transition:
			border-color 0.2s,
			transform 0.25s var(--ease),
			box-shadow 0.25s var(--ease),
			background 0.2s;
	}
	/* Under the pointer the card wakes: it lifts, its edge and its lightning
	   light up. */
	@media (hover: hover) {
		.clash:hover {
			border-color: rgba(229, 56, 79, 0.3);
			background: var(--bg-card-hover);
			transform: translateY(-2px);
			box-shadow: 0 24px 48px -24px rgba(229, 56, 79, 0.35);
		}
		.clash:hover .bolt svg {
			transform: scale(1.15) rotate(-6deg);
			filter: drop-shadow(0 0 8px var(--accent-glow));
		}
		.clash:hover .side.later {
			border-left-color: var(--accent);
		}
	}
	.clash.feature {
		padding: 1.4rem 1.45rem 0.9rem;
		background: radial-gradient(120% 80% at 100% 0%, rgba(229, 56, 79, 0.11), transparent 55%), var(--bg-card);
		border-color: rgba(229, 56, 79, 0.24);
		box-shadow: 0 40px 80px -40px rgba(229, 56, 79, 0.4);
	}
	.who {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
	}
	.who-l {
		display: flex;
		align-items: center;
		gap: 0.7rem;
		min-width: 0;
		color: inherit;
	}
	.who-name {
		display: grid;
		min-width: 0;
	}
	.who-name strong {
		font-size: 0.98rem;
		font-weight: 650;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.who-meta {
		display: flex;
		gap: 0.5rem;
		font-size: 0.78rem;
		color: var(--text-muted);
		min-width: 0;
	}
	.who-meta b {
		font-weight: 600;
		color: var(--text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
	}
	.who-meta time {
		white-space: nowrap;
		flex-shrink: 0;
	}
	.rise {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		flex-shrink: 0;
		height: 1.9rem;
		padding: 0 0.7rem;
		border-radius: var(--radius-full);
		border: 1px solid rgba(239, 69, 70, 0.35);
		background: rgba(239, 69, 70, 0.12);
		color: #ff6b5c;
		font-family: var(--font-num);
		font-weight: 800;
		font-size: 1.15rem;
		line-height: 1;
	}
	.rise span {
		font-size: 0.6rem;
	}
	.body {
		display: grid;
		gap: 0.55rem;
		color: inherit;
	}
	.body:hover {
		color: inherit;
	}
	.side {
		display: grid;
		gap: 0.3rem;
		padding-left: 0.9rem;
		border-left: 2px solid var(--border-card-hover);
		transition: border-color 0.2s;
	}
	.side.later {
		border-left-color: rgba(229, 56, 79, 0.55);
	}
	.side-date {
		display: flex;
		flex-wrap: wrap;
		gap: 0.15rem 0.5rem;
		font-size: 0.76rem;
		font-weight: 700;
		color: var(--text-secondary);
	}
	.side-date span {
		font-weight: 500;
		color: var(--text-muted);
	}
	.side p {
		font-size: 1rem;
		line-height: 1.4;
		color: var(--text-primary);
	}
	.feature .side p {
		font-size: 1.1rem;
	}
	.bolt {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		color: var(--accent);
	}
	.bolt::before,
	.bolt::after {
		content: '';
		flex: 1;
		height: 1px;
		background: linear-gradient(90deg, transparent, rgba(229, 56, 79, 0.35));
	}
	.bolt::after {
		background: linear-gradient(90deg, rgba(229, 56, 79, 0.35), transparent);
	}
	.bolt svg {
		width: 1.05rem;
		height: 1.05rem;
		fill: currentColor;
		transition:
			transform 0.25s var(--ease),
			filter 0.25s;
	}
	.why {
		margin-top: 0.25rem;
		padding: 0.6rem 0.8rem;
		border-radius: 0.7rem;
		background: rgba(255, 255, 255, 0.035);
		font-size: 0.88rem;
		line-height: 1.45;
		color: var(--text-secondary);
	}
	.actions {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		padding-top: 0.55rem;
		border-top: 1px solid var(--border-card);
	}
	.push {
		margin-left: auto;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--text-secondary);
	}
	.push:hover {
		color: var(--text-primary);
	}
</style>
