package entityvalidator

import (
	"context"
	"encoding/json"
	"fmt"

	"digital-mirror/backend/llm"
)

// LLMFilter is the arbiter for "wiki found some pages but are any of them
// actually about this candidate". The pipeline only reaches it when WikiFilter
// has Deferred (i.e. >=1 page was retrieved and stashed in Extra). LLMFilter
// reads those snippets via Candidate.PrevByName("wiki"), constructs a JSON
// input payload, and asks the model to decide.
//
// The LLM output is constrained to JSON via ResponseFormat=json_object. On any
// kind of LLM failure (network, JSON parse, missing env) Check returns
// (Defer, err) — the Validator logs the error and treats it as Defer, so a
// temporary LLM outage cannot turn into mass-deletion of real entities.
type LLMFilter struct {
	client    *llm.Client
	systemMsg string
}

// NewLLMFilter reads OPEN_AI_API + OPEN_AI_KEY + OPEN_AI_CHAT_MODEL from the
// environment (same convention the rest of the codebase uses). Returns an
// error if OPEN_AI_API is unset — call sites can decide whether to skip the
// filter or hard-fail. An unset key is not an error here.
func NewLLMFilter() (*LLMFilter, error) {
	cfg := llm.ConfigFromEnv()
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("OPEN_AI_API not set")
	}
	return &LLMFilter{
		client: cfg.NewClient("entity-validator"),
		systemMsg: "You are an entity validation oracle. You decide whether a candidate " +
			"entity (with attached Wikipedia page snippets retrieved for it) is a real, " +
			"notable, individually identifiable entity that warrants its own database row. " +
			"Respond with valid JSON only.",
	}, nil
}

func (l *LLMFilter) Name() string { return "llm" }

// llmInput is the JSON payload embedded verbatim in the user prompt. Using a
// marshaled struct (rather than fmt.Sprintf with %q) handles quoting and
// escaping cleanly — newlines, smart quotes, and embedded JSON in the snippets
// all survive intact.
type llmInput struct {
	Candidate struct {
		Name       string `json:"name"`
		EntityType string `json:"entity_type"`
	} `json:"candidate"`
	WikiPages  []WikiPage  `json:"wiki_pages"`
	GoogleHits []GoogleHit `json:"google_hits"`
}

type llmVerdict struct {
	Real   bool   `json:"real"`
	Reason string `json:"reason"`
}

func (l *LLMFilter) Check(ctx context.Context, c Candidate) (FilterResult, error) {
	if c.Name == "" {
		return FilterResult{Decision: Defer}, nil
	}

	in := llmInput{}
	in.Candidate.Name = c.Name
	in.Candidate.EntityType = c.EntityType

	if run, ok := c.PrevByName("wiki"); ok && len(run.Result.Extra) > 0 {
		var w wikiExtra
		if err := json.Unmarshal(run.Result.Extra, &w); err == nil {
			in.WikiPages = w.Pages
		}
	}
	if run, ok := c.PrevByName("google"); ok && len(run.Result.Extra) > 0 {
		var g googleExtra
		if err := json.Unmarshal(run.Result.Extra, &g); err == nil {
			in.GoogleHits = g.Hits
		}
	}

	inputJSON, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return FilterResult{Decision: Defer}, fmt.Errorf("marshal llm input: %w", err)
	}

	user := `You will receive a JSON object with:
  - "candidate":    the entity under examination ("name", "entity_type" ∈ {human, party, organization}).
  - "wiki_pages":   top Wikipedia results retrieved for the candidate (may be empty), each {"lang","title","extract"}.
  - "google_hits":  top Google web results for the candidate (may be empty), each {"title","snippet","url"}.

Decide whether the candidate is a real, NAMED entity that should be tracked
in a political-facts database. Real means: a single person, OR a single named
organization/party/association/institution/company with its own identity and
a name that people actually use to refer to it (not a description).

═══ ACCEPT signals (any one is sufficient) ═══

  A. The FIRST google_hit has a title that closely matches the candidate name
     AND describes a real company/org/association/institution/person (e.g.
     candidate "aaa data", first hit "AAA DATA - Données automobiles" with
     snippet about the company → ACCEPT).
  B. A wiki extract clearly describes the same person/org as the candidate.
  C. Multiple Google results (≥2) whose titles/snippets are clearly about the
     same person/org (e.g. an obscure local politician with several news
     articles consistently naming them as the subject).
  D. A close spelling / transliteration variant in either source (e.g.
     "Abdullatif Al Zayani" matching "Abdoullatif Ben Rachid Al Zayani";
     Korean script "그로 할렘 브룬틀란" matching "Gro Harlem Brundtland").

═══ REJECT — these are NOT real named entities ═══

  R1. Government-by-administration patterns: "administration X", "gouvernement X",
      "cabinet X", "présidence X", "administration américaine", "administration
      française" — these are DESCRIPTIVE LABELS people apply to whichever
      government is in power, NOT the institution's actual name. The real
      entities are the underlying offices ("White House", "Élysée", "Cabinet
      Office", "Maison Blanche") or the person ("Donald Trump", "Emmanuel
      Macron"). Reject all "administration/gouvernement/cabinet/présidence + name".
  R2. Meta-group / descriptive phrase referring to people around a real subject:
      "les avocats de marine le pen", "entourage of X", "collaborateurs de Y",
      "famille de Z", "proches de W", "les amis de V". News articles ABOUT a
      real person's lawyers/entourage do NOT make "the lawyers of X" a real
      tracked entity.
  R3. Composite of multiple people: "Alice and Bob", "Alice, Bob, Claude".
  R4. Abstract concept or bloc: "la gauche", "la droite", "l'opposition",
      "l'extrême droite", "the establishment".
  R5. Anonymous count: "27 députés", "100 protesters", "26 organisations".
  R6. Generic role without a proper noun: "the spokesperson", "a federal judge",
      "juges instructeurs", "porte-parole du gouvernement".
  R7. A law, code, decree, petition, or accord/agreement: "code de l'entrée…",
      "accord de Bougival", "pétition X". These are documents/events, not entities.
  R8. Fictional characters that are not political subjects.
  R9. Junk strings / extraction artifacts that don't look like any real entity
      name AND have no matching wiki/google evidence.
  R10. Real but OUT OF SCOPE for a political-facts tracker: athletes, coaches,
      musicians, actors, entertainers, influencers, tech/business figures,
      companies/brands, media outlets and magazines, sports bodies — any entity
      whose public identity per the wiki/google evidence is NON-political —
      UNLESS that evidence shows them holding or running for office, or leading
      a political organization. Examples: "didier deschamps" (football manager)
      → reject; "jimin" (K-pop singer) → reject; "fisheyelemag" (photography
      magazine handle) → reject; "volodymyr zelensky" (actor turned president)
      → accept. Advocacy organizations active in public/political debate
      (anti-racism leagues, unions, NGOs) stay IN scope — do not reject them
      under this rule.

═══ DECISION RULE ═══

If the candidate matches a REJECT pattern (R1–R10), return real=false EVEN IF
it also matches an ACCEPT signal — scope (R10) and shape (R1–R9) rejections
override existence evidence. Otherwise, if it matches an ACCEPT signal
(A/B/C/D), return real=true.
Note real=false here means "do not track", EVEN IF wiki/google
have hits about RELATED entities (e.g. "administration trump" gets google hits
about Trump policy — that's evidence about Trump, not about "administration
trump" as an entity; still reject).
Otherwise, lean ACCEPT when the name reads as a real proper noun + has any
plausible google evidence; lean REJECT when both sources are empty and the
name reads as a descriptive phrase.

Respond with ONLY this JSON, nothing else:
{"reason": "<one short sentence citing the deciding rule (A/B/C/D or R1–R9) and the evidence>", "real": true|false}

INPUT:
` + string(inputJSON)

	content, _, err := l.client.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "system", Content: l.systemMsg},
			{Role: "user", Content: user},
		},
		Temperature:    0.0,
		MaxTokens:      200,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		return FilterResult{Decision: Defer}, fmt.Errorf("llm request: %w", err)
	}

	v, err := llm.ParseJSONResponse[llmVerdict](content)
	if err != nil {
		return FilterResult{Decision: Defer}, fmt.Errorf("decode llm verdict %q: %w", content, err)
	}

	if v.Real {
		return FilterResult{Decision: Approve, Reason: "llm: " + v.Reason}, nil
	}
	return FilterResult{Decision: Reject, Reason: "llm: " + v.Reason}, nil
}
