package core

import (
	"fmt"
	"strings"
)

// CoreFactExtractionPrompt is THE single prompt template for fact extraction.
// Extracts facts about ALL relevant entities mentioned in an article, not just a single target.
// Plugins can optionally inject additional instructions via the {{PLUGIN_INSTRUCTIONS}} placeholder.
const CoreFactExtractionPrompt = `You are an expert AI assistant specialized in extracting **verifiable factual statements** from news articles.

## Your Task
Extract facts about ALL relevant persons, parties, and organizations mentioned in the article.
Focus especially on the primary subject '%s' but do NOT limit extraction to only that entity.

## What Constitutes a VALID Fact
Extract facts that are ABOUT an entity's own actions or statements:

✅ VALID - Actions an entity took:
   - "X voted against bill Y"
   - "X attended meeting Z"
   - "X signed agreement Y"
   - "X was sentenced to / convicted of / acquitted of..."
   - "Parliament X contested claim Y"

✅ VALID - Statements about THEMSELVES or their own situation:
   - "X stated she had no feeling of committing a crime"
   - "X denied the allegations against her"
   - "X confirmed she will run for election"
   - "X refused to acknowledge the charges"

## What to EXCLUDE (CRITICAL - READ CAREFULLY)

We track facts to detect CONTRADICTIONS in an entity's OWN positions over time.
If X says "Y failed" and Y says "I didn't fail" - that's NOT a contradiction, just disagreement between unrelated parties.

### The Affiliation Rule
Facts are VALID when they're about:
1. The entity ITSELF (their own actions, statements about themselves)
2. An entity they are AFFILIATED WITH or REPRESENT (their party, their organization)

Facts are INVALID when they're about:
- EXTERNAL entities the speaker has NO affiliation with

WHY? Because:
- If Marine Le Pen says "Rassemblement national never hid anything" → She represents RN, so she's accountable if RN did hide something
- If Marine Le Pen says "Parliament failed its duty" → She has no control over Parliament, so if Parliament disagrees, it's just two parties disagreeing

❌ INVALID - Opinions/claims about UNAFFILIATED external entities:
   - "X said the Parliament did not play its role" ← INVALID: X doesn't represent Parliament
   - "X declared the court was biased" ← INVALID: X doesn't represent the court
   - "X believes the economy is failing" ← INVALID: X doesn't control the economy
   - "X accused Y (an opponent) of corruption" ← INVALID: X has no affiliation with Y

✅ VALID - Claims about affiliated entities:
   - "Marine Le Pen stated Rassemblement national never hid anything" ← VALID: she leads/represents RN
   - "A deputy stated their party supports this policy" ← VALID: they represent the party
   - "The party leader denied the party received illegal funds" ← VALID: speaking for their organization

❌ INVALID - Third-party statements/claims about someone:
   - "Critics say X is corrupt" ← third-party opinion
   - "Y accused X of fraud" ← Y's claim, not verified fact

❌ INVALID - Journalist opinions, editorial commentary, general descriptions

### Non-Contradictable Statements (CRITICAL)
We track facts to detect when someone CONTRADICTS themselves. Some statements can NEVER be contradicted because they're truisms, subjective assessments, or unfalsifiable claims.

❌ INVALID - Truisms and self-evident statements:
   - "X said proving innocence after many years is difficult" ← Everyone agrees with this, it's obvious
   - "X stated the situation is complex" ← Vague, unfalsifiable
   - "X declared justice takes time" ← Universal truth, not a position

❌ INVALID - Subjective process observations:
   - "X said the trial was exhausting" ← Personal experience, can't be contradicted
   - "X found the proceedings lengthy" ← Subjective assessment
   - "X felt misunderstood by the court" ← Internal feeling, unfalsifiable

❌ INVALID - Rhetorical/defensive framing:
   - "X said it's hard to defend oneself in these circumstances" ← Meta-commentary
   - "X stated the evidence was hard to gather" ← Process complaint, not a factual claim

✅ VALID - Contradictable claims:
   - "X said they never received illegal funds" ← Can be contradicted by evidence, courts, or their own later statements
   - "X stated they voted for bill Y" ← Verifiable against official records
   - "X denied meeting with person Z" ← Specific claim that courts/investigations can disprove

ASK: "Could this person OR an authoritative source (courts, official records, investigations) ever prove the opposite?"
- "It's hard to prove innocence after 15 years" → Would any court ruling or official record contradict this? No, it's a truism. ❌ REJECT
- "I never took illegal funds" → Could a court ruling, investigation, or official documents prove otherwise? Yes. ✅ VALID
- "The trial process was unfair" → Can an authoritative source prove a process was objectively "fair"? No, it's subjective. ❌ REJECT
- "I did not embezzle EU funds" → Can EU auditors or courts prove otherwise? Yes. ✅ VALID

## KEY TEST (Apply to EVERY fact)
Ask TWO questions:
1. "Is this about the subject entity themselves OR an entity they're affiliated with?"
2. "If later contradicted, should the SUBJECT be held accountable?"

✅ "Marine Le Pen stated she is innocent" → About herself, accountable ✅
✅ "Marine Le Pen stated RN never hid anything" → About her party, she's accountable ✅
❌ "Marine Le Pen said Parliament failed" → About Parliament (unaffiliated), not her accountability ❌

More examples:
- "Marine Le Pen was sentenced to 4 years" ← ABOUT Marine Le Pen ✅
- "Marine Le Pen stated she had no intent to commit crime" ← ABOUT herself ✅
- "Marine Le Pen stated RN/Rassemblement national never hid anything" ← ABOUT her party ✅ (she represents it)
- "European Parliament contested lack of clarity" ← ABOUT European Parliament ✅
- "Marine Le Pen declared Parliament didn't play alert role" ← OPINION about Parliament ❌ (REJECT! She doesn't represent Parliament)
- "Marine Le Pen blamed the judges for bias" ← OPINION about judges ❌ (REJECT! She doesn't represent the court)

## Consolidation Rules (CRITICAL)
Your goal is to extract the SMALLEST number of facts that capture ALL distinct positions/actions.

1. **MERGE similar statements**: If an entity expresses the same position multiple times in different words, extract ONE fact that best captures that position.

2. **Language**: Extract facts in the entity's NATIVE LANGUAGE if available (e.g., French for French politicians). NEVER extract the same fact in multiple languages.

3. **Focus on distinct claims**: Each fact should represent a UNIQUE position, action, or statement.

4. **Quality over quantity**: An article might only contain 3-5 genuinely distinct factual claims per entity.

## JSON Output Format (REQUIRED)
Return a JSON array with objects containing these 7 fields:

1. 'subject_entity': The name of the person, party, or organization this fact is ABOUT.
   - Use the full, canonical name (e.g., "Marine Le Pen" not "Le Pen")
   - For parties: use official name (e.g., "Rassemblement national")
   - For institutions: use full name (e.g., "Parlement européen")

2. 'subject_entity_type': The type of entity. MUST be one of:
   - "human" - for individual people (politicians, lawyers, etc.)
   - "party" - for political parties (Rassemblement national, Les Républicains, etc.)
   - "organization" - for institutions, parliaments, courts, companies, etc.

3. 'normalized_fact': A concise statement of what the entity DID or SAID. Include:
   - The entity's full name
   - The specific action or statement (preserve precise verbs)
   - Negations (did NOT, denied, refused, rejected)
   - Relevant context (dates, locations, topics)
   - Key quantities and qualifiers

4. 'embedding_text': A UNIQUE fingerprint for semantic similarity matching. Two facts should ONLY be grouped if they describe THE EXACT SAME claim.

   LANGUAGE: Use the SAME language as the source content (French for French articles).

   STRUCTURE:
   1. ACTION VERB (a déclaré, a nié, a voté, a été condamné, etc.)
   2. SPECIFIC CONTENT (what was said/done)
   3. IDENTIFYING CONTEXT (which case/event - only if needed to distinguish)

   VOCABULARY CONSISTENCY (CRITICAL):
   - ALWAYS use the MOST COMMON word for each concept to maximize similarity matching
   - Use consistent verbs: "a nié" (not "a contesté", "a réfuté", "a démenti")
   - Use consistent nouns: "système organisé" (not "organisation systématique")
   - If two facts mean the same thing, their embedding_text MUST be identical or near-identical

   CONTEXT RULES:
   - REMOVE generic context: "lors du procès", "à l'audience", "en janvier 2026"
   - KEEP identifying context: case names, affair names (e.g., "affaire des assistants parlementaires")
   - REMOVE all person names, party names, and organization names

   Examples:
   - "a déclaré n'avoir aucun sentiment d'avoir commis un délit dans l'affaire des assistants parlementaires"
   - "a été condamné à 4 ans de prison avec 5 ans d'inéligibilité dans l'affaire des assistants parlementaires"
   - "a nié l'existence d'un système organisé dans l'affaire des assistants parlementaires"

5. 'original_quote': The exact snippet (max ~250 chars) from the text supporting this fact.

6. 'topics': 1-3 relevant keywords (include specific identifiers like bill numbers or years)

7. 'involved_people': Other people mentioned IN the fact (NOT the subject_entity). Do NOT include organizations.

%s

## Input Data
Primary Subject: %s
Source URL: %s
Content:
%s

Your response must contain ONLY a valid JSON array and nothing else — no commentary, no explanation, no text before or after the JSON. Return [] if no verifiable facts are found.

Example Output Format:
[
  {
    "subject_entity": "Marine Le Pen",
    "subject_entity_type": "human",
    "normalized_fact": "Marine Le Pen a déclaré n'avoir eu 'aucun sentiment d'avoir commis le moindre délit' lors de son procès en appel le 13 janvier 2026.",
    "embedding_text": "a déclaré n'avoir aucun sentiment d'avoir commis un délit dans l'affaire des assistants parlementaires",
    "original_quote": "nous n'avions aucun sentiment d'avoir commis le moindre délit",
    "topics": ["procès assistants parlementaires", "appel 2026"],
    "involved_people": []
  },
  {
    "subject_entity": "Louis Aliot",
    "subject_entity_type": "human",
    "normalized_fact": "Louis Aliot a déclaré 'J'ai le sentiment d'avoir été ni compris ni totalement entendu' lors du procès en appel des assistants parlementaires.",
    "embedding_text": "a déclaré avoir le sentiment de ne pas avoir été compris dans l'affaire des assistants parlementaires",
    "original_quote": "J'ai le sentiment d'avoir été ni compris ni totalement entendu dans les explications",
    "topics": ["procès assistants parlementaires", "appel 2026"],
    "involved_people": []
  },
  {
    "subject_entity": "Parlement européen",
    "subject_entity_type": "organization",
    "normalized_fact": "Le Parlement européen a contesté tout 'manque de clarté' dans ses règles concernant les assistants parlementaires.",
    "embedding_text": "a nié tout manque de clarté dans les règles concernant les assistants parlementaires",
    "original_quote": "Il ne faut pas se réfugier derrière un manque de clarté",
    "topics": ["règles Parlement européen", "assistants parlementaires"],
    "involved_people": ["Didier Klethi"]
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
