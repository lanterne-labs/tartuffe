package contradiction

import (
	"context"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/promptrules"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	// MaxPairsPerBatch is the maximum number of fact pairs to analyze in a single
	// LLM call on the Gemini BATCH path. Sized so the prompt + the
	// contradictionMaxTokens reservation stays well under a 16k context window:
	// ContradictionRulesAndExamples + 3 pairs ~= 2.6k tokens, plus
	// contradictionMaxTokens=2048 = ~4.7k total reservation. 3 pairs is the
	// historical accuracy baseline on Gemini (we briefly raised to 5 for cost;
	// cross-pair confusion observed at 5, reverted).
	MaxPairsPerBatch = 3
	// MaxPairsPerBatchSync is the bundle size on the sync OpenAI-compatible path.
	// Held at 1: smaller models (local Gemma) occasionally swap fact IDs across
	// pairs when verdicts share a prompt — observed on the eval as a fabricated
	// contradiction between an unrelated pair carrying another pair's reason.
	// One pair per call eliminates the swap entirely. Per-call latency is higher
	// but throughput on a local GPU is fine.
	MaxPairsPerBatchSync = 1
	// contradictionMaxTokens caps the LLM response budget. The reservation is
	// charged against the context window BEFORE generation, so every token
	// reserved is a token the prompt cannot use.
	//
	// Measured 2026-08-31 over live calls: prompt is ~7770 tokens (dominated by
	// the static template + FalsePositivePatterns catalog) and real replies are
	// 75-135 completion tokens, finish_reason "stop" every time. At the old
	// 4096 that left 7770+4096 = 11866 of 12000 — a 1.1% margin, so any pair
	// with slightly longer fact text 400'd on context overflow.
	//
	// 4096 was sized for Gemma's thinking mode. This constant is the answer
	// budget only: when llm.TaskDetect thinks, the client adds the thinking
	// budget on top (llm/task.go). 1024 is ~7.6x the
	// observed maximum and restores ~3000 tokens of prompt headroom. The
	// "contradiction token usage" log line reports completion_tokens and warns
	// on finish_reason=="length", so a too-small budget announces itself.
	contradictionMaxTokens = 1024
	// DelayBetweenBatches is the delay between batch API calls to respect rate limits.
	// 1s is conservative for Gemini Flash via OpenRouter (high RPM limits).
	DelayBetweenBatches = 1 * time.Second
)

// pairRegex matches "PAIR N: YES: reason" or "PAIR N: NO: reason" in LLM batch responses.
var pairRegex = regexp.MustCompile(`(?i)PAIR\s*(\d+)\s*:\s*(YES|NO)\s*:\s*(.*)`)

// reasoningRegex matches "PAIR N: REASONING: ..." lines in LLM batch responses.
var reasoningRegex = regexp.MustCompile(`(?i)PAIR\s*(\d+)\s*:\s*REASONING\s*:\s*(.*)`)

// reasonEnRegex matches "PAIR N: REASON_EN: ..." lines — the optional English
// translation of the YES/NO explanation. Emitted by the LLM only when the
// native verdict line is non-English.
var reasonEnRegex = regexp.MustCompile(`(?i)PAIR\s*(\d+)\s*:\s*REASON_EN\s*:\s*(.*)`)

// axisRegex matches "PAIR N: AXIS: ..." lines — emitted by the LLM only when
// the verdict is YES under the VALUES-BASED / DIRECTIONAL rule. Forces the
// model to commit to a single named axis before flagging a values-based
// contradiction. See the AXIS GATE in the response format.
var axisRegex = regexp.MustCompile(`(?i)PAIR\s*(\d+)\s*:\s*AXIS\s*:\s*(.*)`)

// formatDateForLLM formats a time pointer for display in the LLM prompt.
// Returns "unknown date" if the time is nil.
func formatDateForLLM(t *time.Time) string {
	if t == nil {
		return "unknown date"
	}
	return t.Format("January 2, 2006")
}

// ContradictionRulesAndExamples is the shared rule body used by both the
// detector prompt and the auditor prompt. Keeping a single source of truth
// prevents the two prompts from drifting in their definition of what counts
// as a contradiction. Its false-positive section is the shared
// promptrules.FalsePositivePatterns catalog — the SAME catalog the insertion/
// reverify verifier uses — so all three judges agree on what is NOT a
// contradiction. (var, not const, because the catalog is a //go:embed value.)
var ContradictionRulesAndExamples = `**CRITICAL - WHAT COUNTS AS A CONTRADICTION:**
A contradiction must be about the SAME SUBJECT - specifically about the person's own knowledge, beliefs, actions, or positions.

**HARD GATE — UTTERANCE REQUIREMENT (check FIRST, before any other reasoning):**
At least ONE of the two statements MUST be an utterance, claim, denial, position, commitment, or vote BY THE PERSON THEMSELVES. A statement that ATTRIBUTES words or a position to the person — "X announced/stated/declared/claimed/promised Y" — IS the person's own utterance (it is them asserting Y), NOT third-party narration. The only acceptable substitute is an AUTHORITATIVE FINDING about the person (a court conviction, official investigation conclusion, judicial ruling, audit, official report, or verified official record). If BOTH statements are third-party reports, journalistic narrations, or descriptions of events involving the person without any first-person claim or authoritative finding, the answer is automatically NO — even if the statements appear to disagree. Different reporters or sources giving slightly different versions of an event the person did not personally claim is a SOURCE DISAGREEMENT, not a personal contradiction.

**DATE-ONLY CONFLICTS — NOT A CONTRADICTION:**
If the only difference between two statements is the DATE or TIMING of an event (e.g., "X happened on Monday" vs "X happened on Tuesday"), it is NOT a contradiction UNLESS one of the statements is a personal commitment about that timing that the other breaks (e.g., the person promised "I will do X by date Y" and the other statement shows they did X later, with clear intent to delay or hide). Pure factual date discrepancies between reports — without the person committing to a specific date — reflect source noise, not dishonesty. Default to NO for date-only conflicts.

**VALUES-BASED / DIRECTIONAL CONTRADICTION — IS A CONTRADICTION (even without keyword overlap):**
A stated value, principle, or directional commitment by the person (e.g., "I want to strengthen X", "we will protect Y", "I support Z") is broken by the person's own concrete action that moves in the OPPOSITE direction on the same underlying axis — EVEN WHEN the two statements have NO surface keyword overlap. To judge:
1. Identify the underlying VALUE or DIRECTION embedded in the commitment ("rebuild artisanat" → strengthen domestic French manufacturing/employment).
2. Identify the directional IMPACT of the action ("authorise offshoring of a French factory to China" → weaken domestic French manufacturing/employment).
3. If the action moves in the OPPOSITE direction on the SAME underlying axis, it IS a contradiction. Different topics/keywords are NOT a reason to dismiss — the axis is what matters.
Examples of underlying axes: economic sovereignty / globalisation; environmental protection / industrial expansion; civil liberties / surveillance; austerity / public spending; secularism / religious accommodation. Be conservative: only flag YES when the axis is unambiguous AND the action clearly moves in the opposite direction (a small or symbolic gesture in the other direction is NOT enough — the action must materially undermine the commitment).

**PROMISE-BASED REASONING (core framework):**
The framework only applies when a "promise" actually exists. Try to identify one fact as a "promise" (a claim, denial, or commitment by the person) and the other as a potential "breaker" (a fact that could invalidate it). Either fact can be the promise — it is NOT necessarily the chronologically first one. **If NEITHER fact is a personal claim, denial, or commitment by the person, there is no promise to break — the verdict is automatically NO. Do not manufacture a promise where the facts don't contain one.**

A breaker genuinely invalidates a promise when:
- The person's OWN later or earlier statement directly contradicts their promise (e.g., "I knew X" vs "I never knew X")
- An AUTHORITATIVE FINDING (conviction, court ruling, official investigation conclusion) contradicts the person's denial or claim
- The person's OWN ACTION contradicts their stated commitment or position

A breaker does NOT invalidate a promise when:
- An ACCUSATION, CHARGE, INDICTMENT, or TRIAL (procedural steps) is paired with a denial — being accused or tried is NOT a finding of guilt
- An INVESTIGATION (without conclusion) is paired with a denial — being investigated does NOT establish facts
- An EXTERNAL PARTY'S action is the alleged breaker — only the person's own statements/actions or authoritative findings about them count

` + promptrules.FalsePositivePatterns + `

**TEMPORAL REASONING:**
1. Earlier "didn't know" → Later "knew/claimed" = NOT contradiction (learned over time)
2. Earlier "claimed X" → Later "denied X" = IS contradiction (cannot unknow)
3. Same date conflicts: DEFAULT to NO. Only flag YES when the pair satisfies the HARD GATE on BOTH sides: either BOTH facts are first-person claims by the same person about a fact they directly know (e.g., a number they stated themselves), OR one is the person's own claim and the other an AUTHORITATIVE FINDING about that same fact (official report, audit, court or official record) — and the two are logically incompatible. A bare timing/numeric discrepancy between two third-party reports on the same date is NEVER a contradiction — that is the DATE-ONLY CONFLICTS / UTTERANCE-REQUIREMENT case.
4. Unknown dates: when EITHER date is unknown (one missing is enough), the order is unknown — be SKEPTICAL, only flag if completely irreconcilable regardless of temporal ordering

**EXAMPLES:**

PAIR 1 (Learning Over Time / Unknown-Date Skepticism - NOT Contradictory):
Statement A (dated January 15, 2024): "Marine Le Pen said she didn't know about the mutualization scheme."
Statement B (dated March 20, 2024): "Marine Le Pen claimed the EU Parliament knew about mutualization and it was normal practice."
PAIR 1: NO: Earlier "didn't know" followed by later knowledge fits LEARNING OVER TIME. When a date is missing or ordering is unclear, default to skepticism — apparently opposite stances may be a legitimate change of position over time, not a contradiction.

PAIR 2 (Denial After Claim - Contradictory):
Statement A (dated January 15, 2024): "Marine Le Pen stated she was fully aware of the mutualization scheme and that it was standard practice."
Statement B (dated March 20, 2024): "Marine Le Pen claimed she never knew about any mutualization scheme."
PAIR 2: REASONING: She claimed full awareness in January, then denied any knowledge in March. The temporal rule says earlier "claimed X" followed by later "denied X" IS a contradiction — you cannot unknow something.
PAIR 2: YES: She claimed personal knowledge of the mutualization on January 15, then denied having that knowledge on March 20 - you cannot unknow something.

PAIR 3 (One Date Unknown - Be Skeptical):
Statement A (dated February 1, 2024): "CEO Jones supported policy X."
Statement B (dated unknown date): "CEO Jones opposed policy X."
PAIR 3: REASONING: Statement B's date is unknown, so the order of the two is unknown. The statements appear opposite, but per the UNKNOWN DATES temporal rule, we must be skeptical — B may come before or after A, a legitimate change of position over time.
PAIR 3: NO: With one date unknown, these could represent a legitimate change of position over time.

PAIR 4 (Same Date Conflict, BOTH First-Person Claims - Contradictory):
Statement A (dated February 1, 2024): "Alice, the project owner, stated 'our budget is $5 million.'"
Statement B (dated February 1, 2024): "Alice, the project owner, confirmed 'we approved a budget of $7 million.'"
PAIR 4: REASONING: Both statements are Alice's OWN first-person claims about a number she directly knows — she is the project owner asserting the budget she set. Same date, same fact, two incompatible personal claims by the same person. The HARD GATE is satisfied (Alice is the speaker in both), and the conflict is in her own claims, not in third-party reporting.
PAIR 4: YES: Alice's own first-person budget claim of $5M directly contradicts her own confirmation of $7M for the same project on the same date.

PAIR 5 (Different Events - NOT Contradictory):
Statement A (dated March 2022): "Senator Smith voted against bill H.R. 123."
Statement B (dated June 2024): "Senator Smith sponsored bill H.R. 789."
PAIR 5: NO: The statements concern different actions on different bills at different times.

PAIR 6 (Person's Acknowledgment vs External Party's Action - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen acknowledged that reproaches can be made to her party regarding parliamentary assistants."
Statement B (dated January 20, 2024): "Marine Le Pen said the EU Parliament never advised or criticized her party about assistants."
PAIR 6: NO: Statement A is about her personal acknowledgment that criticism is warranted, while Statement B is about the EU Parliament's actions - these are different subjects (her belief vs Parliament's behavior).

PAIR 7 (Person's Claim vs Authoritative Finding About Them - Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen claimed she was unaware of the fund misappropriation scheme."
Statement B (dated January 21, 2024): "Marine Le Pen ordered and supervised the fund misappropriation scheme according to court investigators."
PAIR 7: YES: Her claim of ignorance directly contradicts the authoritative finding that she knew and supervised the scheme - both statements are about HER knowledge and actions.

PAIR 8 (Trial/Accusation vs Denial - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a été jugée en appel pour détournement de fonds publics européens dans l'affaire des assistants parlementaires du FN."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations dans l'affaire des assistants parlementaires."
PAIR 8: NO: The promise is her denial of wrongdoing. Being tried (a procedural step) is NOT a finding of guilt — it does not break her denial. Only a conviction or court finding would.

PAIR 9 (Court Finding vs Denial - Contradictory):
Statement A (dated March 28, 2024): "Marine Le Pen a été reconnue coupable de détournement de fonds publics par la cour d'appel de Paris."
Statement B (dated January 20, 2024): "Marine Le Pen a nié tout détournement de fonds publics et rejeté les accusations dans l'affaire des assistants parlementaires."
PAIR 9: YES: The promise is her denial of any misappropriation. The court's guilty verdict is an authoritative finding that directly breaks her denial.

PAIR 10 (Vague Reference vs Specific Reference - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered emails she wasn't the recipient of."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the damaging emails between Schaffhauser and Saint-Just."
PAIR 10: NO: Statement A refers to "some emails" vaguely without specifying which ones, while Statement B refers to specific emails between two named people. We cannot confirm these refer to the same emails, so no contradiction can be established.

PAIR 11 (Same Specific Reference - Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen said she discovered the emails between Schaffhauser and Saint-Just."
Statement B (dated January 20, 2024): "Marine Le Pen claimed she had no knowledge of the emails between Schaffhauser and Saint-Just."
PAIR 11: YES: Both statements explicitly reference the same specific emails (between Schaffhauser and Saint-Just), so "discovered" directly contradicts "had no knowledge of".

PAIR 12 (Subjective Awareness vs Objective Fact - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen declared she had 'no consciousness' of any potential offense in the parliamentary assistants affair."
Statement B (dated January 20, 2024): "Marine Le Pen stated the Rassemblement national committed no irregularity regarding the employment of Thierry Légier."
PAIR 12: NO: These are different statement types - A is about her subjective past awareness, B is an objective factual claim. They reinforce each other: she wasn't aware of any offense because she believes none occurred. Denying wrongdoing does NOT imply prior knowledge of wrongdoing.

PAIR 15 (Conviction vs Acknowledgment of Imperfections - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a reconnu que 'beaucoup de choses auraient pu se passer de manière plus rigoureuse' dans le traitement des assistants parlementaires."
Statement B (dated March 28, 2024): "Marine Le Pen a été condamnée pour détournement de fonds publics dans l'affaire des assistants parlementaires."
PAIR 15: NO: The conviction does not contradict her acknowledgment of imperfections — it exceeds it. She admitted things could have been better; the court found it criminal. These are different DEGREES of the same assessment, not logical opposites. Only an explicit denial of wrongdoing + conviction = contradiction.

PAIR 16 (Coherent Defense — Different Denials About Same Case - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a nié avoir donné des instructions d'embauche fictive dans l'affaire des assistants parlementaires."
Statement B (dated January 22, 2024): "Marine Le Pen a rejeté les accusations de faute lourde dans la gestion des fonds européens."
PAIR 16: NO: Both statements are denials about different aspects of the same case — denying hiring instructions and denying gross negligence form a consistent defense posture, not a contradiction.

PAIR 17 (Consistent Ignorance — Didn't Know Different Things - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a déclaré ignorer que 80%% du budget des assistants parlementaires était reversé au parti."
Statement B (dated January 22, 2024): "Marine Le Pen a affirmé ne pas savoir que Julien Odoul avait été embauché comme assistant parlementaire fictif."
PAIR 17: NO: Not knowing about the 80%% budget figure and not knowing about a specific hiring are claims of ignorance about DIFFERENT aspects — they reinforce the consistent "I didn't know" defense rather than contradicting each other.

PAIR 18 (Same Position in Different Words - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a affirmé que le RN n'a jamais dissimulé les détails de l'emploi de ses assistants parlementaires."
Statement B (dated January 22, 2024): "Marine Le Pen a déclaré que le Rassemblement national n'avait rien caché concernant les pratiques d'emploi de ses assistants."
PAIR 18: NO: Both statements express the same position — that the party did not hide information about assistant employment — using different words. Paraphrases of the same claim cannot contradict each other.

PAIR 19 (Unrelated Facts About Same Person - NOT Contradictory):
Statement A (dated January 20, 2024): "Marine Le Pen a été interrogée pendant 13 heures lors de son procès en appel."
Statement B (dated March 28, 2024): "Marine Le Pen a déclaré vouloir épuiser toutes les voies de recours après sa condamnation."
PAIR 19: NO: Being interrogated for 13 hours and wanting to exhaust appeals are completely unrelated facts about different events at different times — no logical relationship exists between them.

PAIR 21 (Legal Process Stages - NOT Contradictory):
Statement A (dated January 20, 2024): "Le Rassemblement National est accusé de détournement de fonds publics dans l'affaire des assistants parlementaires."
Statement B (dated March 28, 2024): "Marine Le Pen a été reconnue coupable de détournement de fonds publics dans l'affaire des assistants parlementaires."
PAIR 21: NO: Accusation and conviction are sequential stages of the same legal process — being accused and then convicted is normal progression, not a contradiction.

PAIR 22 (Different Descriptions of Same Sentence - NOT Contradictory):
Statement A (dated March 28, 2024): "Marine Le Pen a été condamnée à quatre ans de prison, 100 000 euros d'amende et cinq ans d'inéligibilité."
Statement B (dated March 28, 2024): "Marine Le Pen a été condamnée à cinq ans d'inéligibilité avec exécution immédiate."
PAIR 22: NO: Both describe the same first-instance sentence — Statement A gives the full breakdown while Statement B focuses on the ineligibility component. These are supplementary details, not contradictions.

PAIR 23 (Conviction vs Specific Detail Defense - NOT Contradictory):
Statement A (dated March 28, 2024): "Marine Le Pen was sentenced to five years of ineligibility for misappropriation of public funds."
Statement B (dated January 20, 2024): "Marine Le Pen denied that her parliamentary assistant was solely a bodyguard, stating he had broader administrative functions."
PAIR 23: NO: The conviction for the broad embezzlement scheme does not specifically disprove her claim about one assistant's job functions — these operate at different levels of specificity.

PAIR 26 (Geographic/Jurisdictional Scope - NOT Contradictory):
Statement A (dated March 10, 2024): "Seulement 3 communes en Vienne ont déposé des listes pour les élections municipales."
Statement B (dated March 10, 2024): "425 listes ont été déposées sur l'ensemble du territoire national pour les élections municipales."
PAIR 26: REASONING: Statement A is about a specific département (Vienne) while Statement B is about the entire national territory. Different geographic scopes — a small local number is fully compatible with a large national total.
PAIR 26: NO: Facts refer to different geographic scopes (one département vs nationwide) — local data does not contradict national aggregates.

PAIR 25 (Announcement Followed by Action - NOT Contradictory):
Statement A (dated March 15, 2024): "Jean-Luc Mélenchon announced he would come to support his candidate in Lille on Thursday evening."
Statement B (dated March 18, 2024): "Jean-Luc Mélenchon came to support the candidacy of Lahouaria Addouche in Lille."
PAIR 25: REASONING: Statement A is an announcement of intent to support, Statement B describes the actual support happening. This is a fulfilled commitment — the person did what they announced. Past vs future tense about the same action is NOT a contradiction.
PAIR 25: NO: Announcing support and then providing that support is a fulfilled commitment, not a contradiction.

PAIR 27 (Third-Party Narratives — Person Never Spoke - NOT Contradictory):
Statement A (dated March 10, 2024): "Le Monde reported that Emmanuel Macron met with the German chancellor on March 5."
Statement B (dated March 11, 2024): "Reuters reported that Emmanuel Macron met with the German chancellor on March 6."
PAIR 27: REASONING: Neither statement is an utterance by Macron himself — both are third-party reports about an event. There is no personal claim or authoritative finding by/about Macron to contradict. The HARD GATE — UTTERANCE REQUIREMENT applies: this is a source disagreement, not a personal contradiction.
PAIR 27: NO: Both statements are third-party narratives; neither is the person's own claim or an authoritative finding, so no personal contradiction can be established.

PAIR 28 (Date-Only Conflict, No Personal Commitment - NOT Contradictory):
Statement A (dated April 2, 2024): "Emmanuel Macron signed the climate decree on March 20."
Statement B (dated April 3, 2024): "Emmanuel Macron signed the climate decree on March 22."
PAIR 28: REASONING: The only difference between the two statements is the date of the same event — there is no personal commitment about timing being broken, no intent to delay or hide. Pure date discrepancies between sources reflect source noise, not dishonesty. The DATE-ONLY CONFLICTS rule applies.
PAIR 28: NO: A bare date discrepancy with no broken commitment about timing is a source disagreement, not a contradiction.

PAIR 29 (Date Conflict + Broken Personal Commitment - Contradictory):
Statement A (dated January 5, 2024): "Emmanuel Macron promised the law would be signed before December 31, 2023."
Statement B (dated February 2, 2024): "Emmanuel Macron signed the law on January 28, 2024."
PAIR 29: REASONING: Statement A is Macron's own commitment about timing; Statement B is the actual action taken after the committed deadline. The date conflict here reflects a broken personal commitment — exactly the intent-of-misbehavior carve-out in the DATE-ONLY CONFLICTS rule.
PAIR 29: YES: Macron committed to signing before a specific date and then signed after it — the timing conflict reflects a broken commitment.

PAIR 30 (Values-Based Contradiction, No Keyword Overlap - Contradictory):
Statement A (dated March 1, 2024): "Emmanuel Macron a affirmé vouloir redonner du pouvoir d'achat aux Français en reconstruisant l'artisanat français."
Statement B (dated June 15, 2024): "Emmanuel Macron a autorisé la délocalisation en Chine d'une usine française de 5 000 employés."
PAIR 30: REASONING: A is Macron's own stated commitment — the underlying axis is "strengthen French domestic manufacturing and employment". B is Macron's own action (authorising offshoring), and its directional impact on that same axis is the opposite: it weakens domestic French manufacturing and employment. The two statements share no surface keywords (artisanat vs délocalisation/Chine), but the underlying axis is identical and the action materially undermines the commitment. The HARD GATE is satisfied (A is his own statement, B is his own action). The CRIMINAL-EXECUTION carve-out does NOT apply — this is a values-vs-action case, not an execution-of-fraud case. This is the VALUES-BASED / DIRECTIONAL CONTRADICTION rule.
PAIR 30: AXIS: souveraineté industrielle française
PAIR 30: YES: Macron's commitment to rebuild French artisanat is materially undermined by his own decision to authorise offshoring a 5,000-employee French factory to China — opposite directions on the same underlying axis of French industrial sovereignty.

PAIR 31 (Symbolic / Small Counter-Gesture - NOT Contradictory):
Statement A (dated March 1, 2024): "Macron a déclaré vouloir réindustrialiser la France."
Statement B (dated June 15, 2024): "Macron a inauguré un partenariat industriel avec un fabricant allemand pour 50 emplois en Bavière."
PAIR 31: REASONING: A is a stated commitment to French reindustrialisation. B is a small cross-border industrial partnership of 50 jobs — symbolic in scale and not clearly opposed to French reindustrialisation (industrial partnerships often complement domestic policy). The VALUES-BASED rule requires the action to MATERIALLY undermine the commitment; a 50-job partnership in Bavaria does not meet that bar.
PAIR 31: NO: The 50-job German partnership does not materially undermine the French reindustrialisation commitment — small counter-gestures or unrelated cross-border collaborations are not contradictions.

PAIR 32 (Imperative Rhetoric vs Personal Commitment - NOT Contradictory):
Statement A (dated March 15, 2023): "Senator Smith déclare: « Nous devons agir avec détermination contre le changement climatique. »"
Statement B (dated June 10, 2024): "Senator Smith a voté contre la proposition de loi instaurant une taxe carbone aux frontières."
PAIR 32: REASONING: Statement A is imperative rhetoric ("nous devons agir" / "we must act") expressing a general aspiration in the third person. It is NOT a first-person commitment ("je m'engage", "I will", "I commit to"). Per the VAGUE/GENERAL rule, only a personal commitment can be broken by an opposing action. A vote against ONE specific tax mechanism is also a specific policy choice that does not necessarily contradict a broad aspirational direction (the senator could pursue the goal through other policy tools). Without a first-person commitment, there is no promise to break.
PAIR 32: NO: Imperative aspirational rhetoric ("we must act") is not a first-person commitment that a specific policy vote can break — only "I will" / "je m'engage à" style statements create commitments that can be broken.

PAIR 33 (Denounce-Then-Ally — Same Named Target - Contradictory):
Statement A (dated February 2026): "Karim Bouamrane a accusé Clémentine Autain d'avoir « trahi les électeurs de gauche » et d'être responsable de la défaite du camp progressiste."
Statement B (dated March 2026): "Karim Bouamrane et Clémentine Autain ont annoncé une liste commune pour le second tour des municipales."
PAIR 33: REASONING: The SHARED SUBJECT is the NAMED person, Clémentine Autain. In A the speaker publicly blames her; in B he partners with that SAME person weeks later. "Blame is one topic, an alliance is another" is precisely the excuse the DENOUNCE-THEN-ALLY rule forbids: the subject is not "blame" vs "alliance", it is the speaker's stated stance toward ONE named target, which reversed. [different_subjects] does NOT apply.
PAIR 33: YES: Il a désigné nommément Autain comme responsable de la défaite, puis s'est allié avec elle — retournement envers la même personne.

PAIR 34 (Self-Exempting Double Standard — Own Rule, Own Class - Contradictory):
Statement A (dated 2019): "Éric Dupond-Moretti a déclaré qu'un ministre mis en examen devrait immédiatement quitter le gouvernement."
Statement B (dated July 2021): "Éric Dupond-Moretti a affirmé qu'il resterait garde des Sceaux malgré sa mise en examen."
PAIR 34: REASONING: A is his OWN normative rule for a CLASS ("un ministre mis en examen"). By B he is himself in that class (mis en examen) and exempts himself from the very sanction he demanded (leaving the government). Conditions: (a) his own rule, (b) he now falls in the named class, (c) same sanction. "A general normative position is not a personal commitment" is the forbidden excuse — the rule binds him the moment he enters the class it targets. NOT [different_subjects], NOT [vague_vs_specific].
PAIR 34: YES: Ayant exigé la démission de tout ministre mis en examen, il refuse de l'appliquer à lui-même une fois mis en examen — deux poids, deux mesures.

PAIR 35 (Commitment Walk-Back Disguised as Same Direction - Contradictory):
Statement A (dated May 2024): "Le parti a promis de supprimer la taxe foncière pour les résidences principales."
Statement B (dated January 2025): "Le parti a renoncé à supprimer la taxe foncière, indiquant qu'elle serait seulement plafonnée."
PAIR 35: REASONING: Map EACH statement to the concrete outcome it commits to. A commits to outcome X (no tax). B explicitly abandons THAT SAME X and settles for less. That is the ONLY shape that is a walk-back: B must give up the very outcome A promised. Likewise "promised to keep the retirement age at 62" vs "renounced repealing the reform that set it to 64" — B abandons exactly what A promised. Contrast the NON-walk-backs that stay [same_direction] / [partial_vs_total] and must be REMOVED: A promises free transport for three named groups, B rules out free transport for EVERYONE — B abandons a broader outcome A never promised, so A still stands; A laments that circulars are not followed, B says prosecutors must follow circulars — both push enforcement; A disagrees that climate is excluded, B says climate is included — same stance behind a double negative. A walk-back needs the SAME outcome abandoned, not merely the same topic revisited.
PAIR 35: YES: Promesse de suppression, puis renoncement à la suppression au profit d'un simple plafonnement — la promesse est abandonnée, pas tenue.

PAIR 36 (Amendment Vote That Breaks the Person's OWN Demanded Measure - Contradictory):
Statement A: "Le député a déclaré qu'aucune privatisation ne devrait être engagée sans une étude d'impact indépendante préalable."
Statement B: "Le député a voté CONTRE l'amendement instaurant une étude d'impact indépendante préalable à la privatisation."
PAIR 36: REASONING: [relative_vote_preference] protects amendment votes ONLY against inverted readings (anti-modification ≠ anti-bill, ≠ pro-status-quo). Here no inversion is needed: the amendment's CONTENT is exactly the measure the person demanded — an independent prior impact study — and they voted against creating it. The exception inside [relative_vote_preference] applies: the ballot breaks their OWN stated principle directly.
PAIR 36: YES: Il exige une étude d'impact préalable puis vote contre l'amendement qui l'instaure — son vote contredit sa propre exigence, sans aucune inversion de sens.
`

var contradictionPromptTpl = `You are analyzing pairs of statements for contradictions. For EACH pair, determine if the statements logically contradict each other.

` + ContradictionRulesAndExamples + `

` + promptrules.RestatementsRule + `

**PAIRS TO ANALYZE:**
%s

**RESPONSE FORMAT:**
For each pair, respond with these lines in order:

Line 1 (mandatory): PAIR N: REASONING: [Identify core claims in each statement, check which rules apply, 1-2 sentences of analysis]
Line 2 (mandatory IFF your verdict will be YES under the VALUES-BASED / DIRECTIONAL rule; otherwise omit): PAIR N: AXIS: [≤5 words naming the single underlying value-axis on which the two facts move in opposite directions, e.g. "souveraineté industrielle française" or "civil liberties / surveillance"]
Line 3 (mandatory): PAIR N: [YES/NO]: [one-sentence reason]

You MUST write your REASONING line BEFORE your verdict line. Analyze before committing.
If your reasoning concludes the statements are compatible, supplementary, or not contradictory, your verdict MUST be NO.
CRITICAL: Never output YES when your own analysis shows the facts don't contradict.
AXIS GATE: If you are about to answer YES on the basis of values/direction (no surface keyword overlap), you MUST first write an AXIS line naming the single shared axis in ≤5 words. If you cannot name a single concrete axis in ≤5 words, your verdict MUST be NO. The AXIS line is your commitment device — it forces you to ground a values-based YES in a specific, articulable axis rather than a vague "feels opposite" intuition.

**LANGUAGE RULE:** Write your REASONING and YES/NO explanations in French. Do NOT provide any translation — English and other languages are produced separately by the translation phase.`

// OpenAIContradictionDetector implements ContradictionDetector using an OpenAI-compatible API.
type OpenAIContradictionDetector struct {
	client *llm.Client
}

// NewOpenAIContradictionDetectorFromEnv creates a detector using OPEN_AI_ env vars.
// Required: OPEN_AI_KEY, OPEN_AI_API
// Optional: OPEN_AI_CHAT_MODEL (defaults to llm.DefaultChatModel)
func NewOpenAIContradictionDetectorFromEnv() (*OpenAIContradictionDetector, error) {
	client, err := llm.NewClientFromEnv("contradiction-detector")
	if err != nil {
		return nil, err
	}
	return &OpenAIContradictionDetector{client: client}, nil
}

// AreContradictory implements the ContradictionDetector interface.
// Delegates to AreContradictoryBatch with a single pair.
func (d *OpenAIContradictionDetector) AreContradictory(ctx context.Context, textA string, dateA *time.Time, textB string, dateB *time.Time) (isContradictory bool, reason string, err error) {
	// Basic check: identical texts are not contradictory
	if textA == textB {
		metrics.IncrementContradictionChecks("not_found")
		return false, "Identical statements.", nil
	}

	results := d.AreContradictoryBatch(ctx, []FactPair{{
		TextA: textA,
		DateA: dateA,
		TextB: textB,
		DateB: dateB,
	}})

	if len(results) == 0 {
		return false, "", fmt.Errorf("batch returned no results")
	}
	r := results[0]
	if r.Error != nil {
		return false, "", r.Error
	}
	return r.IsContradictory, r.Reason, nil
}

// VerifyContradiction runs the adversarial refutation pass over a single
// claimed contradiction returned by AreContradictoryBatch, before it is
// persisted. keep == true means the claim survived (record it); keep == false
// means it was refuted as a false positive (drop it). A non-nil error is an
// operational failure (LLM unreachable / unparseable) — callers should log and
// fall open (keep the contradiction), never silently drop on error.
//
// Delegates to llm.VerifyContradiction so the detector path and the stance path
// (core.StanceManager.VerifyContradiction) share identical refutation logic.
func (d *OpenAIContradictionDetector) VerifyContradiction(ctx context.Context, a, b promptrules.Statement, claimedReason string) (keep bool, reason string, err error) {
	type verdict struct {
		keep   bool
		reason string
	}
	v, err := llm.MajorityVote(JudgeVotes,
		func() (verdict, error) {
			k, r, e := llm.VerifyContradiction(ctx, d.client, a, b, claimedReason)
			return verdict{keep: k, reason: r}, e
		},
		func(v verdict) string {
			if v.keep {
				return "keep"
			}
			return "drop"
		},
	)
	return v.keep, v.reason, err
}

// SynthesizeGroupSummary produces a merged "one inconsistency" sentence (native +
// English) for a contradiction group — one base claim contradicted by N facts.
// Used by the regroup pass. Delegates to llm.SynthesizeContradictionGroupSummary.
func (d *OpenAIContradictionDetector) SynthesizeGroupSummary(ctx context.Context, baseFact string, spokes []llm.GroupSpoke) (summary, summaryEn string, err error) {
	return llm.SynthesizeContradictionGroupSummary(ctx, d.client, baseFact, spokes)
}

// FactPair represents a pair of facts to check for contradiction
type FactPair struct {
	IndexA int
	TextA  string
	DateA  *time.Time
	IndexB int
	TextB  string
	DateB  *time.Time
	// AlsoA / AlsoB are the other wordings of each statement when it stands
	// for a group of restatements (see promptrules.Statement).
	AlsoA, AlsoB []string
}

// BatchResult represents the result of checking a single pair in a batch
type BatchResult struct {
	PairIndex       int // Index in the input slice
	IsContradictory bool
	Reason          string
	ReasonEn        string // English translation of Reason; empty when Reason is already English
	Reasoning       string // Parsed REASONING line from LLM (analysis before verdict)
	Axis            string // Parsed AXIS line — only present when the LLM identified a single named value-axis (values-based YES verdicts).
	RawResponse     string // Full LLM response text for inspection
	Error           error
}

// AreContradictoryBatch checks multiple pairs for contradictions in a single LLM call.
// Returns results for each pair in the same order as input.
func (d *OpenAIContradictionDetector) AreContradictoryBatch(ctx context.Context, pairs []FactPair) []BatchResult {
	results := make([]BatchResult, len(pairs))

	if len(pairs) == 0 {
		return results
	}

	timer := metrics.NewLLMTimer("chat", d.client.Model)

	// Build the pairs section of the prompt
	var pairsText strings.Builder
	for i, pair := range pairs {
		dateAStr := formatDateForLLM(pair.DateA)
		dateBStr := formatDateForLLM(pair.DateB)
		pairsText.WriteString(fmt.Sprintf("\nPAIR %d:\n", i+1))
		pairsText.WriteString(fmt.Sprintf("Statement A (dated %s): %s\n", dateAStr, promptrules.Statement{Text: pair.TextA, Also: pair.AlsoA}.Render()))
		pairsText.WriteString(fmt.Sprintf("Statement B (dated %s): %s\n", dateBStr, promptrules.Statement{Text: pair.TextB, Also: pair.AlsoB}.Render()))
	}

	prompt := fmt.Sprintf(contradictionPromptTpl, pairsText.String())

	// Thinking is set by llm.TaskDetect (llm/task.go).
	llmResponse, chatResp, err := d.client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.0,
		MaxTokens:   contradictionMaxTokens,
		Task:        llm.TaskDetect,
	})
	timer.ObserveDuration(err)
	if err != nil {
		log.Error().Err(err).Int("pairs", len(pairs)).Msg("Batch contradiction check API call failed")
		for i := range results {
			results[i].PairIndex = i
			results[i].Error = fmt.Errorf("batch API call failed: %w", err)
			metrics.IncrementContradictionChecks("error")
		}
		return results
	}

	// Parse each PAIR N: YES/NO: reason line
	// Token-usage telemetry for calibrating contradictionMaxTokens. The
	// reservation is charged against the 12k context BEFORE generation, so
	// every token reserved here is a token the prompt cannot use — 4096 was
	// picked to survive Gemma's thinking mode, never measured against real
	// replies. finish_reason=="length" means the reply was actually truncated,
	// which is the only signal that the reservation is too SMALL.
	finishReason := chatResp.Choices[0].FinishReason
	usageEvt := log.Info()
	if finishReason == "length" {
		usageEvt = log.Warn()
	}
	usageEvt.
		Int("prompt_tokens", chatResp.Usage.PromptTokens).
		Int("completion_tokens", chatResp.Usage.CompletionTokens).
		Int("total_tokens", chatResp.Usage.TotalTokens).
		Int("reserved_max_tokens", llm.ReservedTokens(llm.TaskDetect, contradictionMaxTokens)).
		Int("unused_reservation", llm.ReservedTokens(llm.TaskDetect, contradictionMaxTokens)-chatResp.Usage.CompletionTokens).
		Str("finish_reason", finishReason).
		Int("pairs", len(pairs)).
		Msg("contradiction token usage")

	// Store raw response on every result for inspection
	for i := range results {
		results[i].RawResponse = llmResponse
	}

	parseBatchResponse(llmResponse, results)

	// Log results and update metrics
	contradictions := 0
	for _, r := range results {
		switch {
		case r.IsContradictory:
			contradictions++
			metrics.IncrementContradictionChecks("found")
			metrics.RecordDecision(metrics.StageContradiction, metrics.OutcomeContradictionFound)
		case r.Error == nil:
			metrics.IncrementContradictionChecks("not_found")
			metrics.RecordDecision(metrics.StageContradiction, metrics.OutcomeContradictionNone)
		default:
			metrics.IncrementContradictionChecks("error")
			metrics.RecordDecision(metrics.StageContradiction, metrics.OutcomeError)
		}
	}
	log.Info().Int("pairs", len(pairs)).Int("contradictions", contradictions).Msg("Batch contradiction check completed")

	return results
}

// parseBatchResponse parses the LLM response and fills in the results slice
func parseBatchResponse(response string, results []BatchResult) {
	// Initialize all results with their index — unparsed pairs are treated as errors
	for i := range results {
		results[i].PairIndex = i
		results[i].Reason = "No response parsed for this pair"
		results[i].Error = fmt.Errorf("no response parsed for pair %d", i+1)
	}

	// Match REASONING and YES/NO verdict lines

	lines := strings.Split(response, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Try REASONING line first
		if rm := reasoningRegex.FindStringSubmatch(line); rm != nil {
			var pairNum int
			fmt.Sscanf(rm[1], "%d", &pairNum)
			pairIdx := pairNum - 1
			if pairIdx >= 0 && pairIdx < len(results) {
				results[pairIdx].Reasoning = strings.TrimSpace(rm[2])
			}
			continue
		}

		// Try REASON_EN line (English translation of the verdict reason)
		if rm := reasonEnRegex.FindStringSubmatch(line); rm != nil {
			var pairNum int
			fmt.Sscanf(rm[1], "%d", &pairNum)
			pairIdx := pairNum - 1
			if pairIdx >= 0 && pairIdx < len(results) {
				results[pairIdx].ReasonEn = strings.TrimSpace(rm[2])
			}
			continue
		}

		// Try AXIS line (named value-axis for VALUES-BASED YES verdicts)
		if rm := axisRegex.FindStringSubmatch(line); rm != nil {
			var pairNum int
			fmt.Sscanf(rm[1], "%d", &pairNum)
			pairIdx := pairNum - 1
			if pairIdx >= 0 && pairIdx < len(results) {
				results[pairIdx].Axis = strings.TrimSpace(rm[2])
			}
			continue
		}

		// Try verdict line (YES/NO)
		matches := pairRegex.FindStringSubmatch(line)
		if matches == nil {
			continue
		}

		// Parse pair number (1-indexed in response, 0-indexed in results)
		var pairNum int
		fmt.Sscanf(matches[1], "%d", &pairNum)
		pairIdx := pairNum - 1

		if pairIdx < 0 || pairIdx >= len(results) {
			log.Warn().Int("pair_num", pairNum).Msg("Batch response contains invalid pair number")
			continue
		}

		verdict := strings.ToUpper(matches[2])
		reason := strings.TrimSpace(matches[3])

		results[pairIdx].IsContradictory = (verdict == "YES")
		results[pairIdx].Reason = reason
		results[pairIdx].Error = nil

		if results[pairIdx].IsContradictory {
			log.Warn().Int("pair", pairNum).Str("reason", reason).Msg("Batch: Contradiction detected")
		} else {
			log.Debug().Int("pair", pairNum).Str("reason", reason).Msg("Batch: No contradiction")
		}
	}
}
