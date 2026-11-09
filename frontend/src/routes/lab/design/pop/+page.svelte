<script lang="ts">
	// Direction « Pop »: the site as a game you share. The UX bet: a young
	// visitor does not read a method, they play. The page opens on « Qui a dit
	// ça ? »: a real quote, three faces, tap one, the contradiction is revealed
	// with its rise; then a stack of the latest ones to flick through, then
	// the leaderboard with giant numbers. Yellow, black borders, hard
	// shadows, stickers. Logo: two speech bubbles that overlap and crack.
	import { onMount } from 'svelte';
	import { loadLanding, type Landing, type Clash, type Laureate } from '$lib/lab/podium';

	let data = $state<Landing | null>(null);
	let round = $state(0);
	let picked = $state<number | null>(null);
	onMount(async () => {
		try {
			data = await loadLanding();
		} catch (err) {
			console.error('landing', err);
		}
	});
	let quiz = $derived<Clash | null>(data?.clashes[round % Math.max(1, data.clashes.length)] ?? null);
	// Three faces: the author and two other candidates, in a fixed shuffle.
	let faces = $derived.by((): Laureate[] => {
		if (!data || !quiz) return [];
		const others = data.candidates.filter((c) => c.id !== quiz.person.id && c.image);
		const pick = [others[(round * 3) % others.length], others[(round * 3 + 1) % others.length]];
		const all = [quiz.person, ...pick];
		const k = round % 3;
		return [...all.slice(k), ...all.slice(0, k)];
	});
	function next() {
		round += 1;
		picked = null;
	}
	const strip = 'Ils disent · Ils votent · Ça ne colle pas toujours · Sources à l’appui · ';
</script>

<svelte:head>
	<title>Pop — tartuffe.me</title>
	<meta name="robots" content="noindex" />
	<link rel="preconnect" href="https://fonts.googleapis.com" />
	<link href="https://fonts.googleapis.com/css2?family=Archivo+Black&family=Space+Grotesk:wght@400;500;700&display=swap" rel="stylesheet" />
</svelte:head>

<div class="pop">
	<header class="bar">
		<a class="brand" href="/">
			<svg class="mark" viewBox="0 0 48 48" aria-hidden="true">
				<path d="M6 10h22a4 4 0 0 1 4 4v10a4 4 0 0 1-4 4H16l-6 6v-6H6a4 4 0 0 1-4-4V14a4 4 0 0 1 4-4z" fill="#111" />
				<path d="M20 18h22a4 4 0 0 1 4 4v10a4 4 0 0 1-4 4h-4v6l-6-6H20a4 4 0 0 1-4-4V22a4 4 0 0 1 4-4z" fill="#ff2d7a" stroke="#111" stroke-width="2.5" />
				<path d="M27 20l-3 5h4l-3 6" fill="none" stroke="#fff" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" />
			</svg>
			<span>tartuffe.me</span>
		</a>
		<nav>
			<a href="/feed">Le fil</a>
			<a href="/dashboard">Classement</a>
			<a href="/about">Méthode</a>
			<a class="pill" href="https://github.com/lanterne-labs/mirror">Code ↗</a>
		</nav>
	</header>

	<section class="quiz">
		<div class="q-left">
			<span class="sticker s1">Un vrai jeu, de vraies citations</span>
			<h1>Qui a dit ça<span class="q">?</span></h1>
			{#if quiz}
				<blockquote class="bubble">
					<small>{quiz.a.date} · {quiz.a.domain}</small>
					{quiz.a.quote || quiz.a.text}
				</blockquote>
				<div class="faces">
					{#each faces as f (f.id)}
						<button class="face" class:right={picked !== null && f.id === quiz.person.id} class:wrong={picked === f.id && f.id !== quiz.person.id} disabled={picked !== null} onclick={() => (picked = f.id)}>
							{#if f.image}<img src={f.image} alt={f.name} />{/if}
							<span>{picked !== null ? f.name : '?'}</span>
						</button>
					{/each}
				</div>
			{/if}
		</div>
		<div class="q-right">
			{#if quiz && picked !== null}
				<div class="reveal">
					<span class="sticker s2">{picked === quiz.person.id ? 'Bien vu !' : 'Raté !'}</span>
					<h2>{quiz.person.name}</h2>
					<p class="then">… et {quiz.b.year} :</p>
					<blockquote class="bubble alt"><small>{quiz.b.date} · {quiz.b.domain}</small>{quiz.b.quote || quiz.b.text}</blockquote>
					<p class="why">{quiz.reason}</p>
					<div class="rise"><b>+{quiz.rise}</b> au Tartuffe-Score → <b>{quiz.person.score}</b>/100</div>
					<div class="ctas">
						<button class="btn" onclick={next}>Encore une →</button>
						<a class="btn alt" href="/person/{quiz.person.id}?c={quiz.id}">Voir les sources</a>
					</div>
				</div>
			{:else}
				<div class="teaser">
					<p><b>Cohérent ou tartuffe ?</b> Ce qu’ils disent, ce qu’ils votent, ce que la justice a tranché. Quand ça ne colle pas, on le montre, sources à l’appui.</p>
					<p class="hint">Touche un visage pour voir la suite.</p>
				</div>
			{/if}
		</div>
	</section>

	<div class="marquee" aria-hidden="true"><div>{strip}{strip}{strip}</div></div>

	<section class="stack">
		<h2>Les dernières <small>à faire défiler</small></h2>
		<div class="rail">
			{#each data?.clashes.slice(0, 8) ?? [] as c, i (c.id)}
				<a class="card" style="--tilt: {(i % 3) - 1}deg" href="/person/{c.person.id}?c={c.id}">
					<div class="head">{#if c.person.image}<img src={c.person.image} alt="" />{/if}<span>{c.person.name}</span></div>
					<p class="a">« {c.a.quote || c.a.text} »<small>{c.a.year}</small></p>
					<p class="b">« {c.b.quote || c.b.text} »<small>{c.b.year}</small></p>
					<span class="badge">+{c.rise}</span>
				</a>
			{/each}
		</div>
	</section>

	<section class="board">
		<h2>Le tableau <small>les plus tartuffes aujourd’hui</small></h2>
		<div class="cards">
			{#each data?.podium ?? [] as p, i (p.id)}
				<a class="big c{i + 1}" href="/person/{p.id}">
					<span class="rank">#{i + 1}</span>
					{#if p.image}<img src={p.image} alt="" />{/if}
					<span class="score">{p.score}</span>
					<span class="name">{p.name}</span>
					<span class="meta">{p.contradictions} contradictions · {p.facts.toLocaleString('fr-FR')} faits</span>
				</a>
			{/each}
		</div>
		<a class="btn" href="/dashboard">Tout le classement →</a>
	</section>

	<section class="access">
		<a href="/about" class="tile t1"><h3>Comment ça marche</h3><p>On relève, on confronte, on publie. Vous corrigez.</p></a>
		<a href="/feed" class="tile t2"><h3>Le fil</h3><p>Toutes les contradictions. Un +1 les fait remonter.</p></a>
		<a href="https://github.com/lanterne-labs/mirror" class="tile t3"><h3>Le code</h3><p>Open source. Lisez-le, vérifiez-le, corrigez-le.</p></a>
	</section>

	<footer>
		<span>tartuffe.me · un labo pour une démocratie plus transparente</span>
		<span><a href="/about">Méthode</a> · <a href="/about/privacy">Confidentialité</a> · <a href="/contact">Contact</a> · <a href="/mentions-legales">Mentions légales</a></span>
	</footer>
</div>

<style>
	.pop {
		--yellow: #ffe14d;
		--pink: #ff2d7a;
		--blue: #2d6bff;
		--green: #27d07a;
		--black: #111;
		--white: #fffdf5;
		--display: 'Archivo Black', Impact, sans-serif;
		--sans: 'Space Grotesk', system-ui, sans-serif;
		min-height: 100vh;
		background: var(--white);
		color: var(--black);
		font-family: var(--sans);
		line-height: 1.45;
	}
	a { color: inherit; text-decoration: none; }
	h1, h2, h3 { font-family: var(--display); font-weight: 400; margin: 0; }
	button { font: inherit; cursor: pointer; }

	.bar { display: flex; align-items: center; justify-content: space-between; padding: 0.8rem 1.5rem; background: var(--yellow); border-bottom: 3px solid var(--black); }
	.brand { display: flex; align-items: center; gap: 0.6rem; font-family: var(--display); font-size: 1.3rem; }
	.mark { width: 38px; height: 38px; }
	.bar nav { display: flex; gap: 1.25rem; align-items: center; font-weight: 700; }
	.pill { padding: 0.3rem 0.8rem; background: var(--black); color: var(--yellow); border-radius: 999px; }

	.quiz { display: grid; grid-template-columns: minmax(0, 1.1fr) minmax(0, 1fr); gap: 2.5rem; align-items: start; max-width: 1180px; margin: 0 auto; padding: 3.5rem 1.5rem 3rem; }
	.q-left { position: relative; }
	h1 { font-size: clamp(3rem, 7vw, 5.5rem); line-height: 0.92; letter-spacing: -0.02em; text-transform: uppercase; margin-bottom: 1.25rem; }
	.q { color: var(--pink); }
	.sticker { position: absolute; padding: 0.3rem 0.7rem; background: var(--pink); color: #fff; font-weight: 700; font-size: 0.8rem; text-transform: uppercase; border: 2px solid var(--black); transform: rotate(-6deg); z-index: 2; }
	.s1 { top: -1.8rem; left: 0; }
	.s2 { top: -1rem; right: 1rem; background: var(--green); color: var(--black); transform: rotate(5deg); }
	.bubble { position: relative; margin: 0; padding: 1rem 1.2rem; border: 3px solid var(--black); border-radius: 1.2rem 1.2rem 1.2rem 0.2rem; background: var(--yellow); font-weight: 700; font-size: 1.25rem; line-height: 1.3; box-shadow: 6px 6px 0 var(--black); }
	.bubble small { display: block; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.08em; opacity: 0.7; margin-bottom: 0.3rem; font-weight: 700; }
	.bubble.alt { background: #fff; border-radius: 1.2rem 1.2rem 0.2rem 1.2rem; font-size: 1.05rem; }
	.faces { display: flex; gap: 1rem; margin-top: 1.75rem; flex-wrap: wrap; }
	.face { display: flex; flex-direction: column; align-items: center; gap: 0.4rem; padding: 0.6rem 0.8rem 0.5rem; background: #fff; border: 3px solid var(--black); box-shadow: 5px 5px 0 var(--black); transition: transform 0.1s; }
	.face:not(:disabled):hover { transform: translate(-2px, -2px); }
	.face img { width: 84px; height: 84px; border-radius: 50%; object-fit: cover; border: 3px solid var(--black); }
	.face span { font-weight: 700; font-size: 0.85rem; }
	.face.right { background: var(--green); }
	.face.wrong { background: var(--pink); color: #fff; opacity: 0.8; }
	.face:disabled:not(.right):not(.wrong) { opacity: 0.45; }

	.q-right { position: relative; }
	.teaser { padding: 1.5rem; border: 3px dashed var(--black); font-size: 1.15rem; font-weight: 500; }
	.hint { margin: 0.75rem 0 0; font-weight: 700; color: var(--pink); }
	.reveal { position: relative; padding: 1.75rem 1.5rem 1.5rem; background: #fff; border: 3px solid var(--black); box-shadow: 10px 10px 0 var(--black); }
	.reveal h2 { font-size: 1.8rem; margin-bottom: 0.25rem; }
	.then { margin: 0 0 0.6rem; font-weight: 700; opacity: 0.7; }
	.why { margin: 1rem 0 0.75rem; font-weight: 500; }
	.rise { font-size: 1.05rem; }
	.rise b { font-family: var(--display); font-size: 1.5rem; color: var(--pink); }
	.ctas { display: flex; gap: 0.75rem; flex-wrap: wrap; margin-top: 1rem; }
	.btn { display: inline-block; padding: 0.8rem 1.3rem; background: var(--black); color: var(--yellow); font-family: var(--display); font-size: 0.95rem; border: 3px solid var(--black); box-shadow: 5px 5px 0 var(--pink); transition: transform 0.1s, box-shadow 0.1s; }
	.btn:hover { transform: translate(2px, 2px); box-shadow: 3px 3px 0 var(--pink); }
	.btn.alt { background: #fff; color: var(--black); box-shadow: 5px 5px 0 var(--blue); }

	.marquee { overflow: hidden; background: var(--black); color: var(--yellow); border-top: 3px solid var(--black); border-bottom: 3px solid var(--black); }
	.marquee div { display: inline-block; white-space: nowrap; padding: 0.55rem 0; font-family: var(--display); font-size: 1.1rem; text-transform: uppercase; animation: slide 28s linear infinite; }
	@keyframes slide { from { transform: translateX(0); } to { transform: translateX(-33.33%); } }

	.stack { padding: 3rem 0 2rem; }
	.stack h2, .board h2 { font-size: 2rem; text-transform: uppercase; margin: 0 auto 1.25rem; max-width: 1180px; padding: 0 1.5rem; }
	h2 small { font-family: var(--sans); font-size: 1rem; text-transform: none; font-weight: 500; opacity: 0.6; margin-left: 0.5rem; }
	.rail { display: flex; gap: 1.25rem; overflow-x: auto; padding: 1rem 1.5rem 1.5rem; scroll-snap-type: x mandatory; }
	.card { position: relative; flex: 0 0 300px; scroll-snap-align: start; padding: 1.1rem; background: #fff; border: 3px solid var(--black); box-shadow: 7px 7px 0 var(--black); transform: rotate(var(--tilt)); transition: transform 0.15s; }
	.card:hover { transform: rotate(0) translateY(-3px); }
	.head { display: flex; align-items: center; gap: 0.5rem; font-weight: 700; margin-bottom: 0.75rem; }
	.head img { width: 36px; height: 36px; border-radius: 50%; border: 2px solid var(--black); object-fit: cover; }
	.card p { margin: 0 0 0.6rem; padding: 0.6rem 0.75rem; border: 2px solid var(--black); font-weight: 500; font-size: 0.9rem; display: -webkit-box; -webkit-line-clamp: 4; line-clamp: 4; -webkit-box-orient: vertical; overflow: hidden; }
	.card .a { background: var(--yellow); }
	.card .b { background: #fff; }
	.card small { display: block; font-weight: 700; opacity: 0.6; font-size: 0.7rem; margin-top: 0.2rem; }
	.badge { position: absolute; top: -0.8rem; right: -0.6rem; padding: 0.2rem 0.6rem; background: var(--pink); color: #fff; font-family: var(--display); border: 2px solid var(--black); transform: rotate(6deg); }

	.board { background: var(--yellow); padding: 3rem 0; border-top: 3px solid var(--black); border-bottom: 3px solid var(--black); }
	.cards { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1.25rem; max-width: 1180px; margin: 0 auto; padding: 0 1.5rem; }
	.big { position: relative; display: flex; flex-direction: column; align-items: flex-start; padding: 1.25rem; background: #fff; border: 3px solid var(--black); box-shadow: 8px 8px 0 var(--black); min-height: 14rem; transition: transform 0.1s; }
	.big:hover { transform: translate(-2px, -2px); }
	.big.c1 { background: var(--pink); color: #fff; }
	.rank { font-family: var(--display); font-size: 1rem; }
	.big img { width: 72px; height: 72px; border-radius: 50%; border: 3px solid var(--black); object-fit: cover; margin: 0.5rem 0; }
	.score { font-family: var(--display); font-size: 4rem; line-height: 1; }
	.name { font-weight: 700; font-size: 1.15rem; }
	.meta { font-size: 0.8rem; opacity: 0.8; }
	.board > .btn { display: table; margin: 1.5rem auto 0; }

	.access { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1.25rem; max-width: 1180px; margin: 0 auto; padding: 3rem 1.5rem; }
	.tile { padding: 1.5rem; border: 3px solid var(--black); color: #fff; box-shadow: 6px 6px 0 var(--black); }
	.t1 { background: var(--blue); }
	.t2 { background: var(--black); color: var(--yellow); }
	.t3 { background: var(--pink); }
	.tile h3 { font-size: 1.5rem; margin-bottom: 0.4rem; }
	.tile p { margin: 0; font-weight: 500; }

	footer { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 0.5rem; padding: 1.25rem 1.5rem 2rem; border-top: 3px solid var(--black); font-weight: 500; font-size: 0.85rem; }

	@media (max-width: 860px) {
		.quiz, .cards, .access { grid-template-columns: minmax(0, 1fr); }
		.bar nav { gap: 0.75rem; font-size: 0.9rem; }
		.face img { width: 64px; height: 64px; }
	}
</style>
