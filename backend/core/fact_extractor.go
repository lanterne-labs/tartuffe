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
A valid fact must be CONCRETE, SPECIFIC, and CONTRADICTABLE. It must describe something the entity DID or SAID with enough specificity that it could later be proven false or contradicted by the entity themselves.

✅ VALID - Concrete actions with specific details:
   - "X voted against bill Y on date Z"
   - "X signed agreement Y with party Z"
   - "X was sentenced to 4 years in prison and 5 years of ineligibility"
   - "X received €120,000 per year from party Y"

✅ VALID - Specific claims or denials that can be contradicted:
   - "X stated she had no feeling of committing a crime"
   - "X denied receiving any illegal funds"
   - "X confirmed she will run for the 2027 election"
   - "X stated RN never employed fictitious assistants"

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

### Concreteness Rule (CRITICAL)
Facts MUST contain specific, concrete information — numbers, dates, named actions, precise claims. Vague qualitative assessments are NOT facts.

❌ INVALID - Vague opinions without concrete data:
   - "Party X pays its leaders generously" ← "generously" is subjective, no numbers
   - "X is very popular" ← vague, no data
   - "X has a strong position on immigration" ← vague qualifier, no specifics

✅ VALID - Same ideas but with concrete data:
   - "Party X paid its president €120,000 per year" ← specific number
   - "X received 41%% of the vote in the first round" ← specific data
   - "X proposed to reduce immigration quotas by 50%%" ← specific policy

### Non-Contradictable Statements (CRITICAL)
We track facts to detect when someone CONTRADICTS themselves. Some statements can NEVER lead to a contradiction and must be EXCLUDED.

❌ INVALID - Scheduling, procedural, and calendar facts:
   - "X will learn her verdict on July 7" ← a court date, not contradictable
   - "X's trial will resume on March 15" ← scheduling, not a position
   - "The ruling will be handed down next month" ← procedural timing
   - WHY: A date can't be "contradicted" — it's just information about when something happens

❌ INVALID - Conditional, potential, or speculative statements:
   - "X risks a sentence of ineligibility that could prevent her from running" ← conditional "could"
   - "X might face up to 10 years in prison" ← speculative "might"
   - "X could lose her seat if convicted" ← hypothetical scenario
   - WHY: Potential outcomes are not facts — they haven't happened yet

❌ INVALID - Third-party commentary about the target's situation:
   - "X's lawyer warned the court that the election is near" ← the lawyer's action, not X's
   - "Analysts say X will struggle to win" ← third-party analysis
   - WHY: These are about OTHER people's actions or opinions regarding X, not X's own positions

❌ INVALID - Hopes, expectations, and wishes:
   - "X said she hoped for a quicker verdict" ← a hope, not a contradictable claim
   - "X expressed her desire for the proceedings to end" ← subjective wish
   - "X declared she expects to be acquitted" ← expectation, not a factual claim
   - WHY: Wishes and hopes can't be contradicted — they're internal states

❌ INVALID - Truisms and subjective observations:
   - "X said proving innocence after many years is difficult" ← obvious truism
   - "X stated the situation is complex" ← vague, unfalsifiable
   - "X said the trial was exhausting" ← personal feeling
   - "X said it's hard to defend oneself" ← meta-commentary

✅ VALID - Contradictable claims:
   - "X said they never received illegal funds" ← can be contradicted by evidence or their own later statements
   - "X stated they voted for bill Y" ← verifiable against official records
   - "X denied meeting with person Z" ← specific claim that can be disproven
   - "X was sentenced to 4 years in prison" ← concrete judicial outcome

ASK: "Could this person OR an authoritative source (courts, official records, investigations) ever prove the opposite? Could the person themselves say the opposite in the future?"
- "I never took illegal funds" → Could a court or the person themselves later contradict this? Yes. ✅ VALID
- "X will learn her verdict on July 7" → Could this be contradicted? No, it's just a date. ❌ REJECT
- "X risks a sentence of ineligibility" → Is this a fact? No, it's a potential outcome. ❌ REJECT
- "X's lawyer said the election is important" → Is this about X? No, it's about X's lawyer. ❌ REJECT

## KEY TEST (Apply to EVERY extracted fact)
Ask ALL THREE questions. The fact must pass ALL of them:

1. "Is this CONCRETE and SPECIFIC?" (contains numbers, names, specific actions — not vague qualifiers)
2. "Is this about the entity's OWN actions/statements or their affiliated organization?"
3. "Could the entity themselves CONTRADICT this in the future, or could evidence prove it false?"

If ANY answer is NO → REJECT the fact.

Examples:
✅ "Marine Le Pen a déclaré n'avoir commis aucun délit" → Concrete claim, about herself, contradictable ✅✅✅
✅ "Marine Le Pen a été condamnée à 4 ans de prison" → Specific sentence, about herself, factual ✅✅✅
✅ "Le RN a versé 120 000€ par an à sa présidente" → Concrete number, about affiliated party, verifiable ✅✅✅
❌ "Le RN rémunère grassement ses dirigeants" → Vague ("grassement"), no numbers ❌ FAILS test 1
❌ "Marine Le Pen sera fixée sur son sort le 7 juillet" → A calendar date, not contradictable ❌ FAILS test 3
❌ "Marine Le Pen risque une peine d'inéligibilité" → Conditional, hasn't happened ❌ FAILS test 3
❌ "L'avocat de Marine Le Pen a prévenu la cour" → About the lawyer, not about Le Pen ❌ FAILS test 2
❌ "Marine Le Pen espérait un délibéré plus rapide" → A hope/wish, not contradictable ❌ FAILS test 3
❌ "Marine Le Pen a déclaré que le Parlement a failli" → About Parliament, unaffiliated ❌ FAILS test 2

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
