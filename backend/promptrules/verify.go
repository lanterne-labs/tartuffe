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
  - SHORT-TERM POSITION REVERSAL: the person's own OPINION on the same question flips within about THREE MONTHS (days, weeks, one campaign), in different words or not ("I support X" then "I oppose X"; "very motivated" then "not particularly enthusiastic"), and the person does not acknowledge or explain the change. A change of opinion over a longer time, or one the person explains ("j'ai changé d'avis parce que...", "les résultats depuis 2015 m'ont convaincu"), is an EVOLUTION, not a contradiction: everybody may change their mind. The gap does not excuse an ACT, a PROMISE or a DENIAL: those are judged by the patterns below whatever the gap. "There will never be an alliance with X" is a PLEDGE: a later call to unite with X, a joint rally or list with X, or an alliance with X breaks it at any gap, whatever form the union takes. The three-month limit never covers an opinion the person changes once a rule or sanction applies to THEMSELF ("ineligibility is a democratic problem" said after their own conviction, against their earlier demand for it): that is the self-exempting double standard below, at any gap.
  - DENYING ONE'S OWN EARLIER POSITION: the person refused, said or claimed X, and later says they never refused, never said or never claimed it ("refused the idea of a primary" then "never said no to a primary on principle"). Claiming X then denying X is a contradiction, even when the later statement adds nuance. Asserting a thesis on a subject, then LATER saying "it is not my subject, I have not studied it" to avoid answering for it, is the same pattern: YES. The ORDER matters: a disclaimer dated BEFORE the thesis is not a denial of it.
  - STATED-CONDITION WALK-BACK: "I will not do X if Y happens" (or "I will not do X under Y"), Y happened, and the person announces, keeps or pursues X anyway ("I will not campaign with an electronic bracelet", then a candidacy announced after a sentence to a bracelet). Do not ask for proof that the sanction is being carried out at that moment.
  - DENIAL vs AUTHORITATIVE FINDING: the person denied wrongdoing / denied personal profit / claimed innocence, and a court conviction or official investigation conclusion in that SAME matter establishes the opposite. A criminal conviction is an authoritative finding that breaks a personal denial of wrongdoing. An appeal or a cassation appeal lodged "to establish her innocence" after the conviction is such a claim of innocence: YES against the conviction.
  - STATED OPPOSITION / WARNING then OPPOSING ACTION: the person warned an action was harmful or opposed it, then voted for or carried out that very action.
  - COMMITMENT vs CONTRARY ACTION: a stated value, promise, or commitment by the person, broken by the person's OWN material action in the opposite direction (e.g. promised to keep retirement at 62, then abandoned the repeal that would have undone the move to 64).
  - DENOUNCE-THEN-ALLY: the person publicly blamed, denounced, attacked, or attributed fault to a SPECIFIC NAMED person or party, then formally allied / merged lists / partnered / agreed to govern with that SAME named person or party. Taking a hostile public stance toward someone and then joining forces with them IS a real reversal — do not excuse it as "different aspects" (blame vs strategy) or realpolitik. This applies only to a stance about a NAMED person/party paired with an alliance toward that SAME person/party; it does NOT apply to two policy positions or two abstract concepts that merely seem opposed.
  - VOTE AGAINST ONE'S OWN DEMAND: the person demanded a specific measure, then voted against a text or an amendment whose content is that same measure (e.g. demanded a referendum before any new EU treaty, then voted against the amendment that requires that referendum). The amendment exemption [relative_vote_preference] never covers this case.
  - SELF-EXEMPTING DOUBLE STANDARD: the person demanded a rule or sanction for a CLASS of people (e.g. "the convicted must be ineligible for life"), then entered that class, and the person's OWN later claim or act exempts them from that same rule (e.g. "no scenario can stop my candidacy" after their own conviction). The partner of the rule must be the person's own claim or act; a conviction, a ruling or a prosecutor's request alone is the trigger, not the contradiction.

` + FalsePositivePatterns + `
` + RestatementsRule + `

**DATES:** each statement is preceded by "(dated YYYY-MM-DD)" when its date is known, "(undated)" else. Use them: a position and its opposite on the SAME question, both dated, is a reversal whatever the order; a yearly text (the budget, the PLF) at dates about a year apart is two texts; a decision deferred to after an event and the decision taken after it is the announced sequence; with an undated statement, be skeptical of the ORDER of a reversal, but do not invent another object: the same named text, office or election in both statements is the same one unless a date or a word says otherwise ("will vote for the military programming law" and "abstained on the military programming law" are about one law). The prefix is the date of the SOURCE; when a statement's own text dates the event ("en octobre 2025", "in 2015", "il y a un an"), the event's date is the one in the text. A broken promise runs from the promise to a LATER act; a denial of the past ("I have never...") is broken only by an EARLIER act. A reason that says "puis", "désormais", "now" with the statements in the wrong order describes a reversal that did not happen in that direction: re-check it with the dates.

The reason it was flagged is a hint from another model, not evidence: judge the two statements as written. If the reason relies on words, facts or events that NEITHER statement contains, it may describe another pair: answer NO unless the two statements contradict each other on their own. Before a YES, find in Statement A and in Statement B the two positions the reason names, quoted or clearly paraphrased; a reason that attributes to a statement a position, a commitment or a party it does not carry ("Philippe included", "he firmly ruled it out", "choose LR") is not supported by the pair: NO. Context the reason recalls about the person (their conviction, their office, an election that took place) is not such an addition: weigh it as context.

Decision procedure:
  - If the case clearly matches one of the NO patterns above, answer NO.
  - Otherwise, if it clearly matches one of the YES patterns (a short-term unexplained opinion reversal, denial of one's own position, stated-condition walk-back, denial-vs-finding, warned-then-acted, commitment-vs-action, denounce-then-ally, vote against one's own demand, self-exempting double standard), answer YES.
  - Only if it fits NEITHER and you genuinely cannot tell, answer NO. A mere "tension" between two statements that can both be true as written is a NO.

CLAIMED CONTRADICTION UNDER REVIEW:
Statement A: %s
Statement B: %s
Reason it was flagged: "%s"

Respond with EXACTLY two lines and nothing else, in this order:
REASON: <one sentence>
VERDICT: YES or NO`
