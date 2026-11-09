<script lang="ts">
	// The colour themes to compare: the site's own and three alternatives,
	// each a set of tokens in app.css (`:root[data-theme="…"]`). A card shows
	// the tokens as swatches and opens the landing page in that theme
	// (`/?theme=<name>`). A page of the lab, for the owner's eye.
	import { lang, type Lang } from '$lib/i18n';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	type Theme = {
		key: string;
		name: string;
		bg: string;
		card: string;
		text: string;
		muted: string;
		accent: string;
		fr: string;
		en: string;
	};
	const THEMES: Theme[] = [
		{ key: '', name: 'Nuit', bg: '#0a0a0f', card: '#1c161e', text: '#f1ebe4', muted: '#a99c92', accent: '#9a2640', fr: 'Le thème actuel : presque noir, lie-de-vin, théâtral.', en: 'The current theme: near black, oxblood, theatrical.' },
		{ key: 'ardoise', name: 'Ardoise', bg: '#11161d', card: '#1e2834', text: '#eef2f6', muted: '#a6b2bf', accent: '#c9a24a', fr: 'Sombre mais moins noir : bleu ardoise, l’accent en or.', en: 'Dark but less black: slate blue, gold as the accent.' },
		{ key: 'papier', name: 'Papier', bg: '#f6f1e8', card: '#fffcf7', text: '#231a17', muted: '#5e504a', accent: '#9a2640', fr: 'Clair et chaud : papier, le lie-de-vin gardé.', en: 'Light and warm: paper, the oxblood kept.' },
		{ key: 'encre', name: 'Encre', bg: '#fbfbfd', card: '#ffffff', text: '#141a2a', muted: '#4f5870', accent: '#1f3a8a', fr: 'Clair et froid : blanc et encre, l’accent bleu marine.', en: 'Light and cool: white and ink, a navy accent.' }
	];
	const t = {
		fr: { kicker: 'Le labo', title: 'Quatre thèmes', lead: 'Le même accueil dans quatre palettes. Chaque carte ouvre la page dans son thème ; seule la page d’accueil le suit, le reste du site reste dans le thème actuel.', open: 'Voir l’accueil', meta: 'Thèmes — tartuffe.me' },
		en: { kicker: 'The lab', title: 'Four themes', lead: 'The same landing page in four palettes. Each card opens the page in its theme; only the landing page follows it, the rest of the site keeps the current one.', open: 'See the landing page', meta: 'Themes — tartuffe.me' }
	};
	let c = $derived(t[currentLang]);
</script>

<svelte:head>
	<title>{c.meta}</title>
	<meta name="robots" content="noindex" />
</svelte:head>

<article class="themes">
	<span class="kicker">{c.kicker}</span>
	<h1>{c.title}</h1>
	<p class="lead">{c.lead}</p>

	<div class="grid">
		{#each THEMES as th (th.key)}
			<a class="theme" href={th.key ? `/?theme=${th.key}` : '/'} style="--t-bg: {th.bg}; --t-card: {th.card}; --t-text: {th.text}; --t-muted: {th.muted}; --t-accent: {th.accent}">
				<div class="preview" aria-hidden="true">
					<span class="p-title">Cohérent ou tartuffe ?</span>
					<span class="p-line"></span>
					<span class="p-line short"></span>
					<span class="p-cta">Voir le classement</span>
					<span class="p-card"><span class="p-dial"></span></span>
				</div>
				<div class="about">
					<h2>{th.name}</h2>
					<p>{th[currentLang]}</p>
					<div class="swatches" aria-hidden="true">
						<span style="background: {th.bg}"></span>
						<span style="background: {th.card}"></span>
						<span style="background: {th.accent}"></span>
						<span style="background: {th.muted}"></span>
						<span style="background: {th.text}"></span>
					</div>
					<span class="open">{c.open} →</span>
				</div>
			</a>
		{/each}
	</div>
</article>

<style>
	.themes {
		max-width: 60rem;
		margin: 0 auto;
		padding: 2rem 1rem 4rem;
	}
	.kicker {
		display: block;
		font-size: 0.75rem;
		font-weight: 700;
		letter-spacing: 0.14em;
		text-transform: uppercase;
		color: var(--accent-hover);
		margin-bottom: 0.5rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: 2.4rem;
		font-weight: 600;
		margin: 0 0 0.5rem;
	}
	.lead {
		color: var(--text-secondary);
		max-width: 40rem;
		margin: 0 0 2rem;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 1.25rem;
	}
	.theme {
		display: flex;
		flex-direction: column;
		border-radius: var(--radius-lg);
		border: 1px solid var(--border-card);
		background: var(--bg-card);
		color: var(--text-primary);
		text-decoration: none;
		overflow: hidden;
		transition:
			border-color 0.2s,
			transform 0.2s;
	}
	.theme:hover {
		border-color: var(--accent);
		transform: translateY(-2px);
	}
	/* A miniature of the landing page in the theme's own colours. */
	.preview {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.45rem;
		padding: 1.5rem 1rem 1.2rem;
		background: var(--t-bg);
		color: var(--t-text);
	}
	.p-title {
		font-family: var(--font-serif);
		font-size: 1.5rem;
		font-weight: 600;
	}
	.p-line {
		width: 60%;
		height: 6px;
		border-radius: 3px;
		background: var(--t-muted);
		opacity: 0.6;
	}
	.p-line.short {
		width: 40%;
	}
	.p-cta {
		margin-top: 0.3rem;
		padding: 0.3rem 0.9rem;
		border-radius: 999px;
		background: var(--t-accent);
		color: #fff;
		font-size: 0.75rem;
		font-weight: 600;
	}
	.p-card {
		margin-top: 0.6rem;
		width: 70%;
		height: 3.4rem;
		border-radius: 0.6rem;
		background: var(--t-card);
		border: 1px solid color-mix(in srgb, var(--t-text) 10%, transparent);
		display: grid;
		place-items: center;
	}
	.p-dial {
		width: 2rem;
		height: 1rem;
		border-radius: 1rem 1rem 0 0;
		background: linear-gradient(90deg, #5fa56a, #d4923a, #c2334a);
	}
	.about {
		padding: 1rem 1.2rem 1.1rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.4rem;
		font-weight: 600;
		margin: 0 0 0.25rem;
	}
	.about p {
		margin: 0 0 0.75rem;
		color: var(--text-secondary);
		font-size: 0.92rem;
	}
	.swatches {
		display: flex;
		gap: 0.4rem;
		margin-bottom: 0.75rem;
	}
	.swatches span {
		width: 1.4rem;
		height: 1.4rem;
		border-radius: 50%;
		border: 1px solid var(--border-card-hover);
	}
	.open {
		font-weight: 600;
		font-size: 0.9rem;
		color: var(--accent-hover);
	}
	@media (max-width: 640px) {
		.grid {
			grid-template-columns: minmax(0, 1fr);
		}
	}
</style>
