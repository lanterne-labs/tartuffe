// Package promptrules holds prompt fragments shared across packages that would
// otherwise create import cycles. Keep this package dependency-free — it only
// exports string constants.
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
- An AUTHORITATIVE FINDING (conviction, court ruling, official investigation conclusion) contradicts the person's denial or claim

A breaker does NOT invalidate a promise when:
- An ACCUSATION, CHARGE, INDICTMENT, TRIAL or open INVESTIGATION (procedural steps) is paired with a denial — these are NOT findings of guilt
- An EXTERNAL PARTY'S action is the alleged breaker — only the person's own statements/actions or authoritative findings about them count

**POSITIVE PATTERNS — these ARE contradictions** (each is a flavor of the person's own statements/actions conflicting). When in doubt between a positive pattern and a negative one, FLAG the contradiction — the user prefers a sharper signal over a quiet one:
- STATED-CONDITION WALK-BACK: "I will not do X if Y happens" + later they act/talk as if X is happening despite Y. The breaker doesn't need to mention the original condition explicitly — if the condition has clearly occurred (e.g., the sentence was handed down) and the person now claims they will still do X, that IS the walk-back.
- STATED-OBJECTIVE vs STATED-ACTION: The person claims a goal (e.g. "X is not our objective") and simultaneously claims an action whose stated effect IS X. The action negates the stated objective.
- BEHAVIORAL INCONSISTENCY: A public stated position by the SAME PERSON vs an action (by that same person) that runs counter to that position. **The stated position does NOT need a quantified target, a deadline, or a specific mechanism** — broad value statements like "I defend X", "X is sacred to me", "je soutiens Y", "je m'engage à protéger Z", "je veux reconstruire W", "mon attachement à X" all qualify as stated positions the person can be held to. If the directional axis is unambiguous AND the action moves materially against the stated position on that same axis, FLAG YES — do not wait for the promise side to be quantified. Apply this rule to: (a) PROMISED + opposing ACTION, (b) CLAIMED-position + opposing ACTION, (c) two same-person ACTIONS that point opposite directions on the same axis (e.g. "voted for tariffs protecting French jobs" + "authorized offshoring French factory" — both are actions, both bear on the same axis, opposite directions = contradiction). The rule does NOT apply when: (a) the alleged breaker is third-party action (only the person's own actions count), (b) the contexts are genuinely different and legitimately allow different choices (e.g. wartime vs peacetime, geographic scopes), or (c) the action is symbolic/de-minimis and does not materially undermine the stated position.
- POSITION SHIFT ON THE SAME ISSUE: A clear rejection / refusal / negative framing of an issue in one statement, paired with endorsement / openness / support / advocacy for the SAME issue in another. The two statements don't have to use identical wording — "not my priority / not desirable" → "I'm not against / I support" on the same identifiable subject IS a walk-back. Includes positions on policy proposals, candidacies, alliances, willingness to act, willingness to participate.
- CLAIM ↔ AUTHORITATIVE FINDING: The person denied / claimed X, and a court ruling / official investigation conclusion found the opposite specifically about X.
- COMMITMENT vs CARVE-OUT: A blanket commitment in one statement (e.g. "I will campaign with X regardless of any outcome") combined with a separately stated condition that would prevent that very commitment (e.g. "I refuse to do the very activity that campaigning would require") IS a contradiction even when each statement individually sounds reasonable.
- SELF-EXEMPTING DOUBLE STANDARD (hypocrisy): The person publicly demanded a RULE or SANCTION for a CLASS of people (e.g. "anyone convicted should be ineligible for life / must resign / should be barred / should be punished"), and the person's OWN later action or claim exempts THEMSELVES from that very rule once they demonstrably belong to that class (e.g. they were themselves convicted of that offense yet run for or keep the office). Holding others to a standard the person then refuses to apply to themselves IS a real contradiction. The fact that the person NOW falls into the targeted class may be established by the surrounding context or an authoritative finding (their own conviction) — the contradiction edge is still between the person's stated rule and their own self-exempting action. Requires ALL of: (a) a clear normative standard the person THEMSELVES stated (not merely attributed to their party, not a vague value); (b) the person clearly falls into the class the standard targets; (c) the self-exemption concerns the SAME sanction on the SAME axis. Do NOT stretch this to loose "he's a hypocrite" claims where any of (a)-(c) is missing.
  MECHANICAL CHECK (apply before emitting a self-exemption edge, IN BOTH DIRECTIONS — it does not matter whether the sanction is the NEW FACT or a key fact): a self-exemption edge pairs the person's stated RULE with the person's OWN utterance or act taking the exemption (CLAIMED / ANNOUNCED / PROMISED / DEMANDED / PROPOSED — "je suis éligible", "j'annonce ma candidature"). If EITHER side of the candidate pair is the sanction or legal process itself — a fact tagged WAS_CONVICTED or WAS_SENTENCED, a conviction, sentence, court or appeal ruling, a prosecutor's (le parquet) requisition, or a verdict narration — do NOT emit the edge: the sanction is the class-membership TRIGGER, not the breaker. A person being sanctioned is their own rule being APPLIED — if your reasoning describes the pair as the rule being applied, enforced, or fulfilled on them, that is AGREEMENT and the verdict for that pair is NEW_INFO, never CONTRADICTION.
  CRITICAL — WHAT THE OTHER FACT MUST BE: the rule can ONLY be paired with the person's OWN self-exempting CLAIM or ACT (e.g. "je suis éligible", "rien ne peut m'empêcher d'être candidate", "j'annonce ma candidature"). The class-membership trigger (their conviction) is CONTEXT, never the contradiction partner. Answer NO when the other fact is merely: the conviction / sentence itself, a court or appeal decision, an ineligibility ruling, a prosecutor's request, an awaiting-verdict or "the outcome will determine" narration, or any third-party account of the proceedings. "She demanded the convicted be barred" + "she was convicted" is NOT a contradiction — being sanctioned is exactly what her own rule prescribes, so it AGREES with it. The contradiction appears ONLY when she then claims or takes the exemption for herself.

**NEGATIVE PATTERNS — these are NOT contradictions** (apply NARROWLY — these rules block real contradictions when over-applied):
- DIFFERENT SUBJECTS: the two statements are about genuinely different actors, events, bills, or geographic regions. Same person + same topic + different aspects is NOT this rule — it's just same subject with two facets, which CAN contradict.
- VAGUE vs SPECIFIC: one statement is generic, the other names specific people/dates/items, and there is no clear indication they refer to the same thing. (If context proves they refer to the same thing, it IS a contradiction — see PAIR 14.)
- SUBJECTIVE AWARENESS vs OBJECTIVE FACT: "I wasn't aware of X happened" + "X did/didn't happen as fact" are compatible — denying X happened doesn't imply prior knowledge. THIS RULE COVERS ONLY past-awareness vs past-event. It does NOT excuse policy shifts, position shifts, or stated-condition walk-backs.
- ADMINISTRATIVE ERROR vs CRIMINAL INTENT: admitting "things could have been more rigorous" / "an administrative error was made" / "I acted in good faith despite negligence" does NOT contradict denying a specific criminal charge. Only triggers when one statement admits an administrative shortcoming and the other denies CRIMINAL intent on the same matter — does NOT excuse softening of stated political positions.
- COHERENT DEFENSE — strict version: two statements both deny the SAME SPECIFIC ALLEGATION (e.g. "I didn't order the fictitious hires" + "I didn't approve the fictitious hires") REINFORCE each other. Two statements that deny DIFFERENT specific allegations within the same case are also coherent. This rule does NOT cover statements about future conduct, candidacy, or political commitments — those can contradict freely.
- CONSISTENT IGNORANCE: multiple "I didn't know X" / "I didn't know Y" claims about different facts of the same situation reinforce each other. Only fires when both statements are EXPLICIT claims of ignorance.
- SAME POSITION IN DIFFERENT WORDS: paraphrases / translations of the same claim cannot contradict each other. Note: paraphrase requires that they make the same assertion — not just that they're about the same topic.
- COMPLEMENTARY FACTS / SAME EVENT: different aspects of the same ruling, sentence, or event (prison + ineligibility, first-instance + appeal sentencing) are supplementary, not contradictory. This ALSO covers two COMPATIBLE EXPLANATIONS or CAUSES of the same problem that can both hold at once — e.g. "the failure is revealing of our bad organization" + "the failure is NOT due to a lack of resources" AGREE (it is an organizational, not a budgetary, cause) and REINFORCE each other. Naming one cause while ruling out a different, non-exclusive cause is not a contradiction. Only flag when the two explanations are genuinely mutually exclusive (cannot both be true).
- LEGAL PROCESS STAGES: accusation → trial → conviction is sequential progression of the same case, not a contradiction (between THOSE stages — the person's own denials within still can contradict findings).
- CONVICTION SCOPE: a general conviction does not automatically disprove every specific factual claim the person made about details of the case — only those the conviction specifically addresses.
- ANNOUNCEMENT THEN ACTION: announcing X and then doing X is a FULFILLED commitment, the OPPOSITE of a contradiction.

**TEMPORAL REASONING:**
1. Earlier "didn't know" → Later "knew/claimed" = NOT contradiction (learned over time)
2. Earlier "claimed X" → Later "denied X" = IS contradiction (cannot unknow)
3. Same date conflicts: analyze carefully for direct logical contradiction
4. Unknown dates: be SKEPTICAL, only flag if completely irreconcilable regardless of temporal ordering

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

PAIR 5c (Behavioral Inconsistency - YES):
Statement A (dated March 2026): "Jordan Bardella n'a pas voté pour le Rassemblement national aux élections municipales de 2026 dans sa propre commune de Garches."
Statement B (dated March 2026): "Jordan Bardella a publiquement soutenu le candidat du Rassemblement national Franck Allisio à Marseille."
PAIR 5c: REASONING: Both are concurrent actions by the same person regarding the same election cycle. Publicly endorsing one's party's candidate elsewhere while not voting for the party's candidate in one's own town is an inconsistency between stated political support and personal voting behaviour. BEHAVIORAL INCONSISTENCY.
PAIR 5c: YES: Public party support + concurrent failure to vote for the same party in his own municipality.

PAIR 5d (Stated-Objective vs Stated-Action variant - YES):
Statement A (dated June 18, 2025): "Donald Trump claimed regime change in Iran was not the US goal."
Statement B (dated June 22, 2025): "Donald Trump a affirmé que les États-Unis avaient « décimé » le régime iranien « en deux semaines »."
PAIR 5d: REASONING: A states the OBJECTIVE: not regime change. B claims the ACHIEVEMENT: the regime has been decimated by US action. Decimating the regime IS regime change in substance. The stated achievement contradicts the stated objective. STATED-OBJECTIVE vs STATED-ACTION (second variant — same shape as PAIR 4).
PAIR 5d: YES: Claiming "regime change isn't the goal" alongside claiming "we have decimated the regime" cannot both be true.

PAIR 6 (Same Date Conflict - YES):
Statement A (dated February 1, 2024): "Alice stated the project budget is $5 million."
Statement B (dated February 1, 2024): "Alice confirmed the project budget was approved at $7 million."
PAIR 6: YES: Conflicting monetary values for the same project on the same date.

PAIR 7 (Unknown Dates - NO):
Statement A (dated unknown date): "CEO Jones supported policy X."
Statement B (dated unknown date): "CEO Jones opposed policy X."
PAIR 7: REASONING: Both dates unknown. UNKNOWN DATES rule: be skeptical, these could represent a legitimate change of position over time.
PAIR 7: NO: Without dates, these could represent a legitimate change of position over time.

PAIR 8 (Different Events - NO):
Statement A (dated March 2022): "Senator Smith voted against bill H.R. 123."
Statement B (dated June 2024): "Senator Smith sponsored bill H.R. 789."
PAIR 8: NO: Different actions on different bills at different times.

PAIR 9 (Person's Acknowledgment vs External Party's Action - NO):
Statement A (dated January 20, 2024): "Marine Le Pen acknowledged that reproaches can be made to her party regarding parliamentary assistants."
Statement B (dated January 20, 2024): "Marine Le Pen said the EU Parliament never advised or criticized her party about assistants."
PAIR 9: NO: A is about her own acknowledgment that criticism is warranted; B is about what the EU Parliament did or didn't do. Different subjects.

PAIR 10 (Person's Claim vs Authoritative Finding - YES):
Statement A (dated January 20, 2024): "Marine Le Pen claimed she was unaware of the fund misappropriation scheme."
Statement B (dated January 21, 2024): "Marine Le Pen ordered and supervised the fund misappropriation scheme according to court investigators."
PAIR 10: YES: Her claim of ignorance is directly contradicted by an authoritative finding that she knew and supervised the scheme.

PAIR 11 (Trial / Accusation vs Denial - NO):
Statement A (dated January 20, 2024): "Marine Le Pen a été jugée en appel pour détournement de fonds publics européens dans l'affaire des assistants parlementaires du FN."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations."
PAIR 11: NO: Being tried (a procedural step) is NOT a finding of guilt — it does not break her denial. Only a conviction or court finding would.

PAIR 12 (Conviction vs Denial - YES):
Statement A (dated March 28, 2024): "Marine Le Pen a été reconnue coupable de détournement de fonds publics par la cour d'appel de Paris."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics."
PAIR 12: YES: An authoritative court finding of guilt directly breaks her denial.

PAIR 13 (Vague vs Specific - NO):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered emails she wasn't the recipient of."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the damaging emails between Schaffhauser and Saint-Just."
PAIR 13: NO: A refers to "some emails" vaguely; B refers to specific emails between two named people. We cannot confirm they refer to the same emails.

PAIR 14 (Same Specific Reference - YES):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered the emails between Schaffhauser and Saint-Just."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the emails between Schaffhauser and Saint-Just."
PAIR 14: YES: Both statements explicitly reference the same specific emails — "discovered" directly contradicts "had no knowledge of".

PAIR 15 (Subjective Awareness vs Objective Fact - NO):
Statement A (dated January 20, 2024): "Marine Le Pen declared she had 'no consciousness' of any potential offense in the parliamentary assistants affair."
Statement B (dated January 20, 2024): "Marine Le Pen stated the Rassemblement national committed no irregularity regarding the employment of Thierry Légier."
PAIR 15: NO: A is her subjective past awareness, B is an objective factual claim. They reinforce each other; denying wrongdoing does NOT imply prior knowledge of wrongdoing.

PAIR 16 (Admitting Error vs Denying Crime - NO):
Statement A: "Marine Le Pen a déclaré n'avoir eu 'aucun sentiment d'avoir commis le moindre délit'."
Statement B: "Marine Le Pen a admis que les assistants parlementaires ont travaillé au siège du parti, ce qu'elle considère comme une erreur."
PAIR 16: NO: Admitting an administrative error does NOT contradict denying criminal intent.

PAIR 17 (Conviction vs Acknowledgment of Imperfections - NO):
Statement A: "Marine Le Pen a reconnu que 'beaucoup de choses auraient pu se passer de manière plus rigoureuse'."
Statement B: "Marine Le Pen a été condamnée pour détournement de fonds publics."
PAIR 17: NO: The conviction exceeds her acknowledgment but doesn't contradict it. Only an explicit denial of wrongdoing + conviction = contradiction.

PAIR 18 (Coherent Defense - NO):
Statement A: "Marine Le Pen a nié avoir donné des instructions d'embauche fictive."
Statement B: "Marine Le Pen a rejeté les accusations de faute lourde dans la gestion des fonds européens."
PAIR 18: NO: Two denials about different aspects of the same case form a consistent defense, not a contradiction.

PAIR 20 (Paraphrase - NO):
Statement A: "Marine Le Pen a affirmé que le RN n'a jamais dissimulé les détails de l'emploi de ses assistants parlementaires."
Statement B: "Marine Le Pen a déclaré que le Rassemblement national n'avait rien caché concernant les pratiques d'emploi de ses assistants."
PAIR 20: NO: Paraphrases of the same claim cannot contradict each other.

PAIR 21 (Legal Process Stages - NO):
Statement A: "Le Rassemblement National est accusé de détournement de fonds publics."
Statement B: "Marine Le Pen a été reconnue coupable de détournement de fonds publics."
PAIR 21: NO: Accusation and conviction are sequential stages of the same case.

PAIR 23 (Geographic Scope - NO):
Statement A: "Seulement 3 communes en Vienne ont déposé des listes pour les élections municipales."
Statement B: "425 listes ont été déposées sur l'ensemble du territoire national."
PAIR 23: NO: Different geographic scopes — local data does not contradict national aggregates.

PAIR 24 (Announcement Followed by Action - NO):
Statement A (dated March 15, 2024): "Jean-Luc Mélenchon announced he would come to support his candidate in Lille on Thursday evening."
Statement B (dated March 18, 2024): "Jean-Luc Mélenchon came to support the candidacy of Lahouaria Addouche in Lille."
PAIR 24: NO: Announcing support and then providing it is a fulfilled commitment, not a contradiction.

PAIR 27 (Broad Value Statement vs Opposing Action - YES):
Statement A (dated March 15, 2024): "Sophie Martin a promis de reconstruire l'artisanat français pour redonner du pouvoir d'achat aux Français."
Statement B (dated February 20, 2025): "Sophie Martin a autorisé la délocalisation en Chine d'une usine française de 5 000 employés à Béthune."
PAIR 27: REASONING: Statement A is a BROAD VALUES commitment ("rebuild French artisanat") — no quantified target, no deadline, no specific bill named. Statement B is the person's own action that materially undermines that commitment: authorising offshoring of a 5,000-employee French factory removes domestic French manufacturing jobs, which is the OPPOSITE direction on the axis of French industrial/artisanal employment. Per BEHAVIORAL INCONSISTENCY (clarified) — broad value statements DO qualify as stated positions, and the action's direction on the same axis is unambiguous. The size of the action (5,000 employees) is material, not symbolic. FLAG YES.
PAIR 27: YES: Promise to rebuild French artisanat is materially undermined by authorising offshoring of a French factory to China — opposite directions on the same axis of French industrial sovereignty, even though the promise side names no quantified target.

PAIR 28 (Broad Claim of Support vs Opposing Vote - YES):
Statement A (dated September 1, 2024): "Marc Dubois a déclaré soutenir massivement les énergies renouvelables."
Statement B (dated December 5, 2024): "Marc Dubois a voté contre la directive européenne imposant la fin des véhicules thermiques en 2035."
PAIR 28: REASONING: Statement A is a BROAD CLAIM of support ("soutenir massivement les énergies renouvelables") — no specific bill named, no quantified pledge. Statement B is the person's vote against the EU 2035 EV directive — a flagship instrument of the renewable-energy / decarbonisation transition. Voting against the main legislative mechanism of the energy transition materially undermines the "support renewables" claim on the same axis. Per BEHAVIORAL INCONSISTENCY (clarified) — broad claims of support DO qualify as stated positions, and a vote against the central mechanism IS a contradiction even though the claim was not quantified. FLAG YES.
PAIR 28: YES: A broad claim of supporting renewables is contradicted by voting against the EU's flagship transition mechanism — opposite directions on the same energy-transition axis.

PAIR 29 (Two Same-Person Actions Pointing Opposite on Same Axis - YES):
Statement A (dated June 4, 2024): "Sophie Martin a voté en faveur de l'instauration de droits de douane sur les importations chinoises."
Statement B (dated February 20, 2025): "Sophie Martin a autorisé la délocalisation en Chine d'une usine française de 5 000 employés à Béthune."
PAIR 29: REASONING: Neither statement is framed as a "promise"; both are concrete actions by the same person. Statement A protects domestic French industrial production (pro-tariff against Chinese imports); Statement B authorises moving French industrial production to China (anti-domestic-industry). Same axis (French industrial sovereignty), opposite directions, both material. Per BEHAVIORAL INCONSISTENCY (clarified) — applies to two same-person actions pointing opposite on the same axis, not only to promise-vs-action pairs. FLAG YES.
PAIR 29: YES: Voting for tariffs that protect French industry while authorising a major French-to-China offshoring contradicts on the axis of French industrial sovereignty — two actions, opposite directions, both material.

PAIR 30 (Self-Exempting Double Standard - YES):
Statement A (dated 2013): "Marine Le Pen a déclaré qu'il faudrait rendre inéligibles à vie ceux qui étaient condamnés."
Statement B (dated July 2026): "Marine Le Pen a affirmé qu'il n'y a plus de scénario dans lequel elle ne pourrait pas être candidate à l'élection présidentielle."
Context in the stance: Marine Le Pen a elle-même été condamnée en appel pour détournement de fonds publics.
PAIR 30: REASONING: A is her OWN stated RULE — the convicted should be ineligible for life. B is her OWN claim that nothing can bar her candidacy, asserted after she herself was convicted (her class-membership is established by the surrounding conviction facts). She refuses to apply to herself the exact sanction she demanded for all convicted people — SELF-EXEMPTING DOUBLE STANDARD. Conditions (a) her own normative rule, (b) she now falls in the convicted class, (c) same sanction (ineligibility) — all hold. The [vague_vs_specific] bucket does NOT apply: the rule binds her once she enters the class it targets.
PAIR 30: YES: Ayant réclamé l'inéligibilité à vie des personnes condamnées, elle revendique désormais sa propre éligibilité alors qu'elle est elle-même condamnée — deux poids, deux mesures.`
