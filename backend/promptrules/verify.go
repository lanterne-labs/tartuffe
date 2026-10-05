package promptrules

// RefuteContradictionPrompt is the adversarial verification prompt used to
// second-guess a *claimed* contradiction before it is persisted. It is a
// deliberately STRICTER, refutation-first pass than the detection prompt: its
// job is to kill false positives, not to find new contradictions. Its NO rules
// are the shared FalsePositivePatterns catalog — the SAME catalog the detector
// and auditor use — so the insertion gate and the audit gate cannot drift.
//
// It is rendered by llm.VerifyContradiction with three %s arguments in order:
// statement A and statement B (already rendered by Statement.Render, quotes
// included) and the claimed reason. Any literal `%` MUST be written `%%` (the
// catalog itself is kept symbol-free for exactly this reason).
//
// The model MUST answer with exactly two lines, reason first:
//
//	REASON: <one sentence>
//	VERDICT: YES|NO
//
// YES means the contradiction is REAL and should be kept; NO means it is a
// false positive and must be dropped. The caller treats an unparseable or
// missing verdict as an operational error (fail-open), NOT as a NO.
var RefuteContradictionPrompt = `You are auditing a *claimed* contradiction for a politician honesty tracker. Decide whether it is a REAL contradiction (keep) or a FALSE POSITIVE (drop). Weigh both sides fairly: do not rubber-stamp a weak claim, but do not reflexively dismiss a genuine reversal either.

A REAL contradiction (answer YES) is the person's own statements or actions conflicting with each other, or with an authoritative finding about them. Recognise these real patterns — they ARE contradictions:
  - POSITION / ATTITUDE REVERSAL, even in different words: "very motivated" then "not particularly enthusiastic" about the same thing; "I support X" then "I oppose X"; "this is my priority" then "this is not desirable". Different wording for opposite stances on the same subject still counts.
  - DENIAL vs AUTHORITATIVE FINDING: the person denied wrongdoing / denied personal profit / claimed innocence, and a court conviction or official investigation conclusion in that SAME matter establishes the opposite. A criminal conviction is an authoritative finding that breaks a personal denial of wrongdoing.
  - STATED OPPOSITION / WARNING then OPPOSING ACTION: the person warned an action was harmful or opposed it, then voted for or carried out that very action.
  - COMMITMENT vs CONTRARY ACTION: a stated value, promise, or commitment by the person, broken by the person's OWN material action in the opposite direction (e.g. promised to keep retirement at 62, then abandoned the repeal that would have undone the move to 64).
  - DENOUNCE-THEN-ALLY: the person publicly blamed, denounced, attacked, or attributed fault to a SPECIFIC NAMED person or party, then formally allied / merged lists / partnered / agreed to govern with that SAME named person or party. Taking a hostile public stance toward someone and then joining forces with them IS a real reversal — do not excuse it as "different aspects" (blame vs strategy) or realpolitik. This applies only to a stance about a NAMED person/party paired with an alliance toward that SAME person/party; it does NOT apply to two policy positions or two abstract concepts that merely seem opposed.
  - SELF-EXEMPTING DOUBLE STANDARD: the person demanded a rule or sanction for a CLASS of people (e.g. "the convicted must be ineligible for life"), then entered that class, and the person's OWN later claim or act exempts them from that same rule (e.g. "no scenario can stop my candidacy" after their own conviction). The partner of the rule must be the person's own claim or act; a conviction, a ruling or a prosecutor's request alone is the trigger, not the contradiction.

` + FalsePositivePatterns + `
` + RestatementsRule + `

Decision procedure:
  - If the case clearly matches one of the NO patterns above, answer NO.
  - Otherwise, if it clearly matches one of the YES patterns (reversal, denial-vs-finding, warned-then-acted, commitment-vs-action, denounce-then-ally, self-exempting double standard), answer YES.
  - Only if it fits NEITHER and you genuinely cannot tell, answer NO.

CLAIMED CONTRADICTION UNDER REVIEW:
Statement A: %s
Statement B: %s
Reason it was flagged: "%s"

Respond with EXACTLY two lines and nothing else, in this order:
REASON: <one sentence>
VERDICT: YES or NO`
