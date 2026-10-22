<script lang="ts">
	// The mirror of the site: a glass where facts float, two of them linked
	// by a contradiction. Decorative.
</script>

<figure class="mirror" aria-hidden="true">
	<svg viewBox="0 0 200 240">
		<defs>
			<radialGradient id="about-glass" cx="50%" cy="38%" r="65%">
				<stop offset="0%" stop-color="var(--accent)" stop-opacity="0.28" />
				<stop offset="70%" stop-color="var(--bg-secondary)" stop-opacity="0.9" />
				<stop offset="100%" stop-color="var(--bg-primary)" stop-opacity="1" />
			</radialGradient>
			<linearGradient id="about-sheen" x1="0" x2="1" y1="0" y2="0">
				<stop offset="0%" stop-color="var(--text-primary)" stop-opacity="0" />
				<stop offset="50%" stop-color="var(--text-primary)" stop-opacity="0.16" />
				<stop offset="100%" stop-color="var(--text-primary)" stop-opacity="0" />
			</linearGradient>
			<filter id="about-blur" x="-30%" y="-30%" width="160%" height="160%"><feGaussianBlur stdDeviation="10" /></filter>
			<clipPath id="about-oval"><ellipse cx="100" cy="112" rx="66" ry="88" /></clipPath>
		</defs>
		<ellipse class="halo" filter="url(#about-blur)" cx="100" cy="112" rx="88" ry="108" />
		<ellipse cx="100" cy="112" rx="66" ry="88" fill="url(#about-glass)" />
		<g clip-path="url(#about-oval)">
			<rect class="sheen" x="-60" y="0" width="50" height="240" fill="url(#about-sheen)" transform="skewX(-18)" />
			<!-- Facts floating in the glass, two of them linked by a contradiction. -->
			<line class="clash" x1="74" y1="96" x2="128" y2="132" />
			<circle class="mote m1" cx="74" cy="96" r="4" />
			<circle class="mote m2" cx="128" cy="132" r="4" />
			<circle class="mote dim m3" cx="112" cy="70" r="2.5" />
			<circle class="mote dim m4" cx="86" cy="152" r="2.5" />
			<circle class="mote dim m5" cx="132" cy="92" r="2" />
			<circle class="mote dim m6" cx="66" cy="128" r="2" />
		</g>
		<ellipse class="frame" cx="100" cy="112" rx="66" ry="88" />
		<ellipse class="frame-outer" cx="100" cy="112" rx="74" ry="96" />
		<path class="frame" d="M100 16 l6 -8 l-6 -6 l-6 6z" />
	</svg>
</figure>

<style>
	.mirror {
		width: 9.5rem;
		margin: 0 auto 1rem;
	}
	.mirror svg {
		display: block;
		width: 100%;
		height: auto;
	}
	.halo {
		fill: var(--accent-glow);
		opacity: 0.35;
		transform-box: fill-box;
		animation: breathe 6s ease-in-out infinite;
		transform-origin: center;
	}
	.frame {
		fill: none;
		stroke: var(--accent-hover);
		stroke-width: 2;
	}
	.frame-outer {
		fill: none;
		stroke: var(--border-card-hover);
		stroke-width: 1;
	}
	.sheen {
		animation: sweep 7s ease-in-out infinite;
	}
	.mote {
		fill: var(--text-primary);
		opacity: 0.85;
		animation: float 8s ease-in-out infinite;
	}
	.mote.dim {
		opacity: 0.35;
	}
	.m2 {
		animation-delay: -2s;
	}
	.m3 {
		animation-delay: -4s;
	}
	.m4 {
		animation-delay: -1s;
	}
	.m5 {
		animation-delay: -5s;
	}
	.m6 {
		animation-delay: -3s;
	}
	.clash {
		stroke: var(--red);
		stroke-width: 1.5;
		stroke-dasharray: 3 3;
		opacity: 0.8;
	}
	@keyframes sweep {
		0%,
		15% {
			transform: skewX(-18deg) translateX(0);
		}
		60%,
		100% {
			transform: skewX(-18deg) translateX(300px);
		}
	}
	@keyframes float {
		0%,
		100% {
			transform: translateY(0);
		}
		50% {
			transform: translateY(-4px);
		}
	}
	@keyframes breathe {
		0%,
		100% {
			opacity: 0.25;
			transform: scale(0.97);
		}
		50% {
			opacity: 0.45;
			transform: scale(1.02);
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.halo,
		.sheen,
		.mote {
			animation: none;
		}
		.sheen {
			display: none;
		}
	}
</style>
