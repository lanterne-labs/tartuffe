package core

import (
	"fmt"
	"strings"
)

// CoreFactExtractionPrompt is THE single prompt template for fact extraction.
// Extracts facts about ALL relevant entities mentioned in an article, not just a single target.
// Plugins can optionally inject additional instructions via the {{PLUGIN_INSTRUCTIONS}} placeholder.
const CoreFactExtractionPrompt = `You are an expert AI assistant specialized in extracting **verifiable factual statements** from news articles.

## Task
Extract facts about ALL relevant persons, parties, and organizations mentioned in the article.
Focus especially on the primary subject '%s' but do NOT limit extraction to only that entity.
We track facts to detect CONTRADICTIONS in an entity's OWN positions over time.

## What is a VALID Fact?
A valid fact is CONCRETE, SPECIFIC, and CONTRADICTABLE. We collect facts to detect when an entity FLIP-FLOPS, BREAKS A PROMISE, or when authoritative evidence CONTRADICTS their claims.

**The contradiction test**: Can you imagine a future headline where this entity does or says the OPPOSITE? If yes, it's a valid fact. If not (travel, scheduling, biography), it's not.

There are TWO types of valid facts:

**Type 1 — Entity's own positions, promises, claims, denials:**
What the entity SAYS, CLAIMS, PROMISES, or DENIES. These are valid because the entity could later say or do the opposite.
- "X stated she had no feeling of committing a crime" — she could later admit guilt
- "X promised to never raise taxes" — she could later raise taxes
- "X denied receiving illegal funds" — evidence could later prove she did

**Type 2 — Authoritative verifiable outcomes about the entity:**
Court rulings, official votes, detailed investigative reports with evidence. These are valid because they are concrete outcomes that the entity could later contradict or that contradict earlier claims.
- "X was sentenced to 4 years in prison" — concrete judicial outcome
- "X voted against bill Y on date Z" — official voting record
- "X received €120,000 per year from party Y" — specific amount from documented sources

⚠️ NOT Type 2: An opinion without evidence is NOT authoritative. "Critics say X is corrupt" or "Analysts say X will struggle" are third-party opinions, not verifiable outcomes.

## REJECT Rules (if ANY rule applies, do NOT extract the fact)

**1. Not about the entity or their organization**
The fact must be either: (a) the entity's OWN statements/actions or those of an organization they lead/represent, or (b) an authoritative outcome about the entity (court ruling, official vote, documented evidence). Reject: third-party opinions without evidence, opponents' claims about the entity, non-actions ("Y didn't comment").
- ❌ "X said the Parliament did not play its role" — X doesn't represent Parliament
- ❌ "Critics say X is corrupt" — opinion without evidence
- ✅ "Marine Le Pen stated RN never hid anything" — she leads/represents RN
- ✅ "X was found guilty by the court of appeal" — authoritative judicial outcome

**2. Lawyers/defense teams as subject**
Lawyers acting in their professional capacity are NEVER valid subject_entity. This includes any "Me/Maître X", "les avocats de", "la défense de" as subject.
- ❌ "Me Bosselut a plaidé l'innocence de X" — lawyer doing their job
- Exception: a lawyer making claims about THEMSELVES outside court would be valid.

**3. Speculative or conditional**
Anything that hasn't happened yet or is uncertain: "risque", "pourrait", "could", "may", "if convicted", future scheduling ("verdict on July 7"), conditional outcomes.
- ❌ "X risks ineligibility" — speculative
- ❌ "X will learn her verdict on July 7" — future scheduling

⚠️ EXCEPTION — the entity's own conditional promise/threat IS a valid Type 1 fact.
The decisive test is **who controls the action that follows the condition**:
- If the action AFTER the "if" is performed BY THE SUBJECT, the statement is a contradictable commitment (the subject either acts as promised or does not — that is exactly the kind of flip-flop we track). KEEP it.
- If the action AFTER the "if" is performed by SOMEONE ELSE or by impersonal forces ("the regime will fall", "the markets will collapse", "Iran will surrender"), the statement is a PREDICTION about a third party. REJECT it.
- ✅ "Trump said he will strike Iranian power stations IF Iran doesn't reopen Hormuz by 20:00" — the action ("strike") is HIS to perform; he can later strike or not. Type 1 conditional threat.
- ✅ "X promised to resign IF the bill passes" — the action ("resign") is HIS to perform. Type 1 conditional promise.
- ❌ "Trump claimed Iran can fall in one night IF pressure continues" — the action ("fall") is performed by Iran, not by Trump. Pure prediction.
- ❌ "X said the economy will crash IF the law is repealed" — the action ("crash") is performed by impersonal markets, not by X. Prediction.

**4. Not contradictable**
If a statement can NEVER lead to a self-contradiction, reject it. This includes:
- Hopes, wishes, preferences, feelings, trust
- Travel, logistics, sightings
- Courtroom behavior/procedure (listening, appearing, being called to the stand)
- Pure biographical facts: birth date, party-founding year, role titles, lifetime events (retirement, marriage)
- Personal purchases, truisms, reactions to scheduling

⚠️ DO NOT reject under rule 4 (these LOOK biographical/historical but ARE contradictable):
- A specific recorded legislative vote — Type 2 outcome. KEEP even when from a prior parliament. ✅ "X voted AGAINST the Sondervermögen in the previous Bundestag" → KEEP.
- The entity's own stated past or present POSITION — even framed as biography. The entity can later flip. ✅ "X stated she has long opposed primaries" → KEEP (she could later support primaries).
- An election result where THE SUBJECT was a candidate — the subject could later claim a different number. ✅ "X obtained 49.79%% at Harfleur" → KEEP.

**5. Journalist characterization without evidence**
A journalist's interpretation or narrative framing is NOT a valid fact. However, a journalist reporting a concrete, documented finding WITH evidence IS valid (Type 2).
- ❌ "X a été séduite par Trump" — journalist's narrative, no evidence
- ❌ "X adopte un ton plus pro-marché" — subjective assessment
- ✅ "X a déclaré soutenir Donald Trump" — X's own statement
- ✅ "An investigation revealed X received €50,000 from Y" — documented finding with specifics

**5b. Pattern-of-behavior / meta-observation about the entity**
A summary statement describing the entity's HABITUAL behavior, RHETORICAL PATTERN, or repeated tendency is NEVER a valid fact, even when phrased as "X did Y", because no single subject/quote/event/date can be attached to it. A fact must point to ONE concrete claim, action, or outcome — not a pattern across many.
- ❌ "X has repeatedly made grand announcements he later contradicts" — describes a pattern, not a single act
- ❌ "X a multiplié les annonces grandiloquentes qu'il a régulièrement contredites" — pattern phrasing ("a multiplié", "régulièrement"), no specific subject or quote
- ❌ "Y change frequently of position on immigration" — habit, not an event
- ❌ "Z has a long history of denying climate change" — meta-summary
- ✅ "Z stated on April 5 that climate change is a hoax" — single dated claim
- ✅ "Y voted against the immigration bill on March 12" — single concrete vote
RED FLAGS that almost always mean PATTERN: "repeatedly", "has a history of", "multiplié", "régulièrement", "tends to", "has long claimed", "is known for", "pattern of", "every time".

**6. Vague or not concrete**
Facts MUST contain specific information: numbers, dates, named actions, precise claims. No vague qualifiers.
- ❌ "Party X pays its leaders generously" — no numbers
- ✅ "Party X paid its president €120,000 per year" — specific

**7. Polls, surveys, editorial comparisons**
Reject poll data, survey results, and journalist comparisons between entities.

## Self-Consistency Check
Before finalizing each fact, re-read your normalized_fact and verify:
- Does the sentence make logical sense on its own? If the first half contradicts the second half, you have misread the source.
- Pay special attention to NEGATION CHAINS: "denied that X lacks Y" vs "accused X of lacking Y" mean OPPOSITE things. When the source says "X ne fait pas preuve de Y" (X does not show Y), the entity is ACCUSING X of lacking Y, NOT denying that X lacks Y.
- If you catch an internal contradiction, re-read the original_quote carefully and rewrite.

## Consolidation
- MERGE facts expressing the same core claim in different words. Keep ONLY the most specific version.
- Typical article yields 2-5 valid facts. If you have more than 6, re-check against the rules.
- Extract in the entity's NATIVE LANGUAGE. Never duplicate across languages.
- Quality over quantity: fewer correct facts is ALWAYS better than including invalid ones.

## JSON Output Format
Return a JSON array. Each object has 8 fields (in this order):

1. 'reasoning': Think step-by-step BEFORE committing to the other fields. Ask yourself: What type of fact is this (Type 1 or Type 2)? Which canonical verb best describes the stance? Is this truly contradictable? This field is for your analysis — be concise (1-2 sentences).
2. 'subject_entity': The person/party WHO PERFORMS THE ACTION or MAKES THE STATEMENT described in the fact.
   This is NOT the person being talked about — it's the ACTOR/SPEAKER. Use full canonical name.
   Example: "Trump criticized Le Pen" → subject_entity = "Donald Trump" (he speaks), involved_people = ["Marine Le Pen"]
   Example: "Marine Le Pen denied wrongdoing" → subject_entity = "Marine Le Pen" (she denies)
3. 'subject_entity_type': MUST be EXACTLY one of these three lowercase strings (no other values are accepted; facts with any other value will be rejected):
   "human" | "party" | "organization"
4. 'normalized_fact': Concise statement of what the entity DID or SAID. Include full name, specific action/statement, negations, dates, quantities.
   ⚠️ LANGUAGE: Write in the entity's NATIVE LANGUAGE (e.g., French for French politicians). NEVER in English.
   ⚠️ NO CANONICAL VERBS: NEVER use DENIED, CLAIMED, PROMISED, WAS_CONVICTED, etc. in this field. Use natural language only. Write "a nié", "a affirmé", "a promis" — NOT "a DENIED" or "a CLAIMED".
5. 'canonical_verb': MUST be EXACTLY one of these 20 UPPERCASE tokens (no synonyms, no inflections, no lowercase, no spaces — facts with any other value will be rejected):
   DENIED | CLAIMED | PROMISED | SUPPORTED | OPPOSED | VOTED_FOR | VOTED_AGAINST | PROPOSED | CONFIRMED | ADMITTED | REFUSED | DEMANDED | THREATENED | WAS_SENTENCED | WAS_CONVICTED | WAS_ACQUITTED | WAS_FINED | RECEIVED_FUNDS | CHANGED_POSITION | ANNOUNCED
   Pick the closest token; do NOT invent new ones (e.g. NEVER return "WAIVED", "SIGNED", "CONVICTED" — use the closest match like ANNOUNCED, PROPOSED, WAS_CONVICTED).
6. 'embedding_text': Topic-only semantic fingerprint for vector similarity. ALWAYS in English. NO VERB PREFIX.
   GOAL: Two facts about the same topic MUST produce identical or near-identical embedding_text, REGARDLESS of the entity's stance. The canonical_verb field already captures the stance separately.
   - Format: [broad policy domain] [one specific detail only if needed]
   - Topic abstraction: use the BROADEST policy domain. Related positions MUST cluster: "more financial freedom for workers" and "reduce holidays for workers" are both "workers-social-rights".
   - REMOVE: all canonical verbs, person/party/organization names, dates, temporal context, language-specific phrasing, specific numbers/percentages/amounts
   - Keep it SHORT: 2-5 words total
   - ⚠️ MUST be a SUBJECT (policy area, case, event, institution), NOT a rhetorical pattern or speech act. Words like "announcements", "contradictions", "promises", "denials", "flip-flops", "rhetoric", "pattern" describe FORM and are forbidden as the topic label. If the fact has no specific subject, you should have rejected it via REJECT rule 5b.
   - ❌ Bad: "policy announcements contradictions" / "campaign promises" / "rhetoric threats" / "denials reversals"
   - ✅ Good: "tariffs china 2025" / "strait-of-hormuz coalition" / "iran nuclear-deal" / "NATO article-5"
   - Examples:
     "a déclaré n'avoir aucun sentiment d'avoir commis un délit" → wrongdoing parliamentary-assistants-case
     "a promis de baisser l'impôt sur le revenu de 15%%" → tax-cut income-tax
     "a voté pour une hausse de 2%% de l'impôt sur le revenu" → tax-increase income-tax
     "a été condamnée à 4 ans de prison" → prison parliamentary-assistants-case
7. 'original_quote': Exact snippet (max ~250 chars) from the source text
8. 'involved_people': Other people MENTIONED in the fact who are NOT the actor/speaker.
   These people will NOT have this fact attributed to them — only subject_entity owns the fact

%s

## Input Data
Primary Subject: %s
Source URL: %s
Content:
%s

Return ONLY a valid JSON array — no commentary, no text before or after. Return [] if no verifiable facts found.
⚠️ JSON VALIDITY: You MUST produce valid JSON. Inside string values, ESCAPE double quotes as \" or replace them with single quotes. NEVER put raw " inside a JSON string value — this breaks parsing.

Example:
[
  {
    "reasoning": "Type 1: Le Pen's own denial of wrongdoing. Contradictable: she could later admit guilt. Verb: DENIED.",
    "subject_entity": "Marine Le Pen",
    "subject_entity_type": "human",
    "normalized_fact": "Marine Le Pen a déclaré n'avoir eu 'aucun sentiment d'avoir commis le moindre délit' lors de son procès en appel le 13 janvier 2026.",
    "canonical_verb": "DENIED",
    "embedding_text": "wrongdoing parliamentary-assistants-case",
    "original_quote": "nous n'avions aucun sentiment d'avoir commis le moindre délit, ce qu'elle appelle une 'bonne foi totale'",
    "involved_people": []
  }
]`

// FactExtractorConfig holds all parameters needed to build a fact extraction prompt.
type FactExtractorConfig struct {
	// TargetName is the name of the person or party to extract facts about.
	TargetName string
	// SourceURL is the URL where the content was found.
	SourceURL string
	// Content is the text content to extract facts from.
	Content string
	// PluginInstructions are optional additional instructions from a plugin.
	// These are injected into the prompt to customize extraction behavior.
	// Leave empty for default behavior.
	PluginInstructions string
}

// BuildFactExtractionPrompt combines the core prompt with optional plugin instructions.
// Returns the complete prompt ready for LLM consumption.
func BuildFactExtractionPrompt(cfg FactExtractorConfig) string {
	pluginSection := ""
	if strings.TrimSpace(cfg.PluginInstructions) != "" {
		pluginSection = fmt.Sprintf("## Additional Context\n%s", cfg.PluginInstructions)
	}

	return fmt.Sprintf(CoreFactExtractionPrompt,
		cfg.TargetName,     // First %s - target name in intro
		pluginSection,      // Second %s - plugin instructions section
		cfg.TargetName,     // Third %s - target name in input data
		cfg.SourceURL,      // Fourth %s - source URL
		cfg.Content,        // Fifth %s - content
	)
}

// Common plugin instruction templates that plugins can use or customize.
const (
	// PartyInstructions provides additional context for extracting facts about political parties.
	PartyInstructions = `When extracting facts about political parties:
- Parties cannot speak directly, so include statements from official spokespersons, party leaders, or official communications
- ONLY extract when the representative is EXPLICITLY speaking about THE PARTY, not personal views
- Track who made the statement on behalf of the party`

	// PersonWithPartiesInstructions provides context when a person has party affiliations.
	// Use fmt.Sprintf with this template, passing the comma-separated party names.
	PersonWithPartiesInstructions = `This person is affiliated with the following political parties: %s

When extracting facts:
- If the person speaks ON BEHALF OF their party (announcing party positions, policy), note this context
- Personal opinions vs official party positions: both are valid facts, just preserve the context`
)
