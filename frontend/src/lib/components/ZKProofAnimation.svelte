<script lang="ts">
	// A short looping animation of what a ZKPassport sign-in sends: the phone
	// reads the document, keeps only "French" and "18+", seals them in a proof
	// and sends the proof to our servers; the rest of the document stays on the
	// phone. Without motion (prefers-reduced-motion) the last frame is shown.
	import { tr, type Lang } from '$lib/i18n';

	let { lang }: { lang: Lang } = $props();
</script>

<figure class="anim">
	<figcaption>
		<strong class="never">{tr('zk.anim.never', lang)}</strong>
		<span class="only">
			{tr('zk.anim.only', lang)}
			<b class="tag">{tr('zk.anim.french', lang)}</b>
			{tr('zk.anim.and', lang)}
			<b class="tag">{tr('zk.anim.adult', lang)}</b>
		</span>
	</figcaption>
	<svg viewBox="0 0 340 150" role="img" aria-label={tr('zk.anim.alt', lang)}>
		<!-- The document: the data page of a passport. -->
		<g class="doc">
			<rect x="8" y="14" width="96" height="112" rx="6" class="page" />
			<text x="16" y="27" class="doc-head">PASSEPORT · RF</text>
			<rect x="16" y="34" width="26" height="32" rx="2" class="photo" />
			<rect x="48" y="36" width="44" height="5" rx="2" class="line" />
			<rect x="48" y="45" width="34" height="5" rx="2" class="line" />
			<rect x="48" y="54" width="40" height="5" rx="2" class="line hl" />
			<rect x="48" y="63" width="30" height="5" rx="2" class="line hl" />
			<rect x="16" y="76" width="76" height="5" rx="2" class="line" />
			<rect x="16" y="85" width="56" height="5" rx="2" class="line" />
			<text x="16" y="105" class="mrz">P&lt;FRA&lt;&lt;&lt;&lt;&lt;&lt;&lt;&lt;</text>
			<text x="16" y="115" class="mrz">0X0X0X0X&lt;&lt;&lt;&lt;&lt;</text>
		</g>
		<!-- Once the two facts are out, the rest of the document is locked on the phone. -->
		<g class="shut">
			<rect x="8" y="14" width="96" height="112" rx="6" class="veil" />
			<g transform="translate(56 62)" class="lock">
				<path d="M-6 -3 v-4 a6 6 0 0 1 12 0 v4" />
				<rect x="-9" y="-3" width="18" height="14" rx="3" />
			</g>
			<text x="56" y="92" text-anchor="middle" class="veil-text">{tr('zk.anim.stays', lang)}</text>
		</g>

		<!-- The path to the server. -->
		<line x1="216" y1="66" x2="256" y2="66" class="wire" />

		<!-- The two facts, pulled out of the document. -->
		<g class="chip chip-fr">
			<rect width="72" height="20" rx="10" />
			<text x="36" y="14" text-anchor="middle">{tr('zk.anim.french', lang)}</text>
		</g>
		<g class="chip chip-age">
			<rect width="72" height="20" rx="10" />
			<text x="36" y="14" text-anchor="middle">{tr('zk.anim.adult', lang)}</text>
		</g>

		<!-- Our server. -->
		<g class="server">
			<rect x="262" y="38" width="60" height="16" rx="3" />
			<rect x="262" y="58" width="60" height="16" rx="3" />
			<rect x="262" y="78" width="60" height="16" rx="3" />
			<circle cx="272" cy="46" r="2" class="led" />
			<circle cx="272" cy="66" r="2" class="led" />
			<circle cx="272" cy="86" r="2" class="led" />
		</g>
		<!-- The sealed proof. -->
		<g class="proof">
			<rect width="84" height="72" rx="8" class="proof-box" />
			<g transform="translate(42 24)" class="seal">
				<path d="M-6 -3 v-4 a6 6 0 0 1 12 0 v4" />
				<rect x="-9" y="-3" width="18" height="14" rx="3" />
			</g>
			<text x="42" y="52" text-anchor="middle" class="proof-label">{tr('zk.anim.proof', lang)}</text>
			<text x="42" y="64" text-anchor="middle" class="proof-sub">FR ✓ · 18+ ✓</text>
		</g>

		<g class="check" transform="translate(320 38)">
			<circle r="11" />
			<path d="M-5 0 l3.5 3.5 l6.5 -7" />
		</g>

		<text x="56" y="144" text-anchor="middle" class="caption">{tr('zk.anim.doc', lang)}</text>
		<text x="170" y="144" text-anchor="middle" class="caption">{tr('zk.anim.sealed', lang)}</text>
		<text x="292" y="144" text-anchor="middle" class="caption">{tr('zk.anim.server', lang)}</text>
	</svg>

</figure>

<style>
	.anim {
		margin: 0 0 0.9rem;
	}
	svg {
		display: block;
		width: 100%;
		height: auto;
		font-family: var(--font-family);
	}
	.page {
		fill: #1f1a22;
		stroke: var(--glass-border);
	}
	.doc-head {
		font-size: 7px;
		font-weight: 700;
		letter-spacing: 0.08em;
		fill: var(--text-muted);
	}
	.photo {
		fill: #3a3038;
	}
	.line {
		fill: #3a3038;
	}
	.line.hl {
		fill: var(--green);
	}
	.mrz {
		font-family: ui-monospace, monospace;
		font-size: 7px;
		fill: var(--text-muted);
	}
	.veil {
		fill: rgba(10, 10, 15, 0.72);
	}
	.lock path,
	.seal path {
		fill: none;
		stroke: var(--text-primary);
		stroke-width: 2.4;
	}
	.lock rect,
	.seal rect {
		fill: var(--text-primary);
	}
	.veil-text {
		font-size: 8px;
		font-weight: 600;
		fill: var(--text-primary);
	}
	.wire {
		stroke: var(--text-muted);
		stroke-width: 1.5;
		stroke-dasharray: 3 4;
	}
	.chip rect {
		fill: var(--green);
	}
	.chip text {
		font-size: 10px;
		font-weight: 700;
		fill: #0a0a0f;
	}
	.chip,
	.proof {
		transform-box: fill-box;
		transform-origin: center;
	}
	/* The resting places of the chips and the proof (the last frame). */
	.chip-fr {
		transform: translate(134px, 36px);
		opacity: 0;
	}
	.chip-age {
		transform: translate(134px, 76px);
		opacity: 0;
	}
	.proof {
		transform: translate(128px, 30px);
	}
	.proof-box {
		fill: color-mix(in oklch, var(--accent) 35%, #15131a);
		stroke: var(--accent-hover);
		stroke-width: 1.5;
	}
	.proof-label {
		font-size: 10px;
		font-weight: 700;
		fill: var(--text-primary);
	}
	.proof-sub {
		font-size: 8px;
		font-weight: 600;
		fill: var(--green);
	}
	.server rect {
		fill: #2a2430;
		stroke: var(--glass-border);
	}
	.led {
		fill: var(--green);
	}
	.check circle {
		fill: var(--green);
	}
	.check path {
		fill: none;
		stroke: #0a0a0f;
		stroke-width: 2.6;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.caption {
		font-size: 9px;
		fill: var(--text-secondary);
	}

	@media (prefers-reduced-motion: no-preference) {
		.line.hl {
			animation: hl 7s infinite;
		}
		.shut {
			animation: shut 7s infinite;
		}
		.chip-fr {
			animation: chip-fr 7s infinite;
		}
		.chip-age {
			animation: chip-age 7s infinite;
		}
		.proof {
			animation: proof 7s infinite;
		}
		.check {
			animation: check 7s infinite;
		}
	}

	/* 0-18%: the two facts light up on the document; 18-38%: they come out;
	   38-46%: the document is locked; 46-56%: the facts are sealed in the
	   proof; 62-76%: the proof goes to the server; 78%: the server accepts. */
	@keyframes hl {
		0%,
		6% {
			fill: #3a3038;
		}
		16%,
		96% {
			fill: var(--green);
		}
		100% {
			fill: #3a3038;
		}
	}
	@keyframes shut {
		0%,
		38% {
			opacity: 0;
		}
		46%,
		94% {
			opacity: 1;
		}
		100% {
			opacity: 0;
		}
	}
	@keyframes chip-fr {
		0%,
		17% {
			opacity: 0;
			transform: translate(50px, 47px) scale(0.5);
		}
		22% {
			opacity: 1;
			transform: translate(50px, 47px) scale(0.6);
		}
		38%,
		46% {
			opacity: 1;
			transform: translate(134px, 36px) scale(1);
		}
		54%,
		100% {
			opacity: 0;
			transform: translate(134px, 50px) scale(0.4);
		}
	}
	@keyframes chip-age {
		0%,
		19% {
			opacity: 0;
			transform: translate(44px, 56px) scale(0.5);
		}
		24% {
			opacity: 1;
			transform: translate(44px, 56px) scale(0.6);
		}
		40%,
		46% {
			opacity: 1;
			transform: translate(134px, 76px) scale(1);
		}
		54%,
		100% {
			opacity: 0;
			transform: translate(134px, 60px) scale(0.4);
		}
	}
	@keyframes proof {
		0%,
		46% {
			opacity: 0;
			transform: translate(128px, 30px) scale(0.5);
		}
		54%,
		62% {
			opacity: 1;
			transform: translate(128px, 30px) scale(1);
		}
		76% {
			opacity: 1;
			transform: translate(250px, 30px) scale(0.45);
		}
		80%,
		100% {
			opacity: 0;
			transform: translate(250px, 30px) scale(0.3);
		}
	}
	@keyframes check {
		0%,
		76% {
			opacity: 0;
		}
		80%,
		96% {
			opacity: 1;
		}
		100% {
			opacity: 0;
		}
	}

	/* The words first, the picture under them. */
	figcaption {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.35rem;
		margin-bottom: 0.6rem;
		text-align: center;
	}
	.never {
		font-size: 1.25rem;
		font-weight: 800;
		line-height: 1.2;
		color: var(--text-primary);
	}
	/* One line, whatever the width: the font shrinks before it wraps. */
	.only {
		display: flex;
		align-items: center;
		gap: 0.3em;
		white-space: nowrap;
		font-size: clamp(0.7rem, 3.2vw, 0.95rem);
		font-weight: 600;
		color: var(--text-primary);
	}
	.tag {
		display: inline-block;
		padding: 0.05rem 0.5rem;
		border-radius: var(--radius-full);
		background: var(--green);
		color: #0a0a0f;
		font-weight: 800;
		white-space: nowrap;
	}
</style>
