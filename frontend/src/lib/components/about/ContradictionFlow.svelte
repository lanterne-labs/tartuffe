<script lang="ts">
	// How a contradiction is sorted, on a made-up example: two facts of the same
	// person on the same topic go to the AI; only when they cannot both be true
	// is the pair kept, read again, and later audited.
	import type { Lang } from '$lib/i18n';

	let { lang }: { lang: Lang } = $props();

	const text = {
		fr: {
			alt: 'Schéma : deux faits comparés par une IA, puis relus avant d’être retenus comme contradiction',
			a: ['2023 · déclaration', '« Nous ne toucherons pas', 'à l’âge de départ. »'],
			b: ['2024 · vote', 'Pour le report', 'de l’âge légal'],
			judge: ['L’IA se demande :', 'les deux peuvent-ils être vrais ?'],
			yes: 'Oui : rien à signaler',
			no: 'Non : seconde lecture',
			kept: 'Contradiction retenue',
			audit: 'Audit périodique',
			note: 'Exemple fictif'
		},
		en: {
			alt: 'Diagram: two facts compared by an AI, then re-read before being kept as a contradiction',
			a: ['2023 · statement', '“We will not touch', 'the retirement age.”'],
			b: ['2024 · vote', 'For raising the', 'legal retirement age'],
			judge: ['The AI asks:', 'can both be true?'],
			yes: 'Yes: nothing to flag',
			no: 'No: second reading',
			kept: 'Contradiction kept',
			audit: 'Periodic audit',
			note: 'Made-up example'
		}
	};
	let c = $derived(text[lang]);
</script>

<figure class="flow">
	<svg viewBox="0 0 360 300" role="img" aria-label={c.alt}>
		<!-- The two facts. -->
		<g class="fact">
			<rect x="6" y="8" width="164" height="62" rx="10" />
			<text x="18" y="27" class="meta">{c.a[0]}</text>
			<text x="18" y="45" class="quote">{c.a[1]}</text>
			<text x="18" y="60" class="quote">{c.a[2]}</text>
		</g>
		<g class="fact">
			<rect x="190" y="8" width="164" height="62" rx="10" />
			<text x="202" y="27" class="meta">{c.b[0]}</text>
			<text x="202" y="45" class="quote">{c.b[1]}</text>
			<text x="202" y="60" class="quote">{c.b[2]}</text>
		</g>

		<path class="wire flowing" d="M88 70 C88 92 180 88 180 112" />
		<path class="wire flowing" d="M272 70 C272 92 180 88 180 112" />

		<!-- The judge. -->
		<g class="judge">
			<rect x="50" y="112" width="260" height="48" rx="24" />
			<text x="180" y="132" text-anchor="middle" class="judge-a">{c.judge[0]}</text>
			<text x="180" y="149" text-anchor="middle" class="judge-b">{c.judge[1]}</text>
		</g>

		<path class="wire muted" d="M120 160 C120 180 88 178 88 196" />
		<path class="wire flowing" d="M240 160 C240 180 272 178 272 196" />

		<g class="pill pill-yes">
			<rect x="6" y="196" width="164" height="30" rx="15" />
			<text x="88" y="215" text-anchor="middle">{c.yes}</text>
		</g>
		<g class="pill pill-no">
			<rect x="190" y="196" width="164" height="30" rx="15" />
			<text x="272" y="215" text-anchor="middle">{c.no}</text>
		</g>

		<path class="wire flowing" d="M272 226 L272 252" />

		<g class="pill pill-kept">
			<rect x="190" y="252" width="164" height="32" rx="16" />
			<text x="272" y="272" text-anchor="middle">{c.kept}</text>
		</g>

		<!-- The audit comes back to the contradictions kept. -->
		<path class="wire audit-wire" d="M170 268 L190 268" />
		<g class="pill pill-audit">
			<rect x="6" y="252" width="164" height="32" rx="16" />
			<g class="spin" transform="translate(30 268)">
				<path d="M-6 0 a6 6 0 1 1 2 4.5" />
				<path d="M-6 -1 l0 5 l4.5 -1.5" />
			</g>
			<text x="98" y="272" text-anchor="middle">{c.audit}</text>
		</g>
	</svg>
	<figcaption>{c.note}</figcaption>
</figure>

<style>
	.flow {
		margin: 0;
		max-width: 30rem;
		margin-inline: auto;
	}
	svg {
		display: block;
		width: 100%;
		height: auto;
		font-family: var(--font-family);
	}
	.fact rect {
		fill: var(--bg-secondary);
		stroke: var(--border-card-hover);
	}
	.meta {
		fill: var(--text-muted);
		font-size: 9.5px;
		letter-spacing: 0.04em;
		text-transform: uppercase;
	}
	.quote {
		fill: var(--text-primary);
		font-size: 12px;
		font-family: var(--font-serif);
		font-weight: 600;
	}
	.judge rect {
		fill: color-mix(in srgb, var(--accent) 18%, var(--bg-secondary));
		stroke: var(--accent);
	}
	.judge-a {
		fill: var(--text-secondary);
		font-size: 10.5px;
	}
	.judge-b {
		fill: var(--text-primary);
		font-size: 12.5px;
		font-weight: 600;
	}
	.wire {
		fill: none;
		stroke: var(--border-card-hover);
		stroke-width: 1.5;
	}
	.flowing {
		stroke: var(--accent-hover);
		stroke-dasharray: 4 5;
		animation: dash 1.6s linear infinite;
	}
	.muted {
		stroke-dasharray: 2 4;
	}
	.audit-wire {
		stroke: var(--amber);
		stroke-dasharray: 3 3;
	}
	@keyframes dash {
		to {
			stroke-dashoffset: -18;
		}
	}
	.pill text {
		font-size: 11.5px;
		font-weight: 600;
	}
	.pill-yes rect {
		fill: transparent;
		stroke: var(--border-card-hover);
	}
	.pill-yes text {
		fill: var(--text-muted);
	}
	.pill-no rect {
		fill: transparent;
		stroke: var(--amber);
	}
	.pill-no text {
		fill: var(--amber);
	}
	.pill-kept rect {
		fill: color-mix(in srgb, var(--red) 22%, transparent);
		stroke: var(--red);
	}
	.pill-kept text {
		fill: var(--text-primary);
	}
	.pill-audit rect {
		fill: transparent;
		stroke: var(--amber);
		stroke-dasharray: 3 3;
	}
	.pill-audit text {
		fill: var(--amber);
	}
	.spin path {
		fill: none;
		stroke: var(--amber);
		stroke-width: 1.5;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	figcaption {
		text-align: center;
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-top: 0.4rem;
		font-style: italic;
	}
	@media (prefers-reduced-motion: reduce) {
		.flowing {
			animation: none;
		}
	}
</style>
