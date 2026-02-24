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

**4. Not contradictable**
If a statement can NEVER lead to a self-contradiction, reject it. This includes:
- Hopes, wishes, preferences, feelings, trust
- Travel, logistics, sightings
- Courtroom behavior/procedure (listening, appearing, being called to the stand)
- Biographical facts, role descriptions, titles, historical election results
- Personal purchases, truisms, reactions to scheduling

**5. Journalist characterization without evidence**
A journalist's interpretation or narrative framing is NOT a valid fact. However, a journalist reporting a concrete, documented finding WITH evidence IS valid (Type 2).
- ❌ "X a été séduite par Trump" — journalist's narrative, no evidence
- ❌ "X adopte un ton plus pro-marché" — subjective assessment
- ✅ "X a déclaré soutenir Donald Trump" — X's own statement
- ✅ "An investigation revealed X received €50,000 from Y" — documented finding with specifics

**6. Vague or not concrete**
Facts MUST contain specific information: numbers, dates, named actions, precise claims. No vague qualifiers.
- ❌ "Party X pays its leaders generously" — no numbers
- ✅ "Party X paid its president €120,000 per year" — specific

**7. Polls, surveys, editorial comparisons**
Reject poll data, survey results, and journalist comparisons between entities.

## 4-Question Test (EVERY fact must pass ALL 4)
1. Is it CONCRETE and SPECIFIC? (contains numbers, names, dates, or specific actions — not vague qualifiers)
2. Is it the entity's OWN position/action, OR an authoritative outcome about them (court ruling, documented evidence)?
3. Can you imagine the entity later doing or saying the OPPOSITE? Or could documented evidence contradict this? (NOT: travel, scheduling, biography, feelings)
4. Does it represent a POLITICAL POSITION, PROMISE, DENIAL, or VERIFIABLE ACTION?

If ANY answer is NO → REJECT.

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
Return a JSON array. Each object has 7 fields:

1. 'subject_entity': The person/party WHO PERFORMS THE ACTION or MAKES THE STATEMENT described in the fact.
   This is NOT the person being talked about — it's the ACTOR/SPEAKER. Use full canonical name.
   Example: "Trump criticized Le Pen" → subject_entity = "Donald Trump" (he speaks), involved_people = ["Marine Le Pen"]
   Example: "Marine Le Pen denied wrongdoing" → subject_entity = "Marine Le Pen" (she denies)
2. 'subject_entity_type': One of "human", "party", or "organization"
3. 'normalized_fact': Concise statement of what the entity DID or SAID. Include full name, specific action/statement, negations, dates, quantities.
   ⚠️ LANGUAGE: Write in the entity's NATIVE LANGUAGE (e.g., French for French politicians). NEVER in English.
   ⚠️ NO CANONICAL VERBS: NEVER use DENIED, CLAIMED, PROMISED, WAS_CONVICTED, etc. in this field. Use natural language only. Write "a nié", "a affirmé", "a promis" — NOT "a DENIED" or "a CLAIMED".
4. 'embedding_text': Canonical semantic fingerprint for vector similarity matching. ALWAYS in English.
   GOAL: Two facts expressing the same meaning MUST produce identical or near-identical embedding_text. Be as GENERIC as possible while still capturing the core meaning. Strip all specific numbers, percentages, thresholds — keep only the abstract action and policy domain. If someone "promised to cut taxes by 15%%" and someone else "promised to reduce income tax", both should produce the SAME embedding_text.
   - Format: CANONICAL_VERB [broad policy domain] [one specific detail only if needed]
   - CANONICAL_VERB must be one of: DENIED, CLAIMED, PROMISED, SUPPORTED, OPPOSED, VOTED_FOR, VOTED_AGAINST, PROPOSED, CONFIRMED, ADMITTED, REFUSED, DEMANDED, THREATENED, WAS_SENTENCED, WAS_CONVICTED, WAS_ACQUITTED, WAS_FINED, RECEIVED_FUNDS, CHANGED_POSITION, ANNOUNCED
   - Topic abstraction: use the BROADEST policy domain that captures the core meaning, then add ONE specific detail only if needed to distinguish from other facts by the same person. Related positions MUST cluster: "more financial freedom for workers" and "reduce holidays for workers" are both "workers-social-rights".
   - REMOVE: all person/party/organization names, dates, temporal context, language-specific phrasing, specific numbers/percentages/amounts
   - Keep it SHORT: 3-6 words total
   - Examples:
     "a déclaré n'avoir aucun sentiment d'avoir commis un délit" → DENIED wrongdoing parliamentary-assistants-case
     "a nié tout détournement de fonds publics" → DENIED wrongdoing parliamentary-assistants-case
     "a promis de baisser l'impôt sur le revenu de 15%%" → PROMISED tax-cut income-tax
     "a promis de ne jamais augmenter les impôts" → PROMISED no-tax-increase
     "a voté pour une hausse de 2%% de l'impôt sur le revenu" → VOTED_FOR tax-increase income-tax
     "a été condamnée à 4 ans de prison" → WAS_SENTENCED prison parliamentary-assistants-case
5. 'original_quote': Exact snippet (max ~250 chars) from the source text
6. 'topics': 1-3 relevant keywords
7. 'involved_people': Other people MENTIONED in the fact who are NOT the actor/speaker.
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
    "subject_entity": "Marine Le Pen",
    "subject_entity_type": "human",
    "normalized_fact": "Marine Le Pen a déclaré n'avoir eu 'aucun sentiment d'avoir commis le moindre délit' lors de son procès en appel le 13 janvier 2026.",
    "embedding_text": "DENIED wrongdoing parliamentary-assistants-case",
    "original_quote": "nous n'avions aucun sentiment d'avoir commis le moindre délit, ce qu'elle appelle une 'bonne foi totale'",
    "topics": ["procès assistants parlementaires", "appel 2026"],
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
