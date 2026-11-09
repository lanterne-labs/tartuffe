<script lang="ts">
	// The current design, kept, and what its first screen could be: four
	// mask studies for the logo (the current one is a domino mask), then the
	// hero as it is, without the mirror, with a mask emblem, with the live
	// barometer as an infographic, and with a schematic of what is compared.
	// A page of the lab, in the site's own tokens and type, for the owner's eye.
	import { onMount } from 'svelte';
	import Mirror from '$lib/components/about/Mirror.svelte';
	import { loadLanding, type Landing } from '$lib/lab/podium';

	let data = $state<Landing | null>(null);
	onMount(async () => {
		try {
			data = await loadLanding();
		} catch (err) {
			console.error('landing', err);
		}
	});
	let placed = $derived.by(() => {
		const ends: number[] = [];
		return (data?.candidates ?? [])
			.filter((c) => c.reliable && c.image)
			.sort((a, b) => a.score - b.score)
			.map((c) => {
				const x = 3 + c.score * 0.94;
				let row = 0;
				while (row < 2 && ends[row] !== undefined && ends[row] > x - 5.2) row++;
				ends[row] = Math.max(x, ends[row] ?? -Infinity);
				return { ...c, x, row };
			});
	});
	let totals = $derived({
		people: data?.candidates.length ?? 0,
		facts: data?.candidates.reduce((s, c) => s + c.facts, 0) ?? 0,
		clashes: data?.candidates.reduce((s, c) => s + c.contradictions, 0) ?? 0
	});
	const fmt = (n: number) => n.toLocaleString('fr-FR');

	const TITLE = 'Cohérent ou tartuffe ?';
	const LEAD = 'Ce qu’ils disent, ce qu’ils votent, ce que la justice a tranché : quand ça ne colle pas, nous le montrons, sources à l’appui. Nous ne jugeons pas qui a raison.';
	const PILLS = ['Tout est sourcé', 'Les mêmes règles pour tous', 'Vous pouvez nous corriger'];
</script>

<svelte:head>
	<title>Le premier écran — tartuffe.me</title>
	<meta name="robots" content="noindex" />
</svelte:head>

{#snippet maskA(cls = '')}
	<!-- A · the current mark: a domino mask. -->
	<svg class="mark {cls}" viewBox="0 0 64 40" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
		<path d="M4 12 C8 4, 16 2, 22 6 C26 8, 28 10, 32 12 C36 10, 38 8, 42 6 C48 2, 56 4, 60 12 C62 20, 58 28, 50 32 C44 34, 38 32, 34 26 C33 24, 31 24, 30 26 C26 32, 20 34, 14 32 C6 28, 2 20, 4 12 Z" />
		<ellipse cx="18" cy="17" rx="5" ry="2.5" />
		<ellipse cx="46" cy="17" rx="5" ry="2.5" />
	</svg>
{/snippet}

{#snippet maskB(cls = '')}
	<!-- B · the domino mask, two faces: one brow calm, one brow knit, a crack between. -->
	<svg class="mark {cls}" viewBox="0 0 64 40" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
		<path d="M4 12 C8 4, 16 2, 22 6 C26 8, 28 10, 32 12 C36 10, 38 8, 42 6 C48 2, 56 4, 60 12 C62 20, 58 28, 50 32 C44 34, 38 32, 34 26 C33 24, 31 24, 30 26 C26 32, 20 34, 14 32 C6 28, 2 20, 4 12 Z" />
		<ellipse cx="18" cy="18" rx="5" ry="2.6" />
		<path d="M41 19.5 C43 16.5, 49 16, 51.5 18.5" />
		<path d="M40 13.5 l10 -2.5" />
		<path class="crack" d="M32 12 l-1.2 4 l2.2 3.5 l-1.4 4" />
	</svg>
{/snippet}

{#snippet maskC(cls = '')}
	<!-- C · the whole face: half smile, half frown, in the two colours of the score. -->
	<svg class="mark {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
		<path d="M32 4 C46 4, 58 13, 58 28 C58 44, 46 60, 32 60 C18 60, 6 44, 6 28 C6 13, 18 4, 32 4 Z" />
		<path d="M32 4 V60" stroke-dasharray="1.5 4" opacity="0.5" />
		<path d="M14 24 c3 -3, 8 -3, 11 0" class="g" />
		<path d="M39 24 c3 -3, 8 -3, 11 0" class="r" />
		<path d="M13 38 c4 7, 11 9, 19 6" class="g" />
		<path d="M32 44 c7 -3, 13 -3, 18 2" class="r" />
	</svg>
{/snippet}

{#snippet maskD(cls = '')}
	<!-- D · the domino mask whose eyes are the two guillemets: the mask of what is said. -->
	<svg class="mark {cls}" viewBox="0 0 64 40" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
		<path d="M4 12 C8 4, 16 2, 22 6 C26 8, 28 10, 32 12 C36 10, 38 8, 42 6 C48 2, 56 4, 60 12 C62 20, 58 28, 50 32 C44 34, 38 32, 34 26 C33 24, 31 24, 30 26 C26 32, 20 34, 14 32 C6 28, 2 20, 4 12 Z" />
		<path d="M20 13 l-4 5 l4 5 M15 13 l-4 5 l4 5" />
		<path d="M44 13 l4 5 l-4 5 M49 13 l4 5 l-4 5" />
	</svg>
{/snippet}

{#snippet signNE(cls = '')}
	<!-- 1 · Inégal: the sign of two things that are not the same. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="4.5" stroke-linecap="round" aria-hidden="true">
		<path d="M12 25 H52 M12 39 H52" />
		<path d="M41 10 L23 54" class="acc" />
	</svg>
{/snippet}

{#snippet signBack(cls = '')}
	<!-- 2 · Dos à dos: two quotation marks that turn their backs. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="4.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
		<path d="M14 20 l9 12 l-9 12 M24 20 l9 12 l-9 12" />
		<path d="M50 20 l-9 12 l9 12 M40 20 l-9 12 l9 12" class="acc" />
	</svg>
{/snippet}

{#snippet signBreak(cls = '')}
	<!-- 3 · Le trait brisé: a line that holds, a line that does not. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="4.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
		<path d="M10 24 H54" />
		<path d="M10 40 H27 L32 48 L37 40" class="acc" />
		<path d="M37 40 H54" />
	</svg>
{/snippet}

{#snippet signNeedle(cls = '')}
	<!-- 4 · L’aiguille: the barometer itself, reduced to an arc and a needle. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="4.5" stroke-linecap="round" aria-hidden="true">
		<path d="M10 44 A22 22 0 0 1 54 44" />
		<path d="M32 44 L44 26" class="acc" />
		<circle cx="32" cy="44" r="3" fill="currentColor" stroke="none" />
	</svg>
{/snippet}

{#snippet signLens(cls = '')}
	<!-- 5 · Les deux bulles: two statements, and what they share that cannot be. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="4" aria-hidden="true">
		<path d="M32 18.3 A16 16 0 0 1 32 45.7 A16 16 0 0 1 32 18.3 Z" class="acc-fill" stroke="none" />
		<circle cx="24" cy="32" r="16" />
		<circle cx="40" cy="32" r="16" />
	</svg>
{/snippet}

{#snippet signMono(cls = '')}
	<!-- 6 · Le monogramme: the t of tartuffe, its crossbar bent on one side. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="4.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
		<path d="M30 10 V46 Q30 54 38 54 H42" />
		<path d="M16 24 H30" />
		<path d="M30 24 L46 30" class="acc" />
	</svg>
{/snippet}

{#snippet lensA(cls = '')}
	<!-- a · thin rings, the lens full: the drawing of the first study, lighter. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="2.5" aria-hidden="true">
		<path d="M32 18.3 A16 16 0 0 1 32 45.7 A16 16 0 0 1 32 18.3 Z" class="acc-fill" stroke="none" />
		<circle cx="24" cy="32" r="16" /><circle cx="40" cy="32" r="16" />
	</svg>
{/snippet}

{#snippet lensB(cls = '')}
	<!-- b · one disc said, one ring done: the solid is what was said, the ring what is on record. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="3.5" aria-hidden="true">
		<circle cx="24" cy="32" r="16" fill="currentColor" stroke="none" />
		<circle cx="40" cy="32" r="16" />
		<path d="M32 18.3 A16 16 0 0 1 32 45.7 A16 16 0 0 1 32 18.3 Z" class="acc-fill" stroke="none" />
	</svg>
{/snippet}

{#snippet lensC(cls = '')}
	<!-- c · the eclipse: an oxblood disc covers part of the cream one. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" aria-hidden="true">
		<circle cx="26" cy="32" r="17" fill="currentColor" />
		<circle cx="40" cy="32" r="17" class="acc-fill" />
	</svg>
{/snippet}

{#snippet lensD(cls = '')}
	<!-- d · two speech bubbles, tails out, the overlap in oxblood. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="3" stroke-linejoin="round" aria-hidden="true">
		<path d="M8 20 h26 a4 4 0 0 1 4 4 v14 a4 4 0 0 1 -4 4 h-14 l-6 6 v-6 h-6 a4 4 0 0 1 -4 -4 v-14 a4 4 0 0 1 4 -4z" />
		<path d="M26 12 h26 a4 4 0 0 1 4 4 v14 a4 4 0 0 1 -4 4 h-6 v6 l-6 -6 h-14 a4 4 0 0 1 -4 -4 v-14 a4 4 0 0 1 4 -4z" />
		<path d="M26 20 h12 v14 h-12z" class="acc-fill" stroke="none" />
	</svg>
{/snippet}

{#snippet lensE(cls = '')}
	<!-- e · the lens alone: what the two share, cut out of the night. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" aria-hidden="true">
		<path d="M32 12 A22 22 0 0 1 32 52 A22 22 0 0 1 32 12 Z" fill="currentColor" />
		<path d="M32 22 A12 12 0 0 1 32 42 A12 12 0 0 1 32 22 Z" class="acc-fill" />
	</svg>
{/snippet}

{#snippet lensF(cls = '')}
	<!-- f · drifting apart: the two barely touch, the sliver between them is the contradiction. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="3.5" aria-hidden="true">
		<circle cx="17" cy="32" r="13" fill="currentColor" stroke="none" />
		<circle cx="47" cy="32" r="13" />
		<path d="M33 25 l-2 7 l2 7" class="acc" stroke-width="3.5" stroke-linecap="round" stroke-linejoin="round" />
	</svg>
{/snippet}

{#snippet lensG(cls = '')}
	<!-- g · a dashed circle for what is said, a solid one for what is done. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" fill="none" stroke="currentColor" stroke-width="3" aria-hidden="true">
		<path d="M32 18.3 A16 16 0 0 1 32 45.7 A16 16 0 0 1 32 18.3 Z" class="acc-fill" stroke="none" />
		<circle cx="24" cy="32" r="16" stroke-dasharray="4 4" />
		<circle cx="40" cy="32" r="16" />
	</svg>
{/snippet}

{#snippet lensH(cls = '')}
	<!-- h · two translucent discs, cream and oxblood, their overlap a third tone. -->
	<svg class="mark sq {cls}" viewBox="0 0 64 64" aria-hidden="true">
		<circle cx="25" cy="32" r="17" fill="currentColor" opacity="0.85" />
		<circle cx="39" cy="32" r="17" class="acc-fill" opacity="0.9" style="mix-blend-mode: screen" />
	</svg>
{/snippet}

{#snippet copy()}
	<h1>{TITLE}</h1>
	<p class="lead">{LEAD}</p>
	<ul class="pills">{#each PILLS as p}<li>{p}</li>{/each}</ul>
	<div class="ctas"><a class="cta" href="/dashboard">Voir le classement</a><a class="cta cta-ghost" href="/feed">Lire le fil →</a></div>
{/snippet}

<article class="hero-lab">
	<span class="kicker">Le labo</span>
	<h1 class="page-title">Le premier écran, dans le design actuel</h1>
	<p class="page-lead">Rien ne change du site : les couleurs, la typographie, les cartes. Ce qui varie, c’est le logo et ce qui ouvre la page. Chaque bloc est à la largeur de la page.</p>

	<h2 class="section">Le logo : quatre masques</h2>
	<div class="logos">
		{#each [{ k: 'A', t: 'Le masque actuel', d: 'Un loup, deux yeux. Juste, mais muet : il ne dit pas la contradiction.' }, { k: 'B', t: 'Le loup, deux visages', d: 'Un œil calme, un sourcil froncé, une fêlure entre les deux : une seule personne, deux faces. Lisible en 24 px.' }, { k: 'C', t: 'Le visage entier', d: 'Moitié sourire en vert, moitié grimace en rouge : les deux bouts du baromètre dans le logo. Plus riche, mais rond, et deux couleurs à petite taille.' }, { k: 'D', t: 'Le loup qui cite', d: 'Les yeux sont les guillemets « » : le masque de ce qui est dit. Malin, un peu moins immédiat.' }] as l (l.k)}
			<div class="logo-card">
				<div class="navline">
					{#if l.k === 'A'}{@render maskA('nav')}{:else if l.k === 'B'}{@render maskB('nav')}{:else if l.k === 'C'}{@render maskC('nav')}{:else}{@render maskD('nav')}{/if}
					<span class="brand-name">tartuffe<span class="brand-dot">.</span>me</span>
				</div>
				<div class="big">
					{#if l.k === 'A'}{@render maskA('large')}{:else if l.k === 'B'}{@render maskB('large')}{:else if l.k === 'C'}{@render maskC('large')}{:else}{@render maskD('large')}{/if}
				</div>
				<h3>{l.k} · {l.t}</h3>
				<p>{l.d}</p>
			</div>
		{/each}
	</div>

	<h2 class="section">Au-delà du masque : six marques</h2>
	<p class="page-lead">Pas de visage, pas de théâtre : un signe. Chacune tient en 24 px dans la barre, en une couleur plus l’accent.</p>
	<div class="logos six">
		{#each [{ k: 1, t: 'Inégal', d: 'Le signe ≠ : deux choses qui ne sont pas les mêmes. Le plus direct ; la barre en lie-de-vin est la contradiction.' }, { k: 2, t: 'Dos à dos', d: 'Deux guillemets qui se tournent le dos : deux déclarations qui ne se regardent plus. Parle de paroles sans les montrer.' }, { k: 3, t: 'Le trait brisé', d: 'Deux lignes, l’une tient, l’autre cède au milieu. La cohérence et son défaut, en deux traits.' }, { k: 4, t: 'L’aiguille', d: 'Le baromètre réduit à un arc et une aiguille : le produit lui-même. Sobre, très lisible, un peu générique.' }, { k: 5, t: 'Les deux bulles', d: 'Deux cercles, deux déclarations ; la lentille commune en lie-de-vin, ce qui ne peut pas être vrai deux fois. Le plus beau à mon œil.' }, { k: 6, t: 'Le monogramme', d: 'Le t de tartuffe, dont la barre penche d’un côté : la lettre qui ne tient pas droit. Une marque, pas une icône.' }] as l (l.k)}
			<div class="logo-card">
				<div class="navline">
					{#if l.k === 1}{@render signNE('nav')}{:else if l.k === 2}{@render signBack('nav')}{:else if l.k === 3}{@render signBreak('nav')}{:else if l.k === 4}{@render signNeedle('nav')}{:else if l.k === 5}{@render signLens('nav')}{:else}{@render signMono('nav')}{/if}
					<span class="brand-name">tartuffe<span class="brand-dot">.</span>me</span>
				</div>
				<div class="big">
					{#if l.k === 1}{@render signNE('large')}{:else if l.k === 2}{@render signBack('large')}{:else if l.k === 3}{@render signBreak('large')}{:else if l.k === 4}{@render signNeedle('large')}{:else if l.k === 5}{@render signLens('large')}{:else}{@render signMono('large')}{/if}
				</div>
				<h3>{l.k} · {l.t}</h3>
				<p>{l.d}</p>
			</div>
		{/each}
	</div>

	<h2 class="section">Les deux bulles : huit styles</h2>
	<p class="page-lead">La même idée, ce qui est dit et ce qui est fait, deux formes qui se recouvrent, ou pas ; huit façons de la dessiner.</p>
	<div class="logos four">
		{#each [{ k: 'a', t: 'Anneaux fins', d: 'Le dessin de la première étude, allégé : deux cercles au trait fin, la lentille pleine.' }, { k: 'b', t: 'Disque et anneau', d: 'Ce qui est dit, plein ; ce qui est fait, un anneau. La lentille sur les deux.' }, { k: 'c', t: 'L’éclipse', d: 'Un disque lie-de-vin recouvre une part du disque crème : la parole qui cache l’acte. Pas de trait, deux aplats.' }, { k: 'd', t: 'Les bulles de parole', d: 'Deux vraies bulles, queues opposées, le recouvrement en lie-de-vin. Le plus littéral.' }, { k: 'e', t: 'La lentille seule', d: 'Les cercles disparaissent, reste ce qu’ils partageaient : une amande crème, son cœur lie-de-vin.' }, { k: 'f', t: 'Qui s’éloignent', d: 'Les deux ne se touchent presque plus ; entre eux, une fêlure. Le recouvrement qui n’a pas eu lieu.' }, { k: 'g', t: 'Pointillé et plein', d: 'Le dit en pointillé, l’acte en trait plein, la lentille où ils coïncident.' }, { k: 'h', t: 'Transparences', d: 'Deux disques translucides, crème et lie-de-vin ; là où ils se croisent, un troisième ton.' }] as l (l.k)}
			<div class="logo-card">
				<div class="navline">
					{#if l.k === 'a'}{@render lensA('nav')}{:else if l.k === 'b'}{@render lensB('nav')}{:else if l.k === 'c'}{@render lensC('nav')}{:else if l.k === 'd'}{@render lensD('nav')}{:else if l.k === 'e'}{@render lensE('nav')}{:else if l.k === 'f'}{@render lensF('nav')}{:else if l.k === 'g'}{@render lensG('nav')}{:else}{@render lensH('nav')}{/if}
					<span class="brand-name">tartuffe<span class="brand-dot">.</span>me</span>
				</div>
				<div class="big">
					{#if l.k === 'a'}{@render lensA('large')}{:else if l.k === 'b'}{@render lensB('large')}{:else if l.k === 'c'}{@render lensC('large')}{:else if l.k === 'd'}{@render lensD('large')}{:else if l.k === 'e'}{@render lensE('large')}{:else if l.k === 'f'}{@render lensF('large')}{:else if l.k === 'g'}{@render lensG('large')}{:else}{@render lensH('large')}{/if}
				</div>
				<h3>{l.k} · {l.t}</h3>
				<p>{l.d}</p>
			</div>
		{/each}
	</div>

	<h2 class="section">L’ouverture : cinq versions</h2>

	<figure class="frame">
		<figcaption><b>1 · Aujourd’hui.</b> Le miroir, puis le titre. Le miroir est joli mais il raconte l’ancien nom : rien sur la page ne l’explique.</figcaption>
		<header class="hero"><Mirror />{@render copy()}</header>
	</figure>

	<figure class="frame">
		<figcaption><b>2 · Sans illustration.</b> Le titre ouvre la page, un cran plus grand, le reste resserré. Le plus sobre ; la question porte seule.</figcaption>
		<header class="hero bare">{@render copy()}</header>
	</figure>

	<figure class="frame">
		<figcaption><b>3 · L’emblème.</b> Le masque B en grand, avec le halo du miroir, à sa place. Le logo et l’ouverture ne font qu’un.</figcaption>
		<header class="hero">
			<div class="emblem">{@render maskB('emblem-mark')}</div>
			{@render copy()}
		</header>
	</figure>

	<figure class="frame">
		<figcaption><b>4 · L’infographie vivante.</b> Le baromètre avec les candidats dessus, sous le titre : on voit le produit avant de le lire. Les visages mènent aux fiches. Dessous, trois chiffres qui disent l’ampleur.</figcaption>
		<header class="hero bare">
			<h1>{TITLE}</h1>
			<p class="lead">{LEAD}</p>
			<div class="baro" aria-label="Le baromètre des candidats">
				<div class="band">
					{#each placed as p (p.id)}
						<a class="pin" href="/person/{p.id}" style="left: {p.x}%; bottom: {p.row * 2.2 + 1.1}rem" title="{p.name} · {p.score}/100"><img src={p.image} alt={p.name} /></a>
					{/each}
					<div class="track"></div>
				</div>
				<div class="ends"><span>0 · cohérent</span><span>tartuffe · 100</span></div>
			</div>
			<ul class="stats">
				<li><b>{totals.people}</b> candidats</li>
				<li><b>{fmt(totals.facts)}</b> faits sourcés</li>
				<li><b>{fmt(totals.clashes)}</b> contradictions</li>
			</ul>
			<div class="ctas"><a class="cta" href="/dashboard">Voir le classement</a><a class="cta cta-ghost" href="/feed">Lire le fil →</a></div>
		</header>
	</figure>

	<figure class="frame">
		<figcaption><b>5 · Le schéma.</b> Ce qui est comparé, en un dessin : la parole, le vote, la justice convergent vers le baromètre, dont l’aiguille bouge. Dit la méthode sans un mot ; moins vivant que 4.</figcaption>
		<header class="hero">
			<svg class="schema" viewBox="0 0 420 176" aria-hidden="true">
				<defs>
					<linearGradient id="lab-band" x1="0" x2="1"><stop offset="0" stop-color="var(--green)" /><stop offset="0.5" stop-color="var(--amber)" /><stop offset="1" stop-color="var(--red)" /></linearGradient>
				</defs>
				<!-- three sources, one above the other -->
				<g class="src" transform="translate(30 6)">
					<rect x="0" y="0" width="64" height="32" rx="8" />
					<path d="M12 36 l6 -5" />
					<text x="32" y="21" text-anchor="middle">« il dit »</text>
				</g>
				<g class="src" transform="translate(30 64)">
					<rect x="0" y="0" width="64" height="32" rx="6" />
					<path d="M20 16 l8 8 l20 -16" />
					<text x="32" y="46" text-anchor="middle" class="cap">il vote</text>
				</g>
				<g class="src" transform="translate(30 122)">
					<path d="M14 26 l20 -20 M18 10 l12 12 M30 4 l8 8 M6 32 h38" />
					<text x="32" y="46" text-anchor="middle" class="cap">la justice tranche</text>
				</g>
				<!-- the flows, to the barometer's hub -->
				<path class="flow" d="M98 22 C150 22, 180 88, 230 88" />
				<path class="flow" d="M98 80 C150 80, 180 88, 230 88" />
				<path class="flow" d="M98 138 C150 138, 180 88, 230 88" />
				<!-- the barometer -->
				<g transform="translate(240 34)">
					<path d="M10 100 A70 70 0 0 1 150 100" fill="none" stroke="url(#lab-band)" stroke-width="12" stroke-linecap="round" />
					<line class="needle" x1="80" y1="100" x2="80" y2="38" />
					<circle cx="80" cy="100" r="5" class="hub" />
					<text x="10" y="122" class="cap">cohérent</text>
					<text x="150" y="122" class="cap" text-anchor="end">tartuffe</text>
				</g>
			</svg>
			{@render copy()}
		</header>
	</figure>

	<h2 class="section">Ce que je changerais, au-delà de l’ouverture</h2>
	<ul class="notes">
		<li><b>Un seul exemple, vrai.</b> L’exemple fictif du député et des retraites peut laisser place à la contradiction du jour, tirée du fil, avec un lien vers la fiche. Le vrai est plus fort que l’inventé, et il change chaque jour.</li>
		<li><b>Le podium plus haut.</b> Juste après l’ouverture, avant la méthode : on comprend le site en voyant trois visages et trois nombres.</li>
		<li><b>Les trois boîtes en fin de page.</b> Classement, fil, code : des sorties, pas des arguments. Elles ferment bien la page.</li>
		<li><b>Un ton de moins dans les fonds.</b> Le fond presque noir et les cartes à 60 % se confondent ; éclaircir les cartes d’un cran (le thème « Ardoise » le montrait) donne du relief sans quitter la nuit.</li>
	</ul>
</article>

<style>
	.hero-lab { max-width: 1120px; margin: 0 auto; padding: 1rem 0 4rem; }
	.kicker { display: block; font-size: 0.75rem; font-weight: 700; letter-spacing: 0.14em; text-transform: uppercase; color: var(--accent-hover); margin-bottom: 0.5rem; }
	.page-title { font-family: var(--font-serif); font-size: 2.4rem; font-weight: 600; margin: 0 0 0.5rem; }
	.page-lead { color: var(--text-secondary); max-width: 44rem; margin: 0 0 2.5rem; }
	.section { font-family: var(--font-serif); font-size: 1.7rem; font-weight: 600; margin: 3rem 0 1.25rem; }

	/* Logo studies */
	.logos { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 1rem; }
	.logo-card { padding: 1rem 1.1rem 1.1rem; background: var(--bg-card); border: 1px solid var(--border-card); border-radius: var(--radius-lg); }
	.navline { display: flex; align-items: center; gap: 0.6rem; padding: 0.6rem 0.8rem; border-radius: var(--radius-md); background: var(--glass-bg); border: 1px solid var(--glass-border); color: var(--text-primary); }
	.brand-name { font-family: var(--font-serif); font-weight: 600; font-size: 1.35rem; letter-spacing: -0.01em; }
	.brand-dot { color: var(--accent); }
	.big { display: grid; place-items: center; height: 9rem; color: var(--text-primary); }
	.mark.nav { width: 38px; height: 24px; }
	.mark.nav:is([viewBox='0 0 64 64']) { width: 28px; height: 28px; }
	.mark.large { width: 150px; height: 96px; }
	.mark.large:is([viewBox='0 0 64 64']) { width: 110px; height: 110px; }
	.mark .crack, .mark .acc { stroke: var(--accent); }
	.mark .acc-fill { fill: var(--accent); opacity: 0.85; }
	.mark.sq.nav { width: 26px; height: 26px; }
	.mark.sq.large { width: 104px; height: 104px; }
	.logos.six { grid-template-columns: repeat(3, minmax(0, 1fr)); }
	.logos.four .big { height: 8rem; }
	.mark .g { stroke: var(--green); }
	.mark .r { stroke: var(--red); }
	.logo-card h3 { font-size: 1rem; margin: 0.25rem 0 0.3rem; }
	.logo-card p { margin: 0; font-size: 0.88rem; color: var(--text-secondary); }

	/* Hero frames, the site's own hero styles */
	.frame { margin: 0 0 2.5rem; padding: 0; }
	figcaption { font-size: 0.9rem; color: var(--text-secondary); margin: 0 0 0.75rem; max-width: 52rem; }
	figcaption b { color: var(--text-primary); }
	.hero { text-align: center; padding: 2.5rem 1.5rem 3rem; border: 1px dashed var(--border-card-hover); border-radius: var(--radius-lg); }
	.hero h1 { font-family: var(--font-serif); font-size: clamp(2.2rem, 7vw, 3.6rem); font-weight: 600; line-height: 1.05; margin: 0.5rem auto 1.1rem; max-width: 40rem; text-wrap: balance; }
	.hero.bare h1 { font-size: clamp(2.6rem, 8vw, 4.4rem); margin-top: 0; }
	.lead { font-size: 1.12rem; color: var(--text-secondary); line-height: 1.6; max-width: 36rem; margin: 0 auto 1.4rem; text-wrap: balance; }
	.pills { list-style: none; padding: 0; margin: 0 0 2rem; display: flex; flex-wrap: wrap; justify-content: center; gap: 0.45rem; }
	.pills li { font-size: 0.78rem; padding: 0.25rem 0.75rem; border-radius: var(--radius-full); border: 1px solid var(--glass-border); color: var(--text-secondary); background: var(--bg-card); }
	.ctas { display: flex; flex-wrap: wrap; justify-content: center; gap: 0.75rem; }
	.cta { display: inline-flex; align-items: center; padding: 0.75rem 1.4rem; border-radius: var(--radius-full); background: var(--accent); color: #fff; font-weight: 600; text-decoration: none; }
	.cta:hover { background: var(--accent-hover); }
	.cta-ghost { background: transparent; border: 1px solid var(--border-card-hover); color: var(--text-primary); }
	.cta-ghost:hover { background: var(--bg-card); }

	.emblem { display: grid; place-items: center; height: 150px; margin-bottom: 0.5rem; color: var(--text-primary); }
	.emblem .mark { width: 190px; height: 120px; filter: drop-shadow(0 0 28px var(--accent-glow)); }

	/* 4 · the live barometer */
	.baro { max-width: 46rem; margin: 0.5rem auto 1.25rem; }
	.band { position: relative; height: 6.2rem; margin: 0 1.2rem; }
	.track { position: absolute; left: 0; right: 0; bottom: 0; height: 10px; border-radius: 5px; background: linear-gradient(90deg, var(--green), var(--amber), var(--red)); }
	.pin { position: absolute; transform: translateX(-50%); transition: transform 0.15s; z-index: 1; }
	.pin:hover { transform: translateX(-50%) scale(1.15); z-index: 2; }
	.pin img { display: block; width: 38px; height: 38px; border-radius: 50%; object-fit: cover; border: 2px solid var(--bg-secondary); box-shadow: 0 3px 10px rgba(0, 0, 0, 0.5); }
	.ends { display: flex; justify-content: space-between; margin: 0.45rem 1.2rem 0; font-size: 0.66rem; font-weight: 700; letter-spacing: 0.1em; text-transform: uppercase; color: var(--text-muted); }
	.stats { list-style: none; padding: 0; margin: 0 0 1.75rem; display: flex; justify-content: center; gap: 2rem; flex-wrap: wrap; color: var(--text-secondary); font-size: 0.9rem; }
	.stats b { font-family: var(--font-serif); font-size: 1.5rem; color: var(--text-primary); margin-right: 0.2rem; }

	/* 5 · the schema */
	.schema { width: min(100%, 440px); height: auto; margin: 0 auto 0.75rem; display: block; overflow: visible; }
	.schema .src rect, .schema .src path { fill: none; stroke: var(--text-secondary); stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
	.schema text { fill: var(--text-secondary); font-size: 12px; font-family: var(--font-serif); font-weight: 600; }
	.schema .cap { font-family: var(--font-family); font-weight: 600; font-size: 9px; letter-spacing: 0.1em; text-transform: uppercase; fill: var(--text-muted); }
	.schema .flow { fill: none; stroke: var(--border-card-hover); stroke-width: 1.5; stroke-dasharray: 3 4; animation: dash 6s linear infinite; }
	@keyframes dash { to { stroke-dashoffset: -70; } }
	.schema .needle { stroke: var(--text-primary); stroke-width: 3; stroke-linecap: round; transform-origin: 320px 134px; transform-box: view-box; animation: swing 7s ease-in-out infinite; }
	.schema .hub { fill: var(--text-primary); }
	@keyframes swing { 0%, 100% { transform: rotate(-40deg); } 50% { transform: rotate(22deg); } }
	@media (prefers-reduced-motion: reduce) { .schema .needle, .schema .flow { animation: none; } }

	.notes { margin: 0; padding-left: 1.1rem; color: var(--text-secondary); max-width: 52rem; display: grid; gap: 0.6rem; }
	.notes b { color: var(--text-primary); }

	@media (max-width: 820px) {
		.logos { grid-template-columns: repeat(2, minmax(0, 1fr)); }
		.hero { padding: 2rem 1rem; }
	}
</style>
