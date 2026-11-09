<script lang="ts">
	// One idea, what is said and what is done as two shapes that overlap or
	// do not, drawn twelve radically different ways (lib/brand.ts): each with
	// its own palette, line, light and type, on its own background. A card
	// applies its world to the real site. A page of the lab, for the owner's eye.
	import { WORLDS, type World } from '$lib/brand';
</script>

<svelte:head>
	<title>Les marques — tartuffe.me</title>
	<meta name="robots" content="noindex" />
	<link rel="preconnect" href="https://fonts.googleapis.com" />
	<link href="https://fonts.googleapis.com/css2?family=Outfit:wght@500;600;700&family=Unbounded:wght@500;600&family=Fraunces:opsz,wght@9..144,600;9..144,700&family=Instrument+Serif&family=Space+Grotesk:wght@600;700&display=swap" rel="stylesheet" />
</svelte:head>

{#snippet card(s: World)}
	<section class="study" style="--bg: {s.bg}; --fg: {s.fg}; --muted: {s.muted}; --font: {s.font}; --w: {s.weight}">
		<div class="navline"><span class="nav-mark">{@html s.svg}</span><span class="wordmark">tartuffe.me</span></div>
		<div class="big">{@html s.svg}</div>
		<h2>{s.name} <small>{s.mood}</small></h2>
		<p>{s.note}</p>
		<div class="foot"><div class="palette">{#each s.palette as c (c)}<span style="background: {c}"></span>{/each}</div><a class="see" href="/?theme={s.key}">Voir sur le site →</a></div>
	</section>
{/snippet}

<article class="logo-lab">
	<span class="kicker">Le labo</span>
	<h1>Les marques, en situation</h1>
	<h2 class="section">Les deux bulles</h2>
	<p class="lead">Une seule idée : ce qui est dit et ce qui est fait, deux formes qui se recouvrent, ou pas. Douze exécutions qui ne partagent ni palette, ni trait, ni lumière, ni police. Chacune est posée sur son propre fond, avec son nom dans la barre à 24 px et la marque en grand ; « Voir sur le site » l’applique au vrai site, marque, palette et titres, le temps de la visite.</p>

	<div class="grid">
		{#each WORLDS.filter((w) => w.group === 'lens') as s (s.key)}
			{@render card(s)}
		{/each}
	</div>

	<h2 class="section">Derrière le masque</h2>
	<p class="lead">Le masque gardé, et quelque chose de vrai qui passe derrière : un œil, un regard, un visage, une bouche. Huit façons de dire qu’on ne montre pas toujours qui l’on est.</p>
	<div class="grid">
		{#each WORLDS.filter((w) => w.group === 'mask') as s (s.key)}
			{@render card(s)}
		{/each}
	</div>
</article>

<style>
	.logo-lab { max-width: 1180px; margin: 0 auto; padding: 1rem 0 4rem; }
	.kicker { display: block; font-size: 0.75rem; font-weight: 700; letter-spacing: 0.14em; text-transform: uppercase; color: var(--accent-hover); margin-bottom: 0.5rem; }
	h1 { font-family: var(--font-serif); font-size: 2.4rem; font-weight: 600; margin: 0 0 0.5rem; }
	.lead { color: var(--text-secondary); max-width: 48rem; margin: 0 0 2rem; }
	.section { font-family: var(--font-serif); font-size: 1.7rem; font-weight: 600; margin: 2.5rem 0 1rem; }
	h1 + .section { margin-top: 0.5rem; }
	.grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 1.25rem; }
	.study {
		display: flex;
		flex-direction: column;
		padding: 1.1rem 1.1rem 1.2rem;
		border-radius: 1.1rem;
		background: var(--bg);
		color: var(--fg);
		font-family: var(--font);
		box-shadow: 0 1px 0 rgba(255, 255, 255, 0.06) inset, 0 10px 30px rgba(0, 0, 0, 0.35);
	}
	.navline { display: flex; align-items: center; gap: 0.55rem; padding: 0.55rem 0.8rem; border-radius: 0.7rem; background: color-mix(in srgb, var(--fg) 7%, transparent); }
	.nav-mark :global(svg) { width: 26px; height: 26px; display: block; }
	.wordmark { font-weight: var(--w); font-size: 1.2rem; letter-spacing: -0.01em; }
	.big { display: grid; place-items: center; height: 10rem; }
	.big :global(svg) { width: 120px; height: 120px; }
	h2 { font-size: 1.25rem; font-weight: var(--w); margin: 0 0 0.3rem; line-height: 1.2; }
	h2 small { display: block; font-family: var(--font-family); font-size: 0.72rem; font-weight: 600; letter-spacing: 0.08em; text-transform: uppercase; color: var(--muted); margin-top: 0.2rem; }
	.study p { flex: 1; margin: 0 0 0.9rem; font-family: var(--font-family); font-size: 0.88rem; line-height: 1.5; color: var(--muted); }
	.foot { display: flex; align-items: center; justify-content: space-between; gap: 0.5rem; }
	.palette { display: flex; gap: 0.35rem; }
	.see { font-family: var(--font-family); font-size: 0.85rem; font-weight: 700; color: var(--fg); text-decoration: none; }
	.see:hover { text-decoration: underline; }
	.palette span { width: 1.1rem; height: 1.1rem; border-radius: 50%; border: 1px solid color-mix(in srgb, var(--fg) 20%, transparent); }
	@media (max-width: 900px) { .grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
	@media (max-width: 560px) { .grid { grid-template-columns: minmax(0, 1fr); } }
</style>
