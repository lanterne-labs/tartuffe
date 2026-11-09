<script lang="ts">
	// The design directions to compare, each a complete landing page of its
	// own: fonts, layout, icons, logo, colours (/lab/design/<name>). A page of
	// the lab, for the owner's eye; the directions are mock-ups on the real
	// copy and the real podium.
	const DIRECTIONS = [
		{ key: 'tribune', name: 'Tribune', mood: 'Le site comme un quotidien', bg: '#f4f0e8', ink: '#121212', accent: '#c81e1e', font: "'Playfair Display', Georgia, serif", body: 'Le pari : on vient pour l’actualité. La contradiction du jour fait la une, le classement est la colonne de droite, les dernières font les dépêches ; la méthode vient après. Encre sur papier, serif énorme, filets. Logo : un T brisé.' },
		{ key: 'pop', name: 'Pop', mood: 'Le site comme un jeu à partager', bg: '#ffe14d', ink: '#111', accent: '#ff2d7a', font: "'Archivo Black', Impact, sans-serif", body: 'Le pari : on ne lit pas une méthode, on joue. « Qui a dit ça ? » : une vraie citation, trois visages, on touche, la contradiction se révèle. Puis les dernières à faire défiler, puis le tableau. Jaune, bordures noires, stickers. Logo : deux bulles qui se fendent.' },
		{ key: 'civic', name: 'Civic', mood: 'Le site comme un instrument public', bg: '#ffffff', ink: '#0f172a', accent: '#1d4ed8', font: "'DM Sans', system-ui, sans-serif", body: 'Le pari : on cherche quelqu’un et on veut savoir où il en est. Un champ de recherche et le baromètre avec tous les candidats placés dessus, d’un coup ; puis les trois pires, les dernières en tableau, la méthode en note. Blanc, ardoise, bleu. Logo : une balance.' },
		{ key: 'scene', name: 'Scène', mood: 'Le site comme une pièce en trois actes', bg: '#0b0a10', ink: '#f3f0ff', accent: '#ff3b5c', font: "'Syne', system-ui, sans-serif", body: 'Le pari : on retient une histoire. Un écran par idée : acte I, deux phrases d’une même personne ; acte II, le score qui bouge ; acte III, les trois sous les projecteurs. Sombre, vert acide et rouge. Logo : le masque, moitié sourire, moitié grimace.' }
	];</script>

<svelte:head>
	<title>Pistes de design — tartuffe.me</title>
	<meta name="robots" content="noindex" />
	<link rel="preconnect" href="https://fonts.googleapis.com" />
	<link href="https://fonts.googleapis.com/css2?family=Playfair+Display:wght@900&family=Archivo+Black&family=DM+Sans:wght@800&family=Syne:wght@800&display=swap" rel="stylesheet" />
</svelte:head>

<article class="designs">
	<span class="kicker">Le labo</span>
	<h1>Quatre pistes de design</h1>
	<p class="lead">Quatre pages d’accueil complètes, chacune sur un pari d’usage différent, avec ses polices, sa mise en page, ses icônes et son logo, sur les vraies contradictions et le vrai classement. Les autres pages du site ne changent pas. Les thèmes de couleur seuls sont sur <a href="/lab/themes">/lab/themes</a>.</p>

	<div class="grid">
		{#each DIRECTIONS as d (d.key)}
			<a class="dir" href="/lab/design/{d.key}" style="--d-bg: {d.bg}; --d-ink: {d.ink}; --d-accent: {d.accent}; --d-font: {d.font}">
				<div class="preview" aria-hidden="true">
					<span class="p-brand">tartuffe.me</span>
					<span class="p-title">Cohérent ou tartuffe ?</span>
					<span class="p-cta">Voir le classement</span>
				</div>
				<div class="about">
					<h2>{d.name} <small>{d.mood}</small></h2>
					<p>{d.body}</p>
					<span class="open">Voir la page →</span>
				</div>
			</a>
		{/each}
	</div>
</article>

<style>
	.designs { max-width: 60rem; margin: 0 auto; padding: 2rem 1rem 4rem; }
	.kicker { display: block; font-size: 0.75rem; font-weight: 700; letter-spacing: 0.14em; text-transform: uppercase; color: var(--accent-hover); margin-bottom: 0.5rem; }
	h1 { font-family: var(--font-serif); font-size: 2.4rem; font-weight: 600; margin: 0 0 0.5rem; }
	.lead { color: var(--text-secondary); max-width: 44rem; margin: 0 0 2rem; }
	.grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1.25rem; }
	.dir { display: flex; flex-direction: column; border-radius: var(--radius-lg); border: 1px solid var(--border-card); background: var(--bg-card); color: var(--text-primary); text-decoration: none; overflow: hidden; transition: border-color 0.2s, transform 0.2s; }
	.dir:hover { border-color: var(--accent); transform: translateY(-2px); }
	.preview { display: flex; flex-direction: column; align-items: center; gap: 0.6rem; padding: 1.4rem 1rem 1.3rem; background: var(--d-bg); color: var(--d-ink); font-family: var(--d-font); }
	.p-brand { font-size: 0.85rem; opacity: 0.7; }
	.p-title { font-size: 1.8rem; font-weight: 800; line-height: 1.05; text-align: center; }
	.p-cta { margin-top: 0.2rem; padding: 0.35rem 0.9rem; border-radius: 999px; background: var(--d-accent); color: #fff; font-size: 0.75rem; font-family: var(--font-family); font-weight: 600; }
	.about { padding: 1rem 1.2rem 1.1rem; }
	h2 { font-family: var(--font-serif); font-size: 1.4rem; font-weight: 600; margin: 0 0 0.25rem; }
	h2 small { font-family: var(--font-family); font-size: 0.8rem; color: var(--text-muted); font-weight: 500; margin-left: 0.4rem; }
	.about p { margin: 0 0 0.75rem; color: var(--text-secondary); font-size: 0.92rem; }
	.open { font-weight: 600; font-size: 0.9rem; color: var(--accent-hover); }
	@media (max-width: 640px) { .grid { grid-template-columns: minmax(0, 1fr); } }
</style>
