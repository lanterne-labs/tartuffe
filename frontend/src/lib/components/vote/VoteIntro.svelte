<script lang="ts">
	// The presentation a voter sees on arriving at the vote for the first
	// time: five slides, "next" after "next". The story first, the passport
	// after: why an online vote is worth nothing (bots, duplicates), what
	// this one offers (one person, one voice, so a proof of a human is
	// needed), then the passport as that proof without showing it, the
	// ballot on an infrastructure we do not control (we only count), and
	// what the voter needs. The page opens it once (it remembers) and again
	// on request. Every line must stay true to the code and to
	// /fonctionnement, which has the long version: the State lines hold
	// because the registry's identifier is salted (isoloir SPEC §6) and a
	// withheld ballot shows in the public log.
	import { fly } from 'svelte/transition';
	import ZKProofAnimation from '../ZKProofAnimation.svelte';
	import type { Lang } from '$lib/i18n';

	interface Props {
		open: boolean;
		lang: Lang;
		// The long version, linked from the last slide.
		howHref: string;
		onclose: () => void;
	}
	let { open, lang, howHref, onclose }: Props = $props();

	// A point is a line, or a line with its sub-points; **…** marks the
	// words to read first.
	type Point = string | { text: string; sub: string[] };
	type Slide = { title: string; points: Point[]; promise?: string };
	const textOf = (p: Point) => (typeof p === 'string' ? p : p.text);
	const content = {
		fr: {
			label: 'Présentation du vote',
			close: 'Fermer',
			skip: 'Passer',
			back: 'Retour',
			next: 'Suivant',
			start: 'C’est parti',
			more: 'Comment ça marche, en détail',
			slide: 'Étape {i} sur {n}',
			slides: [
				{
					title: 'Un vote en ligne ne vaut rien',
					points: [
						'N’importe qui remplit un sondage en ligne : **un robot vote mille fois**, un militant dix fois sous dix adresses.',
						'**Le résultat ne mesure rien**, et tout le monde le sait.'
					]
				},
				{
					title: '1 personne = 1 voix',
					points: [
						'Ici, chaque votant est **une personne réelle, majeure, française**.',
						'Le résultat devient **un vrai signal**, en direct, que personne ne peut gonfler.',
						'Pour ça, nous avons besoin d’une preuve que vous êtes **humain, et unique**.'
					]
				},
				{
					title: 'Votre passeport, sans le montrer',
					points: [
						{
							text: 'Votre téléphone prouve que vous avez une **puce authentique, signée par l’État français**, et ne révèle que :',
							sub: ['vous avez **plus de 18 ans**', 'votre passeport est **français**', 'un **identifiant anonyme** unique, dérivé du passeport']
						},
						'Pas de compte, pas d’e-mail, pas de numéro de téléphone, **pas de numéro de passeport** : **rien qui vous nomme**.'
					],
					promise: 'Rien ne fuite : ni nous ni l’État ne pouvons savoir qui vous êtes, ni que vous êtes inscrit.'
				},
				{
					title: 'Votre vote reste secret',
					points: [
						'Votre bulletin part **scellé** vers des relais publics, **hors de notre contrôle**. Nous ne savons pas d’où il vient.',
						'Nous ne faisons que **compter**, les bulletins toujours scellés, et publier le résultat **avec sa preuve**.',
						'Nous ne pouvons **ni savoir qui a voté, ni écarter un bulletin** en douce : tout est public, **chacun peut recompter**.'
					],
					promise: 'Personne, ni nous ni l’État, ne peut savoir ce que vous avez voté.'
				},
				{
					title: 'Ce qu’il vous faut',
					points: [
						'Votre **passeport à puce** (le petit symbole sur la couverture), un **smartphone NFC** et l’**app ZKPassport**, gratuite.',
						'**Deux minutes, une seule fois.** Ensuite, vous pouvez changer d’avis jusqu’à la clôture.'
					]
				}
			] as Slide[],
			bots: 'Un robot : mille voix',
			inflated: 'Résultat gonflé',
			people: 'Une personne : une voix',
			verified: 'Résultat vérifié',
			phone: 'Téléphone',
			outside: 'Hors de notre contrôle',
			relays: 'Relais publics',
			counts: 'compte seulement',
			needs: ['Passeport à puce', 'Smartphone NFC', 'App ZKPassport']
		},
		en: {
			label: 'About the vote',
			close: 'Close',
			skip: 'Skip',
			back: 'Back',
			next: 'Next',
			start: 'Let’s go',
			more: 'How it works, in detail',
			slide: 'Step {i} of {n}',
			slides: [
				{
					title: 'An online vote is worth nothing',
					points: [
						'Anyone fills an online poll: **a bot votes a thousand times**, an activist ten times from ten addresses.',
						'**The result measures nothing**, and everyone knows it.'
					]
				},
				{
					title: '1 person = 1 voice',
					points: [
						'Here, every voter is **a real person, an adult, French**.',
						'The result becomes **a true signal**, live, that nobody can inflate.',
						'For that, we need a proof that you are **human, and unique**.'
					]
				},
				{
					title: 'Your passport, without showing it',
					points: [
						{
							text: 'Your phone proves that you hold a **genuine chip, signed by the French State**, and reveals only:',
							sub: ['you are **over 18**', 'your passport is **French**', 'a unique **anonymous identifier**, derived from the passport']
						},
						'No account, no e-mail, no phone number, **no passport number**: **nothing that names you**.'
					],
					promise: 'Nothing leaks: neither we nor the State can know who you are, nor that you registered.'
				},
				{
					title: 'Your vote stays secret',
					points: [
						'Your ballot leaves **sealed** for public relays, **outside our control**. We do not know where it comes from.',
						'We only **count**, the ballots still sealed, and publish the result **with its proof**.',
						'We can **neither know who voted, nor quietly drop a ballot**: everything is public, **anyone can recount**.'
					],
					promise: 'Nobody, neither we nor the State, can know what you voted.'
				},
				{
					title: 'What you need',
					points: [
						'Your **chip passport** (the small symbol on the cover), an **NFC smartphone** and the free **ZKPassport app**.',
						'**Two minutes, once.** Then you can change your mind until the close.'
					]
				}
			] as Slide[],
			bots: 'One bot: a thousand voices',
			inflated: 'Inflated result',
			people: 'One person: one voice',
			verified: 'Verified result',
			phone: 'Phone',
			outside: 'Outside our control',
			relays: 'Public relays',
			counts: 'only counts',
			needs: ['Chip passport', 'NFC smartphone', 'ZKPassport app']
		}
	};
	let c = $derived(content[lang]);
	let n = $derived(c.slides.length);

	let dialog: HTMLDialogElement | undefined = $state();
	let i = $state(0);
	// The direction the next slide comes from: +1 forward, -1 back.
	let dir = $state(1);
	let last = $derived(i === n - 1);
	let stage = $state<HTMLElement | null>(null);
	const reduced = typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
	const duration = reduced ? 0 : 240;

	$effect(() => {
		if (!dialog) return;
		if (open && !dialog.open) {
			i = 0;
			dir = 1;
			dialog.showModal();
		} else if (!open && dialog.open) dialog.close();
	});

	function go(to: number) {
		if (to < 0 || to >= n || to === i) return;
		dir = to > i ? 1 : -1;
		i = to;
		// A slide longer than a small screen starts at its top.
		stage?.scrollTo({ top: 0 });
	}
	function next() {
		if (last) onclose();
		else go(i + 1);
	}
	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'ArrowRight') {
			e.preventDefault();
			if (!last) go(i + 1);
		} else if (e.key === 'ArrowLeft') {
			e.preventDefault();
			go(i - 1);
		}
	}
	function onBackdrop(e: MouseEvent) {
		if (e.target === dialog) onclose();
	}
	// A swipe turns the slides on a touch screen.
	let swipeX: number | null = null;
	function onpointerdown(e: PointerEvent) {
		swipeX = e.pointerType === 'mouse' ? null : e.clientX;
	}
	function onpointerup(e: PointerEvent) {
		if (swipeX === null) return;
		const dx = e.clientX - swipeX;
		swipeX = null;
		if (dx < -40 && !last) go(i + 1);
		else if (dx > 40) go(i - 1);
	}
</script>

<!-- A line with its **bold** words. -->
{#snippet rich(s: string)}
	{#each s.split('**') as part, k (k)}{#if k % 2}<strong>{part}</strong>{:else}{part}{/if}{/each}
{/snippet}

<!-- A result card: three bars, the first one long. -->
{#snippet result(x: number, bad: boolean)}
	<g transform="translate({x} 34)">
		<rect x="0" y="0" width="118" height="80" rx="8" class="card" />
		<rect x="12" y="14" width={bad ? 94 : 70} height="10" rx="5" class="bar" class:bar--bad={bad} />
		<rect x="12" y="34" width={bad ? 22 : 50} height="10" rx="5" class="bar bar--dim" />
		<rect x="12" y="54" width={bad ? 14 : 34} height="10" rx="5" class="bar bar--dim" />
		{#if bad}
			<text x="104" y="68" text-anchor="end" class="big big--bad">?</text>
		{:else}
			<g transform="translate(100 60)" class="check">
				<circle r="9" />
				<path d="M-4 0l3 3 5.5-6" />
			</g>
		{/if}
	</g>
{/snippet}

<!-- The usual online vote: one bot, a thousand voices, a result worth nothing. -->
{#snippet stuffed()}
	<svg viewBox="0 0 340 150" class="fig" role="img" aria-label="{c.bots}. {c.inflated}.">
		<g class="bots">
			{#each [0, 1, 2, 3] as k (k)}
				<g transform="translate({34 + k * 22} {62 - k * 6})">
					<g class="bot" style="--k: {k}">
						<line x1="0" y1="-14" x2="0" y2="-6" />
						<circle cx="0" cy="-16" r="2.2" class="dot" />
						<rect x="-16" y="-6" width="32" height="28" rx="6" />
						<circle cx="-6" cy="6" r="3" class="eye" />
						<circle cx="6" cy="6" r="3" class="eye" />
						<rect x="-7" y="14" width="14" height="3" rx="1.5" class="eye" />
					</g>
				</g>
			{/each}
			<text x="124" y="84" class="times">× 1000</text>
		</g>
		<path d="M176 74h26" class="arrow" />
		{@render result(206, true)}
		<text x="92" y="138" text-anchor="middle" class="caption caption--bad">{c.bots}</text>
		<text x="265" y="138" text-anchor="middle" class="caption caption--bad">{c.inflated}</text>
	</svg>
{/snippet}

<!-- This vote: one person, one voice, a result anyone can check. -->
{#snippet people()}
	<svg viewBox="0 0 340 150" class="fig" role="img" aria-label="{c.people}. {c.verified}.">
		<g class="crowd">
			{#each [0, 1, 2, 3, 4] as k (k)}
				<g transform="translate({30 + k * 30} {k % 2 ? 58 : 70})" class="person person--{k}" style="--k: {k}">
					<circle cx="0" cy="-10" r="8" />
					<path d="M-14 16a14 14 0 0 1 28 0z" />
					<text x="0" y="31" text-anchor="middle" class="one">1</text>
				</g>
			{/each}
		</g>
		<path d="M176 74h26" class="arrow" />
		{@render result(206, false)}
		<text x="92" y="138" text-anchor="middle" class="caption caption--good">{c.people}</text>
		<text x="265" y="138" text-anchor="middle" class="caption caption--good">{c.verified}</text>
	</svg>
{/snippet}

<!-- The ballot's way: the phone sends it sealed to public relays we do not
     run; tartuffe.me only reads them and counts. -->
{#snippet infra()}
	<svg viewBox="0 0 340 150" class="fig" role="img" aria-label="{c.phone} → {c.relays} ({c.outside}) → tartuffe.me {c.counts}">
		<g class="handset" transform="translate(30 40)">
			<rect x="0" y="0" width="36" height="62" rx="7" class="phone" />
			<rect x="5" y="8" width="26" height="42" rx="3" class="screen" />
		</g>
		<g class="envelope envelope--out">
			<rect width="20" height="14" rx="2" />
			<path d="M0 0l10 8 10-8" />
		</g>
		<text x="50" y="129" text-anchor="middle" class="caption">{c.phone}</text>

		<g class="outside">
			<rect x="80" y="18" width="156" height="96" rx="10" />
			<text x="158" y="33" text-anchor="middle" class="caption caption--out">{c.outside}</text>
			{#each [113, 158, 203] as x, k (x)}
				<g class="relay" transform="translate({x} {k === 1 ? 54 : 62})">
					<path d="M0 10v24M-7 4a10 10 0 0 1 14 0M-12 -1a17 17 0 0 1 24 0" />
					<circle cx="0" cy="10" r="2.5" />
				</g>
			{/each}
			<text x="158" y="107" text-anchor="middle" class="caption">{c.relays}</text>
		</g>

		<g class="envelope envelope--in">
			<rect width="20" height="14" rx="2" />
			<path d="M0 0l10 8 10-8" />
		</g>
		<g class="server" transform="translate(254 44)">
			<rect x="0" y="0" width="60" height="14" rx="3" />
			<rect x="0" y="18" width="60" height="14" rx="3" />
			<rect x="0" y="36" width="60" height="14" rx="3" />
			<circle cx="9" cy="7" r="2" class="led" />
			<circle cx="9" cy="25" r="2" class="led" />
			<circle cx="9" cy="43" r="2" class="led" />
		</g>
		<text x="284" y="116" text-anchor="middle" class="caption">tartuffe.me</text>
		<text x="284" y="129" text-anchor="middle" class="caption caption--good">{c.counts}</text>
	</svg>
{/snippet}

<!-- What the voter needs: a chip passport, an NFC phone, the app; each ticked. -->
{#snippet needs()}
	<svg viewBox="0 0 340 150" class="fig" role="img" aria-label={c.needs.join(', ')}>
		{#each c.needs as label, k (k)}
			{@const x = 58 + k * 112}
			<g class="need" style="--k: {k}">
				{#if k === 0}
					<!-- The passport, with the chip symbol of its cover. -->
					<g transform="translate({x - 24} 22)">
						<rect x="0" y="0" width="48" height="66" rx="5" class="cover" />
						<rect x="14" y="22" width="20" height="14" rx="2" class="sym" />
						<circle cx="24" cy="29" r="4" class="sym" />
						<line x1="14" y1="29" x2="34" y2="29" class="sym" />
						<rect x="10" y="48" width="28" height="3" rx="1.5" class="line" />
					</g>
				{:else if k === 1}
					<!-- The phone, with the NFC waves. -->
					<g transform="translate({x - 18} 22)">
						<rect x="0" y="0" width="36" height="66" rx="7" class="phone" />
						<rect x="5" y="8" width="26" height="46" rx="3" class="screen" />
						<g transform="translate(18 31)" class="nfc">
							<path d="M-4 -7a9 9 0 0 1 0 14" />
							<path d="M0 -12a15 15 0 0 1 0 24" />
							<path d="M4 -17a21 21 0 0 1 0 34" />
						</g>
					</g>
				{:else}
					<!-- The app: a tile with a shield. -->
					<g transform="translate({x - 26} 30)">
						<rect x="0" y="0" width="52" height="52" rx="12" class="tile" />
						<path d="M26 10l14 5v11c0 9-6 16-14 18-8-2-14-9-14-18V15z" class="shield" />
						<path d="M19 28l5 5 9-10" class="tick" />
					</g>
				{/if}
				<g transform="translate({x + 24} 86)">
					<g class="check">
						<circle r="8" />
						<path d="M-3.5 0l2.5 2.5 5-5" />
					</g>
				</g>
				<text {x} y="126" text-anchor="middle" class="caption">{label}</text>
			</g>
		{/each}
	</svg>
{/snippet}

<dialog bind:this={dialog} class="intro" aria-label={c.label} onclose={onclose} onclick={onBackdrop} {onkeydown}>
	<div class="body">
		<div class="top">
			<span class="count">{c.slide.replace('{i}', String(i + 1)).replace('{n}', String(n))}</span>
			<button class="close" onclick={onclose} aria-label={c.close}>×</button>
		</div>

		<!-- The dialog focuses the slide when it opens: left to itself it would
		     focus the first focusable, the "×", and ring it. -->
		<!-- svelte-ignore a11y_autofocus -->
		<div class="stage" bind:this={stage} tabindex="-1" autofocus {onpointerdown} {onpointerup}>
			{#key i}
				<section class="slide" in:fly={{ x: 28 * dir, duration }}>
					<div class="figure">
						{#if i === 0}
							{@render stuffed()}
						{:else if i === 1}
							{@render people()}
						{:else if i === 2}
							<!-- The site's animation: the phone keeps the passport, sends
							     "French" and "18+" alone, sealed in a proof. -->
							<ZKProofAnimation {lang} />
						{:else if i === 3}
							{@render infra()}
						{:else}
							{@render needs()}
						{/if}
					</div>
					<h2>{c.slides[i].title}</h2>
					<ul class="points">
						{#each c.slides[i].points as p (textOf(p))}
							<li>
								{@render rich(textOf(p))}
								{#if typeof p !== 'string'}
									<ul class="sub">
										{#each p.sub as s (s)}<li>{@render rich(s)}</li>{/each}
									</ul>
								{/if}
							</li>
						{/each}
					</ul>
					{#if c.slides[i].promise}
						<!-- The promise, the one line to keep. -->
						<p class="promise">
							<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3l8 3v6c0 4.5-3.3 8.3-8 9-4.7-.7-8-4.5-8-9V6z" /><path d="M8.5 12l2.5 2.5 4.5-5" /></svg>
							<strong>{c.slides[i].promise}</strong>
						</p>
					{/if}
					{#if last}
						<a class="more" href={howHref}>{c.more} →</a>
					{/if}
				</section>
			{/key}
		</div>

		<div class="foot">
			{#if i === 0}
				<button class="btn btn-quiet" onclick={onclose}>{c.skip}</button>
			{:else}
				<button class="btn btn-quiet" onclick={() => go(i - 1)}>{c.back}</button>
			{/if}
			<div class="dots" role="tablist">
				{#each c.slides as s, k (k)}
					<button class:on={k === i} role="tab" aria-selected={k === i} aria-label={s.title} onclick={() => go(k)}></button>
				{/each}
			</div>
			<button class="btn btn-main" onclick={next}>{last ? c.start : c.next} →</button>
		</div>
	</div>
</dialog>

<style>
	.intro {
		margin: auto;
		width: min(30rem, calc(100vw - 2rem));
		max-height: calc(100vh - 2rem);
		padding: 0;
		overflow: hidden;
		background: var(--bg-secondary);
		color: var(--text-primary);
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-card-hover);
	}
	.intro::backdrop {
		background: rgba(0, 0, 0, 0.6);
		backdrop-filter: blur(3px);
	}
	.body {
		display: flex;
		flex-direction: column;
		padding: 0.9rem 1.4rem 1.2rem;
	}
	.top {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}
	.count {
		font-size: 0.72rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--event);
	}
	.close {
		width: 2.2rem;
		height: 2.2rem;
		margin-right: -0.7rem;
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 1.5rem;
		line-height: 1;
		cursor: pointer;
	}
	.close:hover {
		color: var(--text-primary);
	}
	.stage {
		touch-action: pan-y;
	}
	.stage:focus {
		outline: none;
	}
	.slide {
		min-height: 22rem;
		text-align: center;
	}
	.figure {
		margin: 0.4rem auto 0;
		max-width: 22rem;
	}
	/* The site's animation brings a caption of its own: here the slide's
	   title and points say it, the drawing alone is kept. */
	.figure :global(.anim) {
		margin-bottom: 0;
	}
	.figure :global(figcaption) {
		display: none;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.5rem;
		font-weight: 600;
		line-height: 1.15;
		margin: 0.7rem 0 0.7rem;
		text-wrap: balance;
	}
	.points {
		list-style: none;
		margin: 0 auto;
		padding: 0;
		max-width: 25rem;
		display: grid;
		gap: 0.5rem;
		text-align: left;
	}
	.points li {
		position: relative;
		padding-left: 1.3rem;
		font-size: 0.92rem;
		line-height: 1.5;
		color: var(--text-secondary);
	}
	.points li::before {
		content: '';
		position: absolute;
		left: 0;
		top: 0.5rem;
		width: 0.5rem;
		height: 0.5rem;
		border-radius: 50%;
		background: var(--event);
	}
	.points strong {
		color: var(--text-primary);
		font-weight: 700;
	}
	.sub {
		list-style: none;
		margin: 0.3rem 0 0;
		padding: 0;
		display: grid;
		gap: 0.2rem;
	}
	.sub li {
		padding-left: 1.1rem;
	}
	.sub li::before {
		top: 0.55rem;
		width: 0.35rem;
		height: 0.35rem;
		background: var(--green);
	}
	/* The promise stands out: a green box, the shield, the line in bold. */
	.promise {
		display: flex;
		align-items: center;
		gap: 0.7rem;
		max-width: 25rem;
		margin: 0.9rem auto 0;
		padding: 0.7rem 0.9rem;
		border: 1px solid var(--green);
		border-radius: var(--radius-md, 12px);
		background: color-mix(in oklch, var(--green) 12%, transparent);
		text-align: left;
	}
	.promise svg {
		flex-shrink: 0;
		width: 1.5rem;
		height: 1.5rem;
		fill: none;
		stroke: var(--green);
		stroke-width: 2;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.promise strong {
		font-size: 1rem;
		line-height: 1.35;
		color: var(--text-primary);
	}
	.more {
		display: inline-block;
		margin-top: 0.8rem;
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--event);
	}
	.foot {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		margin-top: 1rem;
	}
	.dots {
		display: flex;
	}
	/* Each dot is drawn in the middle of a button 40px high, the buttons
	   side by side with no gap between them: the whole row is a finger's
	   target, and the dots keep their look. */
	.dots button {
		position: relative;
		width: 1rem;
		height: 2.5rem;
		padding: 0;
		border: none;
		background: none;
		cursor: pointer;
		transition: width var(--transition-fast, 0.15s);
	}
	.dots button::before {
		content: '';
		position: absolute;
		left: 0.25rem;
		right: 0.25rem;
		top: 50%;
		height: 0.5rem;
		margin-top: -0.25rem;
		border-radius: 0.3rem;
		background: var(--glass-border);
		transition: background var(--transition-fast, 0.15s);
	}
	.dots button.on {
		width: 1.8rem;
	}
	.dots button.on::before {
		background: var(--event);
	}
	.btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-height: 2.6rem;
		padding: 0 1.1rem;
		border-radius: var(--radius-full, 999px);
		border: 1px solid transparent;
		font: inherit;
		font-weight: 600;
		cursor: pointer;
		white-space: nowrap;
	}
	.btn-quiet {
		background: none;
		color: var(--text-secondary);
	}
	.btn-quiet:hover {
		color: var(--text-primary);
	}
	.btn-main {
		background: var(--event-strong);
		color: #fff;
		padding: 0 1.3rem;
		box-shadow: 0 6px 20px var(--event-glow);
	}
	.btn-main:hover {
		background: var(--event);
	}

	/* The drawings */
	.fig {
		display: block;
		width: 100%;
		height: auto;
		font-family: var(--font-family);
	}
	/* 12px in the drawing is about 11px on a phone: the floor of a caption. */
	.caption {
		font-size: 12px;
		font-weight: 600;
		fill: var(--text-secondary);
	}
	.caption--bad {
		fill: var(--text-muted);
	}
	.caption--good {
		fill: var(--green);
	}
	.arrow {
		fill: none;
		stroke: var(--text-muted);
		stroke-width: 1.5;
		stroke-dasharray: 3 3;
	}
	.card {
		fill: #1f1a22;
		stroke: var(--glass-border);
	}
	.bar {
		fill: var(--event);
	}
	.bar--dim {
		fill: #3a3038;
	}
	.bar--bad {
		fill: var(--red);
		animation: grow 3s ease-in-out infinite;
		transform-origin: 12px 0;
	}
	@keyframes grow {
		0%,
		15% {
			transform: scaleX(0.3);
		}
		70%,
		100% {
			transform: scaleX(1);
		}
	}
	.big {
		font-size: 22px;
		font-weight: 800;
	}
	.big--bad {
		fill: var(--red);
	}
	.bots rect,
	.bots line {
		fill: #3a3038;
		stroke: var(--text-muted);
		stroke-width: 1.2;
	}
	.bots .eye,
	.bots .dot {
		fill: var(--text-muted);
		stroke: none;
	}
	/* The bots pop up one after the other, again and again: the same one, many times. */
	.bot {
		opacity: calc(1 - var(--k) * 0.18);
		animation: bot-in 4s calc(var(--k) * 0.18s) ease-out infinite;
	}
	@keyframes bot-in {
		0%,
		60%,
		100% {
			transform: translateY(0);
		}
		8% {
			transform: translateY(-7px);
		}
	}
	.times {
		font-size: 15px;
		font-weight: 800;
		fill: var(--text-muted);
	}
	.person circle,
	.person path {
		fill: var(--event);
	}
	.person--1 circle,
	.person--1 path {
		fill: var(--green);
	}
	.person--2 circle,
	.person--2 path {
		fill: var(--amber);
	}
	.person--3 circle,
	.person--3 path {
		fill: var(--text-secondary);
	}
	.person--4 circle,
	.person--4 path {
		fill: var(--event-strong);
	}
	.person .one {
		font-size: 12px;
		font-weight: 800;
		fill: var(--green);
	}
	.person {
		animation: person-in 0.5s calc(var(--k) * 0.12s) ease-out both;
	}
	@keyframes person-in {
		from {
			opacity: 0;
		}
	}
	.envelope rect {
		fill: var(--event);
	}
	.envelope path {
		fill: none;
		stroke: #0a0a0f;
		stroke-width: 1.4;
	}
	/* The ballot's way */
	.handset .phone {
		fill: #1f1a22;
		stroke: var(--text-secondary);
		stroke-width: 1.4;
	}
	.handset .screen {
		fill: #2a2430;
	}
	.outside rect {
		fill: color-mix(in oklch, var(--amber) 6%, transparent);
		stroke: var(--amber);
		stroke-width: 1.2;
		stroke-dasharray: 5 4;
	}
	.caption--out {
		fill: var(--amber);
		font-weight: 700;
	}
	.relay path {
		fill: none;
		stroke: var(--text-secondary);
		stroke-width: 2;
		stroke-linecap: round;
	}
	.relay circle {
		fill: var(--text-secondary);
	}
	.server rect {
		fill: #1f1a22;
		stroke: var(--glass-border);
	}
	.server .led {
		fill: var(--green);
	}
	/* One envelope goes from the phone into the relays, another from the
	   relays to the server: two legs of the same way, in turn. */
	.envelope--out {
		animation: leg-out 4s ease-in-out infinite;
	}
	.envelope--in {
		animation: leg-in 4s ease-in-out infinite;
	}
	@keyframes leg-out {
		0% {
			transform: translate(56px, 58px);
			opacity: 0;
		}
		10%,
		40% {
			opacity: 1;
		}
		45% {
			transform: translate(118px, 80px);
			opacity: 0;
		}
		100% {
			transform: translate(118px, 80px);
			opacity: 0;
		}
	}
	@keyframes leg-in {
		0%,
		50% {
			transform: translate(196px, 80px);
			opacity: 0;
		}
		58%,
		88% {
			opacity: 1;
		}
		95%,
		100% {
			transform: translate(232px, 62px);
			opacity: 0;
		}
	}
	.need .cover {
		fill: #1f1a22;
		stroke: var(--event);
		stroke-width: 1.4;
	}
	.need .sym {
		fill: none;
		stroke: var(--green);
		stroke-width: 1.4;
	}
	.need .line {
		fill: #3a3038;
	}
	.need .phone {
		fill: #1f1a22;
		stroke: var(--text-secondary);
		stroke-width: 1.4;
	}
	.need .screen {
		fill: #2a2430;
	}
	.nfc path {
		fill: none;
		stroke: var(--event);
		stroke-width: 1.6;
		stroke-linecap: round;
		animation: nfc 2.4s ease-in-out infinite;
	}
	.nfc path:nth-child(2) {
		animation-delay: 0.3s;
	}
	.nfc path:nth-child(3) {
		animation-delay: 0.6s;
	}
	@keyframes nfc {
		0%,
		100% {
			opacity: 0.35;
		}
		40% {
			opacity: 1;
		}
	}
	.need .tile {
		fill: var(--event-strong);
	}
	.need .shield {
		fill: none;
		stroke: #fff;
		stroke-width: 2;
		stroke-linejoin: round;
	}
	.need .tick {
		fill: none;
		stroke: #fff;
		stroke-width: 2.2;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.check circle {
		fill: var(--green);
	}
	.check path {
		fill: none;
		stroke: #fff;
		stroke-width: 2;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.need .check {
		animation: check-in 0.5s calc(0.3s + var(--k) * 0.25s) ease-out both;
	}
	@keyframes check-in {
		from {
			opacity: 0;
			transform: scale(0.4);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.bot,
		.bar--bad,
		.person,
		.envelope,
		.nfc path,
		.need .check {
			animation: none;
		}
		.envelope {
			opacity: 1;
		}
		.envelope--out {
			transform: translate(56px, 58px);
		}
		.envelope--in {
			transform: translate(232px, 62px);
		}
	}

	/* A phone: the slides fill the screen, the buttons sit above the home
	   bar, a slide taller than the screen scrolls between the two. */
	@media (max-width: 560px) {
		.intro {
			width: 100vw;
			max-width: none;
			height: 100dvh;
			max-height: none;
			margin: 0;
			border: none;
			border-radius: 0;
		}
		.body {
			height: 100%;
			padding: 0.8rem 1.1rem calc(1rem + env(safe-area-inset-bottom, 0px));
		}
		.stage {
			flex: 1;
			display: flex;
			flex-direction: column;
			justify-content: center;
			min-height: 0;
			overflow-y: auto;
		}
		.slide {
			min-height: 0;
			margin: auto 0;
		}
		.figure {
			max-width: 19rem;
		}
		.btn {
			padding: 0 0.9rem;
		}
		.btn-main {
			padding: 0 1.1rem;
		}
	}
</style>
