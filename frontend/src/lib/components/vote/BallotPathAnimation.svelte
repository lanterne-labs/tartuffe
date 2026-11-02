<script lang="ts">
	// The way of a ballot, looping: the phone seals the ballot and sends it
	// to the public relays with its address; the address stops there. Our
	// server comes to read the relays and takes the sealed ballot among the
	// others, without any address. Without motion the last frame shows.
	import type { Lang } from '$lib/i18n';

	let { lang }: { lang: Lang } = $props();
	const t = {
		fr: { phone: 'Votre téléphone', relays: 'Relais publics', server: 'tartuffe.me', ip: 'votre IP', stops: 's’arrête ici', reads: 'lit les relais' },
		en: { phone: 'Your phone', relays: 'Public relays', server: 'tartuffe.me', ip: 'your IP', stops: 'stops here', reads: 'reads the relays' }
	};
	let c = $derived(t[lang]);
</script>

<svg viewBox="0 0 340 150" class="anim" role="img" aria-label="{c.phone} → {c.relays} → {c.server}">
	<!-- The phone -->
	<g class="phone">
		<rect x="18" y="34" width="44" height="74" rx="8" />
		<rect x="24" y="44" width="32" height="50" rx="3" class="screen" />
	</g>
	<text x="40" y="128" text-anchor="middle" class="label">{c.phone}</text>

	<!-- The relays: three antennas -->
	{#each [138, 170, 202] as x, i (x)}
		<g class="relay" transform="translate({x} {i === 1 ? 44 : 54})">
			<path d="M0 10v26M-7 4a10 10 0 0 1 14 0M-12 -1a17 17 0 0 1 24 0" />
			<circle cx="0" cy="10" r="2.5" />
		</g>
	{/each}
	<text x="170" y="128" text-anchor="middle" class="label">{c.relays}</text>
	<!-- Other ballots waiting on the relays -->
	<g class="other o1"><rect width="16" height="11" rx="2" /><path d="M0 0l8 6 8-6" /></g>
	<g class="other o2"><rect width="16" height="11" rx="2" /><path d="M0 0l8 6 8-6" /></g>

	<!-- The server -->
	<g class="server">
		<rect x="270" y="40" width="52" height="16" rx="3" />
		<rect x="270" y="60" width="52" height="16" rx="3" />
		<rect x="270" y="80" width="52" height="16" rx="3" />
	</g>
	<text x="296" y="128" text-anchor="middle" class="label">{c.server}</text>
	<line x1="262" y1="68" x2="222" y2="68" class="pull" />
	<text x="242" y="62" text-anchor="middle" class="tiny">{c.reads}</text>

	<!-- The ballot: sealed, with the address tag riding it up to the relays -->
	<g class="ballot">
		<rect width="22" height="15" rx="2.5" />
		<path d="M0 0l11 8 11-8" />
	</g>
	<g class="ip">
		<rect width="44" height="14" rx="7" />
		<text x="22" y="10" text-anchor="middle">{c.ip}</text>
	</g>
	<text x="170" y="22" text-anchor="middle" class="stops">{c.ip} {c.stops}</text>
</svg>

<style>
	.anim {
		display: block;
		width: 100%;
		max-width: 26rem;
		height: auto;
		margin: 0 auto;
		font-family: var(--font-family);
	}
	.phone rect {
		fill: #1f1a22;
		stroke: var(--glass-border);
	}
	.phone .screen {
		fill: #2a2430;
	}
	.relay path {
		fill: none;
		stroke: var(--text-secondary);
		stroke-width: 2.2;
		stroke-linecap: round;
	}
	.relay circle {
		fill: var(--text-secondary);
	}
	.server rect {
		fill: #2a2430;
		stroke: var(--glass-border);
	}
	.label {
		font-size: 9px;
		fill: var(--text-secondary);
	}
	.tiny {
		font-size: 7px;
		fill: var(--text-muted);
	}
	.pull {
		stroke: var(--text-muted);
		stroke-width: 1.5;
		stroke-dasharray: 3 4;
	}
	.ballot rect,
	.other rect {
		fill: var(--accent);
	}
	.ballot path,
	.other path {
		fill: none;
		stroke: #0a0a0f;
		stroke-width: 1.4;
	}
	.other rect {
		fill: color-mix(in oklch, var(--accent) 55%, #2a2430);
	}
	.o1 {
		transform: translate(150px, 96px);
	}
	.o2 {
		transform: translate(178px, 100px);
	}
	.ip rect {
		fill: var(--amber);
	}
	.ip text {
		font-size: 7.5px;
		font-weight: 800;
		fill: #0a0a0f;
	}
	.stops {
		font-size: 9px;
		font-weight: 700;
		fill: var(--amber);
		opacity: 1;
	}
	/* The last frame (no motion): the ballot at the server, the address at the relays. */
	.ballot {
		transform: translate(278px, 98px);
	}
	.ip {
		transform: translate(148px, 76px);
	}

	@media (prefers-reduced-motion: no-preference) {
		.ballot {
			animation: ballot 7s infinite;
		}
		.ip {
			animation: ip 7s infinite;
		}
		.stops {
			animation: stops 7s infinite;
		}
		.pull {
			animation: pull 7s infinite;
		}
	}
	/* 0-10: sealed on the phone; 10-38: to the relays with its address;
	   38-52: the address stops; 52-80: the server reads it, without it. */
	@keyframes ballot {
		0%,
		10% {
			transform: translate(29px, 62px);
			opacity: 0;
		}
		14% {
			opacity: 1;
			transform: translate(29px, 62px);
		}
		38%,
		52% {
			transform: translate(159px, 92px);
		}
		80%,
		96% {
			transform: translate(278px, 98px);
			opacity: 1;
		}
		100% {
			transform: translate(278px, 98px);
			opacity: 0;
		}
	}
	@keyframes ip {
		0%,
		10% {
			transform: translate(18px, 40px);
			opacity: 0;
		}
		14% {
			opacity: 1;
			transform: translate(18px, 40px);
		}
		38%,
		96% {
			transform: translate(148px, 76px);
			opacity: 1;
		}
		100% {
			transform: translate(148px, 76px);
			opacity: 0;
		}
	}
	@keyframes stops {
		0%,
		40% {
			opacity: 0;
		}
		48%,
		94% {
			opacity: 1;
		}
		100% {
			opacity: 0;
		}
	}
	@keyframes pull {
		0%,
		52% {
			opacity: 0.2;
		}
		58%,
		80% {
			opacity: 1;
		}
		100% {
			opacity: 0.2;
		}
	}
</style>
