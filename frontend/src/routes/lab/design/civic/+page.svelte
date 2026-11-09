<script lang="ts">
	// Direction « Civic »: the site as a public instrument. The UX bet: a
	// visitor wants to look someone up and see where they stand, so the page
	// is the instrument itself: a search field and the barometer with every
	// candidate placed on it, at once. Then the three worst as stat cards,
	// the latest contradictions as a table, the method as a footnote. White,
	// slate, deep blue, soft cards, line icons. Logo: a balance of two
	// unequal lines.
	import { onMount } from 'svelte';
	import { loadLanding, type Landing } from '$lib/lab/podium';

	let data = $state<Landing | null>(null);
	let query = $state('');
	onMount(async () => {
		try {
			data = await loadLanding();
		} catch (err) {
			console.error('landing', err);
		}
	});
	const colour = (s: number) => (s < 15 ? '#16a34a' : s < 30 ? '#d97706' : '#dc2626');
	// The faces on the band, by score; a face that would overlap the one
	// before on its row climbs to the next row (four at most).
	const GAP = 4.6; // in % of the band's width
	let placed = $derived.by(() => {
		const ends: number[] = [];
		return (data?.candidates ?? [])
			.filter((c) => c.reliable)
			.sort((a, b) => a.score - b.score)
			.map((c) => {
				const x = 3 + c.score * 0.94;
				let row = 0;
				while (row < 3 && ends[row] !== undefined && ends[row] > x - GAP) row++;
				ends[row] = Math.max(x, (ends[row] ?? -Infinity));
				return { ...c, x, row };
			});
	});
	let matches = $derived(query.length < 2 ? [] : (data?.candidates ?? []).filter((c) => c.name.toLowerCase().includes(query.toLowerCase())).slice(0, 5));
</script>

<svelte:head>
	<title>Civic — tartuffe.me</title>
	<meta name="robots" content="noindex" />
	<link rel="preconnect" href="https://fonts.googleapis.com" />
	<link href="https://fonts.googleapis.com/css2?family=DM+Sans:opsz,wght@9..40,400;9..40,500;9..40,700;9..40,800&display=swap" rel="stylesheet" />
</svelte:head>

<div class="civic">
	<header class="nav">
		<a class="brand" href="/">
			<svg class="mark" viewBox="0 0 48 48" aria-hidden="true" fill="none" stroke="currentColor" stroke-width="3.5" stroke-linecap="round">
				<path d="M24 8v32" />
				<path d="M10 16h28" />
				<path d="M10 16l-5 12h10z" fill="#1d4ed8" stroke="#1d4ed8" />
				<path d="M38 16l-5 12h10z" />
				<path d="M16 40h16" />
			</svg>
			<span>tartuffe<b>.me</b></span>
		</a>
		<nav>
			<a href="/dashboard">Classement</a>
			<a href="/feed">Fil</a>
			<a href="/about">Méthode</a>
			<a href="/corrections">Corrections</a>
			<a href="https://github.com/lanterne-labs/mirror">Code</a>
		</nav>
	</header>

	<section class="hero">
		<h1>Cohérent ou tartuffe ?</h1>
		<p>Ce qu’ils disent, ce qu’ils votent, ce que la justice a tranché. Quand ça ne colle pas, nous le montrons, sources à l’appui. Nous ne jugeons pas qui a raison.</p>
		<form class="search" onsubmit={(e) => e.preventDefault()}>
			<svg viewBox="0 0 24 24"><circle cx="11" cy="11" r="7" /><path d="m20 20-4-4" /></svg>
			<input type="search" placeholder="Cherchez un candidat, un élu, un parti…" bind:value={query} />
			{#if matches.length}
				<ul class="hits">
					{#each matches as m (m.id)}
						<li><a href="/person/{m.id}">{#if m.image}<img src={m.image} alt="" />{/if}{m.name}<span style="color: {colour(m.score)}">{m.score}/100</span></a></li>
					{/each}
				</ul>
			{/if}
		</form>
	</section>

	<section class="baro">
		<div class="baro-head"><h2>Les candidats à la présidentielle, sur le baromètre</h2><span>{placed.length} scores établis · {(data?.candidates.length ?? 0) - placed.length} en consolidation</span></div>
		<div class="band">
			<div class="track"></div>
			{#each placed as p (p.id)}
				<a class="pin" href="/person/{p.id}" style="left: {p.x}%; top: {(3 - p.row) * 2.6}rem" title="{p.name} · {p.score}/100">
					{#if p.image}<img src={p.image} alt={p.name} />{/if}
					<span>{p.score}</span>
				</a>
			{/each}
		</div>
		<div class="ends"><span>0 · cohérent</span><span>tartuffe · 100</span></div>
	</section>

	<section class="stats">
		{#each data?.podium ?? [] as p, i (p.id)}
			<a class="stat" href="/person/{p.id}" style="--c: {colour(p.score)}">
				<span class="rank">N° {i + 1} · le plus tartuffe</span>
				<div class="row">
					{#if p.image}<img src={p.image} alt="" />{/if}
					<div><span class="name">{p.name}</span><span class="meta">{p.contradictions} contradictions sur {p.facts.toLocaleString('fr-FR')} faits</span></div>
					<span class="score">{p.score}<small>/100</small></span>
				</div>
				<div class="bar"><span style="width: {p.score}%"></span></div>
			</a>
		{/each}
	</section>

	<section class="latest">
		<div class="baro-head"><h2>Les dernières contradictions</h2><a href="/feed">Tout le fil →</a></div>
		<table>
			<thead><tr><th>Qui</th><th>Sujet</th><th>Avant</th><th>Après</th><th class="r">Score</th></tr></thead>
			<tbody>
				{#each data?.clashes.slice(0, 6) ?? [] as c (c.id)}
					<tr>
						<td><a class="who" href="/person/{c.person.id}?c={c.id}">{#if c.person.image}<img src={c.person.image} alt="" />{/if}{c.person.name}</a></td>
						<td class="topic">{c.topic}</td>
						<td class="q"><small>{c.a.year}</small>{c.a.quote || c.a.text}</td>
						<td class="q"><small>{c.b.year}</small>{c.b.quote || c.b.text}</td>
						<td class="r"><b style="color: {colour(c.person.score)}">+{c.rise}</b></td>
					</tr>
				{/each}
			</tbody>
		</table>
	</section>

	<section class="method">
		<div><svg viewBox="0 0 24 24"><path d="M4 6h16M4 12h10M4 18h7" /></svg><h3>On relève</h3><p>Ses déclarations, ses votes, et les décisions de justice qui le concernent. Chaque fait garde sa source.</p></div>
		<div><svg viewBox="0 0 24 24"><path d="M8 7l-4 5 4 5M16 7l4 5-4 5" /></svg><h3>On confronte</h3><p>Deux faits qui ne peuvent pas être vrais en même temps. Une nuance ne suffit pas. Une seconde IA relit.</p></div>
		<div><svg viewBox="0 0 24 24"><path d="M4 19h16M6 15l4-4 3 3 5-6" /></svg><h3>Le score monte</h3><p>De 0, cohérent, à 100, tartuffe. Chaque contradiction distincte le pousse. Il ne dit pas qui a raison.</p></div>
		<div><svg viewBox="0 0 24 24"><path d="M12 3l8 4v5c0 5-3.5 8-8 9-4.5-1-8-4-8-9V7z" /><path d="m9 12 2 2 4-4" /></svg><h3>Vous corrigez</h3><p>Signalez une erreur : si vous avez raison, elle est retirée, publiquement. <a href="https://github.com/lanterne-labs/mirror">Le code est ouvert.</a></p></div>
	</section>

	<footer>
		<span>tartuffe.me — un laboratoire d’outils pour une démocratie plus transparente.</span>
		<span><a href="/about">Méthode</a> · <a href="/about/privacy">Confidentialité</a> · <a href="/contact">Contact</a> · <a href="/mentions-legales">Mentions légales</a></span>
	</footer>
</div>

<style>
	.civic {
		--bg: #ffffff;
		--soft: #f4f6fa;
		--line: #e3e8f0;
		--ink: #0f172a;
		--grey: #556070;
		--blue: #1d4ed8;
		--sans: 'DM Sans', system-ui, sans-serif;
		min-height: 100vh;
		background: var(--bg);
		color: var(--ink);
		font-family: var(--sans);
		line-height: 1.55;
	}
	a { color: inherit; text-decoration: none; }
	h1, h2, h3 { margin: 0; font-weight: 800; letter-spacing: -0.02em; }
	svg { fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }

	.nav { display: flex; align-items: center; justify-content: space-between; max-width: 1120px; margin: 0 auto; padding: 1rem 1.5rem; }
	.brand { display: flex; align-items: center; gap: 0.6rem; font-weight: 800; font-size: 1.2rem; }
	.brand b { color: var(--blue); }
	.mark { width: 34px; height: 34px; stroke-width: 3.5; }
	.nav nav { display: flex; align-items: center; gap: 1.5rem; font-weight: 500; color: var(--grey); }
	.nav nav a:hover { color: var(--ink); }

	.hero { max-width: 760px; margin: 0 auto; padding: 3.5rem 1.5rem 1.5rem; text-align: center; }
	h1 { font-size: clamp(2.6rem, 6vw, 4rem); line-height: 1.05; margin-bottom: 0.9rem; }
	.hero p { font-size: 1.1rem; color: var(--grey); margin: 0 0 1.5rem; }
	.search { position: relative; display: flex; align-items: center; gap: 0.6rem; padding: 0.3rem 0.5rem 0.3rem 1rem; background: var(--bg); border: 2px solid var(--line); border-radius: 1rem; box-shadow: 0 10px 30px rgba(15, 23, 42, 0.06); text-align: left; }
	.search:focus-within { border-color: var(--blue); }
	.search svg { width: 22px; height: 22px; color: var(--grey); flex: none; }
	.search input { flex: 1; font: inherit; font-size: 1.05rem; padding: 0.7rem 0.2rem; border: 0; outline: 0; background: transparent; color: var(--ink); }
	.hits { position: absolute; left: 0; right: 0; top: calc(100% + 0.4rem); list-style: none; margin: 0; padding: 0.3rem; background: var(--bg); border: 1px solid var(--line); border-radius: 0.9rem; box-shadow: 0 16px 40px rgba(15, 23, 42, 0.12); z-index: 3; }
	.hits a { display: flex; align-items: center; gap: 0.6rem; padding: 0.5rem 0.7rem; border-radius: 0.6rem; font-weight: 500; }
	.hits a:hover { background: var(--soft); }
	.hits img { width: 28px; height: 28px; border-radius: 50%; object-fit: cover; }
	.hits span { margin-left: auto; font-weight: 700; }

	.baro { max-width: 1120px; margin: 0 auto; padding: 2rem 1.5rem 1rem; }
	.baro-head { display: flex; justify-content: space-between; align-items: baseline; gap: 1rem; flex-wrap: wrap; margin-bottom: 1rem; }
	.baro-head h2, .latest h2 { font-size: 1.25rem; }
	.baro-head span, .baro-head a { font-size: 0.85rem; color: var(--grey); font-weight: 500; }
	.baro-head a { color: var(--blue); font-weight: 700; }
	.band { position: relative; height: 12.4rem; margin: 0 1.5rem; }
	.track { position: absolute; left: 0; right: 0; top: 11.6rem; height: 10px; border-radius: 5px; background: linear-gradient(90deg, #16a34a, #d97706, #dc2626); opacity: 0.85; }
	.pin { position: absolute; transform: translateX(-50%); display: flex; flex-direction: column; align-items: center; gap: 0.15rem; transition: transform 0.15s; z-index: 1; }
	.pin:hover { transform: translateX(-50%) scale(1.12); z-index: 2; }
	.pin img { width: 40px; height: 40px; border-radius: 50%; object-fit: cover; border: 2px solid var(--bg); box-shadow: 0 2px 8px rgba(15, 23, 42, 0.2); }
	.pin span { font-size: 0.7rem; font-weight: 700; color: var(--grey); }
	.pin::after { content: ''; width: 2px; flex: 1; min-height: 0.5rem; background: var(--line); }
	.pin { height: 4.2rem; }
	.ends { display: flex; justify-content: space-between; margin: 0.5rem 1.5rem 0; font-size: 0.72rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.08em; color: var(--grey); }

	.stats { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1rem; max-width: 1120px; margin: 0 auto; padding: 2rem 1.5rem; }
	.stat { display: block; padding: 1.1rem 1.25rem; background: var(--bg); border: 1px solid var(--line); border-radius: 1rem; box-shadow: 0 1px 2px rgba(15, 23, 42, 0.04); transition: box-shadow 0.15s, transform 0.15s; }
	.stat:hover { box-shadow: 0 10px 30px rgba(15, 23, 42, 0.08); transform: translateY(-2px); }
	.rank { font-size: 0.72rem; font-weight: 700; text-transform: uppercase; letter-spacing: 0.08em; color: var(--grey); }
	.row { display: grid; grid-template-columns: 44px minmax(0, 1fr) auto; align-items: center; gap: 0.75rem; margin: 0.6rem 0 0.75rem; }
	.row img { width: 44px; height: 44px; border-radius: 50%; object-fit: cover; }
	.name { display: block; font-weight: 700; }
	.meta { display: block; font-size: 0.78rem; color: var(--grey); }
	.score { font-size: 2rem; font-weight: 800; color: var(--c); letter-spacing: -0.03em; }
	.score small { font-size: 0.8rem; color: var(--grey); font-weight: 500; }
	.bar { height: 8px; border-radius: 4px; background: var(--soft); overflow: hidden; }
	.bar span { display: block; height: 100%; border-radius: 4px; background: var(--c); }

	.latest { max-width: 1120px; margin: 0 auto; padding: 1rem 1.5rem 2rem; }
	table { width: 100%; border-collapse: collapse; font-size: 0.9rem; }
	th { text-align: left; font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.08em; color: var(--grey); padding: 0.5rem 0.75rem; border-bottom: 1px solid var(--line); }
	td { padding: 0.8rem 0.75rem; border-bottom: 1px solid var(--line); vertical-align: top; }
	.who { display: flex; align-items: center; gap: 0.5rem; font-weight: 700; white-space: nowrap; }
	.who img { width: 28px; height: 28px; border-radius: 50%; object-fit: cover; }
	.topic { color: var(--grey); max-width: 11rem; }
	.q { color: var(--ink); max-width: 20rem; }
	.q small { display: block; color: var(--grey); font-weight: 700; margin-bottom: 0.1rem; }
	.r { text-align: right; white-space: nowrap; }
	.r b { font-size: 1.1rem; }

	.method { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1.5rem; max-width: 1120px; margin: 0 auto; padding: 2rem 1.5rem 3rem; border-top: 1px solid var(--line); }
	.method svg { width: 26px; height: 26px; color: var(--blue); }
	.method h3 { font-size: 1.05rem; margin: 0.6rem 0 0.3rem; }
	.method p { margin: 0; color: var(--grey); font-size: 0.9rem; }
	.method p a { color: var(--blue); font-weight: 700; }

	footer { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 0.5rem; max-width: 1120px; margin: 0 auto; padding: 1.5rem; border-top: 1px solid var(--line); font-size: 0.85rem; color: var(--grey); }

	@media (max-width: 860px) {
		.stats, .method { grid-template-columns: minmax(0, 1fr); }
		.nav nav a { display: none; }
		.nav nav a:first-child { display: inline; }
		.topic, th:nth-child(2), .q:nth-of-type(2) { display: none; }
		.band { height: 12.4rem; }
	}
</style>
