<script lang="ts">
	// The audit page: the visitor's browser recounts the whole vote from the
	// public log (lib/vote/audit.ts). While it runs, one card follows the
	// reading. When it ends, the checks are told one by one: each card pops
	// on top of the one before, and the diagram of the pipeline (voters,
	// relays, server, public log, results) lights up the part it covers.
	// Every anomaly is listed; a check that could not run is an anomaly.
	import { page } from '$app/stores';
	import { onDestroy } from 'svelte';
	import { lang, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';
	import { settingsProblem } from '$lib/vote/isoloir';
	import type { AuditProgress, AuditResult } from '$lib/vote/audit';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));
	let problem = $derived(settingsProblem(slug));

	type StepKey = 'log' | 'registrations' | 'ballots' | 'tallies';
	const STEPS: StepKey[] = ['log', 'registrations', 'ballots', 'tallies'];
	const REPO = 'https://github.com/lanterne-labs/isoloir';

	const content = {
		fr: {
			title: 'Recomptez le vote vous-même',
			lead: 'Votre navigateur refait chaque contrôle, à partir du registre public.',
			start: 'Vérifier',
			again: 'Vérifier à nouveau',
			stop: 'Arrêter',
			nodes: ['Électeurs', 'Relais', 'Serveur', 'Registre', 'Résultats'],
			steps: {
				log: 'Registre signé et intact',
				registrations: 'Un passeport, une inscription',
				ballots: 'Bulletins valides',
				tallies: 'Résultats recomptés'
			} as Record<StepKey, string>,
			reading: (a: number, b: number) => `Lecture du registre · ${a}/${b}`,
			checking: (a: number, b: number) => `Vérification des preuves · ${a}/${b}`,
			replay: (a: number, b: number) => `Recomptage · ${a}/${b}`,
			checkpoints: (n: number) => `${n} points de contrôle`,
			regs: (n: number) => `${n} inscription${n > 1 ? 's' : ''}`,
			proofs: (n: number) => `${n} preuve${n > 1 ? 's' : ''} vérifiée${n > 1 ? 's' : ''}`,
			tallies: (n: number) => `${n} résultat${n > 1 ? 's' : ''}`,
			okTitle: 'Tout est vérifié',
			koTitle: (n: number) => `${n} anomalie${n > 1 ? 's' : ''}`,
			report: 'Signaler',
			failed: 'La vérification s’est interrompue',
			code: 'Le code de la vérification',
			notConfigured: 'La vérification ouvrira avec le vote.',
			back: 'Retour au vote'
		},
		en: {
			title: 'Recount the vote yourself',
			lead: 'Your browser redoes every check, from the public log.',
			start: 'Verify',
			again: 'Verify again',
			stop: 'Stop',
			nodes: ['Voters', 'Relays', 'Server', 'Log', 'Results'],
			steps: {
				log: 'Log signed and intact',
				registrations: 'One passport, one registration',
				ballots: 'Valid ballots',
				tallies: 'Results recounted'
			} as Record<StepKey, string>,
			reading: (a: number, b: number) => `Reading the log · ${a}/${b}`,
			checking: (a: number, b: number) => `Checking the proofs · ${a}/${b}`,
			replay: (a: number, b: number) => `Recounting · ${a}/${b}`,
			checkpoints: (n: number) => `${n} checkpoints`,
			regs: (n: number) => `${n} registration${n > 1 ? 's' : ''}`,
			proofs: (n: number) => `${n} proof${n > 1 ? 's' : ''} verified`,
			tallies: (n: number) => `${n} result${n > 1 ? 's' : ''}`,
			okTitle: 'Everything checks out',
			koTitle: (n: number) => `${n} anomal${n > 1 ? 'ies' : 'y'}`,
			report: 'Report',
			failed: 'The check stopped',
			code: 'The code of the check',
			notConfigured: 'The check opens with the vote.',
			back: 'Back to the vote'
		}
	};
	let c = $derived(content[currentLang]);

	let progress = $state<AuditProgress | null>(null);
	let result = $state<AuditResult | null>(null);
	let failure = $state<string | null>(null);
	let running = $state(false);
	// How many checks are told so far (after the audit, one by one).
	let shown = $state(0);
	let ctl: AbortController | null = null;
	let timer: ReturnType<typeof setInterval> | null = null;

	async function start() {
		running = true;
		result = null;
		failure = null;
		progress = null;
		shown = 0;
		if (timer) clearInterval(timer);
		ctl = new AbortController();
		try {
			const { runAudit } = await import('$lib/vote/audit');
			result = await runAudit((p) => (progress = p), ctl.signal);
			timer = setInterval(() => {
				shown++;
				if (shown >= STEPS.length + 1 && timer) clearInterval(timer);
			}, 650);
		} catch (e) {
			if (!ctl?.signal.aborted) failure = e instanceof Error ? e.message : String(e);
			console.error('audit failed', e);
		} finally {
			running = false;
		}
	}
	onDestroy(() => {
		ctl?.abort();
		if (timer) clearInterval(timer);
	});

	// Which check a problem belongs to (the auditor's own words).
	function stepOf(p: string): StepKey {
		if (/\(register\)|registr/i.test(p)) return 'registrations';
		if (/\(ballot\)/.test(p)) return 'ballots';
		if (/tally|close/.test(p)) return 'tallies';
		return 'log';
	}
	let failed = $derived(new Set((result?.problems ?? []).map(stepOf)));
	// The checks told so far, in order, each new one under the last.
	let told = $derived(STEPS.slice(0, Math.min(shown, STEPS.length)));
	let done = $derived(!!result && shown > STEPS.length);
	const passed = (k: StepKey) => STEPS.indexOf(k) < shown && !failed.has(k);
	const missed = (k: StepKey) => STEPS.indexOf(k) < shown && failed.has(k);

	function count(k: StepKey): string {
		const r = result;
		if (!r) return '';
		switch (k) {
			case 'log':
				return c.checkpoints(r.checkpoints);
			case 'registrations':
				return c.regs(r.registrations);
			case 'ballots':
				return c.proofs(r.proofs.checked);
			case 'tallies':
				return c.tallies(r.tallies);
		}
	}
	let live = $derived.by(() => {
		const p = progress;
		if (!p) return '';
		if (p.phase === 'download') return c.reading(p.checkpoint, p.checkpoints);
		if (p.phase === 'proofs') return c.checking(p.verified, p.asked);
		return c.replay(p.checkpoint, p.checkpoints);
	});
	let pct = $derived(
		!progress
			? 2
			: progress.phase === 'download'
				? (progress.checkpoint / Math.max(1, progress.checkpoints)) * 45
				: progress.phase === 'proofs'
					? 45 + (progress.verified / Math.max(1, progress.asked)) * 15
					: progress.phase === 'replay'
						? 60 + (progress.checkpoint / Math.max(1, progress.checkpoints)) * 35
						: 98
	);

	// The pipeline: what each check covers, node by node and link by link.
	const NODE_STEPS: StepKey[][] = [['registrations', 'ballots'], ['ballots'], ['log'], ['log'], ['tallies']];
	const LINK_STEPS: StepKey[] = ['ballots', 'ballots', 'log', 'tallies'];
	function lit(ks: StepKey[]): 'idle' | 'ok' | 'ko' {
		if (ks.some(missed)) return 'ko';
		return ks.every(passed) ? 'ok' : 'idle';
	}
	const ICONS = [
		'M12 11.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM5 20c0-3.6 3.1-6 7-6s7 2.4 7 6',
		'M12 11v9M8.5 7.5a5 5 0 0 1 7 0M5.5 4.5a9 9 0 0 1 13 0M12 11.5a1 1 0 1 0 0-2 1 1 0 0 0 0 2z',
		'M5 4h14v7H5zM5 13h14v7H5zM8 7.5h.01M8 16.5h.01',
		'M6 4h10a3 3 0 0 1 3 3v13H9a3 3 0 0 1-3-3zM6 17a3 3 0 0 1 3-3h10M10 8h5',
		'M6 20v-7M12 20V5M18 20v-10'
	];
</script>

<div class="page-content audit">
	<h1>{c.title}</h1>
	<p class="lead">{c.lead}</p>

	{#if problem}
		<p class="notice">{c.notConfigured}</p>
	{:else}
		<!-- The pipeline: each part lights up when its check is told. -->
		<div class="pipe" class:pipe--run={running} aria-hidden="true">
			{#each c.nodes as name, i (i)}
				{#if i > 0}
					<span class="link link--{lit([LINK_STEPS[i - 1]])}"><span class="dot"></span></span>
				{/if}
				<span class="node node--{lit(NODE_STEPS[i])}">
					<span class="disc">
						<svg viewBox="0 0 24 24"><path d={ICONS[i]} /></svg>
						{#if lit(NODE_STEPS[i]) === 'ok'}<span class="tick">✓</span>{/if}
					</span>
					<span class="name">{name}</span>
				</span>
			{/each}
		</div>

		{#if running}
			<div class="card card--live">
				<span class="spin"></span>
				<span class="live">{live}</span>
				<span class="bar"><span style="width: {pct}%"></span></span>
			</div>
			<button class="btn btn--ghost" onclick={() => ctl?.abort()}>{c.stop}</button>
		{/if}

		<ol class="stack">
			{#each told as k (k)}
				<li class="card" class:card--ko={failed.has(k)}>
					<span class="icon">
						{#if failed.has(k)}
							<svg viewBox="0 0 24 24"><path d="M7 7l10 10M17 7L7 17" /></svg>
						{:else}
							<svg viewBox="0 0 24 24"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>
						{/if}
					</span>
					<span class="text">
						<strong>{c.steps[k]}</strong>
						<span class="count">{count(k)}</span>
					</span>
				</li>
			{/each}
		</ol>

		{#if !running}
			{#if done && result}
				{#if result.ok}
					<div class="verdict verdict--ok" role="status">
						<svg viewBox="0 0 52 52" class="seal" aria-hidden="true"><circle cx="26" cy="26" r="24" /><path d="M15 27l7 7 15-16" /></svg>
						<h2>{c.okTitle}</h2>
					</div>
				{:else}
					<div class="verdict verdict--ko" role="alert">
						<h2>{c.koTitle(result.problems.length)}</h2>
						<ul class="problems">
							{#each result.problems as p (p)}<li><code>{p}</code></li>{/each}
						</ul>
						<a class="btn btn--ghost" href="{REPO}/issues/new" target="_blank" rel="noopener">{c.report}</a>
					</div>
				{/if}
			{:else if failure}
				<div class="verdict verdict--ko" role="alert">
					<h2>{c.failed}</h2>
					<p><code>{failure}</code></p>
				</div>
			{/if}
			{#if !result || done}
				<button class="btn btn--primary" onclick={start}>{result || failure ? c.again : c.start}</button>
			{/if}
		{/if}

	{/if}

	<p class="links">
		<a href={REPO} target="_blank" rel="noopener">{c.code} ↗</a>
		<span aria-hidden="true">·</span>
		<a href={paths.vote}>{c.back}</a>
	</p>
</div>

<style>
	.audit {
		max-width: 34rem;
		margin: 0 auto;
		text-align: center;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(1.7rem, 5vw, 2.3rem);
		line-height: 1.15;
		margin: 1.5rem 0 0.5rem;
	}
	.lead {
		color: var(--text-secondary);
		margin: 0 auto 1.6rem;
	}
	.notice {
		padding: 1rem;
		border-radius: var(--radius-md);
		background: var(--bg-card);
		color: var(--text-secondary);
	}

	/* The pipeline */
	.pipe {
		display: flex;
		align-items: flex-start;
		justify-content: center;
		margin: 0 auto 1.6rem;
	}
	.node {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.35rem;
		width: 3.9rem;
		flex-shrink: 0;
	}
	.disc {
		position: relative;
		width: 2.9rem;
		height: 2.9rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: var(--bg-card);
		border: 1.5px solid var(--glass-border);
		color: var(--text-muted);
		transition:
			background 0.4s,
			border-color 0.4s,
			color 0.4s;
	}
	.disc svg {
		width: 1.35rem;
		height: 1.35rem;
		fill: none;
		stroke: currentColor;
		stroke-width: 1.8;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.node--ok .disc {
		background: color-mix(in oklch, var(--green) 22%, var(--bg-card));
		border-color: var(--green);
		color: var(--green);
		animation: pop 0.4s ease-out;
	}
	.node--ko .disc {
		border-color: var(--red);
		color: var(--red);
	}
	.tick {
		position: absolute;
		right: -0.25rem;
		bottom: -0.25rem;
		width: 1.1rem;
		height: 1.1rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: var(--green);
		color: #0a0a0f;
		font-size: 0.65rem;
		font-weight: 900;
	}
	.name {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-secondary);
	}
	.link {
		position: relative;
		flex: 1;
		min-width: 0.8rem;
		max-width: 3rem;
		height: 2px;
		margin-top: 1.45rem;
		background: var(--glass-border);
		overflow: hidden;
		transition: background 0.4s;
	}
	/* A narrow phone: the five parts fit the width. */
	@media (max-width: 400px) {
		.node {
			width: 3.2rem;
		}
		.disc {
			width: 2.5rem;
			height: 2.5rem;
		}
		.link {
			min-width: 0.4rem;
			margin-top: 1.25rem;
		}
	}
	.link--ok {
		background: var(--green);
	}
	.link--ko {
		background: var(--red);
	}
	.dot {
		display: none;
		position: absolute;
		top: -1px;
		width: 0.6rem;
		height: 4px;
		border-radius: 2px;
		background: var(--accent-hover, var(--accent));
	}
	.pipe--run .link .dot {
		display: block;
		animation: flow 1.1s linear infinite;
	}
	@keyframes flow {
		from {
			left: -0.6rem;
		}
		to {
			left: 100%;
		}
	}

	/* The live card, the told checks, the verdict */
	.btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-height: 3rem;
		padding: 0 1.6rem;
		margin: 0.2rem 0 1.2rem;
		border-radius: var(--radius-full, 999px);
		font: inherit;
		font-weight: 700;
		cursor: pointer;
		text-decoration: none;
	}
	.btn--primary {
		border: none;
		background: var(--accent);
		color: #fff;
		width: min(100%, 18rem);
		font-size: 1.05rem;
	}
	.btn--primary:hover {
		background: var(--accent-hover);
	}
	.btn--ghost {
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-primary);
	}
	.stack {
		list-style: none;
		padding: 0;
		margin: 0 0 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		text-align: left;
	}
	.card {
		display: grid;
		grid-template-columns: 2.1rem 1fr;
		gap: 0.8rem;
		align-items: center;
		padding: 0.75rem 1rem;
		border-radius: var(--radius-md);
		background: var(--bg-card);
		border: 1px solid color-mix(in oklch, var(--green) 40%, transparent);
		animation: drop 0.45s cubic-bezier(0.22, 1, 0.36, 1);
	}
	.card--ko {
		border-color: var(--red);
	}
	.card--live {
		grid-template-columns: 2.1rem 1fr;
		border-color: color-mix(in oklch, var(--accent) 50%, transparent);
		margin-bottom: 0.8rem;
		text-align: left;
	}
	.card--live .bar {
		grid-column: 1 / -1;
		height: 4px;
		border-radius: 999px;
		background: rgba(255, 255, 255, 0.08);
		overflow: hidden;
	}
	.card--live .bar span {
		display: block;
		height: 100%;
		background: var(--accent);
		transition: width 0.3s ease;
	}
	.live {
		font-weight: 600;
		font-variant-numeric: tabular-nums;
	}
	@keyframes drop {
		from {
			opacity: 0;
			transform: translateY(-0.8rem) scale(0.97);
		}
	}
	@keyframes pop {
		from {
			transform: scale(0.8);
		}
	}
	.icon {
		width: 2.1rem;
		height: 2.1rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: var(--green);
		color: #0a0a0f;
	}
	.card--ko .icon {
		background: var(--red);
		color: #fff;
	}
	.icon svg {
		width: 1.1rem;
		height: 1.1rem;
		fill: none;
		stroke: currentColor;
		stroke-width: 3;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.spin {
		justify-self: center;
		width: 1.3rem;
		height: 1.3rem;
		border-radius: 50%;
		border: 2.5px solid rgba(255, 255, 255, 0.15);
		border-top-color: var(--accent-hover, var(--accent));
		animation: spin 0.8s linear infinite;
	}
	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
	.text {
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
	}
	.count {
		font-size: 0.8rem;
		color: var(--text-secondary);
		font-variant-numeric: tabular-nums;
	}
	.verdict {
		padding: 1.2rem;
		margin-bottom: 1rem;
		border-radius: var(--radius-lg, 18px);
		animation: drop 0.5s cubic-bezier(0.22, 1, 0.36, 1);
	}
	.verdict h2 {
		margin: 0.4rem 0 0;
		font-size: 1.35rem;
	}
	.verdict--ok {
		background: color-mix(in oklch, var(--green) 14%, var(--bg-card));
		border: 1px solid color-mix(in oklch, var(--green) 50%, transparent);
	}
	.verdict--ko {
		background: color-mix(in oklch, var(--red) 12%, var(--bg-card));
		border: 1px solid var(--red);
	}
	.seal {
		width: 3.4rem;
		height: 3.4rem;
	}
	.seal circle {
		fill: var(--green);
	}
	.seal path {
		fill: none;
		stroke: #0a0a0f;
		stroke-width: 4.5;
		stroke-linecap: round;
		stroke-linejoin: round;
		stroke-dasharray: 40;
		stroke-dashoffset: 40;
		animation: draw 0.6s ease-out 0.2s forwards;
	}
	@keyframes draw {
		to {
			stroke-dashoffset: 0;
		}
	}
	.problems {
		text-align: left;
		margin: 0.8rem 0 1rem;
		padding-left: 1.2rem;
	}
	.problems code {
		font-size: 0.78rem;
		word-break: break-word;
	}
	/* The error of a stopped check is one long line: it breaks anywhere
	   rather than running off a phone. */
	.verdict p code {
		font-size: 0.78rem;
		overflow-wrap: anywhere;
	}
	.links {
		display: flex;
		justify-content: center;
		align-items: center;
		gap: 0.3rem;
		margin-top: 1.5rem;
		font-size: 0.85rem;
	}
	/* The links are a finger's target: the padding brings them to 40px. */
	.links a {
		display: inline-block;
		padding: 0.65rem 0.5rem;
		color: var(--text-secondary);
	}
	@media (prefers-reduced-motion: reduce) {
		.spin,
		.seal path,
		.card,
		.verdict,
		.node--ok .disc,
		.pipe--run .link .dot {
			animation: none;
			stroke-dashoffset: 0;
		}
	}
</style>
