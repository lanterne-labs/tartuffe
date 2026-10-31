<script lang="ts">
	// The event's presentation: what the lab's temporary experiment is, its
	// promise and how to take part, with its two buttons, to vote and to see
	// how it works. The vote itself is on /voter, the explanation on
	// /fonctionnement.
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { eventPaths, showVoters } from '$lib/event';
	import type { PollEvent } from '$lib/types';
	import { latestTally, type LatestTally } from '$lib/vote/isoloir';
	import { loadEventMeta, byName, nameKey, type Candidate } from '$lib/vote/candidates';
	import { rankedRows, byFirst } from '$lib/vote/results';
	import { displayName } from '$lib/share';

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
			title2: 'Personne ne saura pour qui.',
			lead: 'Scannez votre passeport, puis classez vos 3 candidats préférés. Personne ne saura pour qui vous avez voté, et tout le monde peut recompter.',
			vote: 'Voter maintenant',
			how: 'Comment ça marche',
			votersNow: '{n} votants · {c} candidats',
			candidatesNow: '{c} candidats',
			stats: [
				['0', 'lien entre vous et votre vote'],
				['1', 'voix par passeport'],
				['3', 'choix, classés'],
				['100 %', 'recomptable par tous']
			],
			stepsKicker: 'Trois gestes',
			stepsTitle: 'Une minute, une seule fois.',
			steps: [
				['Scannez votre passeport', 'Vous ne le faites qu’une fois : il prouve seulement que vous êtes majeur et français.'],
				['Classez jusqu’à 3 candidats', 'Vous pouvez aussi voter blanc, et changer votre vote à tout moment.'],
				['Vérifiez', 'Votre navigateur s’assure que votre vote est bien compté.']
			],
			anonKicker: 'Le secret du vote',
			anonTitle: 'Votre document ouvre la porte. Il ne vote pas.',
			anonBody: 'Le scan crée une clé de vote, sur votre appareil seulement. Le bulletin prouve qu’il vient d’une clé inscrite, majeure et française, sans dire laquelle, et passe par des relais publics. Derrière un bulletin, nous ne voyons ni votre document ni votre adresse IP.',
			anonLabels: ['Votre document', 'Votre clé', 'Votre bulletin'],
			anonCut: 'aucun lien',
			countKicker: 'Même vote, deux résultats',
			countTitle: 'Et si le premier tour mentait ?',
			countBody: 'Les mêmes bulletins sont comptés de deux façons. Au premier choix seulement, on ne compte que le candidat placé en tête par chaque votant, comme au premier tour. Avec le classement, le gagnant est celui qu’une majorité préfère à chacun des autres.',
			countMajority: 'Premier choix seulement',
			countRanking: 'Avec le classement',
			countWinner: 'bat chacun des autres',
			countMore: 'Comprendre les deux comptages',
			blankTitle: 'Le vote blanc compte.',
			blankBody: 'C’est un bulletin à part entière, compté dans chaque pourcentage.',
			honestyTitle: 'Votez, et jugez sur pièces.',
			honestyBody: 'Chaque candidat est affiché avec son score d’honnêteté, calculé à partir de ses contradictions, sources à l’appui.',
			checkTitle: 'Impossible de tricher sans que ça se voie.',
			checkBody: 'Chaque bulletin entre, chiffré, dans une liste publique que personne ne peut modifier après coup. Tout le monde peut la télécharger et recompter.',
			checkMore: 'Comment vérifier',
			disclaimer: 'Ce n’est ni un scrutin officiel ni un sondage : les votants ne sont pas représentatifs. Les résultats sont masqués la veille et le jour de l’élection.',
			finalTitle: 'J-{n}. À vous de voter.',
			finalTitleNoDate: 'À vous de voter.',
			labLine: 'Une expérience du labo tartuffe.me.',
			labLink: 'Le labo',
			phTitle: 'Mon vote',
			phBlank: 'Vote blanc',
			phSeal: 'preuve anonyme'
		},
		en: {
			kicker: 'Citizen pre-poll · 2027 presidential election',
			until: 'Until {d}',
			title1: 'Vote.',
			title2: 'No one will know for whom.',
			lead: 'One scan of your passport, then your 3 favourite candidates. Nobody will know whom you voted for, and anyone can recount.',
			vote: 'Vote now',
			how: 'How it works',
			votersNow: '{n} voters · {c} candidates',
			candidatesNow: '{c} candidates',
			stats: [
				['0', 'link between you and your vote'],
				['1', 'vote per passport'],
				['3', 'choices, ranked'],
				['100 %', 'recountable by anyone']
			],
			stepsKicker: 'Three steps',
			stepsTitle: 'One minute, once.',
			steps: [
				['Scan your passport', 'Only once. Adult and French, nothing more.'],
				['Rank up to 3 candidates', 'Or vote blank. Change it any time.'],
				['Check', 'Your browser makes sure your vote is counted.']
			],
			anonKicker: 'The secret ballot',
			anonTitle: 'Your document opens the door. It does not vote.',
			anonBody: 'The scan makes a voting key, on your device only. The ballot proves it comes from a registered key, an adult French national’s, without saying which, and goes through public relays. Behind a ballot, we see neither your document nor your IP address.',
			anonLabels: ['Your document', 'Your key', 'Your ballot'],
			anonCut: 'no link',
			countKicker: 'Same votes, two results',
			countTitle: 'What if the first round lied?',
			countBody: 'The same ballots, counted twice. With the first choice only, only the candidate each voter put first is counted, as in a first round. With the ranking, the winner is the one a majority prefers to each other candidate.',
			countMajority: 'First choice only',
			countRanking: 'With the ranking',
			countWinner: 'beats each other one',
			countMore: 'Understand the two counts',
			blankTitle: 'The blank vote counts.',
			blankBody: 'A ballot in its own right, counted in every share.',
			honestyTitle: 'Vote, and look at their record.',
			honestyBody: 'Next to each candidate, their honesty score: their contradictions, with sources.',
			checkTitle: 'We cannot cheat without it showing.',
			checkBody: 'Every ballot goes, encrypted, into a public list that nobody can alter afterwards. Anyone can download it and recount.',
			checkMore: 'How to check',
			disclaimer: 'Neither an official election nor an opinion poll: the voters are not representative. Results hidden the day before and the day of the election.',
			finalTitle: '{n} days left. Your turn.',
			finalTitleNoDate: 'Your turn.',
			labLine: 'An experiment of the tartuffe.me lab.',
			labLink: 'The lab',
			phTitle: 'My vote',
			phBlank: 'Blank vote',
			phSeal: 'anonymous proof'
		}
	};
	let c = $derived(content[currentLang]);

	// The example of the two counts: the same ballots, two winners.
	const majority = [
		['B', 35],
		['C', 28],
		['A', 20],
		['D', 7]
	] as const;
	const ranking = ['A', 'B', 'C', 'D'];
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
				<div class="ph-blank">◻ {c.phBlank}</div>
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
							<span class="initial" aria-hidden="true">{o.blank ? '◻' : nameOf(o.slot).charAt(0).toUpperCase()}</span>
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

	<ul class="stats">
		{#each c.stats as [n, label]}<li><strong>{n}</strong><span>{label}</span></li>{/each}
	</ul>

	<section class="block">
		<span class="sec-kicker">{c.stepsKicker}</span>
		<h2>{c.stepsTitle}</h2>
		<ol class="steps">
			{#each c.steps as [title, body], i}
				<li>
					<svg class="step-icon" viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
						{#if i === 0}
							<rect x="9" y="6" width="30" height="36" rx="3" /><circle cx="24" cy="20" r="6" /><path d="M15 34h18M24 26v0" /><path d="M36 14c3 2 3 8 0 10M40 11c5 4 5 13 0 17" />
						{:else if i === 1}
							<rect x="8" y="8" width="32" height="8" rx="2" /><rect x="8" y="20" width="32" height="8" rx="2" /><rect x="8" y="32" width="32" height="8" rx="2" /><path d="M12 12h3M12 24h3M12 36h3" />
						{:else}
							<path d="M24 5l15 6v11c0 9-6.5 16.5-15 18-8.5-1.5-15-9-15-18V11z" /><path d="M17 24l5 5 9-10" />
						{/if}
					</svg>
					<span class="step-n">{i + 1}</span>
					<h3>{title}</h3>
					<p>{body}</p>
				</li>
			{/each}
		</ol>
	</section>

	<section class="block anon">
		<div>
			<span class="sec-kicker">{c.anonKicker}</span>
			<h2>{c.anonTitle}</h2>
			<p>{c.anonBody}</p>
		</div>
		<!-- The document admits a key; the key casts the ballot; nothing joins the document to the ballot. -->
		<svg class="anon-diagram" viewBox="0 0 360 230" role="img" aria-label="{c.anonLabels[0]} → {c.anonLabels[1]} → {c.anonLabels[2]} : {c.anonCut}">
			<defs>
				<marker id="arrow" viewBox="0 0 10 10" refX="8" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0 0L10 5L0 10z" fill="var(--event)" /></marker>
			</defs>
			<g class="node">
				<rect x="10" y="20" width="100" height="70" rx="10" />
				<rect x="38" y="34" width="44" height="30" rx="4" class="ico" />
				<circle cx="60" cy="47" r="7" class="ico" />
				<text x="60" y="82">{c.anonLabels[0]}</text>
			</g>
			<g class="node">
				<rect x="130" y="140" width="100" height="70" rx="10" />
				<circle cx="166" cy="166" r="9" class="ico" />
				<path d="M175 166h26M193 166v8M200 166v6" class="ico" />
				<text x="180" y="202">{c.anonLabels[1]}</text>
			</g>
			<g class="node">
				<rect x="250" y="20" width="100" height="70" rx="10" />
				<rect x="278" y="32" width="44" height="34" rx="4" class="ico" />
				<path d="M286 42h28M286 50h20M286 58h24" class="ico" />
				<text x="300" y="82">{c.anonLabels[2]}</text>
			</g>
			<path d="M70 92 C80 130, 110 160, 128 170" class="flow" marker-end="url(#arrow)" />
			<path d="M232 170 C255 160, 285 130, 295 94" class="flow" marker-end="url(#arrow)" />
			<path d="M114 55 H246" class="cut" />
			<g class="cut-mark">
				<circle cx="180" cy="55" r="14" />
				<path d="M174 49l12 12M186 49l-12 12" />
			</g>
			<text x="180" y="36" class="cut-label">{c.anonCut}</text>
		</svg>
	</section>

	<section class="block count">
		<span class="sec-kicker">{c.countKicker}</span>
		<h2>{c.countTitle}</h2>
		<p>{c.countBody}</p>
		<div class="count-grid">
			<div class="count-card">
				<h3>{c.countMajority}</h3>
				<ol>
					{#each majority as [name, pct], i}
						<li class:top={i === 0}>
							<span class="who">{name}</span>
							<span class="bar"><span style="width: {pct * 2.4}%"></span></span>
							<span class="pct">{pct}&nbsp;%</span>
						</li>
					{/each}
				</ol>
			</div>
			<div class="count-vs" aria-hidden="true">≠</div>
			<div class="count-card count-card--ranking">
				<h3>{c.countRanking}</h3>
				<ol>
					{#each ranking as name, i}
						<li class:top={i === 0}>
							<span class="who">{name}</span>
							{#if i === 0}<span class="winner">{c.countWinner}</span>{:else}<span class="rank">{i + 1}</span>{/if}
						</li>
					{/each}
				</ol>
			</div>
		</div>
		<p class="more"><a href="{paths.how}#comptage">{c.countMore} →</a></p>
	</section>

	<section class="trio">
		<div class="card">
			<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="2.2" aria-hidden="true"><rect x="10" y="8" width="28" height="32" rx="3" /><rect x="17" y="17" width="14" height="14" rx="1" /></svg>
			<h3>{c.blankTitle}</h3>
			<p>{c.blankBody}</p>
		</div>
		<div class="card">
			<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" aria-hidden="true"><path d="M8 34a16 16 0 0 1 32 0" /><path d="M24 34l8-10" /><circle cx="24" cy="34" r="2.5" /></svg>
			<h3>{c.honestyTitle}</h3>
			<p>{c.honestyBody}</p>
		</div>
		<div class="card">
			<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="24" cy="10" r="4" /><circle cx="12" cy="26" r="4" /><circle cx="36" cy="26" r="4" /><circle cx="6" cy="40" r="3" /><circle cx="18" cy="40" r="3" /><circle cx="30" cy="40" r="3" /><circle cx="42" cy="40" r="3" /><path d="M21 13l-6 10M27 13l6 10M10 30l-3 7M14 30l3 7M34 30l-3 7M38 30l3 7" /></svg>
			<h3>{c.checkTitle}</h3>
			<p>{c.checkBody}</p>
			<a href="{paths.how}#journal">{c.checkMore} →</a>
		</div>
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

	/* Stats */
	.stats {
		list-style: none;
		display: grid;
		grid-template-columns: repeat(4, minmax(0, 1fr));
		gap: 0.75rem;
		margin: 0 0 4rem;
		padding: 0;
	}
	.stats li {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		padding: 1rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		text-align: center;
	}
	.stats strong {
		font-size: 1.9rem;
		font-weight: 700;
		letter-spacing: -0.02em;
		line-height: 1;
		color: var(--event);
	}
	.stats span {
		font-size: 0.8rem;
		color: var(--text-secondary);
	}

	/* Blocks */
	.block {
		margin: 0 0 4.5rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: clamp(1.8rem, 4.5vw, 2.6rem);
		font-weight: 600;
		line-height: 1.1;
		margin: 0.4rem 0 1rem;
	}
	.block p {
		color: var(--text-secondary);
		line-height: 1.65;
		max-width: 40rem;
	}
	.steps {
		list-style: none;
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 1rem;
		margin: 1.5rem 0 0;
		padding: 0;
		counter-reset: step;
	}
	.steps li {
		position: relative;
		padding: 1.4rem 1.2rem 1.2rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: radial-gradient(ellipse 80% 60% at 0% 0%, var(--event-glow), transparent 70%), var(--bg-card);
	}
	.step-icon {
		width: 2.6rem;
		height: 2.6rem;
		color: var(--event);
	}
	.step-n {
		position: absolute;
		top: 0.9rem;
		right: 1.1rem;
		font-family: var(--font-serif);
		font-size: 2.4rem;
		line-height: 1;
		color: var(--glass-border);
	}
	.steps h3 {
		margin: 0.7rem 0 0.35rem;
		font-size: 1.05rem;
	}
	.steps p {
		margin: 0;
		font-size: 0.92rem;
	}

	.anon {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
		gap: 2rem;
		align-items: center;
	}
	.anon-diagram {
		width: 100%;
		max-width: 26rem;
		justify-self: center;
	}
	.node rect:first-child {
		fill: var(--bg-card);
		stroke: var(--event);
		stroke-width: 1.5;
	}
	.node .ico {
		fill: none;
		stroke: var(--text-secondary);
		stroke-width: 2;
		stroke-linecap: round;
	}
	.node text {
		fill: var(--text-primary);
		font-size: 12px;
		font-weight: 600;
		text-anchor: middle;
	}
	.flow {
		fill: none;
		stroke: var(--event);
		stroke-width: 2;
	}
	.cut {
		stroke: var(--red);
		stroke-width: 2;
		stroke-dasharray: 5 6;
	}
	.cut-mark circle {
		fill: var(--bg-primary);
		stroke: var(--red);
		stroke-width: 2;
	}
	.cut-mark path {
		stroke: var(--red);
		stroke-width: 2.5;
		stroke-linecap: round;
	}
	.cut-label {
		fill: var(--red);
		font-size: 12px;
		font-weight: 700;
		text-anchor: middle;
		letter-spacing: 0.05em;
		text-transform: uppercase;
	}

	.count-grid {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto minmax(0, 1fr);
		gap: 1rem;
		align-items: center;
		margin: 1.5rem 0 0.5rem;
	}
	.count-card {
		padding: 1.1rem 1.2rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
	}
	.count-card--ranking {
		border-color: var(--event);
	}
	.count-card h3 {
		margin: 0 0 0.8rem;
		font-size: 0.78rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	.count-card ol {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		gap: 0.5rem;
	}
	.count-card li {
		display: grid;
		grid-template-columns: 2rem 1fr auto;
		align-items: center;
		gap: 0.6rem;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-sm);
	}
	.count-card li.top {
		background: var(--event-glow);
	}
	.who {
		display: grid;
		place-items: center;
		width: 1.8rem;
		height: 1.8rem;
		border-radius: 50%;
		background: var(--bg-secondary);
		font-weight: 700;
	}
	.bar {
		height: 8px;
		border-radius: 4px;
		background: var(--bg-secondary);
		overflow: hidden;
	}
	.bar span {
		display: block;
		height: 100%;
		background: var(--event-strong);
	}
	.pct {
		font-weight: 700;
		font-variant-numeric: tabular-nums;
	}
	.winner {
		grid-column: 2 / -1;
		justify-self: start;
		padding: 0.12rem 0.55rem;
		border-radius: var(--radius-full);
		background: var(--event-strong);
		color: white;
		font-size: 0.72rem;
		font-weight: 700;
	}
	.rank {
		grid-column: 2 / -1;
		color: var(--text-muted);
		font-size: 0.85rem;
	}
	.count-vs {
		font-family: var(--font-serif);
		font-size: 2.6rem;
		color: var(--event);
	}
	.more a,
	.card a {
		color: var(--event);
		font-weight: 600;
	}

	.trio {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 1rem;
		margin: 0 0 4.5rem;
	}
	.card {
		padding: 1.3rem 1.2rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
	}
	.card svg {
		width: 2.4rem;
		height: 2.4rem;
		color: var(--event);
	}
	.card h3 {
		margin: 0.6rem 0 0.4rem;
		font-family: var(--font-serif);
		font-size: 1.35rem;
		line-height: 1.15;
	}
	.card p {
		margin: 0 0 0.6rem;
		color: var(--text-secondary);
		font-size: 0.92rem;
		line-height: 1.6;
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
		.hero,
		.anon {
			grid-template-columns: minmax(0, 1fr);
		}
		.phone {
			width: min(14rem, 80%);
			margin-top: 1rem;
		}
		.stats {
			grid-template-columns: repeat(2, minmax(0, 1fr));
		}
		.steps,
		.trio {
			grid-template-columns: minmax(0, 1fr);
		}
		.count-grid {
			grid-template-columns: minmax(0, 1fr);
		}
		.count-vs {
			justify-self: center;
			line-height: 1;
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
</style>
