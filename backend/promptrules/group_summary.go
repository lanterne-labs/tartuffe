package promptrules

// GroupSummaryPrompt synthesizes a single merged "one inconsistency" sentence for
// a contradiction GROUP: one base claim contradicted by N facts. The model is given
// the base claim and the list of contradicting facts (each with the reason it
// conflicts), and must produce ONE concise sentence capturing the single underlying
// inconsistency — not a per-fact list (the UI already shows the facts individually).
//
// It returns strict JSON so the caller can parse both the native-language summary
// and an English version without brittle line parsing:
//
//	{"summary": "...", "summary_en": "..."}
//
// `summary` MUST be in the same language as the base claim; `summary_en` is its
// English rendering (identical to `summary` when the base is already English).
//
// Format args (in order): base claim text, then the pre-formatted bullet list of
// contradicting facts.
const GroupSummaryPrompt = `You are summarizing a single factual inconsistency for a political fact-checking UI.

A person made this BASE claim or commitment:
"%s"

The following facts by or about the same person conflict with that base claim:
%s

Write ONE short sentence (max 30 words) stating the single underlying inconsistency between the base claim and these facts. Do NOT enumerate each fact — the UI already lists them. Focus on what the contradiction *is*, neutrally and factually.

Respond with STRICT JSON only, no markdown, in exactly this shape:
{"summary": "<one sentence, SAME language as the base claim>", "summary_en": "<the same sentence in English>"}`
