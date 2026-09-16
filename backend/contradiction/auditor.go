package contradiction

import (
	"context"
	"crypto/sha256"
	"digital-mirror/backend/llm"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// contradictionAuditAllowedRulesList is the closed set of RULE tokens the
// contradiction-auditor prompt asks the model to return, alphabetised after
// "none" so the prompt and the strict-retry quote a stable, ordered list.
// Anything outside this set is a parse error that triggers the judge's
// corrective retry (see llm.VerdictJudge); no fuzzy matching, because that
// would silently hide cases where the LLM is not actually reasoning under our
// enum. The last seven buckets came from the shared
// promptrules.FalsePositivePatterns catalog (the verifier's) and must be
// nameable by the auditor too.
var contradictionAuditAllowedRulesList = []string{
	"none",
	"accusation_not_finding",
	"announcement_then_action",
	"coherent_defense",
	"complementary_facts",
	"condition_later_met",
	"consistent_ignorance",
	"conviction_vs_acknowledgment",
	"date_only_no_commitment",
	"different_events",
	"different_subjects",
	"endorsement_vs_non_candidacy",
	"forecast_vs_current",
	"geographic_scope",
	"learning_over_time",
	"legal_process_stages",
	"numbers_measurements",
	"paraphrase_same_position",
	"procedural_scope",
	"same_direction",
	"subjective_vs_objective",
	"symbolic_counter_gesture",
	"third_party_narrative",
	"vague_vs_specific",
}

// contradictionAuditUnlistedRules are catalog buckets the auditor accepts as
// a RULE although the prompt's token list above never advertised them. Moving
// one into contradictionAuditAllowedRulesList changes the prompt bytes and the
// AuditInputHash, which re-opens every pinned KEEP verdict; do it deliberately.
var contradictionAuditUnlistedRules = []string{
	"relative_vote_preference",
}

// AuditVerdict is the auditor's judgement on an existing contradiction record.
type AuditVerdict string

const (
	VerdictKeep      AuditVerdict = "keep"      // contradiction is solid, leave it in DB
	VerdictRemove    AuditVerdict = "remove"    // false positive, should be deleted
	VerdictAmbiguous AuditVerdict = "ambiguous" // unclear, manual review needed
)

// AuditResult is the structured outcome of auditing one stored contradiction.
type AuditResult struct {
	Verdict   AuditVerdict
	Reasoning string // 1-2 sentence justification
	Rule      string // which rule applied (e.g. "accusation_not_finding"), or "none"
	RawResp   string // full LLM response for inspection
	Err       error
}

// auditorKeptExamples lists the worked examples retained in the auditor's
// prompt, by their header prefix in ContradictionRulesAndExamples. The full
// body pushes the audit prompt to ~10K words, and that bloat measurably
// degrades judgment: the same local model judges FP_177/TP_114-shaped cases
// correctly under the ~1.5K-word verifier prompt and wrongly under the full
// audit prompt (CHANGES.md 2026-07-17). The false-positive knowledge is
// already carried by the embedded FalsePositivePatterns catalog, so the
// auditor keeps ONE example per decision family. The batch detector keeps the
// full example set — its raw-pair task is the one the examples were written
// for, and its prompt is not the one misjudging.
var auditorKeptExamples = []string{
	// keep side (real contradictions the auditor must never evict)
	"PAIR 2 (",  // denial after claim
	"PAIR 9 (",  // court finding vs denial
	"PAIR 30 (", // values-based / directional axis
	"PAIR 33 (", // denounce-then-ally (same named target)
	"PAIR 34 (", // self-exempting double standard (own rule, own class)
	"PAIR 35 (", // commitment walk-back that looks like same_direction
	"PAIR 36 (", // amendment vote breaking the person's own demanded measure (keep)
	// remove side (false-positive families)
	"PAIR 1 (",  // learning over time
	"PAIR 8 (",  // trial/accusation is not a finding
	"PAIR 15 (", // conviction vs acknowledgment of imperfections
	"PAIR 18 (", // same position in different words (double negatives)
	"PAIR 25 (", // announcement then action (fulfilled commitment)
	"PAIR 27 (", // third-party narratives only
	"PAIR 28 (", // date-only conflict, no commitment
}

// leanAuditRules returns ContradictionRulesAndExamples with the worked-example
// tail filtered down to auditorKeptExamples. Derived from the single shared
// source at init so the rule text itself can never drift from the detector's;
// only the example SELECTION differs. Falls back to the full body if the
// EXAMPLES marker ever moves.
func leanAuditRules() string {
	const marker = "**EXAMPLES:**"
	i := strings.Index(ContradictionRulesAndExamples, marker)
	if i < 0 {
		return ContradictionRulesAndExamples
	}
	head := ContradictionRulesAndExamples[:i+len(marker)]
	blocks := strings.Split(ContradictionRulesAndExamples[i+len(marker):], "\n\n")
	kept := make([]string, 0, len(auditorKeptExamples))
	for _, b := range blocks {
		t := strings.TrimSpace(b)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "PAIR ") {
			// Non-example chunk inside the tail (none today) — keep, fail-safe.
			kept = append(kept, t)
			continue
		}
		for _, want := range auditorKeptExamples {
			if strings.HasPrefix(t, want) {
				kept = append(kept, t)
				break
			}
		}
	}
	return head + "\n\n" + strings.Join(kept, "\n\n")
}

// auditPromptTpl is the audit prompt. The %s is replaced by the audit block
// (the two facts and the original detection reason). The rule body is shared
// with the detector via ContradictionRulesAndExamples so audit and detection
// cannot drift in their definition of a contradiction — the auditor just sees
// a curated subset of the worked examples (see auditorKeptExamples).
var auditPromptTpl = `You are auditing previously-flagged contradictions to remove false positives.

An earlier automated pass marked the two statements below as contradictory, with the reason given. Re-evaluate STRICTLY using the rules and examples below — would you flag this today?

Be CONSERVATIVE: when in doubt, return "ambiguous" rather than "keep". Only return "keep" when the contradiction is unambiguous under the rules. Return "remove" when the rules clearly indicate the pair should NOT have been flagged (false positive).

**STEP 0 — RESOLVE BEFORE COMPARING.** Before applying any rule, rewrite EACH statement as a plain positive stance on a concrete outcome, unwinding negations and nested clauses: "disagrees with the view that climate is NOT covered" → FOR climate being covered; "regrets that circulars are not followed" → FOR enforcing circulars; "rules out TOTAL free transport" → AGAINST total free transport (says nothing about partial). When a statement names a number or threshold, the resolved stance MUST carry that exact number: "promised to keep the retirement age at 62" → FOR age = 62; "renounced repealing the reform that set it to 64" → accepts age = 64. Two different numbers on one axis are OPPOSITE outcomes (62 ≠ 64: leaving earlier vs later), and "maintain" means the number the person stated, never "whatever the status quo later became". Resolve vocabulary too: "imprescriptibilité" / "non-prescription" means NO statute of limitations, so defending imprescriptibilité and saying that prescription "protects perpetrators" are the SAME stance. Resolve WHO a rule binds: a sanction the person demanded for a CLASS ("the convicted should be ineligible") binds the person themselves once they are in that class — the detection reason may establish membership — and the shared subject is then that sanction, not "her candidacy" vs "a general rule". That binding only bites when the OTHER statement is the person's OWN act or claim exempting themselves (running, staying in office, "nothing can bar me"). A prosecutor's réquisition, an indictment, a court's sentence, or anyone else's demand is a THIRD PARTY's act — it is not her exempting herself and is never a self-exempting double standard on its own: remove. Compare the two RESOLVED stances. If they point the same way, or concern different outcomes (partial vs total, one group vs everyone), there is no reversal — do not let a negation or a dramatic verb ("écarté", "renoncé", "désaccord") stand in for an actual opposite position.

**SELF-CHECK BEFORE RETURNING "remove"** — re-read your own reasoning; if it reproduces one of these forbidden excuses, the verdict is "keep":
- a named-target blame "n'est pas incompatible" with a later alliance with that same target → that IS the DENOUNCE-THEN-ALLY contradiction;
- the person's own class-rule "traite un sujet différent" from their self-claim while the surrounding facts place them inside that very class → that IS the SELF-EXEMPTING DOUBLE STANDARD contradiction;
- a promise pinned to one NUMBER and an act settling on a DIFFERENT number called "the same position/direction" (promised 62, accepts 64) → if your resolved stances carry two different numbers on one axis, they are OPPOSITE outcomes and that IS the COMMITMENT WALK-BACK contradiction.

**NEVER REMOVE A GENUINE CONTRADICTION.** Check these REAL-contradiction patterns FIRST — if the pair matches one, the verdict is "keep", NOT "remove", even when it superficially resembles a false-positive bucket below:
- POSITION / ATTITUDE REVERSAL: the person stated, announced, or pleaded FOR X, then stated, announced, or did the OPPOSITE about the same subject — e.g. "I will run" then "I will not run"; "I won't be a candidate" then leading/joining a winning list at that election; "very motivated" then "not enthusiastic"; pleads FOR a primary then is AGAINST one. This is a reversal — do NOT file it under announcement_then_action, different_events, vague_vs_specific, same_direction, or different_subjects. Before using same_direction, map EACH statement to the concrete direction it pushes a shared policy axis: proposing to ABOLISH any legal retirement age (lets people leave earlier/freely) vs wanting to HARDEN a reform that raises the age (makes people leave later) are OPPOSITE directions on the same axis — a reversal, even though both criticize the status quo.
- COMMITMENT vs CONTRARY ACTION: a promise, value, or commitment broken by the person's own opposing action (e.g. promised to keep the retirement age at 62, then abandoned the repeal that would have undone the move to 64).
- DENIAL vs AUTHORITATIVE FINDING: a personal denial contradicted by a conviction or official investigation conclusion in the same matter.
- DENOUNCE-THEN-ALLY: publicly blaming or attacking a SPECIFIC NAMED person or party, then allying, merging lists, or partnering with that SAME target. The shared subject is that NAMED person, so this is never different_subjects: do NOT excuse it with "blame and alliance are different topics" or "attributing responsibility to him is not logically incompatible with a later alliance with him".
- SELF-EXEMPTING DOUBLE STANDARD: the person demanded a rule or sanction for a CLASS of people ("anyone convicted should be ineligible / must resign / be barred / be punished"), then — once they themselves demonstrably fall into that class (e.g. they were themselves convicted) — exempt themselves from that same sanction by their own action or claim (running for or keeping the office). Their class-membership may be established by the context in the detection reason. Do NOT file this under different_subjects, vague_vs_specific, or complementary_facts, and do NOT excuse it with "a general normative position is not a personal commitment" — the shared subject is that ONE sanction and they are inside the very class they named, so the rule binds them. Example (verdict keep, RULE none): "il faudrait rendre inéligibles à vie ceux qui étaient condamnés" (2013) vs her own claim to be a candidate while she is herself convicted.
Reported speech of the person ("X plaide / affirme / a dit / a annoncé Y") is the person's OWN position or claim — it is NOT third_party_narrative.

` + leanAuditRules() + `

**CONTRADICTION TO AUDIT:**
%s

**AUDIT RESPONSE FORMAT:**
Respond with EXACTLY three lines, in this order:

REASONING: [1-2 sentences identifying the core claims and which rule(s) apply]
RULE: [exactly one token, no spaces, no quotes — see closed list below]
VERDICT: [keep|remove|ambiguous]

⚠️ STRICT FORMATTING — the RULE field MUST be EXACTLY ONE of these tokens (lowercase snake_case, no extra words, no parentheses, no rule numbers):
%s

If VERDICT is "keep", RULE MUST be "none". Otherwise return EXACTLY one token from the list above. Do NOT invent variants. Your verdict MUST follow logically from your reasoning. If your reasoning concludes the pair is compatible/supplementary/paraphrastic, your verdict MUST be "remove".

**LANGUAGE RULE:** Write your REASONING in the same language as the statements being audited. If statements span multiple languages, write in English.`

// OpenAIContradictionAuditor calls an OpenAI-compatible chat API to audit a
// single recorded contradiction. Unlike the detector it does not batch — audits
// are run ad-hoc by a human and verdict independence is more important than
// throughput.
type OpenAIContradictionAuditor struct {
	judge *llm.VerdictJudge
}

// NewOpenAIContradictionAuditorFromEnv builds an auditor from the same env
// vars as the detector: OPEN_AI_KEY, OPEN_AI_API, OPEN_AI_CHAT_MODEL.
func NewOpenAIContradictionAuditorFromEnv() (*OpenAIContradictionAuditor, error) {
	client, err := llm.NewClientFromEnv("contradiction-auditor")
	if err != nil {
		return nil, err
	}
	return &OpenAIContradictionAuditor{judge: &llm.VerdictJudge{
		Client:        client,
		Name:          "Contradiction audit",
		Verdicts:      []string{string(VerdictKeep), string(VerdictRemove), string(VerdictAmbiguous)},
		Rules:         contradictionAuditAllowedRulesList,
		UnlistedRules: contradictionAuditUnlistedRules,
		// 0.0 (greedy): the audit verdict gates PERMANENT row deletion, so
		// sampling noise is not acceptable — the same pair must always get the
		// same verdict. Was 0.1, which made borderline keeps flap run-to-run
		// (mirror-bii: Vuibert 0/3 vs 2/3 removes across identical inputs).
		Temperature: 0.0,
		// 3-line response (REASONING/RULE/VERDICT) is ~150 tokens. 1024 leaves
		// headroom and keeps prompt+reservation well under any 8k ctx.
		MaxTokens: 1024,
	}}, nil
}

// AuditInputHash returns a stable hash of EVERYTHING that determines an audit
// verdict: the full audit prompt (template + embedded rulebook + rule tokens)
// and the per-row inputs (fact texts, dates, detection reason). A KEEP verdict
// pinned under this hash stays valid until one of those inputs changes — the
// audit step uses it to skip re-judging unchanged rows, so a verdict can never
// flip between runs on sampling or server noise. Editing the prompt/catalog or
// repointing a fact changes the hash and re-opens the row.
func AuditInputHash(factA, factB string, dateA, dateB *time.Time, originalReason string) string {
	h := sha256.New()
	for _, part := range []string{
		auditPromptTpl,
		strings.Join(contradictionAuditAllowedRulesList, "|"),
		factA, factB,
		formatDateForLLM(dateA), formatDateForLLM(dateB),
		originalReason,
	} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Audit judges whether a recorded contradiction should be evicted. It takes
// the verdict by majority over JudgeVotes samples — the judge is not
// deterministic even at temperature 0 (see JudgeVotes), and an eviction is
// destructive, so a single unlucky sample must not delete a real contradiction.
func (a *OpenAIContradictionAuditor) Audit(ctx context.Context, factA, factB string, dateA, dateB *time.Time, originalReason string) AuditResult {
	res, err := llm.MajorityVote(AuditVotes,
		func() (AuditResult, error) {
			r := a.auditOnce(ctx, factA, factB, dateA, dateB, originalReason)
			return r, r.Err
		},
		func(r AuditResult) string { return string(r.Verdict) },
	)
	if err != nil && res.Err == nil {
		res.Err = err
	}
	return res
}

func (a *OpenAIContradictionAuditor) auditOnce(ctx context.Context, factA, factB string, dateA, dateB *time.Time, originalReason string) AuditResult {
	if factA == factB {
		return AuditResult{
			Verdict:   VerdictRemove,
			Reasoning: "Both statements are textually identical; an identical pair cannot be a contradiction.",
			Rule:      "paraphrase_same_position",
		}
	}

	auditBlock := fmt.Sprintf(
		"Statement A (dated %s): %q\nStatement B (dated %s): %q\nOriginal detection reason: %q",
		formatDateForLLM(dateA), factA,
		formatDateForLLM(dateB), factB,
		originalReason,
	)
	// The allowed-token list is generated from the single-source enum slice so
	// the prompt can never drift from what the judge accepts.
	ruleTokens := "  " + strings.Join(contradictionAuditAllowedRulesList, "\n  ")
	prompt := fmt.Sprintf(auditPromptTpl, auditBlock, ruleTokens)

	v, err := a.judge.Judge(ctx, prompt)
	return AuditResult{
		Verdict:   AuditVerdict(v.Verdict),
		Reasoning: v.Reasoning,
		Rule:      v.Rule,
		RawResp:   v.RawResp,
		Err:       err,
	}
}
