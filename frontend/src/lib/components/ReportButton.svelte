<script lang="ts">
	// Report a fact or a contradiction as wrong. Only a signed-in user
	// reports, once per target; an AI re-examines the report with the sources
	// and removes the item when the report is right (the worker, within
	// minutes). A signed-out click opens the sign-in, then this dialog.
	import Modal from './Modal.svelte';
	import { lang, tr, type Lang } from '$lib/i18n';
	import { session, signInOpen } from '$lib/auth';
	import { createReport, reported, reportKey, ReportError, MIN_REASON, MAX_REASON } from '$lib/reports';
	import type { ReportKind } from '$lib/types';

	interface Props {
		kind: ReportKind;
		targetId: number;
		// preview is the reported text, shown in the dialog.
		preview: string;
	}

	let { kind, targetId, preview }: Props = $props();

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
		if (waitingSignIn && $session) {
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
			await createReport(kind, targetId, reason.trim());
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
		<blockquote>{preview}</blockquote>
		{#if sent}
			<p class="ok" role="status">{tr('report.sent', currentLang)}</p>
			<p class="muted">{tr('report.sent_next', currentLang)} <a href="/profile">{tr('report.see_profile', currentLang)}</a></p>
		{:else}
			<label for="reason-{kind}-{targetId}" class="muted">{tr(`report.why.${kind}`, currentLang)}</label>
			<textarea
				id="reason-{kind}-{targetId}"
				bind:value={reason}
				rows="5"
				maxlength={MAX_REASON}
				placeholder={tr(`report.placeholder.${kind}`, currentLang)}
			></textarea>
			<p class="hint">
				<span>{tr('report.hint', currentLang)}</span>
				<span class:short={length > 0 && length < MIN_REASON}>{length}/{MAX_REASON}</span>
			</p>
			{#if error}<p class="err" role="alert">{error}</p>{/if}
		{/if}
		{#snippet actions()}
			{#if sent}
				<button class="btn btn-primary" onclick={() => (open = false)}>{tr('auth.close', currentLang)}</button>
			{:else}
				<button class="btn" onclick={() => (open = false)} disabled={sending}>{tr('vote.cancel', currentLang)}</button>
				<button class="btn btn-primary" onclick={submit} disabled={sending || length < MIN_REASON}>
					{sending ? tr('report.sending', currentLang) : tr('report.submit', currentLang)}
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
		margin-bottom: 0.4rem;
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
