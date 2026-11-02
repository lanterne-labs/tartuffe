<script lang="ts">
	// The audit page: the visitor's browser recounts the whole vote from the
	// public log (lib/vote/audit.ts), and shows each check as it runs: the
	// log's signatures, the registrations, the ballots' proofs, the tallies,
	// the relays. Everything it finds is listed; a check that could not run
	// is a problem, never a pass.
	import { page } from '$app/stores';
	import { onDestroy } from 'svelte';
	import { lang, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';
	import { settingsProblem, LOG_KEY, mirrorSources } from '$lib/vote/isoloir';
	import type { AuditProgress, AuditResult } from '$lib/vote/audit';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let slug = $derived($page.params.slug ?? '');
	let paths = $derived(eventPaths(slug));
	let problem = $derived(settingsProblem(slug));

	type StepKey = 'log' | 'registrations' | 'ballots' | 'tallies' | 'relays';
	const STEPS: StepKey[] = ['log', 'registrations', 'ballots', 'tallies', 'relays'];
	const N = ' ';

	const content = {
		fr: {
			kicker: 'Vérifier',
			title: 'Recomptez tout le vote vous-même',
			lead: 'Votre navigateur relit le registre public du début à la fin et refait chaque contrôle : chaque bulletin, chaque preuve, chaque résultat. Vous n’avez pas besoin de nous croire.',
			start: 'Lancer la vérification',
			again: 'Relancer la vérification',
			stop: 'Arrêter',
			duration: 'Quelques secondes à quelques minutes selon le nombre de bulletins. Gardez la page ouverte.',
			steps: {
				log: ['Le registre', 'Chaque point de contrôle est signé, et aucun ne réécrit le précédent.'],
				registrations: ['Les inscriptions', 'Chaque inscription porte une preuve de passeport, et un passeport ne s’inscrit qu’une fois.'],
				ballots: ['Les bulletins', 'Chaque bulletin porte une preuve qu’il est valide, sans révéler le choix.'],
				tallies: ['Les résultats', 'Chaque résultat publié est la somme des derniers bulletins de chacun, déchiffrée avec une preuve.'],
				relays: ['Les relais', 'Aucun bulletin reçu par les relais n’a été écarté en silence.']
			} as Record<StepKey, [string, string]>,
			checkpoints: (a: number, b: number) => `${a} sur ${b} points de contrôle`,
			proofs: (a: number, b: number) => `${a} preuves vérifiées sur ${b}`,
			regs: (n: number) => `${n} inscription${n > 1 ? 's' : ''}`,
			passports: (n: number) => `${n} passeport${n > 1 ? 's' : ''} vérifié${n > 1 ? 's' : ''}`,
			simulated: 'inscriptions de test (passeport simulé)',
			tallies: (n: number) => `${n} résultat${n > 1 ? 's' : ''} recompté${n > 1 ? 's' : ''}`,
			events: (n: number, r: number) => `${n} messages lus sur ${r} relais`,
			noRelays: 'pas de relais à lire',
			replay: 'Deuxième lecture, avec les preuves vérifiées',
			okTitle: 'Tout est vérifié',
			okBody: (b: number, t: number) => `${b} bulletin${b > 1 ? 's' : ''} et ${t} résultat${t > 1 ? 's' : ''} recomptés dans votre navigateur. Aucune anomalie.`,
			koTitle: (n: number) => `${n} anomalie${n > 1 ? 's' : ''} trouvée${n > 1 ? 's' : ''}`,
			koBody: 'Votre navigateur a trouvé ce qui suit. Signalez-le : chacune peut être vérifiée par n’importe qui.',
			report: 'Signaler',
			failed: 'La vérification n’a pas pu aller au bout',
			warnings: 'Remarques',
			experts: 'Pour les experts : la même vérification en ligne de commande',
			expertsBody: 'Avec l’outil isoloir, compilé par vous depuis ses sources, et vos propres clés de vérification :',
			notConfigured: 'La vérification n’est pas encore ouverte pour ce vote.',
			back: 'Retour au vote'
		},
		en: {
			kicker: 'Verify',
			title: 'Recount the whole vote yourself',
			lead: 'Your browser reads the public log from start to end and redoes every check: every ballot, every proof, every result. You do not need to trust us.',
			start: 'Start the check',
			again: 'Run it again',
			stop: 'Stop',
			duration: 'A few seconds to a few minutes depending on the number of ballots. Keep the page open.',
			steps: {
				log: ['The log', 'Every checkpoint is signed, and none rewrites the one before.'],
				registrations: ['The registrations', 'Every registration carries a passport proof, and a passport registers once.'],
				ballots: ['The ballots', 'Every ballot carries a proof that it is valid, without revealing the choice.'],
				tallies: ['The results', 'Every published result is the sum of everyone’s last ballot, decrypted with a proof.'],
				relays: ['The relays', 'No ballot the relays received was silently dropped.']
			} as Record<StepKey, [string, string]>,
			checkpoints: (a: number, b: number) => `${a} of ${b} checkpoints`,
			proofs: (a: number, b: number) => `${a} of ${b} proofs verified`,
			regs: (n: number) => `${n} registration${n > 1 ? 's' : ''}`,
			passports: (n: number) => `${n} passport${n > 1 ? 's' : ''} verified`,
			simulated: 'test registrations (simulated passport)',
			tallies: (n: number) => `${n} result${n > 1 ? 's' : ''} recounted`,
			events: (n: number, r: number) => `${n} messages read from ${r} relay${r > 1 ? 's' : ''}`,
			noRelays: 'no relay to read',
			replay: 'Second reading, with the verified proofs',
			okTitle: 'Everything checks out',
			okBody: (b: number, t: number) => `${b} ballot${b > 1 ? 's' : ''} and ${t} result${t > 1 ? 's' : ''} recounted in your browser. No anomaly.`,
			koTitle: (n: number) => `${n} anomal${n > 1 ? 'ies' : 'y'} found`,
			koBody: 'Your browser found the following. Report it: anyone can check each one.',
			report: 'Report',
			failed: 'The check could not finish',
			warnings: 'Notes',
			experts: 'For experts: the same check on the command line',
			expertsBody: 'With the isoloir tool, built by you from its sources, and your own verification keys:',
			notConfigured: 'The check is not open yet for this vote.',
			back: 'Back to the vote'
		}
	};
	let c = $derived(content[currentLang]);

	let progress = $state<AuditProgress | null>(null);
	let result = $state<AuditResult | null>(null);
	let failure = $state<string | null>(null);
	let running = $state(false);
	let ctl: AbortController | null = null;

	async function start() {
		running = true;
		result = null;
		failure = null;
		progress = null;
		ctl = new AbortController();
		try {
			const { runAudit } = await import('$lib/vote/audit');
			result = await runAudit((p) => (progress = p), ctl.signal);
		} catch (e) {
			if (!ctl?.signal.aborted) failure = e instanceof Error ? e.message : String(e);
			console.error('audit failed', e);
		} finally {
			running = false;
		}
	}
	function stop() {
		ctl?.abort();
	}
	onDestroy(() => ctl?.abort());

	// Which step a problem belongs to (the auditor's own words).
	function stepOf(p: string): StepKey {
		if (/\(register\)/.test(p) || /registr/i.test(p)) return 'registrations';
		if (/\(ballot\)/.test(p)) return 'ballots';
		if (/tally|close/.test(p)) return 'tallies';
		if (/relay|event |acknowledged/.test(p)) return 'relays';
		return 'log';
	}
	let problems = $derived(result ? result.problems : []);
	let failedSteps = $derived(new Set(problems.map(stepOf)));
	let simulated = $derived(!!result?.warnings.some((w) => w.includes('dev provider')));

	type Status = 'wait' | 'run' | 'ok' | 'fail';
	function status(k: StepKey): Status {
		if (result) return failedSteps.has(k) ? 'fail' : 'ok';
		if (!progress) return 'wait';
		const ph = progress.phase;
		if (k === 'relays') return ph === 'relays' ? 'run' : 'wait';
		if (k === 'ballots' && ph === 'proofs') return 'run';
		if (ph === 'download' || ph === 'replay') return k === 'tallies' && ph === 'download' ? 'wait' : 'run';
		return ph === 'relays' || ph === 'done' ? 'ok' : 'wait';
	}
	function detail(k: StepKey): string {
		const r = result ?? progress?.report ?? null;
		const p = progress;
		switch (k) {
			case 'log':
				return p ? c.checkpoints(p.checkpoint, p.checkpoints) : '';
			case 'registrations':
				if (!r) return '';
				if (result && result.passports.checked) return `${c.regs(r.registrations)} · ${c.passports(result.passports.checked)}`;
				return result && simulated ? `${c.regs(r.registrations)} · ${c.simulated}` : c.regs(r.registrations);
			case 'ballots':
				return p ? c.proofs(p.verified, p.asked) : '';
			case 'tallies':
				return r ? c.tallies(r.tallies) : '';
			case 'relays':
				if (result) return result.relays ? c.events(result.relays.events, result.relays.read.length) : c.noRelays;
				return '';
		}
	}
	let pct = $derived(
		!progress
			? 0
			: progress.phase === 'download'
				? (progress.checkpoint / Math.max(1, progress.checkpoints)) * 45
				: progress.phase === 'proofs'
					? 45 + (progress.verified / Math.max(1, progress.asked)) * 15
					: progress.phase === 'replay'
						? 60 + (progress.checkpoint / Math.max(1, progress.checkpoints)) * 35
						: progress.phase === 'relays'
							? 97
							: 100
	);
	let mirrorURL = $derived(mirrorSources()[0]?.url('', '') ?? '<mirror>');
	let command = $derived(`isoloir audit --mirror ${mirrorURL} \\\n  --log-key "${LOG_KEY}" \\\n  --circuits ./circuits-vk --zkverifier ./zkverifier/verify.mjs --relays --bitcoin`);
</script>

<div class="page-content audit">
	<p class="kicker">{c.kicker}</p>
	<h1>{c.title}</h1>
	<p class="lead">{c.lead}</p>

	{#if problem}
		<p class="notice">{c.notConfigured}</p>
	{:else}
		<div class="actions">
			{#if running}
				<button class="btn btn--ghost" onclick={stop}>{c.stop}</button>
			{:else}
				<button class="btn btn--primary" onclick={start}>{result || failure ? c.again : c.start}</button>
			{/if}
			<p class="duration">{c.duration}</p>
		</div>

		{#if progress || result || failure}
			<div class="bar" aria-hidden="true"><span style="width: {pct}%"></span></div>
			{#if progress?.phase === 'replay'}<p class="phase">{c.replay}</p>{/if}
		{/if}

		<ol class="steps">
			{#each STEPS as k, i (k)}
				{@const s = status(k)}
				<li class="step step--{s}">
					<span class="icon" aria-hidden="true">
						{#if s === 'ok'}
							<svg viewBox="0 0 24 24"><path d="M5 12.5l4.5 4.5L19 7.5" /></svg>
						{:else if s === 'fail'}
							<svg viewBox="0 0 24 24"><path d="M7 7l10 10M17 7L7 17" /></svg>
						{:else if s === 'run'}
							<span class="spin"></span>
						{:else}
							<span class="num">{i + 1}</span>
						{/if}
					</span>
					<div class="text">
						<strong>{c.steps[k][0]}</strong>
						<span class="what">{c.steps[k][1]}</span>
						{#if detail(k)}<span class="count">{detail(k)}</span>{/if}
					</div>
				</li>
			{/each}
		</ol>

		{#if result}
			{#if result.ok}
				<div class="verdict verdict--ok" role="status">
					<svg viewBox="0 0 52 52" class="seal" aria-hidden="true"><circle cx="26" cy="26" r="24" /><path d="M15 27l7 7 15-16" /></svg>
					<h2>{c.okTitle}</h2>
					<p>{c.okBody(result.ballots, result.tallies)}</p>
				</div>
			{:else}
				<div class="verdict verdict--ko" role="alert">
					<h2>{c.koTitle(result.problems.length)}</h2>
					<p>{c.koBody}</p>
					<ul class="problems">
						{#each result.problems as p (p)}<li><code>{p}</code></li>{/each}
					</ul>
					<a class="btn btn--ghost" href="https://github.com/tartuffeme/isoloir/issues/new" target="_blank" rel="noopener">{c.report}</a>
				</div>
			{/if}
			{#if result.warnings.length}
				<details class="more">
					<summary>{c.warnings} ({result.warnings.length})</summary>
					<ul>{#each result.warnings as w (w)}<li><code>{w}</code></li>{/each}</ul>
				</details>
			{/if}
		{:else if failure}
			<div class="verdict verdict--ko" role="alert">
				<h2>{c.failed}</h2>
				<p><code>{failure}</code></p>
			</div>
		{/if}

		<details class="more">
			<summary>{c.experts}</summary>
			<p>{c.expertsBody}</p>
			<pre><code>{command}</code></pre>
		</details>
	{/if}

	<p class="back"><a href={paths.vote}>← {c.back}</a></p>
</div>

<style>
	.audit {
		max-width: 40rem;
		margin: 0 auto;
		text-align: center;
	}
	.kicker {
		text-transform: uppercase;
		letter-spacing: 0.12em;
		font-size: 0.75rem;
		font-weight: 700;
		color: var(--accent-hover, var(--accent));
		margin: 0 0 0.4rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(1.7rem, 5vw, 2.4rem);
		line-height: 1.15;
		margin: 0 0 0.8rem;
	}
	.lead {
		color: var(--text-secondary);
		margin: 0 auto 1.6rem;
		max-width: 34rem;
	}
	.notice {
		padding: 1rem;
		border-radius: var(--radius-md);
		background: var(--bg-card);
		color: var(--text-secondary);
	}
	.actions {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 1.4rem;
	}
	.btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-height: 3rem;
		padding: 0 1.6rem;
		border-radius: var(--radius-full, 999px);
		font: inherit;
		font-weight: 700;
		font-size: 1rem;
		cursor: pointer;
		text-decoration: none;
	}
	.btn--primary {
		border: none;
		background: var(--accent);
		color: #fff;
		width: min(100%, 22rem);
	}
	.btn--primary:hover {
		background: var(--accent-hover);
	}
	.btn--ghost {
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-primary);
	}
	.duration {
		margin: 0;
		font-size: 0.8rem;
		color: var(--text-muted);
	}
	.bar {
		height: 6px;
		border-radius: 999px;
		background: rgba(255, 255, 255, 0.08);
		overflow: hidden;
		margin: 0 0 0.5rem;
	}
	.bar span {
		display: block;
		height: 100%;
		background: var(--accent);
		transition: width 0.3s ease;
	}
	.phase {
		font-size: 0.8rem;
		color: var(--text-muted);
		margin: 0 0 0.6rem;
	}
	.steps {
		list-style: none;
		padding: 0;
		margin: 1rem 0 1.5rem;
		display: flex;
		flex-direction: column;
		gap: 0.6rem;
		text-align: left;
	}
	.step {
		display: grid;
		grid-template-columns: 2.4rem 1fr;
		gap: 0.8rem;
		align-items: start;
		padding: 0.85rem 1rem;
		border-radius: var(--radius-md);
		background: var(--bg-card);
		border: 1px solid var(--border-card, var(--glass-border));
		transition: border-color 0.3s;
	}
	.step--run {
		border-color: color-mix(in oklch, var(--accent) 60%, transparent);
	}
	.step--ok {
		border-color: color-mix(in oklch, var(--green) 45%, transparent);
	}
	.step--fail {
		border-color: var(--red);
	}
	.icon {
		width: 2.4rem;
		height: 2.4rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: rgba(255, 255, 255, 0.06);
		color: var(--text-muted);
		font-weight: 700;
	}
	.step--ok .icon {
		background: var(--green);
		color: #0a0a0f;
		animation: pop 0.35s ease-out;
	}
	.step--fail .icon {
		background: var(--red);
		color: #fff;
	}
	.icon svg {
		width: 1.2rem;
		height: 1.2rem;
		fill: none;
		stroke: currentColor;
		stroke-width: 2.8;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.spin {
		width: 1.2rem;
		height: 1.2rem;
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
	@keyframes pop {
		from {
			transform: scale(0.6);
		}
	}
	.text {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
	}
	.what {
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
	.count {
		font-size: 0.8rem;
		font-weight: 700;
		color: var(--text-primary);
		font-variant-numeric: tabular-nums;
	}
	.verdict {
		padding: 1.4rem 1.2rem;
		border-radius: var(--radius-lg, 18px);
		margin-bottom: 1rem;
	}
	.verdict h2 {
		margin: 0.4rem 0 0.3rem;
		font-size: 1.35rem;
	}
	.verdict p {
		margin: 0;
		color: var(--text-secondary);
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
		stroke: none;
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
	.problems code,
	.more code {
		font-size: 0.78rem;
		word-break: break-word;
	}
	.more {
		margin: 0.6rem 0;
		text-align: left;
		padding: 0.7rem 1rem;
		border-radius: var(--radius-md);
		background: var(--bg-card);
	}
	.more summary {
		cursor: pointer;
		font-weight: 600;
		text-align: center;
	}
	.more pre {
		overflow-x: auto;
		padding: 0.7rem;
		border-radius: 8px;
		background: rgba(0, 0, 0, 0.35);
		font-size: 0.75rem;
	}
	.back {
		margin-top: 1.5rem;
	}
	.back a {
		color: var(--text-secondary);
	}
	@media (prefers-reduced-motion: reduce) {
		.spin,
		.seal path,
		.step--ok .icon {
			animation: none;
			stroke-dashoffset: 0;
		}
	}
</style>
