<script lang="ts">
	// The event's presentation, simply: the promise, the current top 3, what
	// the vote offers, and that it is private by default (one diagram). The
	// vote itself is on /voter; how it works is on /fonctionnement, not here.
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { eventPaths, showVoters } from '$lib/event';
	import type { PollEvent } from '$lib/types';
	import { latestTally, type LatestTally } from '$lib/vote/isoloir';
	import { loadEventMeta, byName, nameKey, type Candidate } from '$lib/vote/candidates';
	import { rankedRows, byFirst } from '$lib/vote/results';
	import { displayName } from '$lib/share';
	import BallotPathAnimation from '$lib/components/vote/BallotPathAnimation.svelte';
	import ZKProofAnimation from '$lib/components/ZKProofAnimation.svelte';
	import AskAI from '$lib/components/vote/AskAI.svelte';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));

	let event = $state<PollEvent | null>(null);
	let cardCount = $state<number | null>(null);
	// The current results, read from the public mirror like the vote page
	// does: the latest tally, proven in the signed log and verified here,
	// and the poll's options (names in slot order, the blank last); the
	// site's cards give the photos. Absent until a first tally exists.
	let latest = $state<LatestTally | null>(null);
	let cards = $state<Map<string, Candidate>>(new Map());
	onMount(async () => {
		try {
			const meta = await loadEventMeta(slug);
			event = meta.event;
			cards = byName(meta.candidates);
			cardCount = meta.candidates.length;
		} catch (err) {
			console.error('failed to load the event', err);
		}
		try {
			latest = await latestTally(slug);
		} catch (err) {
			console.warn('event: the mirror', err);
		}
	});
	let ends = $derived(event?.closes_at ? new Date(event.closes_at) : null);
	let daysLeft = $derived(ends ? Math.max(0, Math.ceil((ends.getTime() - Date.now()) / 86_400_000)) : null);
	// Only a tally that verified is shown.
	let shown = $derived(latest?.verdict.ok ? rankedRows(latest.verdict.results) : null);
	let voters = $derived(shown?.counted ?? null);
	let options = $derived(latest?.poll.kind.options ?? []);
	let blankSlot = $derived(latest?.poll.kind.blank ? options.length : -1);
	let candidates = $derived(latest ? options.length : cardCount);
	// The top 3 by first-choice share, and the winner of the ranking.
	let top3 = $derived(shown && shown.counted > 0 ? byFirst(shown.rows).slice(0, 3) : []);
	let winnerName = $derived(shown?.condorcet != null ? nameOf(shown.condorcet) : '');

	function nameOf(slot: number): string {
		if (slot === blankSlot) return tr('vote.blank.name', currentLang);
		const n = options[slot];
		if (n === undefined) return `#${slot}`;
		return displayName(cards.get(nameKey(n))?.name ?? n);
	}
	function imageOf(slot: number): string | undefined {
		const n = options[slot];
		return n === undefined ? undefined : cards.get(nameKey(n))?.image_url;
	}
	function pctText(n: number, total: number): string {
		if (total <= 0) return '—';
		const v = Math.round((100 * n) / total);
		return currentLang === 'en' ? `${v}%` : `${v} %`;
	}

	function dateText(d: Date): string {
		return d.toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}

	const content = {
		fr: {
			kicker: 'Consultation citoyenne · Présidentielle 2027',
			until: 'Jusqu’au {d}',
			title1: 'Votez.',
			title2: 'Les résultats en direct.',
			lead: 'Classez jusqu’à 3 candidats et voyez, en direct, qui une majorité préfère, pas seulement qui arrive en tête. En ligne, et votre vote reste secret.',
			vote: 'Voter maintenant',
			how: 'Comment ça marche',
			votersNow: '{n} votants · {c} candidats',
			candidatesNow: '{c} candidats',
			features: [
				['Classez jusqu’à 3 candidats', 'Ou votez blanc : il compte vraiment.'],
				['Deux résultats en direct', 'Le classement et le premier choix, côte à côte.'],
				['Changez d’avis', 'Jusqu’à la clôture, votre dernier vote compte, au plus un changement par heure.'],
				['Jugez sur pièces', 'Chaque candidat avec son Tartuffe-Score.']
			],
			privateKicker: 'Privé par défaut',
			privateTitle: 'Votre vote reste secret, même pour nous.',
			privateBody: 'Votre passeport reste sur votre téléphone et votre bulletin voyage scellé. Nous ne détenons rien qui vous identifie : même une fuite chez nous ne révélerait rien.',
			verify: 'Vérifier le vote',
			disclaimer: 'Un vote d’opinion en ligne, distinct du scrutin officiel et des sondages. Les résultats sont masqués la veille et le jour de l’élection.',
			finalTitle: 'J-{n}. À vous de voter.',
			finalTitleNoDate: 'À vous de voter.',
			labLink: 'Le labo',
			phTitle: 'Mon vote',
			phBlank: 'Vote blanc',
			phSeal: 'preuve anonyme'
		},
		en: {
			kicker: 'Citizen pre-poll · 2027 presidential election',
			until: 'Until {d}',
			title1: 'Vote.',
			title2: 'Live results.',
			lead: 'Rank up to 3 candidates and see, live, whom a majority prefers, not only who comes first. Online, and your vote stays secret.',
			vote: 'Vote now',
			how: 'How it works',
			votersNow: '{n} voters · {c} candidates',
			candidatesNow: '{c} candidates',
			features: [
				['Rank up to 3 candidates', 'Or vote blank: it really counts.'],
				['Two live results', 'The ranking and the first choices, side by side.'],
				['Change your mind', 'Until the close, your last vote counts, at most one change an hour.'],
				['Look at their record', 'Every candidate with their Tartuffe-Score.']
			],
			privateKicker: 'Private by default',
			privateTitle: 'Your vote stays secret, even from us.',
			privateBody: 'Your passport stays on your phone and your ballot travels sealed. We hold nothing that identifies you: even a leak on our side would reveal nothing.',
			verify: 'Verify the vote',
			disclaimer: 'An online opinion vote, distinct from the official election and from polls. Results hidden the day before and the day of the election.',
			finalTitle: '{n} days left. Your turn.',
			finalTitleNoDate: 'Your turn.',
			labLink: 'The lab',
			phTitle: 'My vote',
			phBlank: 'Blank vote',
			phSeal: 'anonymous proof'
		}
	};
	let c = $derived(content[currentLang]);

</script>

<svelte:head>
	<title>{c.title1} {c.title2} — tartuffe.me</title>
</svelte:head>

<article class="intro">
	<section class="hero">
		<div class="hero-text">
			<span class="kicker">{c.kicker}{#if ends}<span class="until"> · {c.until.replace('{d}', dateText(ends))}</span>{/if}</span>
			<h1>{c.title1}<br /><em>{c.title2}</em></h1>
			<p class="lead">{c.lead}</p>
			<p class="needs">{tr('event.needs', currentLang)}</p>
			<div class="actions">
				<a class="btn-main" href={paths.vote}>{c.vote} →</a>
				<a class="btn-alt" href={paths.how}>{c.how}</a>
			</div>
			{#if voters !== null && candidates}
				<p class="live"><span class="pulse" aria-hidden="true"></span>{(showVoters(voters) ? c.votersNow : c.candidatesNow).replace('{n}', String(voters)).replace('{c}', String(candidates))}</p>
			{/if}
		</div>

		<!-- The ballot on a phone: the ranking, the mask, the seal. -->
		<div class="phone" aria-hidden="true">
			<div class="phone-screen">
				<div class="ph-head"><span class="ph-dot"></span><span class="ph-bar"></span></div>
				<div class="ph-title">{c.phTitle}</div>
				{#each ['①', '②', '③'] as n, i}
					<div class="ph-slot" style="--d: {i * 0.25}s"><span class="ph-num">{n}</span><span class="ph-name" style="width: {[62, 48, 55][i]}%"></span></div>
				{/each}
				<div class="ph-blank"><span class="ph-disc" aria-hidden="true"></span>{c.phBlank}</div>
				<div class="ph-seal">
					<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l8 3v6c0 4.5-3.3 8.3-8 9-4.7-.7-8-4.5-8-9V6z" /><path d="M8.5 12l2.5 2.5 4.5-5" /></svg>
					<span>{c.phSeal}</span>
				</div>
			</div>
			<svg class="mask" viewBox="0 0 64 40" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">
				<path d="M4 12 C8 4, 16 2, 22 6 C26 8, 28 10, 32 12 C36 10, 38 8, 42 6 C48 2, 56 4, 60 12 C62 20, 58 28, 50 32 C44 34, 38 32, 34 26 C33 24, 31 24, 30 26 C26 32, 20 34, 14 32 C6 28, 2 20, 4 12 Z" />
				<ellipse cx="18" cy="17" rx="5" ry="2.5" />
				<ellipse cx="46" cy="17" rx="5" ry="2.5" />
			</svg>
		</div>
	</section>

	{#if top3.length && shown}
		<section class="top3" aria-labelledby="top3-title">
			<h2 id="top3-title">{tr('poll.top3.title', currentLang)}</h2>
			<ol>
				{#each top3 as o, i (o.slot)}
					<li class:first={i === 0}>
						{#if imageOf(o.slot)}
							<img src={imageOf(o.slot)} alt="" />
						{:else}
							<span class="initial" class:initial--blank={o.blank} aria-hidden="true">{o.blank ? '' : nameOf(o.slot).charAt(0).toUpperCase()}</span>
						{/if}
						<span class="top3-name">{nameOf(o.slot)}</span>
						<strong>{pctText(o.first, shown.counted)}</strong>
					</li>
				{/each}
			</ol>
			{#if winnerName}<p class="top3-winner">{tr('poll.winner.line', currentLang, { name: winnerName })}</p>{/if}
			<p class="more"><a href={paths.vote}>{tr('profile.go_results', currentLang)} →</a></p>
		</section>
	{/if}

	<ul class="features">
		{#each c.features as [title, line], i (i)}
			<li>
				<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
					{#if i === 0}
						<rect x="8" y="8" width="32" height="8" rx="2" /><rect x="8" y="20" width="32" height="8" rx="2" /><rect x="8" y="32" width="32" height="8" rx="2" /><path d="M12 12h3M12 24h3M12 36h3" />
					{:else if i === 1}
						<path d="M10 38V26M20 38V14M30 38V20M40 38V8" />
					{:else if i === 2}
						<path d="M36 18a13 13 0 1 0 2 10" /><path d="M38 8v10H28" />
					{:else}
						<path d="M8 34a16 16 0 0 1 32 0" /><path d="M24 34l8-10" /><circle cx="24" cy="34" r="2.5" />
					{/if}
				</svg>
				<h3>{title}</h3>
				<p>{line}</p>
			</li>
		{/each}
	</ul>

	<section class="private">
		<span class="sec-kicker">{c.privateKicker}</span>
		<h2>{c.privateTitle}</h2>
		<div class="private-anim"><ZKProofAnimation lang={currentLang} /></div>
		<BallotPathAnimation lang={currentLang} />
		<p>{c.privateBody}</p>
		<div class="private-ask"><AskAI lang={currentLang} /></div>
		<p class="private-links">
			<a href={paths.how}>{c.how} →</a>
			<a href={paths.verify}>{c.verify} →</a>
		</p>
	</section>

	<section class="final">
		<h2>{daysLeft !== null ? c.finalTitle.replace('{n}', String(daysLeft)) : c.finalTitleNoDate}</h2>
		<div class="actions">
			<a class="btn-main" href={paths.vote}>{c.vote} →</a>
			<a class="btn-alt" href={paths.how}>{c.how}</a>
		</div>
		<p class="disclaimer">{c.disclaimer}</p>
		<p class="lab-line">{tr('event.who', currentLang)} <a href="/lab">{c.labLink} →</a></p>
	</section>
</article>

<style>
	.intro {
		max-width: 64rem;
		margin: 0 auto;
	}
	.hero {
		display: grid;
		grid-template-columns: minmax(0, 1.25fr) minmax(0, 0.75fr);
		gap: 2.5rem;
		align-items: center;
		margin: 2.5rem 0 2.5rem;
		text-align: center;
	}
	.kicker,
	.sec-kicker {
		display: block;
		font-size: 0.75rem;
		font-weight: 700;
		letter-spacing: 0.14em;
		text-transform: uppercase;
		color: var(--event);
	}
	.until {
		color: var(--text-secondary);
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2.6rem, 7vw, 4.6rem);
		font-weight: 600;
		line-height: 1;
		margin: 0.6rem 0 1.1rem;
	}
	h1 em {
		font-style: italic;
		background: linear-gradient(100deg, var(--event), color-mix(in oklch, var(--event) 55%, white) 60%, var(--event-strong));
		-webkit-background-clip: text;
		background-clip: text;
		color: transparent;
	}
	.lead {
		font-size: 1.12rem;
		line-height: 1.6;
		color: var(--text-secondary);
		max-width: 36rem;
		margin: 0 0 1.5rem;
	}
	.needs {
		margin: -0.9rem 0 1.3rem;
		font-size: 0.95rem;
		font-weight: 600;
		color: var(--text-primary);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: 0.75rem;
	}
	/* The current top 3, from the mirror. */
	.top3 {
		margin: 0 0 2.5rem;
		padding: 1.1rem 1.2rem;
		border: 1px solid var(--event);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
	}
	.top3 h2 {
		font-size: 0.78rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--text-muted);
		margin: 0 0 0.8rem;
		font-family: inherit;
	}
	.top3 ol {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 0.45rem;
	}
	.top3 li {
		display: grid;
		grid-template-columns: 2.2rem minmax(0, 1fr) auto;
		align-items: center;
		gap: 0.7rem;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-sm);
	}
	.top3 li.first {
		background: var(--event-glow);
	}
	.top3 .initial.initial--blank {
		background: #fff;
	}
	.top3 img,
	.top3 .initial {
		width: 2.2rem;
		height: 2.2rem;
		border-radius: 50%;
		object-fit: cover;
		display: grid;
		place-items: center;
		background: var(--bg-secondary);
		font-weight: 700;
		color: var(--text-secondary);
	}
	.top3-name {
		font-weight: 600;
	}
	.top3 strong {
		font-size: 1.3rem;
		font-weight: 700;
		font-variant-numeric: tabular-nums;
	}
	.top3-winner {
		margin: 0.8rem 0 0;
		font-size: 0.9rem;
		color: var(--text-secondary);
	}
	.top3 .more {
		margin: 0.5rem 0 0;
	}
	.btn-main,
	.btn-alt {
		display: inline-flex;
		align-items: center;
		padding: 0.8rem 1.5rem;
		border-radius: var(--radius-full);
		font-weight: 600;
		text-decoration: none;
		transition: all var(--transition-fast);
	}
	.btn-main {
		background: var(--event-strong);
		color: white;
		box-shadow: 0 8px 28px var(--event-glow);
	}
	.btn-main:hover {
		background: var(--event);
		transform: translateY(-1px);
	}
	.btn-alt {
		border: 1px solid var(--event);
		color: var(--text-primary);
	}
	.btn-alt:hover {
		background: var(--event-glow);
	}
	.live {
		display: flex;
		align-items: center;
		justify-content: center;
		gap: 0.5rem;
		margin: 1.1rem 0 0;
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	.pulse {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--green);
		box-shadow: 0 0 0 0 var(--green);
		animation: pulse 2s infinite;
	}
	@keyframes pulse {
		70% {
			box-shadow: 0 0 0 8px transparent;
		}
		100% {
			box-shadow: 0 0 0 0 transparent;
		}
	}

	/* The phone */
	.phone {
		position: relative;
		justify-self: center;
		width: min(16rem, 100%);
	}
	.phone-screen {
		position: relative;
		padding: 1rem 0.9rem 1.1rem;
		border: 2px solid var(--glass-border);
		border-radius: 2rem;
		background: linear-gradient(160deg, var(--bg-secondary), var(--bg-primary));
		box-shadow:
			0 30px 60px rgba(0, 0, 0, 0.45),
			0 0 0 6px rgba(127, 159, 214, 0.06),
			0 0 60px var(--event-glow);
		transform: rotate(-3deg);
	}
	.ph-head {
		display: flex;
		justify-content: center;
		align-items: center;
		gap: 0.4rem;
		margin-bottom: 0.9rem;
	}
	.ph-dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: var(--glass-border);
	}
	.ph-bar {
		width: 3.5rem;
		height: 6px;
		border-radius: 3px;
		background: var(--glass-border);
	}
	.ph-title {
		font-family: var(--font-serif);
		font-size: 1.2rem;
		font-weight: 600;
		margin-bottom: 0.6rem;
	}
	.ph-slot {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.45rem;
		padding: 0.45rem 0.55rem;
		border: 1px solid var(--event);
		border-radius: 0.6rem;
		background: var(--event-glow);
		animation: slot-in 0.6s var(--d) ease-out both;
	}
	@keyframes slot-in {
		from {
			opacity: 0;
			transform: translateX(14px);
		}
	}
	.ph-num {
		color: var(--event);
		font-weight: 700;
	}
	.ph-name {
		height: 8px;
		border-radius: 4px;
		background: color-mix(in oklch, var(--text-primary) 55%, transparent);
	}
	.ph-disc {
		display: inline-block;
		width: 0.7rem;
		height: 0.7rem;
		margin-right: 0.4rem;
		border-radius: 50%;
		background: #fff;
		vertical-align: -0.1rem;
	}
	.ph-blank {
		margin: 0.7rem 0 0.9rem;
		padding: 0.4rem 0.55rem;
		border: 1px dashed var(--text-muted);
		border-radius: 0.6rem;
		font-size: 0.75rem;
		color: var(--text-secondary);
	}
	.ph-seal {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--green);
	}
	.ph-seal svg {
		width: 18px;
		height: 18px;
	}
	.mask {
		position: absolute;
		right: -1.2rem;
		top: -1.6rem;
		width: 5.5rem;
		color: var(--event);
		filter: drop-shadow(0 0 12px var(--event-glow));
		transform: rotate(12deg);
	}
	@media (prefers-reduced-motion: reduce) {
		.ph-slot,
		.pulse {
			animation: none;
		}
	}

	h2 {
		font-family: var(--font-serif);
		font-size: clamp(1.8rem, 4.5vw, 2.6rem);
		font-weight: 600;
		line-height: 1.1;
		margin: 0.4rem 0 1rem;
	}

	.more a {
		color: var(--event);
		font-weight: 600;
	}

	.final {
		text-align: center;
		padding: 2.5rem 1.5rem;
		border: 1px solid var(--event);
		border-radius: var(--radius-lg);
		background: radial-gradient(ellipse 70% 90% at 50% 0%, var(--event-glow), transparent 70%), var(--bg-card);
	}
	.final .actions {
		justify-content: center;
	}
	.disclaimer {
		max-width: 38rem;
		margin: 1.5rem auto 0.5rem;
		font-size: 0.78rem;
		color: var(--text-muted);
		line-height: 1.5;
	}
	.lab-line {
		margin: 0;
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	.lab-line a {
		color: var(--event);
	}

	@media (max-width: 860px) {
		.hero {
			grid-template-columns: minmax(0, 1fr);
		}
		.phone {
			width: min(14rem, 80%);
			margin-top: 1rem;
		}
	}
	@media (max-width: 560px) {
		.hero {
			margin-top: 1.5rem;
		}
		.mask {
			right: -0.4rem;
			width: 4.2rem;
		}
	}
	.features {
		list-style: none;
		padding: 0;
		margin: 2.5rem 0;
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.8rem;
		text-align: center;
	}
	@media (min-width: 820px) {
		.features {
			grid-template-columns: repeat(4, minmax(0, 1fr));
		}
	}
	.features li {
		padding: 1.2rem 0.9rem;
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		border: 1px solid var(--border-card, var(--glass-border));
	}
	.features svg {
		width: 2.2rem;
		height: 2.2rem;
		color: var(--event);
	}
	.features h3 {
		margin: 0.5rem 0 0.25rem;
		font-size: 1rem;
		line-height: 1.25;
	}
	.features p {
		margin: 0;
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	.private {
		max-width: 38rem;
		margin: 0 auto 2.5rem;
		padding: 1.6rem 1.2rem 1.3rem;
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		border: 1px solid var(--border-card, var(--glass-border));
		text-align: center;
	}
	.private h2 {
		font-family: var(--font-serif);
		font-size: clamp(1.6rem, 4.5vw, 2.2rem);
		line-height: 1.15;
		margin: 0.4rem 0 1rem;
		text-wrap: balance;
	}
	.private p {
		color: var(--text-secondary);
		margin: 0.9rem auto 0;
		max-width: 30rem;
		text-wrap: balance;
	}
	.private-anim {
		margin: 0 0 1.4rem;
	}
	.private-ask {
		margin: 1.1rem 0 0;
	}
	.private-links {
		display: flex;
		justify-content: center;
		gap: 1.2rem;
		flex-wrap: wrap;
	}
	.private-links a {
		color: var(--event);
		font-weight: 600;
	}
</style>
