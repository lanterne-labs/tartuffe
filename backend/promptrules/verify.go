package promptrules

// RefuteContradictionPrompt is the adversarial verification prompt used to
// second-guess a *claimed* contradiction before it is persisted. It is a
// deliberately STRICTER, refutation-first pass than the detection prompt: its
// job is to kill false positives, not to find new contradictions. It encodes
// the four false-positive buckets observed in production (numeric/measurement
// mismatches, different procedural scopes, conditional-met logic inversions,
// and same-direction claims) as hard NO rules.
//
// It is rendered by llm.VerifyContradiction with three %s arguments in order:
// factA, factB, claimedReason. Any literal `%` MUST be written `%%`.
//
// The model MUST answer with exactly two lines:
//
//	VERDICT: YES|NO
//	REASON: <one sentence>
//
// YES means the contradiction is REAL and should be kept; NO means it is a
// false positive and must be dropped. The caller treats an unparseable or
// missing verdict as an operational error (fail-open), NOT as a NO.
const RefuteContradictionPrompt = `You are auditing a *claimed* contradiction for a politician honesty tracker. Decide whether it is a REAL contradiction (keep) or a FALSE POSITIVE (drop). Weigh both sides fairly: do not rubber-stamp a weak claim, but do not reflexively dismiss a genuine reversal either.

A REAL contradiction (answer YES) is the person's own statements or actions conflicting with each other, or with an authoritative finding about them. Recognise these real patterns — they ARE contradictions:
  - POSITION / ATTITUDE REVERSAL, even in different words: "very motivated" then "not particularly enthusiastic" about the same thing; "I support X" then "I oppose X"; "this is my priority" then "this is not desirable". Different wording for opposite stances on the same subject still counts.
  - DENIAL vs AUTHORITATIVE FINDING: the person denied wrongdoing / denied personal profit / claimed innocence, and a court conviction or official investigation conclusion in that SAME matter establishes the opposite. A criminal conviction is an authoritative finding that breaks a personal denial of wrongdoing.
  - STATED OPPOSITION / WARNING then OPPOSING ACTION: the person warned an action was harmful or opposed it, then voted for or carried out that very action.
  - COMMITMENT vs CONTRARY ACTION: a stated value, promise, or commitment by the person, broken by the person's OWN material action in the opposite direction (e.g. promised to keep retirement at 62, then abandoned the repeal that would have undone the move to 64).

It is NOT a contradiction (answer NO) when ANY of these false-positive patterns apply — these OVERRIDE a YES:
  1. NUMBERS / MEASUREMENTS: different measurements, counts, percentages, vote shares, poll figures, or snapshots of the same quantity at different times, sources, or electoral rounds (e.g. "22.49%% at the municipal election" vs "17.77%% in the first round"; "6.2%%" vs "4.981%%"). Different metrics or rounds are not lies.
  2. FORECAST vs CURRENT: one is a projection/forecast about the future and the other is a current poll or regional figure (e.g. "projected strongest in the 2029 federal election" vs "currently at 41%% in Saxony-Anhalt").
  3. PROCEDURAL SCOPE: different procedural scopes or components of the SAME action (e.g. "49-3 on the revenue part" vs "49-3 on the expenditure part" vs "49-3 to adopt the overall budget"). Normal procedure is not inconsistency.
  4. CONDITION LATER MET: one fact is a CONDITIONAL statement ("I would do X IF Y happens") and the other shows the condition Y came true or X happening — that is CONSISTENT. Do NOT invert the logic into a contradiction.
  5. SAME DIRECTION: both facts take the SAME side / point the SAME way, even in different words (e.g. "pledged to reset relations with Africa" vs "claims to have ended la Françafrique"). Same-direction claims cannot contradict.
  6. DIFFERENT SUBJECTS / TOPICS / TIMES: the facts are about different events, different people, different geographic scopes, or unrelated topics; or both are third-party narratives with no first-person claim and no authoritative finding.

Decision procedure:
  - If the case clearly matches one of the NO patterns (1-6), answer NO.
  - Otherwise, if it clearly matches one of the YES patterns (reversal, denial-vs-finding, warned-then-acted, commitment-vs-action), answer YES.
  - Only if it fits NEITHER and you genuinely cannot tell, answer NO.

CLAIMED CONTRADICTION UNDER REVIEW:
Statement A: "%s"
Statement B: "%s"
Reason it was flagged: "%s"

Respond with EXACTLY two lines and nothing else:
VERDICT: YES or NO
REASON: <one sentence>`
