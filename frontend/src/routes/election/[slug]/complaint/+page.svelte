<script lang="ts">
	// A complaint (isoloir SPEC §11.3), judged in the reader's browser. The
	// complaint is the voter's own copy: the relays keep none, so it comes in
	// the link's fragment (#c=…, which no server ever receives), a pasted
	// text or a saved file. It holds the ack our server signed and the body
	// it answered; the library judges it against the public copy of the log,
	// with the log key pinned in this app. Nothing comes from our server.
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { lang, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';
	import { decodeComplaint, encodeComplaint, judgeComplaint, type ComplaintVerdict, type NostrEvent } from '@isoloir/web';
	import { core, mirrorSources, settingsProblem, LOG_KEY } from '$lib/vote/isoloir';
	import { verifiers } from '$lib/vote/audit';
	import { messageOf } from '$lib/vote/errors';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let paths = $derived(eventPaths($page.params.slug ?? ''));

	const content = {
		fr: {
			title: 'Une réclamation',
			lead: 'Un votant montre la confirmation que notre serveur a signée, avec ce qu’il avait envoyé. Votre navigateur refait la vérification sur la copie publique du registre : rien n’est à croire sur parole.',
			paste: 'Collez ici le lien ou le texte de la réclamation, ou ouvrez le fichier enregistré.',
			file: 'Ouvrir un fichier',
			judge: 'Vérifier',
			loading: 'Vérification…',
			replay: (a: number, b: number) => `Relecture du registre · ${a}/${b}`,
			nosetup: 'Ce vote n’est pas encore configuré sur ce site.',
			unreadable: 'Ce texte n’est pas une réclamation lisible.',
			error: 'La vérification a échoué. Réessayez.',
			retry: 'Réessayer',
			other: 'Vérifier une autre réclamation',
			what: (k: string) => (k === 'ballot' ? 'bulletin' : 'inscription'),
			dropped: {
				holds: (w: string, n: number) => `Notre serveur a confirmé ce ${w} pour la signature n° ${n} du registre, mais ne l’y a pas inscrit : la réclamation est fondée.`,
				late: (w: string, n: number, m: number) => `Notre serveur a confirmé ce ${w} pour la signature n° ${n}, mais ne l’a inscrit qu’à la n° ${m}, en retard : la réclamation est fondée.`,
				fails: (w: string, n: number, seq: number) => `Le ${w} figure dans le registre (signature n° ${n}, position ${seq}) : la réclamation n’est pas fondée.`,
				pending: (n: number) => `La copie publique ne va pas encore jusqu’à la signature n° ${n}. Réessayez dans quelques minutes.`
			},
			refusal: {
				holds: (w: string) => `Notre serveur a refusé ce ${w}, mais votre navigateur l’accepte en rejouant le registre à ce moment-là : le refus était injustifié.`,
				fails: (w: string) => `Votre navigateur refuse lui aussi ce ${w} : le refus était justifié.`,
				uncertain: 'Le verdict dépend de la seconde exacte du refus : impossible de trancher.',
				unchecked: 'Une preuve n’a pas pu être vérifiée dans votre navigateur. Réessayez.'
			},
			invalid: 'Ce n’est pas une réclamation valable pour ce vote : la confirmation n’est pas signée par notre serveur, ou le texte a été modifié.',
			verified: 'Vérifié dans votre navigateur : la signature de la confirmation, celle du registre, et son contenu.',
			details: 'Détails techniques',
			copy: 'Copier le lien de la réclamation',
			copied: 'Lien copié',
			save: 'Enregistrer le fichier',
			how: 'Comment ça marche'
		},
		en: {
			title: 'A complaint',
			lead: 'A voter shows the confirmation our server signed, with what they had sent. Your browser checks it again against the public copy of the log: nothing is to be taken on trust.',
			paste: 'Paste the complaint’s link or text here, or open the saved file.',
			file: 'Open a file',
			judge: 'Check',
			loading: 'Checking…',
			replay: (a: number, b: number) => `Replaying the log · ${a}/${b}`,
			nosetup: 'This vote is not set up on this site yet.',
			unreadable: 'This text is not a readable complaint.',
			error: 'The check failed. Try again.',
			retry: 'Try again',
			other: 'Check another complaint',
			what: (k: string) => (k === 'ballot' ? 'ballot' : 'registration'),
			dropped: {
				holds: (w: string, n: number) => `Our server confirmed this ${w} for signature no. ${n} of the log, but did not log it: the complaint holds.`,
				late: (w: string, n: number, m: number) => `Our server confirmed this ${w} for signature no. ${n}, but logged it only at no. ${m}, late: the complaint holds.`,
				fails: (w: string, n: number, seq: number) => `The ${w} is in the log (signature no. ${n}, position ${seq}): the complaint does not hold.`,
				pending: (n: number) => `The public copy does not reach signature no. ${n} yet. Try again in a few minutes.`
			},
			refusal: {
				holds: (w: string) => `Our server refused this ${w}, but your browser accepts it, replaying the log at that time: the refusal was wrong.`,
				fails: (w: string) => `Your browser refuses this ${w} too: the refusal was right.`,
				uncertain: 'The verdict depends on the exact second of the refusal: it cannot be settled.',
				unchecked: 'A proof could not be checked in your browser. Try again.'
			},
			invalid: 'This is not a valid complaint for this vote: the confirmation is not signed by our server, or the text was changed.',
			verified: 'Checked in your browser: the confirmation’s signature, the log’s, and its contents.',
			details: 'Technical details',
			copy: 'Copy the complaint’s link',
			copied: 'Link copied',
			save: 'Save the file',
			how: 'How it works'
		}
	};
	let c = $derived(content[currentLang]);

	type View = { kind: 'input' } | { kind: 'loading' } | { kind: 'nosetup' } | { kind: 'unreadable'; detail: string } | { kind: 'error'; detail: string } | { kind: 'done'; v: ComplaintVerdict };
	let view = $state<View>({ kind: 'input' });
	let pasted = $state('');
	let event = $state<NostrEvent | null>(null);
	let progress = $state<{ checkpoint: number; checkpoints: number } | null>(null);
	let copied = $state(false);

	async function judge(text: string) {
		progress = null;
		copied = false;
		if (settingsProblem($page.params.slug ?? '')) {
			view = { kind: 'nosetup' };
			return;
		}
		let ev: NostrEvent;
		try {
			ev = decodeComplaint(text.trim());
		} catch (e) {
			view = { kind: 'unreadable', detail: messageOf(e) };
			return;
		}
		event = ev;
		view = { kind: 'loading' };
		try {
			const v = await judgeComplaint(await core(), ev, {
				mirror: mirrorSources(),
				verifierKey: LOG_KEY,
				...(await verifiers()),
				allowDev: import.meta.env.VITE_VOTE_SIMULATE_PASSPORT === '1',
				onProgress: (p) => (progress = p)
			});
			view = { kind: 'done', v };
		} catch (e) {
			console.error('complaint', e);
			view = { kind: 'error', detail: messageOf(e) };
		}
	}

	async function openFile(e: Event) {
		const f = (e.currentTarget as HTMLInputElement).files?.[0];
		if (f) await judge(await f.text());
	}

	function sentence(v: ComplaintVerdict): { text: string; tone: 'ok' | 'bad' | 'none' } {
		if (v.verdict === 'invalid' || !v.claim) return { text: c.invalid, tone: 'none' };
		const w = c.what(v.claim.kind);
		const n = v.claim.checkpoint ?? 0;
		if (v.claim.claim === 'dropped') {
			if (v.verdict === 'holds') return { text: v.found?.late ? c.dropped.late(w, n, v.found.checkpoint) : c.dropped.holds(w, n), tone: 'bad' };
			if (v.verdict === 'fails') return { text: c.dropped.fails(w, v.found?.checkpoint ?? n, v.found?.seq ?? 0), tone: 'ok' };
			return { text: c.dropped.pending(n), tone: 'none' };
		}
		if (v.verdict === 'holds') return { text: c.refusal.holds(w), tone: 'bad' };
		if (v.verdict === 'fails') return { text: c.refusal.fails(w), tone: 'ok' };
		if (v.verdict === 'uncertain') return { text: c.refusal.uncertain, tone: 'none' };
		return { text: c.refusal.unchecked, tone: 'none' };
	}

	let link = $derived(event ? `${window.location.origin}${window.location.pathname}#c=${encodeComplaint(event)}` : '');
	let fileUrl = $derived(event ? URL.createObjectURL(new Blob([JSON.stringify(event)], { type: 'application/json' })) : '');

	async function copy() {
		try {
			await navigator.clipboard.writeText(link);
			copied = true;
		} catch (e) {
			console.warn('complaint: copy', e);
		}
	}

	function fromHash() {
		const h = window.location.hash;
		if (h.startsWith('#c=')) void judge(h);
	}

	onMount(() => {
		fromHash();
		window.addEventListener('hashchange', fromHash);
		return () => window.removeEventListener('hashchange', fromHash);
	});
</script>

<svelte:head>
	<title>{c.title}</title>
</svelte:head>

<article class="complaint">
	<h1>{c.title}</h1>
	<p class="lead">{c.lead}</p>

	{#if view.kind === 'input' || view.kind === 'unreadable'}
		<label class="muted" for="complaint-text">{c.paste}</label>
		<textarea id="complaint-text" rows="4" bind:value={pasted}></textarea>
		{#if view.kind === 'unreadable'}
			<p class="verdict verdict--none" role="alert">{c.unreadable}</p>
		{/if}
		<p class="actions">
			<button class="btn btn--primary" disabled={!pasted.trim()} onclick={() => judge(pasted)}>{c.judge}</button>
			<label class="btn btn--ghost file">{c.file}<input type="file" accept=".json,application/json,text/plain" onchange={openFile} /></label>
		</p>
	{:else if view.kind === 'loading'}
		<p class="muted" aria-live="polite">{progress ? c.replay(progress.checkpoint, progress.checkpoints) : c.loading}</p>
	{:else if view.kind === 'nosetup'}
		<p class="verdict verdict--none">{c.nosetup}</p>
	{:else if view.kind === 'error'}
		<p class="verdict verdict--none" role="alert">{c.error}</p>
		<details class="tech"><summary>{c.details}</summary><p>{view.detail}</p></details>
		<button class="btn" onclick={() => event && judge(encodeComplaint(event))}>{c.retry}</button>
	{:else}
		{@const s = sentence(view.v)}
		<p class="verdict verdict--{s.tone}">{s.text}</p>
		{#if view.v.verdict === 'pending' || view.v.verdict === 'unchecked'}
			<button class="btn" onclick={() => event && judge(encodeComplaint(event))}>{c.retry}</button>
		{/if}
		{#if view.v.verdict !== 'invalid'}
			<p class="muted">{c.verified}</p>
		{/if}
		<details class="tech"><summary>{c.details}</summary><p>{view.v.detail}</p></details>
		{#if event && view.v.verdict !== 'invalid'}
			<p class="actions">
				<button class="btn btn--ghost" onclick={copy}>{copied ? c.copied : c.copy}</button>
				<a class="btn btn--ghost" href={fileUrl} download="reclamation.json">{c.save}</a>
			</p>
		{/if}
	{/if}

	<p class="links">
		{#if view.kind !== 'input'}<a href={paths.complaint} onclick={() => ((view = { kind: 'input' }), (event = null))}>{c.other}</a> · {/if}<a href={paths.how}>{c.how}</a>
	</p>
</article>

<style>
	.complaint {
		max-width: 40rem;
		margin: 0 auto;
		padding: 1.5rem 1rem 3rem;
	}
	h1 {
		font-family: var(--font-display);
		font-size: clamp(1.7rem, 5vw, 2.3rem);
		line-height: 1.15;
		margin: 0.5rem 0;
	}
	/* The site's dark field, at 1rem: under 16px iOS zooms into a field on focus. */
	textarea {
		width: 100%;
		box-sizing: border-box;
		margin: 0.5rem 0;
		padding: 0.7rem 0.8rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-md);
		background: var(--bg-secondary);
		color: var(--text-primary);
		font-family: var(--font-mono, monospace);
		font-size: 1rem;
		line-height: 1.4;
		resize: vertical;
	}
	textarea:focus {
		outline: none;
		border-color: var(--accent);
	}
	.actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.6rem;
	}
	.file input {
		display: none;
	}
	.tech {
		margin: 0 0 0.9rem;
		font-size: 0.78rem;
		color: var(--text-muted);
	}
	.tech summary {
		cursor: pointer;
	}
	.tech p {
		margin: 0.3rem 0 0;
		font-family: var(--font-mono, monospace);
		overflow-wrap: anywhere;
	}
	.lead {
		color: var(--text-secondary);
	}
	.verdict {
		padding: 0.9rem 1rem;
		border-radius: 0.6rem;
		border: 1px solid var(--glass-border);
		font-weight: 600;
	}
	.verdict--ok {
		border-color: #2e7d4f;
	}
	.verdict--bad {
		border-color: #c0392b;
	}
	.verdict--none {
		opacity: 0.8;
	}
	.links {
		margin-top: 2rem;
	}
	/* The links are a finger's target: the padding brings them to 40px. */
	.links a {
		display: inline-block;
		padding: 0.6rem 0.4rem;
	}
</style>
