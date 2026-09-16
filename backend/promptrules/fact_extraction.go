package promptrules

import (
	"fmt"
	"strings"
	"time"
)

// CoreFactExtractionPrompt is THE single prompt template for fact extraction.
// Extracts facts about ALL relevant entities mentioned in an article, not just a single target.
// Plugins can optionally inject additional instructions via the {{PLUGIN_INSTRUCTIONS}} placeholder.
const CoreFactExtractionPrompt = `You are an expert AI assistant specialized in extracting **verifiable factual statements** from news articles.

TODAY'S DATE: %s (YYYY-MM-DD). Use this as the reference point for any relative date phrases ("2 days ago", "il y a 3 jours", "yesterday", "hier", "la semaine dernière") you encounter — convert each to an absolute YYYY-MM-DD value when populating date fields below.

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
Anything that hasn't happened yet or is uncertain: "risque" / "could" / "may" / "if convicted", future scheduling, conditional outcomes.
- ❌ "X risks ineligibility" — speculative
- ❌ "X will learn her verdict on July 7" — future scheduling

⚠️ EXCEPTION — the entity's own conditional promise/threat IS a valid Type 1 fact. Decisive test: **who controls the action AFTER the "if"**?
- If the action is performed BY THE SUBJECT, it's a contradictable commitment — KEEP.
- If the action is performed by SOMEONE ELSE or impersonal forces ("the regime will fall", "markets will collapse"), it's a PREDICTION — REJECT.
- ✅ "Trump said he will strike Iranian power stations IF Iran doesn't reopen Hormuz by 20:00" — HE strikes. Type 1 threat.
- ✅ "X promised to resign IF the bill passes" — HE resigns. Type 1 promise.
- ❌ "Trump claimed Iran can fall in one night IF pressure continues" — Iran falls, not Trump. Prediction.

⚠️ CALLS, DEMANDS, ADVICE and ULTIMATUMS are NEVER predictions even when the
action falls to a third party — the entity TOOK A POSITION on what should happen,
and that position is contradictable (the entity can later reverse, soften, drop, or
deny it). KEEP them as Type 1 STANCES.
- ✅ "X demanded the government scrap the immigration bill" — Type 1 demand (X holds a position; could later support the bill).
- ✅ "X called for a coalition without Y" — Type 1 call.
- ✅ "X advised the regulator DGCCRF to investigate Z" — Type 1 advice.
- ✅ "X issued an ultimatum that Iran reopen Hormuz by 20:00" — Type 1 ultimatum (X's stance; he can later drop the deadline).
The third-party-action rejection above applies ONLY to FORECASTS (X stated/predicted what someone else WILL do), NOT to STANCES (X demanded/called/advised what someone else SHOULD do).

**4. Not contradictable**
If a statement can NEVER lead to a self-contradiction, reject it. This includes:
- Hopes, wishes, preferences, feelings, trust
- Travel, logistics, sightings
- PASSIVE events the entity did not choose or act on: being invited/scheduled/expected, an event happening to or around them, a date being set. ❌ "X est invité à un banquet le 13 juin" ❌ "L'issue de l'appel de X sera connue le 7 juillet" — these are not the entity's statement or action.
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
A summary statement describing the entity's HABITUAL behavior, RHETORICAL PATTERN, or repeated tendency is NEVER a valid fact — no single subject/quote/event/date can attach to a pattern. A fact must point to ONE concrete claim, action, or outcome.
- ❌ "X has repeatedly made grand announcements he later contradicts" — describes a pattern
- ❌ "Z has a long history of denying climate change" — meta-summary
- ✅ "Z stated on April 5 that climate change is a hoax" — single dated claim
- ✅ "Y voted against the immigration bill on March 12" — single concrete vote
RED FLAGS for PATTERN: "repeatedly" / "régulièrement", "has a history of" / "multiplié", "tends to", "is known for", "pattern of", "every time".

**6. Vague or not concrete**
Facts MUST contain specific information: numbers, dates, named actions, precise claims. No vague qualifiers.
- ❌ "Party X pays its leaders generously" — no numbers
- ✅ "Party X paid its president €120,000 per year" — specific

**7. Polls, surveys, ratings, rankings — ALWAYS reject (frequent error)**
An approval rating, popularity or confidence score, voting-intention figure, candidate ranking, or any "%%" attributed to a poll/survey/opinion is a THIRD-PARTY MEASUREMENT, NOT the entity's own contradictable statement — reject it even when the entity is the subject of the number, and even when no source is named. The entity did not say it, and a snapshot percentage cannot self-contradict (two snapshots at different numbers are not a lie).
- ❌ "L'opinion favorable pour Macron est tombée à 18%%" — poll snapshot (the entity did not claim it)
- ❌ "La cote de popularité de Lecornu a atteint 26%% (+2 points)" — rating
- ❌ "Bardella est en tête du classement des candidats avec 38%% selon le sondage" — ranking/poll
- ❌ "Attal jouit d'une image positive auprès de 32%% des Français" — survey result
- ✅ "Macron a affirmé que sa politique porte ses fruits" — the entity's OWN claim is fine (you do NOT need the poll number to record his statement)

**8. Out-of-scope subject — not a political actor (frequent error)**
This service scores the honesty of POLITICAL actors: officeholders, candidates, parties, governments, political institutions, and organizations acting in the political arena. If the subject_entity is an athlete, musician, actor, entertainer, influencer, business/company, or media outlet acting OUTSIDE politics, REJECT the fact — even when the statement itself is concrete and verifiable.
- ❌ "Jimin a déclaré que le concert à Paris était le plus grand public de l'histoire de BTS" — entertainment
- ❌ "Didier Deschamps a annoncé la composition de l'équipe de France" — sports
- ❌ "Paul Graham a affirmé que les startups devraient..." — tech commentary, no political role
- ✅ "Gérald Darmanin a affirmé..." — officeholder
- ✅ A CEO testifying before parliament, or a celebrity endorsing a candidate or contesting an election law, IS in scope — a non-political figure enters scope only when the FACT itself is political.

## Self-Consistency Check
Before finalizing each fact, re-read your normalized_fact and verify:
- Is it a poll/rating/ranking number (rule 7) or a passive event the entity didn't act on — an invitation, a scheduled date, an outcome that "will be known" (rules 3, 4)? If so, DROP it. These are the two most common bad facts.
- Is the subject a political actor, or is the fact itself political (rule 8)? If neither, DROP it.
- Is the subject_entity the one who actually MADE the statement or PERFORMED the action? A measurement or event ABOUT the entity is not the entity's own fact.
- Does the sentence make logical sense on its own? If the first half contradicts the second half, you have misread the source.
- Pay special attention to NEGATION CHAINS: "denied that X lacks Y" vs "accused X of lacking Y" mean OPPOSITE things. When the source says "X ne fait pas preuve de Y" (X does not show Y), the entity is ACCUSING X of lacking Y, NOT denying that X lacks Y.
- GROUNDING — do NOT over-specify: every specific in normalized_fact (a date, a named body like "GIEC"/IPCC, a figure like "30 milliards", a place like "le Salvador", a motive like "pour déstabiliser l'Europe", an outlet/byline like "Politico") MUST appear in original_quote. If the quote is generic ("les scientifiques"), keep the fact generic — do NOT swap in a specific name the article didn't attribute. If the quote carries no date, add no date. Strip any detail the quote doesn't prove, or drop the fact. NEVER import a detail from a different sentence or source.
- ATTRIBUTION — the subject must be the person who actually spoke or acted. A statement by an aide, deputy, spokesperson, "le bras droit de X", "un proche de X", "l'entourage de X", or a party colleague is NOT X's statement: attribute it to the actual speaker BY NAME — provided that speaker is themselves a political actor in scope (the scope rule below still applies: a named coach, singer, or CEO speaking outside politics is still REJECTED) — or DROP the fact if that speaker is not named. A descriptive role is never a valid subject_entity — never emit a subject like "le bras droit de Jean-Luc Mélenchon"; an unnamed speaker means no fact. A social-media handle (e.g. "@eciotti") names exactly one person — the one whose name the handle spells (@eciotti = Éric Ciotti). Resolve it ONLY from the handle's letters, NEVER from surrounding context or from what you know about the real world: "mon ami @eciotti, aujourd'hui maire de Nice" is a fact about Ciotti even if you believe someone else is mayor of Nice. If you cannot name the handle's owner from the handle itself, keep the handle text verbatim. NEVER substitute a different politician's name for a handle or an unnamed speaker.
- QUALIFIERS — preserve hedges and scope limiters from the quote ("en partie", "notamment", "selon lui", "sous conditions", "à ce stade"). Dropping "en partie" from "en partie responsable" turns a partial claim into an absolute one the entity never made.
- EVENT DATE ≠ PUBLISH DATE — the article's publication date is NOT the date of the event it reports. Date an event only from the quote/article's own dating of that event ("hier", "le 14 juillet", "lundi dernier" — converted via TODAY'S DATE); if the event itself is undated, leave the date out.
- If you catch an internal contradiction, re-read the original_quote carefully and rewrite.

## Consolidation
- MERGE facts expressing the same core claim in different words. Keep ONLY the most specific version.
- Typical article yields 2-5 valid facts. If you have more than 6, re-check against the rules.
- Extract in the entity's NATIVE LANGUAGE. Never duplicate across languages.
- Quality over quantity: fewer correct facts is ALWAYS better than including invalid ones.

## JSON Output Format
Return a JSON array. Each object has 10 fields (in this order):

1. 'reasoning': Think step-by-step BEFORE committing to the other fields. Ask yourself: What type of fact is this (Type 1 or Type 2)? Which canonical verb best describes the stance? Is this truly contradictable? This field is for your analysis — be concise (1-2 sentences).
2. 'subject_entity': The person/party WHO PERFORMS THE ACTION or MAKES THE STATEMENT described in the fact.
   This is NOT the person being talked about — it's the ACTOR/SPEAKER. Use full canonical name.
   Example: "Trump criticized Le Pen" → subject_entity = "Donald Trump" (he speaks), involved_people = ["Marine Le Pen"]
   Example: "Marine Le Pen denied wrongdoing" → subject_entity = "Marine Le Pen" (she denies)
3. 'subject_entity_type': MUST be EXACTLY one of these three lowercase strings (no other values are accepted; facts with any other value will be rejected):
   "human" | "party" | "organization"
4. 'normalized_fact': Concise statement of what the entity DID or SAID. Include full name, specific action/statement, negations, dates, quantities. Write in the SAME LANGUAGE as the SOURCE ARTICLE (not the entity's nationality — a French speech reported in English stays English). Use natural language, NEVER the canonical verbs ("a nié" / "denied", not "a DENIED").
4b. 'lang': REQUIRED. BCP-47 code matching the language of 'normalized_fact' ("fr", "en", "de", "es", ...). Missing or non-BCP-47 values are rejected at parse time.
   Do NOT translate here — write ONLY 'normalized_fact' in the source language. Translations are produced separately by the translation phase.
5. 'canonical_verb': MUST be EXACTLY one of these 20 UPPERCASE tokens (no synonyms, no inflections, no lowercase, no spaces — facts with any other value will be rejected):
   DENIED | CLAIMED | PROMISED | SUPPORTED | OPPOSED | VOTED_FOR | VOTED_AGAINST | PROPOSED | CONFIRMED | ADMITTED | REFUSED | DEMANDED | THREATENED | WAS_SENTENCED | WAS_CONVICTED | WAS_ACQUITTED | WAS_FINED | RECEIVED_FUNDS | CHANGED_POSITION | ANNOUNCED
   Pick the closest token; do NOT invent new ones (e.g. NEVER return "WAIVED", "SIGNED", "CONVICTED" — use the closest match like ANNOUNCED, PROPOSED, WAS_CONVICTED).
6. 'embedding_text': Topic-only semantic fingerprint for vector similarity, in the SAME language as the fact. NO verb prefix.
   GOAL: Two facts about the same topic MUST produce near-identical embedding_text REGARDLESS of stance (canonical_verb captures stance separately).
   - Format: [broad policy domain] [one specific detail only if needed]. 2-5 words.
   - Use the BROADEST policy domain so opposing positions cluster ("more financial freedom for workers" and "reduce holidays for workers" → both "workers-social-rights").
   - REMOVE: canonical verbs, person/party names, dates, numbers/percentages.
   - ⚠️ MUST be a SUBJECT (policy area, case, event), NOT a speech act. Words like "announcements", "promises", "denials", "rhetoric", "pattern" are FORBIDDEN — if the fact has no specific subject, reject it via rule 5b.
   - ❌ "campaign promises" / "rhetoric threats" / "denials reversals"
   - ✅ "droits-douane chine" / "coalition détroit-hormuz" / "accord-nucléaire iran"
   - More French examples: "baisse-impôts impôt-revenu" (raise OR cut), "prison affaire-assistants-parlementaires"
7. 'original_quote': Exact verbatim snippet (max ~250 chars) from the source text that SUBSTANTIATES THE WHOLE fact. Every concrete detail in normalized_fact — names, dates, numbers, places, the action, and any motive/attribution — must appear in (or be directly entailed by) this quote. If a detail is not in a quote you can cite, it does NOT belong in normalized_fact. Never stitch a quote together from distant fragments, and never borrow a detail from a DIFFERENT statement or source in the article.
8. 'involved_people': Other people MENTIONED in the fact who are NOT the actor/speaker.
   These people will NOT have this fact attributed to them — only subject_entity owns the fact
9. 'article_published_at': OPTIONAL ISO 8601 timestamp when THIS ARTICLE was published. Use the most precise form supplied ("YYYY-MM-DD", "YYYY-MM-DDTHH:MM:SS", or full RFC3339 with timezone). Set this when the body explicitly states either (a) an absolute date/time in a byline ("Publié le 19/05/2026 à 09h", "Posted on May 19, 2026"), OR (b) a relative phrase ("Publié hier", "Posted 2 days ago", "il y a 3 heures") — resolve relative phrases to absolute values using TODAY'S DATE above, preserving the implied hour when given. OMIT this field entirely otherwise. DO NOT guess from article content, topic, or named events.

%s

## Input Data
Primary Subject: %s
Source URL: %s
Content:
%s

Return ONLY a valid JSON array — no commentary, no text before or after. Return [] if no verifiable facts found.
⚠️ DO NOT INVENT FACTS ABOUT THE PRIMARY SUBJECT if the Content does not contain
clear, attributable claims or actions by them. If the Primary Subject is barely
mentioned or absent, still extract facts about OTHER entities that ARE present
in the Content (the system stores facts under each fact's own subject_entity
field, not under the Primary Subject). Only return [] when the Content contains
NO verifiable facts about ANY entity. The Primary Subject being off-topic is NOT
by itself a reason to return [] — extract whatever real facts are there about
whoever is there.
⚠️ JSON VALIDITY: You MUST produce valid JSON. Inside string values, ESCAPE double quotes as \" or replace them with single quotes. NEVER put raw " inside a JSON string value — this breaks parsing.

Example (French source — note 'lang' = "fr", NO translation field, 'article_published_at' from explicit "Publié le ..." byline):
[
  {
    "reasoning": "Type 1: Le Pen's own denial of wrongdoing. Contradictable: she could later admit guilt. Verb: DENIED.",
    "subject_entity": "Marine Le Pen",
    "subject_entity_type": "human",
    "normalized_fact": "Marine Le Pen a déclaré n'avoir eu 'aucun sentiment d'avoir commis le moindre délit' lors de son procès en appel le 13 janvier 2026.",
    "lang": "fr",
    "canonical_verb": "DENIED",
    "embedding_text": "délits affaire-assistants-parlementaires",
    "original_quote": "nous n'avions aucun sentiment d'avoir commis le moindre délit, ce qu'elle appelle une 'bonne foi totale'",
    "involved_people": [],
    "article_published_at": "2026-01-14T09:00:00+01:00"
  }
]

For an English source: set 'lang': "en" and write 'normalized_fact' / 'embedding_text' in English. All other rules are identical.`

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
	// Now is the reference time the LLM uses to resolve relative date phrases
	// it sees in the article body ("2 days ago", "il y a 3 jours"). Zero value
	// falls back to time.Now() at prompt-build time — tests should pin it.
	Now time.Time
}

// BuildFactExtractionPrompt combines the core prompt with optional plugin instructions.
// Returns the complete prompt ready for LLM consumption.
func BuildFactExtractionPrompt(cfg FactExtractorConfig) string {
	pluginSection := ""
	if strings.TrimSpace(cfg.PluginInstructions) != "" {
		pluginSection = fmt.Sprintf("## Additional Context\n%s", cfg.PluginInstructions)
	}

	now := cfg.Now
	if now.IsZero() {
		now = time.Now()
	}

	return fmt.Sprintf(CoreFactExtractionPrompt,
		now.Format("2006-01-02"), // First %s - TODAY'S DATE for relative-date resolution
		cfg.TargetName,           // Second %s - target name in intro
		pluginSection,            // Third %s - plugin instructions section
		cfg.TargetName,           // Fourth %s - target name in input data
		cfg.SourceURL,            // Fifth %s - source URL
		cfg.Content,              // Sixth %s - content
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
