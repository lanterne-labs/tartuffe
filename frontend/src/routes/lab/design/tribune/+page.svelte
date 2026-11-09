<script lang="ts">
	// Direction « Tribune »: the site as a daily. The UX bet: a visitor comes
	// for the news, so the page leads with today's contradiction as its front
	// page story, the ranking as the sidebar every paper has, then the wire of
	// the latest ones; what the score is comes after, in a column. Ink on
	// paper, a huge serif, rules. Logo: a T broken in two.
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
	let wire = $derived(data?.clashes.slice(1, 6) ?? []);
	const today = new Date().toLocaleDateString('fr-FR', { weekday: 'long', day: 'numeric', month: 'long', year: 'numeric' });
</script>

<svelte:head>
	<title>Tribune — tartuffe.me</title>
	<meta name="robots" content="noindex" />
	<link rel="preconnect" href="https://fonts.googleapis.com" />
	<link href="https://fonts.googleapis.com/css2?family=Playfair+Display:ital,wght@0,700;0,900;1,700&family=Inter:wght@400;500;700&display=swap" rel="stylesheet" />
</svelte:head>

<div class="tribune">
	<header class="masthead">
		<div class="wrap">
			<div class="top-rule">
				<span>{today}</span>
				<span>Cohérent ou tartuffe ? · ce qu’ils disent, ce qu’ils votent, ce que la justice a tranché</span>
				<span>FR · EN</span>
			</div>
			<a class="brand" href="/">
				<svg class="mark" viewBox="0 0 48 48" aria-hidden="true">
					<rect x="6" y="6" width="36" height="9" fill="currentColor" />
					<rect x="19" y="15" width="10" height="13" fill="currentColor" />
					<rect x="21" y="30" width="10" height="12" fill="currentColor" transform="skewX(-14)" />
					<path d="M15 28 L34 28" stroke="#c81e1e" stroke-width="3" />
				</svg>
				<span class="wordmark">tartuffe<span class="dot">.</span>me</span>
			</a>
			<nav class="rubrics">
				<a href="/feed">Le fil</a>
				<a href="/dashboard">Le classement</a>
				<a href="/about">La méthode</a>
				<a href="/corrections">Corrections</a>
				<a href="https://github.com/lanterne-labs/mirror">Le code</a>
			</nav>
		</div>
	</header>

	<main class="wrap">
		<section class="une">
			<article class="story">
				{#if lead}
					<p class="surtitre">À la une · {lead.topic}</p>
					<h1>{lead.person.name} <em>se contredit</em></h1>
					<p class="chapo">{lead.reason}</p>
					<div class="pair">
						<blockquote><cite>{lead.a.date} · {lead.a.domain}</cite>{lead.a.quote || lead.a.text}</blockquote>
						<blockquote class="late"><cite>{lead.b.date} · {lead.b.domain}</cite>{lead.b.quote || lead.b.text}</blockquote>
					</div>
					<p class="effect"><strong>+{lead.rise}</strong> au Tartuffe-Score, qui atteint <strong>{lead.person.score}/100</strong>. <a href="/person/{lead.person.id}?c={lead.id}">Lire la fiche et les sources →</a></p>
				{:else}
					<p class="surtitre">À la une</p>
					<h1>Cohérent<br />ou <em>tartuffe</em> ?</h1>
					<p class="chapo">Chargement de l’édition du jour…</p>
				{/if}
			</article>
			<aside class="side">
				<h2 class="rubrique">Le classement du jour</h2>
				<ol class="rank">
					{#each data?.podium ?? [] as p, i (p.id)}
						<li>
							<span class="n">{i + 1}</span>
							{#if p.image}<img src={p.image} alt="" />{/if}
							<span class="who"><a href="/person/{p.id}">{p.name}</a><small>{p.contradictions} contradictions · {p.facts.toLocaleString('fr-FR')} faits</small></span>
							<span class="score">{p.score}<small>/100</small></span>
						</li>
					{/each}
				</ol>
				<a class="more" href="/dashboard">Les {data?.candidates.length ?? ''} candidats →</a>
				<div class="box">
					<h3>Le Tartuffe-Score</h3>
					<p>De 0, cohérent, à 100, tartuffe. Il mesure la cohérence d’un élu avec lui-même et avec la justice : chaque contradiction distincte le fait monter. Il ne dit pas qui a raison.</p>
					<div class="scale"><span>0</span><i></i><span>100</span></div>
					<a href="/about">La méthode →</a>
				</div>
			</aside>
		</section>

		<section class="wire">
			<h2 class="rubrique">Dépêches · les dernières contradictions</h2>
			<div class="wire-grid">
				{#each wire as c (c.id)}
					<a class="item" href="/person/{c.person.id}?c={c.id}">
						<span class="meta">{c.topic} · {c.b.date}</span>
						<h3>{c.person.name}</h3>
						<p>{c.reason}</p>
						<span class="rise">+{c.rise}</span>
					</a>
				{/each}
			</div>
			<a class="more" href="/feed">Tout le fil →</a>
		</section>

		<section class="method">
			<article>
				<span class="n">1</span><h3>On relève</h3><p>Ses déclarations, ses votes, et les décisions de justice qui le concernent. Chaque fait garde sa source et sa citation.</p>
			</article>
			<article>
				<span class="n">2</span><h3>On confronte</h3><p>Une IA cherche deux faits qui ne peuvent pas être vrais en même temps. Une nuance ne suffit pas. Une seconde relit.</p>
			</article>
			<article>
				<span class="n">3</span><h3>Vous corrigez</h3><p>Un fait faux, une contradiction douteuse : signalez-la. Si vous avez raison, elle est retirée, et la correction est publique.</p>
			</article>
			<article class="open">
				<span class="n">∞</span><h3>Tout est ouvert</h3><p>Le code, la collecte, la détection, le calcul : <a href="https://github.com/lanterne-labs/mirror">lisez-le, vérifiez-le, corrigez-le</a>.</p>
			</article>
		</section>
	</main>

	<footer class="wrap colophon">
		<span>tartuffe.me — un laboratoire d’outils pour une démocratie plus transparente.</span>
		<span><a href="/about">Méthode</a> · <a href="/about/privacy">Confidentialité</a> · <a href="/contact">Contact</a> · <a href="/mentions-legales">Mentions légales</a></span>
	</footer>
</div>

<style>
	.tribune {
		--paper: #f4f0e8;
		--ink: #121212;
		--grey: #5a564f;
		--rule: #121212;
		--red: #c81e1e;
		--serif: 'Playfair Display', Georgia, serif;
		--sans: 'Inter', system-ui, sans-serif;
		min-height: 100vh;
		background: var(--paper);
		color: var(--ink);
		font-family: var(--sans);
		line-height: 1.5;
	}
	.wrap { max-width: 1180px; margin: 0 auto; padding: 0 1.5rem; }
	a { color: inherit; text-decoration: none; }
	h1, h2, h3 { margin: 0; }

	.masthead { border-bottom: 3px double var(--rule); }
	.top-rule { display: flex; justify-content: space-between; gap: 1rem; padding: 0.4rem 0; border-bottom: 1px solid var(--rule); font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.1em; color: var(--grey); }
	.brand { display: flex; align-items: center; justify-content: center; gap: 0.75rem; padding: 1.1rem 0 0.9rem; }
	.mark { width: 44px; height: 44px; color: var(--ink); }
	.wordmark { font-family: var(--serif); font-weight: 900; font-size: 2.6rem; letter-spacing: -0.02em; }
	.dot { color: var(--red); }
	.rubrics { display: flex; justify-content: center; gap: 2rem; padding: 0.5rem 0; border-top: 1px solid var(--rule); font-size: 0.8rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.1em; }
	.rubrics a:hover { color: var(--red); }

	.une { display: grid; grid-template-columns: minmax(0, 1.6fr) minmax(0, 1fr); gap: 2.5rem; padding: 2rem 0; border-bottom: 1px solid var(--rule); }
	.surtitre { font-size: 0.75rem; text-transform: uppercase; letter-spacing: 0.14em; color: var(--red); font-weight: 700; margin: 0 0 0.6rem; }
	h1 { font-family: var(--serif); font-weight: 900; font-size: clamp(2.6rem, 5.5vw, 4.6rem); line-height: 1; letter-spacing: -0.03em; margin-bottom: 1rem; }
	h1 em { font-style: italic; color: var(--red); }
	.chapo { font-size: 1.15rem; color: var(--grey); margin: 0 0 1.25rem; font-weight: 500; }
	.pair { display: grid; grid-template-columns: 1fr 1fr; gap: 1.25rem; }
	.pair blockquote { margin: 0; padding: 0.9rem 1.1rem; border-top: 3px solid var(--ink); font-family: var(--serif); font-size: 1.15rem; font-weight: 700; line-height: 1.3; }
	.pair .late { border-top-color: var(--red); }
	.pair cite { display: block; font-family: var(--sans); font-style: normal; font-weight: 500; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--grey); margin-bottom: 0.4rem; }
	.effect { margin: 1.25rem 0 0; font-size: 0.95rem; color: var(--grey); }
	.effect strong { font-family: var(--serif); font-size: 1.5rem; color: var(--red); }
	.effect a { font-weight: 700; color: var(--ink); border-bottom: 2px solid var(--red); margin-left: 0.4rem; }

	.rubrique { font-size: 0.78rem; text-transform: uppercase; letter-spacing: 0.14em; font-weight: 700; border-bottom: 3px solid var(--ink); padding-bottom: 0.4rem; margin-bottom: 0.5rem; }
	.rank { list-style: none; margin: 0; padding: 0; }
	.rank li { display: grid; grid-template-columns: 2.2rem 44px minmax(0, 1fr) auto; align-items: center; gap: 0.7rem; padding: 0.7rem 0; border-bottom: 1px solid rgba(18, 18, 18, 0.2); }
	.rank .n { font-family: var(--serif); font-weight: 900; font-size: 1.8rem; line-height: 1; }
	.rank img { width: 44px; height: 44px; border-radius: 50%; object-fit: cover; filter: grayscale(1) contrast(1.1); }
	.who { display: flex; flex-direction: column; min-width: 0; }
	.who a { font-family: var(--serif); font-weight: 700; font-size: 1.1rem; }
	.who small { color: var(--grey); font-size: 0.72rem; }
	.score { font-family: var(--serif); font-weight: 900; font-size: 1.7rem; color: var(--red); }
	.score small { font-size: 0.8rem; color: var(--grey); font-family: var(--sans); font-weight: 500; }
	.more { display: inline-block; margin-top: 0.75rem; font-weight: 700; font-size: 0.85rem; border-bottom: 2px solid var(--red); }
	.box { margin-top: 1.75rem; padding: 1rem 1.1rem; border: 1px solid var(--ink); }
	.box h3 { font-family: var(--serif); font-size: 1.2rem; margin-bottom: 0.4rem; }
	.box p { font-size: 0.88rem; color: var(--grey); margin: 0 0 0.75rem; }
	.box a { font-weight: 700; font-size: 0.85rem; }
	.scale { display: flex; align-items: center; gap: 0.5rem; font-size: 0.7rem; font-weight: 700; margin-bottom: 0.75rem; }
	.scale i { flex: 1; height: 8px; background: linear-gradient(90deg, #3d7a46, #d4923a, var(--red)); }

	.wire { padding: 1.75rem 0; border-bottom: 1px solid var(--rule); }
	.wire-grid { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 0; }
	.item { position: relative; padding: 0.75rem 1rem 1rem; border-left: 1px solid rgba(18, 18, 18, 0.25); }
	.item:first-child { border-left: 0; padding-left: 0; }
	.item:hover h3 { color: var(--red); }
	.item .meta { display: block; font-size: 0.66rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--grey); margin-bottom: 0.3rem; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
	.item h3 { font-family: var(--serif); font-size: 1.15rem; font-weight: 700; line-height: 1.15; margin-bottom: 0.35rem; }
	.item p { font-size: 0.82rem; color: var(--grey); margin: 0 0 1.4rem; display: -webkit-box; -webkit-line-clamp: 4; line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; }
	.item .rise { position: absolute; bottom: 0.6rem; left: 1rem; font-family: var(--serif); font-weight: 900; color: var(--red); }
	.item:first-child .rise { left: 0; }

	.method { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1.5rem; padding: 2rem 0 3rem; }
	.method .n { font-family: var(--serif); font-weight: 900; font-size: 2.4rem; line-height: 1; color: var(--red); }
	.method h3 { font-family: var(--serif); font-size: 1.3rem; margin: 0.4rem 0 0.3rem; }
	.method p { font-size: 0.9rem; color: var(--grey); margin: 0; }
	.method .open { padding: 1rem; border: 1px solid var(--ink); }
	.method .open a { font-weight: 700; color: var(--ink); border-bottom: 2px solid var(--red); }

	.colophon { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 0.5rem; padding-top: 1rem; padding-bottom: 2rem; border-top: 3px double var(--rule); font-size: 0.78rem; color: var(--grey); }
	.colophon a:hover { color: var(--red); }

	@media (max-width: 900px) {
		.une, .method { grid-template-columns: minmax(0, 1fr); }
		.wire-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
		.item, .item:first-child { border-left: 0; padding-left: 0; border-top: 1px solid rgba(18, 18, 18, 0.25); padding-top: 0.75rem; }
		.item .rise { left: 0; }
		.top-rule span:nth-child(2) { display: none; }
		.rubrics { flex-wrap: wrap; gap: 0.75rem 1.25rem; }
		.wordmark { font-size: 2rem; }
		.pair { grid-template-columns: minmax(0, 1fr); }
	}
</style>
