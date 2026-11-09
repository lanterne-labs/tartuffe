<script lang="ts">
	// Direction « Scène »: the site as a play. The UX bet: a visitor remembers
	// a story, so the page tells one in three acts, one per screen, with one
	// idea each: Act I a real contradiction in two lines, Act II the score it
	// moves, Act III the curtain call, the three under the spotlights. Dark,
	// one spotlight, acid green and red for the two ends. Logo: the mask,
	// half smile, half frown.
	import { onMount } from 'svelte';
	import { loadLanding, type Landing } from '$lib/lab/podium';

	let data = $state<Landing | null>(null);
	onMount(async () => {
		try {
			data = await loadLanding();
		} catch (err) {
			console.error('landing', err);
		}
	});
	let lead = $derived(data?.clashes[0] ?? null);
</script>

<svelte:head>
	<title>Scène — tartuffe.me</title>
	<meta name="robots" content="noindex" />
	<link rel="preconnect" href="https://fonts.googleapis.com" />
	<link href="https://fonts.googleapis.com/css2?family=Syne:wght@500;700;800&family=Sora:wght@400;500;600&display=swap" rel="stylesheet" />
</svelte:head>

<div class="scene">
	<header class="nav">
		<a class="brand" href="/">
			<svg class="mark" viewBox="0 0 48 48" aria-hidden="true" fill="none" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round">
				<path d="M24 5c10 0 18 6 18 16 0 12-9 22-18 22S6 33 6 21C6 11 14 5 24 5z" stroke="#f3f0ff" />
				<path d="M24 5v38" stroke="#f3f0ff" stroke-dasharray="2 4" opacity="0.5" />
				<path d="M12 20c2-2 5-2 7 0" stroke="#b6ff3b" />
				<path d="M29 20c2-2 5-2 7 0" stroke="#ff3b5c" />
				<path d="M12 30c3 4 8 5 12 3" stroke="#b6ff3b" />
				<path d="M24 33c4-2 9-1 12-5" stroke="#ff3b5c" />
			</svg>
			<span>tartuffe<em>.me</em></span>
		</a>
		<nav>
			<a href="/dashboard">Classement</a>
			<a href="/feed">Le fil</a>
			<a href="/about">Méthode</a>
			<a href="https://github.com/lanterne-labs/mirror">Code</a>
		</nav>
	</header>

	<section class="curtain">
		<div class="spot"></div>
		<p class="over">Ce qu’ils disent · ce qu’ils votent · ce que la justice a tranché</p>
		<h1><span class="g">Cohérent</span> ou <span class="r">tartuffe</span> ?</h1>
		<p class="lead">Quand ça ne colle pas, nous le montrons, sources à l’appui. Nous ne jugeons pas qui a raison. Trois actes, puis la scène est à vous.</p>
		<a class="down" href="#acte1">Acte I ↓</a>
	</section>

	<section class="act" id="acte1">
		<span class="roman">Acte I</span>
		<h2>Deux phrases, une personne</h2>
		{#if lead}
			<div class="lines">
				<div class="line"><span class="when">{lead.a.year}</span><p>« {lead.a.quote || lead.a.text} »</p><small>{lead.a.domain}</small></div>
				<div class="line"><span class="when r">{lead.b.year}</span><p>« {lead.b.quote || lead.b.text} »</p><small>{lead.b.domain}</small></div>
			</div>
			<p class="who">{#if lead.person.image}<img src={lead.person.image} alt="" />{/if}<span><a href="/person/{lead.person.id}?c={lead.id}">{lead.person.name}</a>, sur {lead.topic}. {lead.reason}</span></p>
		{/if}
	</section>

	<section class="act two">
		<span class="roman">Acte II</span>
		<h2>Le score bouge</h2>
		{#if lead}
			<div class="meter">
				<span class="g">0 · cohérent</span>
				<div class="track"><i class="was" style="left: {Math.max(0, lead.person.score - lead.rise)}%"></i><i class="now" style="left: {lead.person.score}%"></i></div>
				<span class="r">tartuffe · 100</span>
			</div>
			<p class="delta"><b>+{lead.rise}</b> pour cette contradiction. {lead.person.name} est à <b>{lead.person.score}</b>/100, après {lead.person.contradictions} contradictions sur {lead.person.facts.toLocaleString('fr-FR')} faits.</p>
		{/if}
		<p class="rule">Chaque contradiction distincte pousse le repère vers « tartuffe ». Une nuance ne compte pas. Une condamnation niée compte. Et vous pouvez corriger.</p>
	</section>

	<section class="act three">
		<span class="roman">Acte III</span>
		<h2>Sous les projecteurs <small>· aujourd’hui</small></h2>
		<div class="row">
			{#each data?.podium ?? [] as p, i (p.id)}
				<a class="star s{i + 1}" href="/person/{p.id}">
					<span class="halo"></span>
					{#if p.image}<img src={p.image} alt="" />{/if}
					<span class="score">{p.score}</span>
					<span class="name">{p.name}</span>
					<span class="meta">{p.contradictions} contradictions · {p.facts.toLocaleString('fr-FR')} faits</span>
				</a>
			{/each}
		</div>
		<a class="more" href="/dashboard">Toute la troupe : les {data?.candidates.length ?? ''} candidats →</a>
	</section>

	<section class="wings">
		<a href="/feed"><h3>Le fil</h3><p>Les dernières contradictions, au fil de l’eau. Un +1 les fait remonter.</p></a>
		<a href="/about"><h3>La méthode</h3><p>Des sources publiques jusqu’au score, de façon automatique et identique pour tous.</p></a>
		<a href="https://github.com/lanterne-labs/mirror"><h3>Le code</h3><p>Open source : la collecte, la détection, le score. Lisez-le, corrigez-le.</p></a>
	</section>

	<footer>
		<span>tartuffe.me — un laboratoire d’outils pour une démocratie plus transparente.</span>
		<span><a href="/about">Méthode</a> · <a href="/about/privacy">Confidentialité</a> · <a href="/contact">Contact</a> · <a href="/mentions-legales">Mentions légales</a></span>
	</footer>
</div>

<style>
	.scene {
		--bg: #0b0a10;
		--panel: #15131d;
		--line: rgba(243, 240, 255, 0.1);
		--text: #f3f0ff;
		--muted: #9d98b3;
		--green: #b6ff3b;
		--red: #ff3b5c;
		--display: 'Syne', system-ui, sans-serif;
		--sans: 'Sora', system-ui, sans-serif;
		min-height: 100vh;
		background: var(--bg);
		color: var(--text);
		font-family: var(--sans);
		line-height: 1.55;
	}
	a { color: inherit; text-decoration: none; }
	h1, h2, h3 { font-family: var(--display); margin: 0; }
	.g { color: var(--green); }
	.r { color: var(--red); }

	.nav { display: flex; align-items: center; justify-content: space-between; max-width: 1120px; margin: 0 auto; padding: 1.1rem 1.5rem; }
	.brand { display: flex; align-items: center; gap: 0.6rem; font-family: var(--display); font-weight: 800; font-size: 1.25rem; }
	.brand em { font-style: normal; color: var(--green); }
	.mark { width: 36px; height: 36px; }
	.nav nav { display: flex; gap: 1.5rem; color: var(--muted); font-weight: 500; }
	.nav nav a:hover { color: var(--text); }

	.curtain { position: relative; min-height: min(78vh, 640px); display: flex; flex-direction: column; justify-content: center; align-items: center; text-align: center; max-width: 880px; margin: 0 auto; padding: 3rem 1.5rem; }
	.spot { position: absolute; top: -10rem; left: 50%; width: 70rem; height: 46rem; transform: translateX(-50%); background: radial-gradient(ellipse at 50% 0%, rgba(243, 240, 255, 0.16), rgba(182, 255, 59, 0.05) 35%, transparent 65%); pointer-events: none; }
	.curtain > *:not(.spot) { position: relative; }
	.over { font-size: 0.78rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.14em; color: var(--muted); margin: 0 0 1.25rem; }
	h1 { font-weight: 800; font-size: clamp(3rem, 8vw, 6.4rem); line-height: 0.98; letter-spacing: -0.04em; margin-bottom: 1.25rem; }
	.lead { font-size: 1.15rem; color: var(--muted); max-width: 36rem; margin: 0 0 2rem; }
	.down { padding: 0.8rem 1.4rem; border-radius: 999px; border: 1px solid var(--line); font-weight: 600; }
	.down:hover { border-color: var(--green); color: var(--green); }

	.act { max-width: 880px; margin: 0 auto; padding: 5rem 1.5rem; border-top: 1px solid var(--line); }
	.roman { display: block; font-family: var(--display); font-weight: 800; color: var(--green); letter-spacing: 0.1em; text-transform: uppercase; font-size: 0.85rem; margin-bottom: 0.5rem; }
	h2 { font-size: clamp(2rem, 4vw, 3rem); font-weight: 800; letter-spacing: -0.03em; margin-bottom: 2rem; }
	h2 small { font-family: var(--sans); font-size: 1rem; font-weight: 500; color: var(--muted); letter-spacing: 0; }
	.lines { display: grid; gap: 1.25rem; }
	.line { display: grid; grid-template-columns: 6.5rem minmax(0, 1fr); gap: 1.25rem; align-items: start; }
	.when { font-family: var(--display); font-weight: 800; font-size: 1.6rem; line-height: 1.2; color: var(--green); }
	.line p { margin: 0; font-size: 1.45rem; font-weight: 500; line-height: 1.3; }
	.line small { grid-column: 2; color: var(--muted); font-size: 0.8rem; }
	.who { display: flex; align-items: flex-start; gap: 0.75rem; margin: 2rem 0 0; color: var(--muted); }
	.who img { width: 36px; height: 36px; border-radius: 50%; object-fit: cover; flex: none; }
	.who a { color: var(--text); font-weight: 600; border-bottom: 1px solid var(--green); }

	.meter { display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: 1rem; font-size: 0.72rem; font-weight: 600; text-transform: uppercase; letter-spacing: 0.1em; margin-bottom: 1.5rem; }
	.track { position: relative; height: 8px; border-radius: 4px; background: linear-gradient(90deg, var(--green), #ffd166, var(--red)); }
	.track i { position: absolute; top: -7px; width: 22px; height: 22px; border-radius: 50%; transform: translateX(-50%); }
	.track .was { background: transparent; border: 2px dashed var(--text); opacity: 0.6; }
	.track .now { background: var(--text); box-shadow: 0 0 0 4px var(--bg), 0 0 28px rgba(243, 240, 255, 0.7); }
	.delta { font-size: 1.25rem; margin: 0 0 1rem; }
	.delta b { font-family: var(--display); font-weight: 800; font-size: 2.2rem; color: var(--red); }
	.rule { margin: 0; color: var(--muted); }

	.three { max-width: 1120px; text-align: center; }
	.three .roman, .three h2 { text-align: center; }
	.row { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1rem; align-items: end; }
	.star { position: relative; display: flex; flex-direction: column; align-items: center; gap: 0.2rem; padding: 2rem 1rem 1.5rem; background: var(--panel); border: 1px solid var(--line); border-radius: 1.25rem; overflow: hidden; }
	.star.s1 { order: 2; padding-top: 3rem; }
	.star.s2 { order: 1; }
	.star.s3 { order: 3; }
	.halo { position: absolute; top: -40%; left: 50%; width: 120%; height: 80%; transform: translateX(-50%); background: radial-gradient(ellipse at 50% 100%, rgba(255, 59, 92, 0.35), transparent 70%); pointer-events: none; }
	.s1 .halo { background: radial-gradient(ellipse at 50% 100%, rgba(255, 59, 92, 0.55), transparent 70%); }
	.star img { position: relative; width: 88px; height: 88px; border-radius: 50%; object-fit: cover; border: 3px solid var(--red); }
	.s1 img { width: 112px; height: 112px; }
	.score { position: relative; font-family: var(--display); font-weight: 800; font-size: 3rem; line-height: 1.1; color: var(--red); letter-spacing: -0.04em; }
	.s1 .score { font-size: 4rem; }
	.name { font-weight: 600; font-size: 1.05rem; }
	.meta { font-size: 0.8rem; color: var(--muted); }
	.more { display: inline-block; margin-top: 1.5rem; color: var(--green); font-weight: 600; }

	.wings { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1rem; max-width: 1120px; margin: 0 auto; padding: 3rem 1.5rem; border-top: 1px solid var(--line); }
	.wings a { padding: 1.5rem; border: 1px solid var(--line); border-radius: 1.25rem; transition: border-color 0.15s, background 0.15s; }
	.wings a:hover { border-color: var(--green); background: var(--panel); }
	.wings h3 { font-size: 1.3rem; font-weight: 700; margin-bottom: 0.35rem; }
	.wings p { margin: 0; color: var(--muted); font-size: 0.95rem; }

	footer { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 0.5rem; max-width: 1120px; margin: 0 auto; padding: 1.5rem; border-top: 1px solid var(--line); font-size: 0.85rem; color: var(--muted); }

	@media (max-width: 820px) {
		.row, .wings { grid-template-columns: minmax(0, 1fr); }
		.star.s1, .star.s2, .star.s3 { order: initial; }
		.line { grid-template-columns: minmax(0, 1fr); }
		.line small { grid-column: 1; }
		.act { padding: 3.5rem 1.5rem; }
	}
</style>
