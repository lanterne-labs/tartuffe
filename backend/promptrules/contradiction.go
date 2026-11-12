// Package promptrules holds prompt fragments shared across packages that would
// otherwise create import cycles. Keep this package dependency-free (standard
// library only) — it exports prompt text and the pure helpers that render it.
package promptrules

// ContradictionRules is the rule body of the STANCE ANALYZE judge — the
// per-fact analyze-vs-stance prompt in backend/core/stance_manager.go, the
// only consumer. (The batch detector and the auditor embed their own
// ContradictionRulesAndExamples in backend/contradiction/openai_detector.go;
// what all judges DO share is promptrules.FalsePositivePatterns.)
//
// Because the analyze judge is the one that CREATES contradiction edges,
// changes here are tested by TestAnalyzeGolden
// (backend/core/testdata/analyze_cases.yaml) — and, usefully, tuning this
// body cannot regress the verifier or auditor prompts.
//
// Format note: the prompt is consumed by fmt.Sprintf in some call sites, so
// any literal `%` MUST appear as `%%`.
const ContradictionRules = `**WHAT COUNTS AS A CONTRADICTION**
A contradiction is about the SAME SUBJECT — the person's own knowledge, beliefs, statements, commitments, or actions conflicting with each other (or with an authoritative finding about them).

**PROMISE-BASED REASONING (core framework):**
For every pair, identify which fact is the "promise" (a claim, denial, commitment, stated condition, stated objective, or position) and which is the potential "breaker". Either fact can be the promise — it is NOT necessarily the chronologically first one.

A breaker invalidates a promise when:
- The person's OWN later or earlier statement directly contradicts their promise (e.g., "I knew X" vs "I never knew X")
- The person's OWN ACTION contradicts their stated commitment, position, or condition
- An AUTHORITATIVE FINDING (conviction, court ruling, official investigation conclusion) contradicts a denial or claim the person makes AFTER it

A breaker does NOT invalidate a promise when:
- An ACCUSATION, CHARGE, INDICTMENT, TRIAL or open INVESTIGATION (procedural steps) is paired with a denial — these are NOT findings of guilt
- An EXTERNAL PARTY'S action is the alleged breaker — only the person's own statements/actions or authoritative findings about them count
- The alleged breaker is a vote on an AMENDMENT or SOUS-AMENDEMENT: that ballot judges the modification only, in context. It does not imply support for the unamended text, nor opposition to the parent bill (rejecting a rival group's dilatory amendment to a bill the person champions is consistent with championing it), nor endorsement of the modification's "opposite". This exemption is about AMENDMENT VOTES ONLY and removes nothing else: a vote on the text itself (l'ensemble, an article, a motion) still breaks a contrary position, and an amendment vote still breaks a promise when the amendment's content is precisely the measure the person demanded.

**POSITIVE PATTERNS — these ARE contradictions** (each is a flavor of the person's own statements/actions conflicting). When in doubt between a positive pattern and a negative one, FLAG the contradiction — the user prefers a sharper signal over a quiet one:
- STATED-CONDITION WALK-BACK: "I will not do X if Y happens" + later they act/talk as if X is happening despite Y. The breaker doesn't need to mention the original condition explicitly — if the condition has clearly occurred (e.g., the sentence was handed down) and the person now claims they will still do X, that IS the walk-back. "If my sentence is confirmed on appeal" is met by an appeal that upholds the conviction, even with a lighter sentence; only an acquittal, or a condition naming one specific penalty the appeal did not apply, leaves it unmet.
- STATED-OBJECTIVE vs STATED-ACTION: The person claims a goal (e.g. "X is not our objective") and simultaneously claims an action whose stated effect IS X. The action negates the stated objective.
- BEHAVIORAL INCONSISTENCY: A public stated position by the SAME PERSON vs an action (by that same person) that runs counter to that position. **The stated position does NOT need a quantified target, a deadline, or a specific mechanism** — broad value statements like "I defend X", "X is sacred to me", "je soutiens Y", "je m'engage à protéger Z", "je veux reconstruire W", "mon attachement à X" all qualify as stated positions the person can be held to. If the directional axis is unambiguous AND the action moves materially against the stated position on that same axis, FLAG YES — do not wait for the promise side to be quantified. Apply this rule to: (a) PROMISED + opposing ACTION, (b) CLAIMED-position + opposing ACTION, (c) two same-person ACTIONS that point opposite directions on the same axis (e.g. "voted for tariffs protecting French jobs" + "authorized offshoring French factory" — both are actions, both bear on the same axis, opposite directions = contradiction). The rule does NOT apply when: (a) the alleged breaker is third-party action (only the person's own actions count), (b) the contexts are genuinely different and legitimately allow different choices (e.g. wartime vs peacetime, geographic scopes), or (c) the action is symbolic/de-minimis and does not materially undermine the stated position.
- SHORT-TERM POSITION SHIFT ON THE SAME ISSUE: a clear rejection / refusal / negative framing of an issue in one statement, paired with endorsement / openness / support for the SAME issue in another, the two at most about THREE MONTHS apart and the change neither acknowledged nor explained by the person. The wording need not match — "not my priority / not desirable" → "I'm not against / I support" within weeks IS a walk-back. An opinion that changes over a longer time, or that the person explains ("j'ai évolué", "les résultats des nationalistes depuis 2015 m'ont convaincu"), is an EVOLUTION of opinion: NOT a contradiction. This limit is for OPINIONS only: an ACT against stated words, a broken PROMISE or PLEDGE, a stated-condition walk-back and a DENIAL of one's own earlier position are contradictions whatever the gap. "Never an alliance with X" is a pledge: a later call to unite with X or a joint rally with X breaks it. An opinion changed once a rule or sanction applies to the person themself (their own ineligibility) is the self-exempting double standard, at any gap.
- CLAIM ↔ AUTHORITATIVE FINDING: The person denied / claimed X, and a court ruling / official investigation conclusion found the opposite specifically about X. Worked example: key fact "condamnée en appel pour détournement de fonds publics" + new fact "a affirmé son innocence après sa condamnation" → CONTRADICTION, flag the conviction fact. A denial arriving AFTER the finding is not "additional information about her defense" — it is the person's claim against the authoritative finding, the exact pattern this rule exists for. The conviction of the key fact can be an earlier stage of the same case (first instance) than the one the denial follows (appeal): it is the same matter, so flag it. An appeal, or the person's contest of the rulings in the stance summary, never excuses the denial. The ORDER decides: the denial must be made after a conviction in the case (the day of the ruling counts). A denial made BEFORE any conviction (to the investigators, at the trial, while the verdict is awaited) is a defendant pleading not guilty: the conviction that comes later does NOT make it a contradiction, and a denial whose date and text do not place it after a conviction is not flagged.
- COMMITMENT vs CARVE-OUT: A blanket commitment in one statement (e.g. "I will campaign with X regardless of any outcome") combined with a separately stated condition that would prevent that very commitment (e.g. "I refuse to do the very activity that campaigning would require") IS a contradiction even when each statement individually sounds reasonable. The carve-out must exclude the very activity committed to: a condition on the person's own CANDIDACY ("I will not be a candidate under a bracelet") does not prevent CAMPAIGNING with a named running mate whatever the verdict (campaigning for the ticket does not require being its candidate) — that pair is compatible, NOT this pattern.
- SELF-EXEMPTING DOUBLE STANDARD (hypocrisy): The person publicly demanded a RULE or SANCTION for a CLASS of people (e.g. "anyone convicted should be ineligible for life / must resign / should be barred / should be punished"), and the person's OWN later action or claim exempts THEMSELVES from that very rule once they demonstrably belong to that class (e.g. they were themselves convicted of that offense yet run for or keep the office). Holding others to a standard the person then refuses to apply to themselves IS a real contradiction. The fact that the person NOW falls into the targeted class may be established by the surrounding context or an authoritative finding (their own conviction) — the contradiction edge is still between the person's stated rule and their own self-exempting action. Requires ALL of: (a) a clear normative standard the person THEMSELVES stated (not merely attributed to their party, not a vague value); (b) the person clearly falls into the class the standard targets; (c) the self-exemption concerns the SAME sanction on the SAME axis. Do NOT stretch this to loose "he's a hypocrite" claims where any of (a)-(c) is missing.
  MECHANICAL CHECK (apply before emitting a self-exemption edge, IN BOTH DIRECTIONS — it does not matter whether the sanction is the NEW FACT or a key fact): a self-exemption edge pairs the person's stated RULE with the person's OWN utterance or act taking the exemption (CLAIMED / ANNOUNCED / PROMISED / DEMANDED / PROPOSED — "je suis éligible", "j'annonce ma candidature"). If EITHER side of the candidate pair is the sanction or legal process itself — a fact tagged WAS_CONVICTED or WAS_SENTENCED, a conviction, sentence, court or appeal ruling, a prosecutor's (le parquet) requisition, or a verdict narration — do NOT emit the edge: the sanction is the class-membership TRIGGER, not the breaker. A person being sanctioned is their own rule being APPLIED — if your reasoning describes the pair as the rule being applied, enforced, or fulfilled on them, that is AGREEMENT and the verdict for that pair is NEW_INFO, never CONTRADICTION.
  CRITICAL — WHAT THE OTHER FACT MUST BE: the rule can ONLY be paired with the person's OWN self-exempting CLAIM or ACT (e.g. "je suis éligible", "rien ne peut m'empêcher d'être candidate", "j'annonce ma candidature"). The class-membership trigger (their conviction) is CONTEXT, never the contradiction partner. Answer NO when the other fact is merely: the conviction / sentence itself, a court or appeal decision, an ineligibility ruling, a prosecutor's request, an awaiting-verdict or "the outcome will determine" narration, or any third-party account of the proceedings. "She demanded the convicted be barred" + "she was convicted" is NOT a contradiction — being sanctioned is exactly what her own rule prescribes, so it AGREES with it. The contradiction appears ONLY when she then claims or takes the exemption for herself.

**NEGATIVE PATTERNS — these are NOT contradictions** (apply NARROWLY — these rules block real contradictions when over-applied):
- DIFFERENT SUBJECTS: the two statements are about genuinely different actors, events, bills, or geographic regions. A YEARLY text named without its year (the budget, the PLF, the PLFSS) at dates about a year apart is two bills: "we will vote against this budget" (October 2025, the 2026 budget) and "amend the budget rather than block it" (October 2026, the 2027 budget) are positions on two texts, not a reversal. So are elections years apart, an amendment or an exemption named without an identifier in two successive finance texts, two meetings with different conveners, and an alliance or a primary for one election or perimeter (the presidential, a primary of the whole left with LFI) against one for another (the legislatives, the closed PS primary). A statement bounding ONE bill in preparation ("this bill will not go beyond what was voted") binds that bill, not a programme years later in another role. Also different subjects: a JUDGMENT of a leader ("Mélenchon est le boulet de la gauche") and a call to bring the left together including his party (only a stated refusal of that cooperation, or a formal alliance after denouncing the same target, is a reversal); a vote for a TARGET and a later pledge to abolish the TOOL named as its means (renovation goals backed by energy-saving certificates); two statements that assign an act to OPPOSITE actors ("no ambush was set against him" and "the other group set a trap": one story); two budget AGGREGATES (the State deficit and the public deficit, euros and share of GDP), which can move in opposite directions. Also: an article voted at two readings when its wording changed (two versions of the text); several outlets' figures of one presentation on one day (one plan read three ways); an assessment of a situation at its date ("les finances sont tenues", 2024) against a plan years later. Same person + same topic + different aspects is NOT this rule — it's just same subject with two facets, which CAN contradict.
- VAGUE vs SPECIFIC: one statement is generic, the other names specific people/dates/items, and there is no clear indication they refer to the same thing. (If context proves they refer to the same thing, it IS a contradiction — see PAIR 14.)
- SUBJECTIVE AWARENESS vs OBJECTIVE FACT: "I wasn't aware of X happened" + "X did/didn't happen as fact" are compatible — denying X happened doesn't imply prior knowledge. THIS RULE COVERS ONLY past-awareness vs past-event. It does NOT excuse policy shifts, position shifts, or stated-condition walk-backs.
- ADMINISTRATIVE ERROR vs CRIMINAL INTENT: admitting "things could have been more rigorous" / "an administrative error was made" / "I acted in good faith despite negligence" does NOT contradict denying a specific criminal charge. Only triggers when one statement admits an administrative shortcoming and the other denies CRIMINAL intent on the same matter — does NOT excuse softening of stated political positions.
- COHERENT DEFENSE — strict version: two statements both deny the SAME SPECIFIC ALLEGATION (e.g. "I didn't order the fictitious hires" + "I didn't approve the fictitious hires") REINFORCE each other. Two statements that deny DIFFERENT specific allegations within the same case are also coherent. This rule does NOT cover statements about future conduct, candidacy, or political commitments — those can contradict freely.
- CONSISTENT IGNORANCE: multiple "I didn't know X" / "I didn't know Y" claims about different facts of the same situation reinforce each other. Only fires when both statements are EXPLICIT claims of ignorance.
- SAME POSITION IN DIFFERENT WORDS: paraphrases / translations of the same claim cannot contradict each other. Note: paraphrase requires that they make the same assertion — not just that they're about the same topic.
- COMPLEMENTARY FACTS / SAME EVENT: different aspects of the same ruling, sentence, or event (prison + ineligibility, first-instance + appeal sentencing) are supplementary, not contradictory. This ALSO covers two COMPATIBLE EXPLANATIONS or CAUSES of the same problem that can both hold at once — e.g. "the failure is revealing of our bad organization" + "the failure is NOT due to a lack of resources" AGREE (it is an organizational, not a budgetary, cause) and REINFORCE each other. Naming one cause while ruling out a different, non-exclusive cause is not a contradiction. Only flag when the two explanations are genuinely mutually exclusive (cannot both be true).
- LEGAL PROCESS STAGES: accusation → trial → conviction is sequential progression of the same case, not a contradiction (between THOSE stages — the person's own denial made after a conviction still contradicts it; a denial made before the first conviction is a plea, not a contradiction).
- CONVICTION SCOPE: a general conviction does not automatically disprove every specific factual claim the person made about details of the case — only those the conviction specifically addresses.
- BLAME FRAMING vs ACT: a claim about who is to blame in a conflict ("I have no problem with the media, the media have a problem with me") is not contradicted by the speaker's own hostile act within that conflict (excluding those outlets): the act fits the blame. Do not infer an attitude from an act and set it against the words; an act contradicts words only when the words state, promise or deny that act or a practice it breaks.
- PLEDGE KEPT, WORDING REGRETTED: a person who still honours a pledge and only says it should have been worded otherwise ("I should have said the median wage, not the minimum wage; but I committed, so I do it") has not walked it back. NO.
- SELF-POSITIONING: « ne pas se situer vis-à-vis de X » says how the person defines themself, not what they think of X: a judgement of X is no denial of it.
- SELF-CORRECTION: a factual claim the person corrects within days, saying so (« nulle part » then « vu, page 70 »), is a correction, not a reversal.
- WHOSE PROPOSAL: « ce n'est pas une position que nous défendons » said of a measure the person still wants implemented marks it as another's proposal, not a refusal of it.
- DEADLINE MOVED: a deadline the person moves themself before it passed, with no act and no denial (« fin de l'année » then « fin mai » days later), is a pledge replaced, not broken, until the term passes unkept.
- PLEA BEFORE THE VERDICT: a defence plea made during the trial (« bonne foi », « pas de système ») is a stage before the judgement, not a claim against it; only a claim made after the conviction contradicts it.
- DISOWNED CLAIM (a contradiction): the person's own words that explicitly withdraw the basis of a claim they made on the same subject: a disclaimer of knowledge after precise claims ("it's not my subject, I haven't studied it" after deportation figures), an admission that a figure was invented or a judged text was not read, a claim said to be made for effect. The withdrawal must be stated by the person; never infer it from an act.
- REPORTED POSITION: a journalist's plain factual report of what the person asks for, proposes or did is the person's position, not the journalist's opinion. The journalist's judgement, motive or interpretation of the person is not.
- ANNOUNCEMENT THEN ACTION: announcing X and then doing X is a FULFILLED commitment, the OPPOSITE of a contradiction.
- QUALIFIED DENIAL: a denial hedged by a qualifier ("je n'ai jamais dit non PAR PRINCIPE à la primaire") is not contradicted by a practical objection ("une usine à gaz") to ONE FORM of the thing: principle and form are different scopes. And a reason must find each of its two horns IN its fact: two facts blaming the same cause (lower receipts) are one position, however the reason words it. A decision DEFERRED to after an event ("I will wait for the speech before deciding") and the decision taken once the event has passed is the announced sequence, whatever the decision. Likewise "no other solution than X" followed by X plus a second measure Y toward the same goal adds a lever, it does not drop X: not a reversal unless X is abandoned.

**TEMPORAL REASONING:**
1. Earlier "didn't know" → Later "knew/claimed" = NOT contradiction (learned over time)
2. Earlier "claimed X" → Later "denied X" = IS contradiction (cannot unknow)
3. Same date conflicts: analyze carefully; the two must be incompatible, not two different claims
3b. ORDER: a past-tense denial ("I have never proposed X") is contradicted only by an act or statement dated BEFORE it; a pledge about future conduct ("I will never criticise Z") only by an act dated AFTER it. A date written in the fact's text ("en octobre 2025", "en 2015") is the event's date, whatever the line's "(dated ...)" prefix, which is the source's.
3c. A present state of mind with no pledge ("je n'y pense pas", "ce n'est pas d'actualité") commits to nothing: a decision a year later is a change of mind, not a contradiction. A pledge to a single candidacy with a named rival of the same camp ("we will not run against each other") is compatible with campaigning until one steps aside. Seeking allies to REACH a condition the person set ("no Matignon without an absolute majority", then "if a few deputies are missing I will reach out") pursues it; only taking the office without it breaks it. A pledge of loyalty to a leader or a party is not broken by an argument or an ultimatum inside that camp, only by leaving, running against, voting down or disowning them. A pledge to carry out a measure is not broken when it is suspended or postponed under an event outside the person's choice (a snap election called by another, a censure, a court ruling): only abandoning or opposing it breaks it. A private remark the press reports (« selon l'Opinion », « aurait confié ») set against the person's own public words is the press's word against theirs: NO. A pledge to rally behind or not to hinder a primary's winner binds only once someone else wins it: running « primaire ou pas », or withholding support without obstructing, is NO. A call for a collective act (« on doit tous quitter X ») not followed alone when the others did not move is NO. A pledge narrowed (a smaller version) or postponed (a later date) before any act, keeping its direction, is a revised programme, NO; renouncing it or stating its opposite stays YES. Declining to argue about X now is not opposing X. Opposing a bill, then voting the text rewritten in CMP and saying why, is two versions, NO. A class rule (« pas de condamnation pour être parlementaire ») binds the office it names and the person themself, not an ally's candidacy to another office.
4. Unknown dates: be SKEPTICAL, only flag if completely irreconcilable regardless of temporal ordering
5. Both dates known, opposite OPINIONS on the SAME question: a contradiction ONLY when they are at most about three months apart and the person does not explain the change; further apart, or explained, it is an evolution of opinion (NEW_INFO). An ACT (vote, decision, alliance, candidacy) against stated words, a broken PROMISE or pledge and a DENIAL of the earlier position stay contradictions at any gap. It is not a reversal either when the two answer different questions, concern different events or texts, or one is a forecast.

**EXAMPLES (positive = YES contradiction, negative = NO):**

PAIR 1 (Learning Over Time - NO):
Statement A (dated January 15, 2024): "Marine Le Pen said she didn't know about the mutualization scheme."
Statement B (dated March 20, 2024): "Marine Le Pen claimed the EU Parliament knew about mutualization and it was normal practice."
PAIR 1: REASONING: She denied knowledge in January, then discussed mutualization as normal in March. Earlier "didn't know" followed by later familiarity is the LEARNING OVER TIME temporal rule — not a contradiction.
PAIR 1: NO: The later statement reflects knowledge gained after the earlier denial.

PAIR 2 (Denial After Claim - YES):
Statement A (dated January 15, 2024): "Marine Le Pen stated she was fully aware of the mutualization scheme and that it was standard practice."
Statement B (dated March 20, 2024): "Marine Le Pen claimed she never knew about any mutualization scheme."
PAIR 2: REASONING: She claimed full awareness in January, then denied any knowledge in March. Earlier "claimed X" followed by later "denied X" IS a contradiction — you cannot unknow.
PAIR 2: YES: She claimed personal knowledge of the mutualization on Jan 15, then denied that knowledge on Mar 20.

PAIR 3 (Stated-Condition Walk-Back - YES):
Statement A (dated October 15, 2025): "Marine Le Pen a affirmé qu'elle ne se présenterait pas à l'élection présidentielle de 2027 si elle était condamnée à porter un bracelet électronique."
Statement B (dated November 5, 2025): "Marine Le Pen a déclaré qu'elle n'avait pas renoncé à sa candidature pour l'élection présidentielle de 2027, malgré sa condamnation à un bracelet électronique en première instance."
PAIR 3: REASONING: She stated the explicit condition "won't run if I get a bracelet". The bracelet sentence was then handed down (Statement B's "malgré sa condamnation à un bracelet"). She is now asserting she has NOT given up her candidacy. The stated condition was met but the stated outcome was abandoned — STATED-CONDITION WALK-BACK.
PAIR 3: YES: She set the bracelet sentence as the trigger to drop her candidacy and is now keeping the candidacy after the bracelet sentence was handed down.

PAIR 4 (Stated-Objective vs Stated-Action - YES):
Statement A (dated June 20, 2025): "Donald Trump claimed regime change in Iran was not the US goal."
Statement B (dated June 22, 2025): "Donald Trump claimed the objectives for Operation Epic Fury were to systematically dismantle the Iranian regime's ability to threaten America or project power outside its borders."
PAIR 4: REASONING: He stated the OBJECTIVE "regime change is not our goal", and simultaneously stated the ACTION's objective is to "systematically dismantle the regime's ability to project power outside its borders". Dismantling a regime's projection ability is regime change in substance — the stated action negates the stated objective. STATED-OBJECTIVE vs STATED-ACTION.
PAIR 4: YES: Stating regime change is not the goal while stating the operation aims to systematically dismantle the regime's power projection contradicts the stated objective.

PAIR 5 (Same-Concrete-Proposal Position Shift - YES):
Statement A (dated April 1, 2026): "Jordan Bardella a déclaré que la taxation des superprofits n'était pas sa priorité, estimant que créer de nouvelles taxes dans un pays avec 46%% de prélèvements obligatoires n'était pas souhaitable."
Statement B (dated May 8, 2026): "Jordan Bardella n'est pas contre l'idée d'une taxation des superprofits liés à la crise."
PAIR 5: REASONING: Both statements address the SAME concrete proposal — taxing superprofits. In A he frames it as not desirable / not a priority; in B he is "not against" it. Same identifiable measure, explicit rejection then openness — SAME-CONCRETE-PROPOSAL POSITION SHIFT.
PAIR 5: YES: Same concrete proposal (superprofits tax) — moved from "not desirable" to "not against".

PAIR 5b (Commitment vs Carve-Out - YES):
Statement A (dated October 15, 2025): "Marine Le Pen a refusé d'envisager de mener campagne en portant un bracelet électronique."
Statement B (dated November 8, 2025): "Marine Le Pen a assuré qu'elle ferait campagne avec Jordan Bardella quel que soit le résultat de son jugement en appel."
Context: Le Pen has been sentenced in first instance to wearing an electronic bracelet — the very condition she said would prevent her campaigning.
PAIR 5b: REASONING: A is a stated CARVE-OUT — she will not campaign under bracelet conditions. B is a blanket COMMITMENT — she will campaign with Bardella regardless of the appeal's outcome. The two statements are individually reasonable but jointly inconsistent: campaigning with Bardella (B) requires campaigning at all (which A excludes given the first-instance bracelet). COMMITMENT vs CARVE-OUT.
PAIR 5b: YES: Blanket "I will campaign regardless" combined with separately stated refusal to campaign under the actual conditions she now faces.

PAIR 5c (Candidacy condition vs Campaign pledge - NO):
Statement A (dated July 10, 2026): "Marine Le Pen avait indiqué ne pas vouloir se présenter à l'élection présidentielle de 2027 sous bracelet électronique."
Statement B (dated June 25, 2025): "Marine Le Pen a promis de mener la campagne présidentielle avec Jordan Bardella, quel que soit le résultat de l'appel."
PAIR 5c: REASONING: Unlike 5b, A is a condition on her own CANDIDACY (not running under a bracelet), not a refusal to campaign. Campaigning for the ticket with Bardella does not require being its candidate, so B does not need what A excludes. Not COMMITMENT vs CARVE-OUT.
PAIR 5c: NO: A condition on her candidacy and a pledge to campaign with a running mate are compatible.

PAIR 7 (Unknown Dates - NO):
Statement A (dated unknown date): "CEO Jones supported policy X."
Statement B (dated unknown date): "CEO Jones opposed policy X."
PAIR 7: REASONING: Both dates unknown. UNKNOWN DATES rule: the order is unknown, so this cannot be read as a reversal. With both dates known, the same pair would be a POSITION REVERSAL (YES).
PAIR 7: NO: Without dates, the order of the two positions is unknown.

PAIR 8 (Different Events - NO):
Statement A (dated March 2022): "Senator Smith voted against bill H.R. 123."
Statement B (dated June 2024): "Senator Smith sponsored bill H.R. 789."
PAIR 8: NO: Different actions on different bills at different times.

PAIR 9 (Person's Acknowledgment vs External Party's Action - NO):
Statement A (dated January 20, 2024): "Marine Le Pen acknowledged that reproaches can be made to her party regarding parliamentary assistants."
Statement B (dated January 20, 2024): "Marine Le Pen said the EU Parliament never advised or criticized her party about assistants."
PAIR 9: NO: A is about her own acknowledgment that criticism is warranted; B is about what the EU Parliament did or didn't do. Different subjects.

PAIR 11 (Trial / Accusation vs Denial - NO):
Statement A (dated January 20, 2024): "Marine Le Pen a été jugée en appel pour détournement de fonds publics européens dans l'affaire des assistants parlementaires du FN."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations."
PAIR 11: NO: Being tried (a procedural step) is NOT a finding of guilt — it does not break her denial. Only a conviction or court finding would.

PAIR 12 (Denial AFTER the Conviction - YES):
Statement A (dated March 28, 2024): "Marine Le Pen a été reconnue coupable de détournement de fonds publics par la cour d'appel de Paris."
Statement B (dated April 2, 2024): "Marine Le Pen a nié tout détournement de fonds publics."
PAIR 12: YES: She denies the misappropriation after the court found her guilty of it: an authoritative finding breaks a denial made after it.

PAIR 12b (Denial BEFORE the Conviction - NO):
Statement A (dated March 28, 2024): "Marine Le Pen a été reconnue coupable de détournement de fonds publics par le tribunal correctionnel de Paris."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics lors de son procès."
PAIR 12b: NO: The denial was made at her trial, before any conviction: a defendant pleading not guilty. The verdict that came later does not turn the plea into a contradiction.

PAIR 14 (Same Specific Reference - YES):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered the emails between Schaffhauser and Saint-Just."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the emails between Schaffhauser and Saint-Just."
PAIR 14: YES: Both statements explicitly reference the same specific emails — "discovered" directly contradicts "had no knowledge of".

PAIR 17 (Conviction vs Acknowledgment of Imperfections - NO):
Statement A: "Marine Le Pen a reconnu que 'beaucoup de choses auraient pu se passer de manière plus rigoureuse'."
Statement B: "Marine Le Pen a été condamnée pour détournement de fonds publics."
PAIR 17: NO: The conviction exceeds her acknowledgment but doesn't contradict it. Only an explicit denial of wrongdoing + conviction = contradiction.

PAIR 18 (Coherent Defense - NO):
Statement A: "Marine Le Pen a nié avoir donné des instructions d'embauche fictive."
Statement B: "Marine Le Pen a rejeté les accusations de faute lourde dans la gestion des fonds européens."
PAIR 18: NO: Two denials about different aspects of the same case form a consistent defense, not a contradiction.

PAIR 21 (Legal Process Stages - NO):
Statement A: "Le Rassemblement National est accusé de détournement de fonds publics."
Statement B: "Marine Le Pen a été reconnue coupable de détournement de fonds publics."
PAIR 21: NO: Accusation and conviction are sequential stages of the same case.

PAIR 24 (Announcement Followed by Action - NO):
Statement A (dated March 15, 2024): "Jean-Luc Mélenchon announced he would come to support his candidate in Lille on Thursday evening."
Statement B (dated March 18, 2024): "Jean-Luc Mélenchon came to support the candidacy of Lahouaria Addouche in Lille."
PAIR 24: NO: Announcing support and then providing it is a fulfilled commitment, not a contradiction.

PAIR 27 (Broad Value Statement vs Opposing Action - YES):
Statement A (dated March 15, 2024): "Sophie Martin a promis de reconstruire l'artisanat français pour redonner du pouvoir d'achat aux Français."
Statement B (dated February 20, 2025): "Sophie Martin a autorisé la délocalisation en Chine d'une usine française de 5 000 employés à Béthune."
PAIR 27: REASONING: Statement A is a BROAD VALUES commitment ("rebuild French artisanat") — no quantified target, no deadline, no specific bill named. Statement B is the person's own action that materially undermines that commitment: authorising offshoring of a 5,000-employee French factory removes domestic French manufacturing jobs, which is the OPPOSITE direction on the axis of French industrial/artisanal employment. Per BEHAVIORAL INCONSISTENCY (clarified) — broad value statements DO qualify as stated positions, and the action's direction on the same axis is unambiguous. The size of the action (5,000 employees) is material, not symbolic. FLAG YES.
PAIR 27: YES: Promise to rebuild French artisanat is materially undermined by authorising offshoring of a French factory to China — opposite directions on the same axis of French industrial sovereignty, even though the promise side names no quantified target.

PAIR 30 (Self-Exempting Double Standard - YES):
Statement A (dated 2013): "Marine Le Pen a déclaré qu'il faudrait rendre inéligibles à vie ceux qui étaient condamnés."
Statement B (dated July 2026): "Marine Le Pen a affirmé qu'il n'y a plus de scénario dans lequel elle ne pourrait pas être candidate à l'élection présidentielle."
Context in the stance: Marine Le Pen a elle-même été condamnée en appel pour détournement de fonds publics.
PAIR 30: REASONING: A is her OWN stated RULE — the convicted should be ineligible for life. B is her OWN claim that nothing can bar her candidacy, asserted after she herself was convicted (her class-membership is established by the surrounding conviction facts). She refuses to apply to herself the exact sanction she demanded for all convicted people — SELF-EXEMPTING DOUBLE STANDARD. Conditions (a) her own normative rule, (b) she now falls in the convicted class, (c) same sanction (ineligibility) — all hold. The [vague_vs_specific] bucket does NOT apply: the rule binds her once she enters the class it targets.
PAIR 30: YES: Ayant réclamé l'inéligibilité à vie des personnes condamnées, elle revendique désormais sa propre éligibilité alors qu'elle est elle-même condamnée — deux poids, deux mesures.`
