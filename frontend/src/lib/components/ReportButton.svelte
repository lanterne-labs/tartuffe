<script lang="ts">
	// Report a fact or a contradiction as wrong. Only a signed-in user
	// reports, once per target; an AI reads the report with the sources and a
	// person of the team decides. A signed-out click opens the sign-in, then
	// this dialog. A contradiction's dialog shows its two facts face to face
	// and what was found between them, and asks what is wrong: the pairing
	// (the contradiction is reported) or one of the facts (that fact is).
	import Spinner from './Spinner.svelte';
	import Modal from './Modal.svelte';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { session, signInOpen } from '$lib/auth';
	import { createReport, reported, reportKey, ReportError, MIN_REASON, MAX_REASON } from '$lib/reports';
	import type { ReportKind } from '$lib/types';

	interface Side {
		id: number;
		text: string;
		date?: string | null;
	}

	interface Props {
		kind: ReportKind;
		targetId: number;
		// preview is the reported fact's text (kind fact).
		preview?: string;
		// pair is the contradiction's two facts and the reason found
		// between them (kind contradiction).
		pair?: { a: Side; b: Side; reason?: string | null };
	}

	let { kind, targetId, preview = '', pair }: Props = $props();

	// What the user says is wrong in a contradiction: the pairing, or one
	// of its facts.
	type Choice = 'pair' | 'a' | 'b';
	let choice = $state<Choice>('pair');
	let target = $derived.by((): { kind: ReportKind; id: number } => {
		if (kind !== 'contradiction' || !pair || choice === 'pair') return { kind, id: targetId };
		return { kind: 'fact', id: choice === 'a' ? pair.a.id : pair.b.id };
	});
	let targetDone = $derived($reported.has(reportKey(target.kind, target.id)));

	function day(d?: string | null): string {
		if (!d) return '';
		return new Date(d).toLocaleDateString(currentLang === 'fr' ? 'fr-FR' : 'en-GB', { day: 'numeric', month: 'long', year: 'numeric' });
	}

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	let open = $state(false);
	let reason = $state('');
	let sending = $state(false);
	let error: string | null = $state(null);
	let sent = $state(false);
	// A signed-out click waits for the sign-in to open the dialog.
	let waitingSignIn = $state(false);

	let done = $derived($reported.has(reportKey(kind, targetId)));
	let length = $derived(reason.trim().length);

	$effect(() => {
		if (waitingSignIn && $session && !$signInOpen) {
			waitingSignIn = false;
			open = true;
		}
	});
	$effect(() => {
		// Closing the sign-in without signing in drops the wait.
		if (waitingSignIn && !$signInOpen && !$session) waitingSignIn = false;
	});

	function start() {
		error = null;
		sent = false;
		if (!$session) {
			waitingSignIn = true;
			signInOpen.set(true);
			return;
		}
		open = true;
	}

	async function submit() {
		if (length < MIN_REASON || length > MAX_REASON || sending) return;
		sending = true;
		error = null;
		try {
			await createReport(target.kind, target.id, reason.trim());
			sent = true;
			reason = '';
		} catch (err) {
			if (err instanceof ReportError) {
				if (err.code === 'already_reported') sent = true;
				else if (err.code === 'signed_out') {
					open = false;
					waitingSignIn = true;
					signInOpen.set(true);
				} else error = tr(`report.error.${err.code}`, currentLang);
			} else {
				error = err instanceof Error ? err.message : String(err);
			}
		} finally {
			sending = false;
		}
	}
</script>

<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events -->
<span class="report" onclick={(e) => e.stopPropagation()} onkeydown={(e) => e.stopPropagation()}>
	<button
		class="report-btn"
		class:done
		onclick={start}
		disabled={done}
		title={done ? tr('report.done_short', currentLang) : tr(`report.title.${kind}`, currentLang)}
		aria-label={done ? tr('report.done_short', currentLang) : tr(`report.title.${kind}`, currentLang)}
	>
		<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
			<path d="M4 15s1-1 4-1 5 2 8 2 4-1 4-1V3s-1 1-4 1-5-2-8-2-4 1-4 1z" />
			<line x1="4" y1="22" x2="4" y2="15" />
		</svg>
		{#if done}<span>{tr('report.done_short', currentLang)}</span>{/if}
	</button>
	<Modal {open} title={tr(`report.title.${kind}`, currentLang)} onclose={() => (open = false)} closeLabel={tr('auth.close', currentLang)}>
		{#if pair}
			<div class="pair">
				<div class="side">
					<span class="side-label">{tr('report.pair.fact', currentLang, { n: 1 })}{#if pair.a.date}{' · '}{day(pair.a.date)}{/if}</span>
					<p>{pair.a.text}</p>
				</div>
				<div class="versus" aria-hidden="true">⟷</div>
				<div class="side">
					<span class="side-label">{tr('report.pair.fact', currentLang, { n: 2 })}{#if pair.b.date}{' · '}{day(pair.b.date)}{/if}</span>
					<p>{pair.b.text}</p>
				</div>
				{#if pair.reason}
					<p class="found"><strong>{tr('report.pair.found', currentLang)}</strong> {pair.reason}</p>
				{/if}
			</div>
		{:else}
			<blockquote>{preview}</blockquote>
		{/if}
		{#if sent}
			<p class="ok" role="status">{tr('report.sent', currentLang)}</p>
			<p class="muted">{tr('report.sent_next', currentLang)} <a href="/profile">{tr('report.see_profile', currentLang)}</a></p>
		{:else}
			{#if pair}
				<fieldset class="choices">
					<legend class="muted">{tr('report.pair.what', currentLang)}</legend>
					<label><input type="radio" bind:group={choice} value="pair" /> {tr('report.pair.choice.pair', currentLang)}</label>
					<label><input type="radio" bind:group={choice} value="a" /> {tr('report.pair.choice.fact', currentLang, { n: 1 })}</label>
					<label><input type="radio" bind:group={choice} value="b" /> {tr('report.pair.choice.fact', currentLang, { n: 2 })}</label>
				</fieldset>
			{/if}
			<label for="reason-{kind}-{targetId}" class="muted">{tr(`report.why.${target.kind}`, currentLang)}</label>
			<textarea
				id="reason-{kind}-{targetId}"
				bind:value={reason}
				rows="5"
				maxlength={MAX_REASON}
				placeholder={tr(`report.placeholder.${target.kind}`, currentLang)}
			></textarea>
			<p class="hint">
				<span>{tr('report.hint', currentLang)}</span>
				<span class:short={length > 0 && length < MIN_REASON}>{length}/{MAX_REASON}</span>
			</p>
			{#if targetDone}<p class="muted">{tr('report.pair.already', currentLang)}</p>{/if}
			{#if error}<p class="err" role="alert">{error}</p>{/if}
		{/if}
		{#snippet actions()}
			{#if sent}
				<button class="btn btn-primary" onclick={() => (open = false)}>{tr('auth.close', currentLang)}</button>
			{:else}
				<button class="btn" onclick={() => (open = false)} disabled={sending}>{tr('vote.cancel', currentLang)}</button>
				<button class="btn btn-primary" onclick={submit} disabled={sending || targetDone || length < MIN_REASON}>
					{#if sending}<Spinner size="sm" label={tr('report.sending', currentLang)} />{:else}{tr('report.submit', currentLang)}{/if}
				</button>
			{/if}
		{/snippet}
	</Modal>
</span>

<style>
	.report {
		display: inline-flex;
	}
	.report-btn {
		display: inline-flex;
		align-items: center;
		gap: 0.3rem;
		min-width: 28px;
		height: 28px;
		padding: 0 0.35rem;
		justify-content: center;
		background: transparent;
		border: 1px solid transparent;
		border-radius: var(--radius-sm);
		color: var(--text-muted);
		font: inherit;
		font-size: 0.75rem;
		cursor: pointer;
	}
	.report-btn:hover:not(:disabled) {
		color: var(--red);
		border-color: var(--border-card);
	}
	/* A finger needs more than a mouse. */
	@media (pointer: coarse) {
		.report-btn {
			min-width: 2.5rem;
			height: 2.5rem;
		}
	}
	.report-btn.done {
		color: var(--amber);
		cursor: default;
	}
	blockquote {
		margin: 0 0 1rem;
		padding: 0.6rem 0.8rem;
		border-left: 3px solid var(--border-card-hover);
		background: var(--bg-secondary);
		border-radius: var(--radius-sm);
		font-size: 0.85rem;
		line-height: 1.5;
		color: var(--text-primary);
		white-space: pre-line;
	}
	label {
		display: block;
		text-align: left;
		margin-bottom: 0.4rem;
	}
	.pair {
		text-align: left;
		margin: 0 0 1rem;
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}
	.side {
		padding: 0.55rem 0.8rem;
		background: var(--bg-secondary);
		border-left: 3px solid var(--border-card-hover);
		border-radius: var(--radius-sm);
	}
	.side-label {
		font-size: 0.72rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		color: var(--text-muted);
	}
	.side p {
		margin: 0.2rem 0 0;
		font-size: 0.85rem;
		line-height: 1.5;
		color: var(--text-primary);
	}
	.versus {
		text-align: center;
		color: var(--red);
		font-size: 1.2rem;
		line-height: 1;
	}
	.found {
		margin: 0.35rem 0 0;
		font-size: 0.85rem;
		line-height: 1.5;
		color: var(--text-secondary);
	}
	.choices {
		text-align: left;
		border: none;
		padding: 0;
		margin: 0 0 0.9rem;
	}
	.choices legend {
		padding: 0;
		margin-bottom: 0.4rem;
	}
	.choices label {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		font-size: 0.88rem;
		color: var(--text-primary);
		cursor: pointer;
	}
	textarea {
		width: 100%;
		box-sizing: border-box;
		padding: 0.6rem;
		background: var(--bg-secondary);
		color: var(--text-primary);
		border: 1px solid var(--border-card-hover);
		border-radius: var(--radius-sm);
		font: inherit;
		font-size: 0.9rem;
		resize: vertical;
	}
	textarea:focus {
		outline: 2px solid var(--accent);
		outline-offset: 1px;
	}
	.hint {
		display: flex;
		justify-content: space-between;
		gap: 1rem;
		font-size: 0.75rem;
		color: var(--text-muted);
		margin: 0.35rem 0 0;
	}
	.short {
		color: var(--amber);
	}
	.muted {
		color: var(--text-secondary);
		font-size: 0.88rem;
		line-height: 1.5;
	}
	.ok {
		color: var(--green);
		margin: 0 0 0.5rem;
	}
	.err {
		color: var(--red);
		font-size: 0.85rem;
	}
</style>
