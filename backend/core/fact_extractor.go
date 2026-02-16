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

## MANDATORY REJECTION (Read FIRST, apply BEFORE extracting)

We track facts to detect CONTRADICTIONS in an entity's OWN positions over time.
Before extracting ANY fact, check it against these AUTO-REJECT rules. If it matches ANY rule, do NOT include it.

### AUTO-REJECT TRIGGERS (check these words/patterns FIRST)
If a candidate fact contains any of these patterns, REJECT it immediately:

**Conditional/speculative language** → REJECT:
"risque", "pourrait", "pourra", "pourront", "pourrait être", "might", "could", "may", "if convicted"

**Hopes/wishes/preferences** → REJECT:
"espère", "espérait", "espérons", "souhaite", "souhaiterait", "hoped", "wishes", "desires", "mieux je me portais"

**Scheduling/calendar** → REJECT:
"sera fixé(e)", "sera rendu(e)", "est attendu(e) le", "le prononcé est prévu", "la décision sera", "will learn on", "verdict on [date]"

**Lawyer/legal representative as subject** → REJECT:
"Me ", "Maître ", "l'avocat(e) de", "a plaidé que", "a plaidé l'innocence", "les avocats de", "la défense de", "la défense a", "ont plaidé"

**Courtroom behavior/procedural** → REJECT:
"a écouté", "a plaidé", "a été appelé(e) à la barre", "a comparu", "a pris la parole au tribunal", "lors de l'audience", "au palais de justice", "à la barre"

**Biographical/role descriptions** → REJECT:
"est le ", "est la ", "est chef", "est cheffe", "est président", "est présidente", "a été élu(e)", "a prêté serment", "a acquis sa résidence", "a atteint le second tour"

**Personal trust/feelings** → REJECT:
"a maintenu sa confiance", "fait confiance à", "a embauché" (when hiring counsel)

**Journalist narrative framing** → REJECT:
"a joué sa candidature", "n'est plus prêt(e) à", "joue une candidature", "n'est plus prête à"

**Travel/logistics/sightings** → REJECT:
"est arrivé(e) à/en", "a été aperçu(e)", "était accompagné(e) de", "arrivée discrète", "descente d'avion"

**Reactions to scheduling/procedural events** → REJECT:
"a réagi à la date", "a réagi au calendrier", "more time would have been", "plus tôt c'était"

### Rule 1: Affiliation Rule
Facts are VALID when about:
1. The entity ITSELF (their own actions, statements about themselves)
2. An entity they are AFFILIATED WITH or REPRESENT (their party, their organization)

Facts are INVALID when about EXTERNAL entities the speaker has NO affiliation with.

❌ INVALID - About unaffiliated entities:
   - "X said the Parliament did not play its role" ← X doesn't represent Parliament
   - "X declared the court was biased" ← X doesn't represent the court
   - "X accused Y (an opponent) of corruption" ← X has no affiliation with Y

✅ VALID - About affiliated entities:
   - "Marine Le Pen stated RN never hid anything" ← she leads/represents RN
   - "The party leader denied the party received illegal funds" ← speaking for their organization

### Rule 2: Lawyers are NOT valid entities
Lawyers (Me/Maître X) acting in their PROFESSIONAL capacity are NEVER valid subject_entity.
- A lawyer pleading for their client is performing a job, not expressing a personal contradictable position
- "Me Bosselut a plaidé l'innocence de Marine Le Pen" → REJECT (lawyer doing their job)
- "Me Chirac-Kollarik a déclaré que travail fictif = zéro travail" → REJECT (lawyer argument in court)
- "Les avocats de Marine Le Pen ont plaidé la relaxe" → REJECT (lawyers as subject, even without "Me")
- "La défense de Marine Le Pen a demandé la relaxe" → REJECT (defense team as subject)
- EXCEPTION: A lawyer making claims about THEMSELVES outside court ("I never bribed anyone") would be valid

### Rule 3: Concreteness Rule
Facts MUST contain specific, concrete information — numbers, dates, named actions, precise claims. Vague qualitative assessments are NOT facts.

❌ "Party X pays its leaders generously" ← "generously" is subjective, no numbers
✅ "Party X paid its president €120,000 per year" ← specific number

### Rule 4: Non-Contradictable Statements
If a statement can NEVER lead to a self-contradiction, REJECT it.

❌ Scheduling/calendar: "X will learn her verdict on July 7" ← a date can't be contradicted
❌ Conditional/speculative: "X risks ineligibility" ← hasn't happened, "risque" = speculative
❌ Hopes/wishes: "X hoped for a quicker verdict" ← internal state, not contradictable
❌ Truisms: "X said defending oneself is hard" ← obvious, unfalsifiable
❌ Travel/logistics: "X arrived in Guadeloupe" ← mundane movement, not a position
❌ Reactions to events: "X said 'the sooner the better'" ← preference, not a political claim

✅ Contradictable claims:
   - "X said they never received illegal funds" ← can be contradicted by evidence
   - "X was sentenced to 4 years in prison" ← concrete judicial outcome (ALREADY happened)
   - "X denied meeting with person Z" ← specific denial that can be disproven

### Rule 5: Third-party statements are NOT facts about the target
- "X's lawyer warned the court" → about the LAWYER, not X
- "Analysts say X will struggle" → about analysts' opinions
- "Y didn't comment on X's situation" → about Y's non-action
- "Critics say X is corrupt" → third-party opinion

### Rule 7: Courtroom behavior is NOT a fact
How someone BEHAVED during a trial is NEVER a valid fact:
- How they listened, reacted, or spoke is a mundane observation, NOT a political position
- Being called to the stand is PROCEDURE, not a claim
- Courtroom demeanor cannot be contradicted in a meaningful way

❌ "X a écouté calmement la plaidoirie" → courtroom behavior, not a position
❌ "X a été appelée à la barre quatre heures après" → procedural, not contradictable
❌ "X a pris la parole au tribunal" → courtroom procedure
❌ "X a comparu devant la cour d'appel" → procedural appearance

### Rule 8: Biographical and role descriptions are NOT facts
Known titles, roles, historical election results, and personal purchases are NOT extractable facts:
- Titles and roles are common knowledge — they can't be contradicted
- Historical election results are established record
- Personal purchases (houses, cars) are not political positions

❌ "X est la cheffe de file des députés du RN" → role/title, common knowledge
❌ "X a atteint le second tour en 2017 et 2022" → historical results, common knowledge
❌ "X a acquis sa résidence principale en 2025" → personal purchase, not a position
❌ "X a prêté serment comme avocate en 1992" → biographical trivia

### Rule 6: Deduplication (STRICT)
If two candidate facts express the SAME CORE CLAIM in different words, keep ONLY ONE — the most specific version.
Example: "didn't commit a crime", "had no awareness of committing a crime", "couldn't imagine breaking a rule" → These ALL mean "claims innocence". Keep ONLY the most concrete version.

## KEY TEST (Apply to EVERY fact BEFORE including it)
Ask ALL FOUR questions. The fact must pass ALL of them:

1. "Is this CONCRETE and SPECIFIC?" (contains numbers, names, specific actions — not vague qualifiers)
2. "Is this about the entity's OWN actions/statements or their affiliated organization?" (NOT lawyers, NOT third parties)
3. "Could the entity themselves CONTRADICT this in the future, or could evidence prove it false?" (NOT hopes, NOT scheduling, NOT travel)
4. "Does this represent a POLITICAL POSITION, POLICY CLAIM, DENIAL, or VERIFIABLE ACTION?" (NOT logistics, NOT reactions to calendar events, NOT feelings)

If ANY answer is NO → REJECT the fact.

Examples:
✅ "Marine Le Pen a déclaré n'avoir commis aucun délit" → Specific denial, about herself, contradictable, political position ✅✅✅✅
✅ "Marine Le Pen a été condamnée à 4 ans de prison" → Specific, about herself, factual outcome, verifiable action ✅✅✅✅
✅ "Le RN a versé 120 000€ par an à sa présidente" → Concrete number, affiliated party, verifiable, financial action ✅✅✅✅
❌ "Le RN rémunère grassement ses dirigeants" → Vague ("grassement"), no numbers ❌ FAILS test 1
❌ "Marine Le Pen sera fixée sur son sort le 7 juillet" → Calendar date, not contradictable ❌ FAILS test 3
❌ "Marine Le Pen risque une peine d'inéligibilité" → Conditional ("risque"), hasn't happened ❌ FAILS test 3
❌ "Me Bosselut a plaidé l'innocence de Marine Le Pen" → About the lawyer, not about Le Pen ❌ FAILS test 2
❌ "Marine Le Pen espérait un délibéré plus rapide" → A hope/wish, not contradictable ❌ FAILS test 3
❌ "Marine Le Pen est arrivée en Guadeloupe" → Travel/logistics, not a political position ❌ FAILS test 4
❌ "Marine Le Pen a réagi en disant 'plus tôt c'était mieux'" → Preference/reaction, not a position ❌ FAILS test 4
❌ "Marine Le Pen a déclaré que le Parlement a failli" → About Parliament, unaffiliated ❌ FAILS test 2
❌ "Rody Tolassy n'a pas souhaité commenter" → Third-party non-action ❌ FAILS test 2
❌ "Marine Le Pen a écouté calmement la plaidoirie" → Courtroom behavior, not a position ❌ FAILS test 4
❌ "Marine Le Pen est la cheffe de file des députés RN" → Role description, common knowledge ❌ FAILS test 3
❌ "Marine Le Pen a acquis sa résidence en 2025" → Personal purchase, not a position ❌ FAILS test 4
❌ "Les avocats de Marine Le Pen ont plaidé la relaxe" → Lawyers as subject ❌ FAILS test 2
❌ "Marine Le Pen a joué sa quatrième candidature" → Journalist narrative framing ❌ FAILS test 4

## Consolidation Rules (CRITICAL)
Your goal is to extract the SMALLEST number of facts that capture ALL distinct positions/actions.
Typical article: 2-5 genuinely valid facts. If you have more than 6, you are probably including invalid ones. Re-check.

1. **MERGE aggressively**: If an entity expresses the same core idea in different words, keep ONLY ONE — the most specific, concrete version. Examples of facts that MUST be merged into one:
   - "n'avoir aucun sentiment d'avoir commis un délit" / "n'avoir pas la conscience de commettre un délit" / "n'avoir pu imaginer enfreindre une règle" → ALL mean "claims innocence" → keep ONE
   - "a nié tout détournement" / "n'a pas détourné de fonds" → same denial → keep ONE

2. **Language**: Extract facts in the entity's NATIVE LANGUAGE if available (e.g., French for French politicians). NEVER extract the same fact in multiple languages.

3. **Focus on distinct claims**: Each fact should represent a UNIQUE position, action, or statement that is semantically different from all others.

4. **Quality over quantity**: Fewer correct facts is ALWAYS better than more facts with invalid ones included. When in doubt, EXCLUDE.

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
