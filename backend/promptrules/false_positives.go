package promptrules

import _ "embed"

// FalsePositivePatterns is the single canonical catalog of the false-positive
// buckets a contradiction can fall into. It is the ONE source of truth shared by
// every LLM judge that decides "is this a real contradiction?":
//   - the detector and the auditor, via ContradictionRulesAndExamples
//   - the insertion/reverify verifier, via RefuteContradictionPrompt
//
// Edit false_positives.txt (plain prose) to add or refine a bucket; all three
// prompts pick it up. Each bucket is tagged with a rule token in [brackets] that
// MUST stay in sync with the auditor's closed enum (contradictionAuditAllowedRules);
// the verifier ignores the tokens and reads only the descriptions.
//
// IMPORTANT: the catalog is concatenated into fmt.Sprintf templates, so it must
// contain NO literal '%' (write "percentages", not the symbol) — otherwise the
// format call mis-parses it. Keep it symbol-free.
//
//go:embed false_positives.txt
var FalsePositivePatterns string
