<script lang="ts">
	// "Ask your AI": a ready prompt that has an AI read the public code and
	// answer yes or no to one claim, that the passport stays on the phone.
	// Copied, or opened in Claude or ChatGPT with the prompt filled in.
	import Modal from '$lib/components/Modal.svelte';
	import type { Lang } from '$lib/i18n';

	let { lang }: { lang: Lang } = $props();

	const N = ' ';
	const ISOLOIR = 'https://github.com/lanterne-labs/isoloir';
	const SITE = 'https://github.com/lanterne-labs/tartuffe';

	const t = {
		fr: {
			button: 'Demandez à votre IA de vérifier',
			title: 'Demandez à votre IA',
			lead: 'Collez ce texte dans l’IA de votre choix. Elle lira le code public et vous dira, preuves à l’appui, si votre passeport reste bien sur votre téléphone.',
			copy: 'Copier',
			copied: 'Copié',
			close: 'Fermer',
			prompt: `Je veux vérifier une affirmation du site tartuffe.me${N}: «${N}votre passeport ne quitte jamais votre téléphone${N}; le site ne reçoit qu’une preuve que vous êtes majeur et français, et un identifiant anonyme, publié au registre, pour qu’un passeport ne vote qu’une fois${N}».

Lis le code source public ci-dessous et dis-moi si c’est vrai, en citant les fichiers et les lignes.

1. L’app ZKPassport, qui lit la puce du passeport${N}: ${'https://github.com/zkpassport'}
   Les données de la puce sont-elles lues et traitées sur le téléphone${N}? Qu’est-ce qui en sort exactement${N}?
2. La page de vote de tartuffe.me${N}: ${SITE}/blob/main/frontend/src/lib/components/VotePassport.svelte
   Que reçoit le navigateur de l’app ZKPassport, et qu’en fait-il${N}?
3. La bibliothèque de vote isoloir${N}: ${ISOLOIR}
   Regarde js/src/session.ts (scanPassport, submitRegistration) et crates/proto/src/client.rs (register_body)${N}: que contient l’inscription envoyée${N}? Y a-t-il un nom, une photo, une date de naissance ou un numéro de passeport${N}? L’identifiant envoyé permet-il de retrouver la personne ou son bulletin${N}?
4. Le serveur qui vérifie l’inscription${N}: ${ISOLOIR}/blob/main/crates/server/src/eligibility.rs et ${ISOLOIR}/blob/main/zkverifier/verify.mjs
   Que reçoit-il et que vérifie-t-il${N}?

Réponds d’abord par OUI ou NON à la question${N}: «${N}Le nom, la photo, la date de naissance et le numéro du passeport restent-ils sur le téléphone${N}?», puis explique, et signale tout ce qui pourrait contredire l’affirmation.`
		},
		en: {
			button: 'Ask your AI to check',
			title: 'Ask your AI',
			lead: 'Paste this into the AI of your choice. It will read the public code and tell you, with evidence, whether your passport really stays on your phone.',
			copy: 'Copy',
			copied: 'Copied',
			close: 'Close',
			prompt: `I want to check a claim of the site tartuffe.me: “your passport never leaves your phone; the site only receives a proof that you are an adult French national, and an anonymous identifier, published in the register, so that a passport votes once”.

Read the public source code below and tell me whether it is true, citing the files and lines.

1. The ZKPassport app, which reads the passport chip: https://github.com/zkpassport
   Is the chip's data read and processed on the phone? What exactly leaves it?
2. The tartuffe.me vote page: ${SITE}/blob/main/frontend/src/lib/components/VotePassport.svelte
   What does the browser receive from the ZKPassport app, and what does it do with it?
3. The isoloir voting library: ${ISOLOIR}
   Look at js/src/session.ts (scanPassport, submitRegistration) and crates/proto/src/client.rs (register_body): what does the registration sent contain? Is there a name, a photo, a date of birth or a passport number? Can the identifier sent lead back to the person or to their ballot?
4. The server that checks the registration: ${ISOLOIR}/blob/main/crates/server/src/eligibility.rs and ${ISOLOIR}/blob/main/zkverifier/verify.mjs
   What does it receive and what does it check?

First answer YES or NO to the question: “Do the passport's name, photo, date of birth and number stay on the phone?”, then explain, and point out anything that could contradict the claim.`
		}
	};
	let c = $derived(t[lang]);

	let open = $state(false);
	let copied = $state(false);
	async function copy() {
		try {
			await navigator.clipboard.writeText(c.prompt);
			copied = true;
			setTimeout(() => (copied = false), 2000);
		} catch (err) {
			console.error('copy failed', err);
		}
	}
	let claude = $derived(`https://claude.ai/new?q=${encodeURIComponent(c.prompt)}`);
	let chatgpt = $derived(`https://chatgpt.com/?q=${encodeURIComponent(c.prompt)}`);
</script>

<button class="ask" onclick={() => (open = true)}>
	<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 3l1.8 4.7L18.5 9.5l-4.7 1.8L12 16l-1.8-4.7L5.5 9.5l4.7-1.8z" /><path d="M18.5 15l.9 2.1 2.1.9-2.1.9-.9 2.1-.9-2.1-2.1-.9 2.1-.9z" /></svg>
	{c.button}
</button>

<Modal {open} title={c.title} onclose={() => (open = false)} closeLabel={c.close}>
	<p class="lead">{c.lead}</p>
	<pre class="prompt">{c.prompt}</pre>
	<div class="actions">
		<button class="btn btn-primary" onclick={copy}>{copied ? c.copied : c.copy}</button>
		<a class="btn" href={claude} target="_blank" rel="noopener">Claude ↗</a>
		<a class="btn" href={chatgpt} target="_blank" rel="noopener">ChatGPT ↗</a>
	</div>
</Modal>

<style>
	.ask {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.55rem 1rem;
		border-radius: var(--radius-full, 999px);
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-primary);
		font: inherit;
		font-weight: 600;
		font-size: 0.9rem;
		cursor: pointer;
	}
	.ask:hover {
		border-color: var(--accent-hover, var(--accent));
	}
	.ask svg {
		width: 1.1rem;
		height: 1.1rem;
		fill: none;
		stroke: var(--accent-hover, var(--accent));
		stroke-width: 1.6;
		stroke-linejoin: round;
	}
	.lead {
		margin: 0 0 0.8rem;
		color: var(--text-secondary);
		text-align: center;
	}
	.prompt {
		max-height: 16rem;
		overflow: auto;
		margin: 0 0 1rem;
		padding: 0.8rem;
		border-radius: var(--radius-md);
		background: rgba(0, 0, 0, 0.35);
		font-family: inherit;
		font-size: 0.8rem;
		line-height: 1.45;
		white-space: pre-wrap;
		text-align: left;
	}
	.actions {
		display: flex;
		justify-content: center;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
</style>
