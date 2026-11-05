package categorizer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend"
	"digital-mirror/backend/llm"
)

// The GROUNDING check reads every extracted fact against its quote, after the
// extraction and before the insertion. The extraction prompt already forbids a
// fact to say more than its quote (GROUNDING, REFERENTS), but the extractor
// still adds claims: 7.1 pct of the text facts of the audit of 2026-11-01
// carried a condition, a motive, an attribution or a referent their quote
// does not (Le Pen "soutenir qu'il reprenne le flambeau dès 2027 si
// nécessaire" on « je suis la candidate de Jordan »; a fact naming the RN on
// « un projet qui n'est pas le bon »). A wrong fact is where most false
// contradictions come from, and the judges trust the fact text.
//
// One call per document: every fact with its quote and the text around the
// quote. Per fact the reply is
//   - grounded: the fact says what the quote says;
//   - rewrite: the fact keeps only what the quote supports (the subject, the
//     verb, what is said); when the referent the fact names is in the text
//     around, the checker may give a longer quote, a verbatim span of the
//     source that holds the original quote (REFERENTS option a), checked here;
//   - drop: the quote holds no statement or act of the subject (a score, a
//     third party's words). Whether a statement is a POSITION stays the
//     extractor's call: the checker only reads the fact against its quote.
//
// A claim counts as grounded only when the QUOTE holds it: the subject's own
// words next to the quote extend the quote (rewrite with a longer quote), they
// do not ground the fact as it is. Until the audit of 2026-11-02 they did, and
// in a debate document, where every sentence is the speaker's, 22 pct of the
// new facts carried a sentence their quote does not show (Juvin "la Corse
// n'est pas une collectivité comme une autre" on a quote that stops before it).
//
// Only free text goes through it: the frpolitics votes are built from their
// records and never reach the extractor.

// groundingBatch caps the facts of one call: a long article's facts go in
// several calls, each with the replies' room.
const groundingBatch = 12

// groundingWindow is how much of the source, on each side of the quote, the
// checker sees: enough for the question asked or the sentence before, not
// the article (the extraction already paid for that).
const groundingWindow = 400

// groundingMaxTokens is the reply's room per fact (the reasoning and a
// rewritten fact).
const groundingMaxTokens = 220

const groundingPrompt = `You check facts extracted from a source about a political figure, for a political honesty tracker. A fact must not claim what its source does not say: an added condition or reason becomes a false accusation. But a fact must also stand alone, in the third person, and keep the meaning of what was said.

For each fact you get its QUOTE (the words of the source it was extracted from), the TEXT AROUND the quote in the source, and the SOURCE DATE when known. The subject is %s.

The QUOTE is what the site shows under the fact and what the judges read: every claim of the fact must be in the QUOTE itself. The text around is there to find the referent of a pronoun and the occasion, and to make the quote longer when the fact needs it; it does not ground a claim the quote lacks, even when it holds the subject's own words (in a parliamentary debate EVERY sentence around is the speaker's own).

Judge each fact against its QUOTE:
- "grounded" (the default): every claim of the fact is in the quote. NOT additions, keep the fact as it is:
  - a pronoun or a "nous", "on", "notre camp" of the subject resolved to its referent: the person's party, camp, government or movement, written in the third person ("nous avons la capacité d'obtenir la majorité absolue" said by Marine Le Pen = "son camp a la capacité..."); a fact NEVER keeps the "nous", "je" or "vous" of the quote;
  - the subject's name, the date and the occasion (an interview, a debate, a post, the text or amendment under debate), and the event made plain by the source's date or the text around (a call to vote "dimanche" in a tweet of 4 July 2024 = the 2024 legislative elections);
  - a reported "il a affirmé que" turned into a direct statement, small differences of wording, a referent the quote's own words make unambiguous.
- "rewrite": when the fact carries a claim the QUOTE does not: a condition ("si nécessaire", "si le gouvernement..."), a reason or a goal ("afin d'éviter un second tour..."), an attribution ("selon le HCFP"), a judgement, a second claim, a contrast ("et non de...", "mais désormais..."), a detail from another sentence, or a referent taken from a QUESTION or another person's words while the subject's words have only a pronoun (« Si c'était ainsi, je partirais. » answering a journalist's question about the PS and the budget). This holds for a bare answer too: « Bien sûr. », « Oui », « C'est exactement ça » whose content is only in the journalist's question or in the article's own framing (an editorial sentence, the writer's comparison) are not the subject's words on that content — the fact keeps only what the subject's words carry (rewrite), or nothing is left (drop). Two ways, in this order of preference:
  1. if the added part is in the subject's OWN words next to the quote (the same sentence, the subject's sentence just before or after, or the article's plain factual report of what the subject said, asks for, proposes or did: « a déclaré ne pas voir d'autre solution que de se tourner vers les Français en organisant de nouvelles législatives » says what the subject asks), keep the fact and give "quote": the longer span COPIED EXACTLY from the text around, which must contain the whole original quote and every claim of the fact. Never extend it into a question or another person's words.
     The article's own judgement of the subject is never that report: a motive, a strategy, an interpretation (« pour faire pression sur l'Élysée », « signe de sa fébrilité », « une façon de ») goes to kind 2, even in the same sentence.
  2. else write "fact": the same fact without the added claim, in the third person, with the subject's full name and the same verb, keeping the MEANING of what the quote says. A rewrite must never change the meaning: do not cut a "ni... ni..., mais..." into something the person did not say, do not turn a rejection into a proposal, do not widen an objection to one part of a measure (who decides it, its length) into an objection to the whole measure. If no cut keeps the meaning, extend the quote (way 1); if that is not possible either, answer "drop".
- "drop": the quote holds no statement or act of the subject (an electoral score the subject obtained, a third party's words about the subject), or nothing would be left of the fact.

When unsure, answer "grounded". Judge only against the source, never against what you know.

FACTS:
%s
Return ONLY strict JSON, one entry per fact in the same order, the reasoning first and the verdict last:
{"facts": [{"n": 1, "reasoning": "<what the quote says, and what the fact claims beyond it if anything (and whether the subject's own words next to the quote carry it)>", "fact": "<the rewritten fact, only for a rewrite of kind 2>", "quote": "<the longer verbatim quote, only for a rewrite of kind 1>", "verdict": "grounded" | "rewrite" | "drop"}]}`

type groundingEntry struct {
	N         int    `json:"n"`
	Reasoning string `json:"reasoning"`
	Fact      string `json:"fact"`
	Quote     string `json:"quote"`
	Verdict   string `json:"verdict"`
}

type groundingReply struct {
	Facts []groundingEntry `json:"facts"`
}

// GroundingOutcome counts what the check did to a document's facts.
type GroundingOutcome struct {
	Grounded, Rewritten, Requoted, Dropped, Unchecked int
}

// groundFacts checks the facts of one document against their quotes and
// returns the facts kept, rewritten where they added a claim. source is the
// document's text, where the quotes are looked up for their context. A call
// that fails because no endpoint serves (llm.ErrUnavailable) fails the
// document, which is extracted again later; a reply that does not parse
// leaves that batch's facts unchecked (logged).
func groundFacts(ctx context.Context, client *llm.Client, subject, source, url string, facts []backend.Fact) ([]backend.Fact, GroundingOutcome, error) {
	var out GroundingOutcome
	if client == nil || len(facts) == 0 {
		return facts, out, nil
	}
	kept := make([]backend.Fact, 0, len(facts))
	for start := 0; start < len(facts); start += groundingBatch {
		batch := facts[start:min(start+groundingBatch, len(facts))]
		entries, err := askGrounding(ctx, client, subject, source, batch)
		if err != nil {
			if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
				return nil, out, fmt.Errorf("grounding check of %s: %w", url, err)
			}
			log.Warn().Err(err).Str("url", url).Int("facts", len(batch)).Msg("grounding check failed; the facts are kept unchecked")
			out.Unchecked += len(batch)
			kept = append(kept, batch...)
			continue
		}
		for i, f := range batch {
			e, ok := entries[i+1]
			if !ok {
				out.Unchecked++
				kept = append(kept, f)
				continue
			}
			switch strings.ToLower(strings.TrimSpace(e.Verdict)) {
			case "drop":
				out.Dropped++
				log.Info().Str("url", url).Str("fact", f.NormalizedFactText).Str("quote", f.OriginalQuote).Str("why", e.Reasoning).Msg("grounding: fact dropped")
			case "rewrite":
				g, requoted, ok := applyRewrite(f, e, source)
				if !ok {
					out.Unchecked++
					kept = append(kept, f)
					continue
				}
				out.Rewritten++
				if requoted {
					out.Requoted++
				}
				log.Info().Str("url", url).Str("fact", f.NormalizedFactText).Str("rewritten", g.NormalizedFactText).Bool("requoted", requoted).Str("why", e.Reasoning).Msg("grounding: fact rewritten to its quote")
				kept = append(kept, g)
			case "grounded":
				out.Grounded++
				kept = append(kept, f)
			default:
				log.Warn().Str("url", url).Str("verdict", e.Verdict).Msg("grounding: unknown verdict; the fact is kept unchecked")
				out.Unchecked++
				kept = append(kept, f)
			}
		}
	}
	return kept, out, nil
}

// applyRewrite returns the fact with the checker's rewrite: the new text,
// and the longer quote when it is a verbatim span of the source holding the
// original quote (else the original quote stays and the text is used only if
// given). ok is false when the reply gives nothing usable.
func applyRewrite(f backend.Fact, e groundingEntry, source string) (backend.Fact, bool, bool) {
	g := f
	requoted := false
	if q := strings.TrimSpace(e.Quote); q != "" && q != strings.TrimSpace(f.OriginalQuote) {
		if span, ok := verbatimSpan(source, q); ok && containsFolded(span, f.OriginalQuote) {
			g.OriginalQuote = span
			requoted = true
		}
	}
	text := strings.TrimSpace(e.Fact)
	if text != "" {
		g.NormalizedFactText = text
	} else if !requoted {
		return f, false, false
	}
	if g.NormalizedFactText != f.NormalizedFactText {
		// The English text described the old fact: the translation repair
		// fills it again.
		g.NormalizedFactTextEn = ""
	}
	return g, requoted, true
}

func askGrounding(ctx context.Context, client *llm.Client, subject, source string, batch []backend.Fact) (map[int]groundingEntry, error) {
	var b strings.Builder
	for i, f := range batch {
		fmt.Fprintf(&b, "\n[%d] FACT: %s\n    QUOTE: %s\n    TEXT AROUND: %s\n", i+1, f.NormalizedFactText, oneLine(f.OriginalQuote), oneLine(quoteContext(source, f.OriginalQuote)))
		if d := factSourceDate(f); d != "" {
			fmt.Fprintf(&b, "    SOURCE DATE: %s\n", d)
		}
	}
	content, _, err := client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: fmt.Sprintf(groundingPrompt, subject, b.String())}},
		Temperature:    0.0,
		MaxTokens:      200 + groundingMaxTokens*len(batch),
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskGround,
	})
	if err != nil {
		return nil, err
	}
	reply, err := llm.ParseJSONResponse[groundingReply](content)
	if err != nil {
		return nil, fmt.Errorf("parsing %q: %w", content, err)
	}
	entries := make(map[int]groundingEntry, len(reply.Facts))
	for i, e := range reply.Facts {
		n := e.N
		if n < 1 || n > len(batch) {
			n = i + 1
		}
		entries[n] = e
	}
	return entries, nil
}

// quoteContext returns the source around the quote: groundingWindow
// characters on each side, cut at word boundaries. A quote not found in the
// source (a translated or reworded one) gets no context.
func quoteContext(source, quote string) string {
	start, end, ok := findFolded(source, quote)
	if !ok {
		return "(not found in the source)"
	}
	from := max(0, start-groundingWindow)
	to := min(len(source), end+groundingWindow)
	for from > 0 && !utf8Start(source[from]) {
		from--
	}
	for to < len(source) && !utf8Start(source[to]) {
		to++
	}
	return source[from:to]
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// verbatimSpan finds q in the source, whitespace and quote marks folded, and
// returns the source's own text of it.
func verbatimSpan(source, q string) (string, bool) {
	start, end, ok := findFolded(source, q)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(source[start:end]), true
}

func containsFolded(s, sub string) bool {
	_, _, ok := findFolded(s, sub)
	return ok
}

// findFolded finds needle in hay comparing runes case-folded, with every run
// of whitespace equal to one space, the apostrophe variants equal and the
// quote marks left out; it returns the byte span in hay.
func findFolded(hay, needle string) (int, int, bool) {
	hk, hpos := foldWithPos(hay)
	nk, _ := foldWithPos(strings.TrimSpace(needle))
	if len(nk) == 0 {
		return 0, 0, false
	}
	idx := indexRunes(hk, nk)
	if idx < 0 {
		return 0, 0, false
	}
	start := hpos[idx]
	end := len(hay)
	if idx+len(nk) < len(hpos) {
		end = hpos[idx+len(nk)]
	}
	return start, end, true
}

// foldWithPos folds s and returns the folded runes with the byte offset in s
// of each.
func foldWithPos(s string) ([]rune, []int) {
	runes := make([]rune, 0, len(s))
	pos := make([]int, 0, len(s))
	space := false
	for i, r := range s {
		if unicode.IsSpace(r) {
			if !space && len(runes) > 0 {
				runes = append(runes, ' ')
				pos = append(pos, i)
			}
			space = true
			continue
		}
		switch r {
		case '’', '‘', 'ʼ':
			r = '\''
		case '"', '“', '”', '«', '»':
			// A quote mark, and the space French puts inside « », is left
			// out: a quote is often stored without its marks.
			continue
		}
		space = false
		runes = append(runes, unicode.ToLower(r))
		pos = append(pos, i)
	}
	return runes, pos
}

func indexRunes(h, n []rune) int {
outer:
	for i := 0; i+len(n) <= len(h); i++ {
		for j := range n {
			if h[i+j] != n[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// factSourceDate is the date of the fact's source as the extraction knows
// it: the page's, else the one the extractor read in the text.
func factSourceDate(f backend.Fact) string {
	for _, d := range []*time.Time{f.PublishedAt, f.ArticlePublishedAt} {
		if d != nil && !d.IsZero() {
			return d.Format("2006-01-02")
		}
	}
	return ""
}
