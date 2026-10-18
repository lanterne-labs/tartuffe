package frpolitics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/llm"

	"github.com/rs/zerolog/log"
)

// A scrutin on an article of a bill votes one provision, not the bill. The
// ballot must say what the article does, or it says nothing verifiable: the
// detector would read "voted FOR article 14" and "voted AGAINST article 1" of
// one bill as a reversal on the bill's purpose. The resolver finds the text
// the séance voted on, reads the article in it, and asks the model for one
// statement of what the article does.
//
// The scrutin does not name its text. The amendments voted in the same
// séance on the same bill do (amendement.texteLegislatifRef), and the
// transcript of the séance names the texts it discussed (the ordre du jour
// points). The text is the PDF the Assemblée publishes for every texte (texte
// de la commission, texte déposé), read with pdftotext.

// articleStatementMinConfidence is the confidence under which the statement
// of an article does not make a fact.
const articleStatementMinConfidence = 0.5

// articleTextMaxChars bounds the article text the model reads.
const articleTextMaxChars = 6000

// articleResolver resolves the voted object of the article votes of one run.
type articleResolver struct {
	store    *Store
	laws     []Law // every scrutin of the run: the siblings of a séance
	orgCache map[string]OrganeInfo
	cacheDir string
	// lookup returns the texts an amendment scrutin can have voted on
	// (LookupTexteRefs).
	lookup func(ctx context.Context, legislature, numero int, scrutinDate *time.Time) ([]string, error)
	// statement asks the model what an article does (articleStatement).
	statement func(ctx context.Context, bill, article, text string) (LawSummary, error)
	// readText returns the text of the PDF at url (pdfText).
	readText func(ctx context.Context, cacheDir, url string) (string, error)
	// crTexts returns the numbers of the texts the transcript of a séance
	// discussed (transcriptTexts).
	crTexts func(ctx context.Context, legislature int, seanceRef string) ([]string, error)

	bySeance map[string][]Law
	// crIndex is, per legislature, the text numbers per séance of the
	// transcripts archive, built on the first need.
	crIndex map[int]map[string][]string
}

// textSlugs are the pages a text can have when only its number is known:
// the text of the commission is what a séance votes on, the deposited text
// when the commission adopted none.
var textSlugs = []string{"texte-adopte-commission", "proposition-loi", "projet-loi"}

func newArticleResolver(store *Store, laws []Law, orgCache map[string]OrganeInfo) *articleResolver {
	r := &articleResolver{store: store, laws: laws, orgCache: orgCache, cacheDir: getCacheDir(),
		lookup: LookupTexteRefs, statement: articleStatement, readText: pdfText, bySeance: map[string][]Law{}, crIndex: map[int]map[string][]string{}}
	r.crTexts = r.transcriptTexts
	for _, l := range laws {
		if l.SeanceRef != "" {
			r.bySeance[l.SeanceRef] = append(r.bySeance[l.SeanceRef], l)
		}
	}
	return r
}

// resolve returns the voted object of an article vote: the article and what
// it does, "l'article 7 (« Permet ... »)". ok is false when the text of the
// séance, the article in it or a confident statement of it cannot be found:
// the scrutin is gated, with the reason logged. An error is a failure to
// read a source (the archive, the PDF, the model), not a gate.
func (r *articleResolver) resolve(ctx context.Context, law Law) (object string, ok bool, err error) {
	article, bill, ok := splitArticle(law.Title)
	if !ok {
		log.Warn().Str("law_key", law.Key).Str("titre", law.Title[:min(80, len(law.Title))]).Msg("frpolitics: article title does not split; gating")
		return "", false, nil
	}
	if a := strings.ToLower(article); !strings.HasPrefix(a, "l'article") && !strings.HasPrefix(a, "l’article") {
		// A part of a budget bill ("la première partie") has no article to read.
		log.Warn().Str("law_key", law.Key).Str("object", article).Msg("frpolitics: a vote on a part of the text; gating")
		return "", false, nil
	}
	num, slugs, ok, err := r.seanceText(ctx, law, bill)
	if err != nil || !ok {
		return "", ok, err
	}
	summary, found, err := r.store.StoredArticleStatement(ctx, law.Legislature, num, article)
	if err != nil {
		return "", false, err
	}
	if !found {
		var text string
		for _, slug := range slugs {
			text, err = r.readText(ctx, r.cacheDir, textPDFURL(law.Legislature, num, slug))
			if !errors.Is(err, errNotFound) {
				break
			}
		}
		if errors.Is(err, errNotFound) {
			// No page of the text where its reference says: this scrutin is
			// gated, the run goes on.
			log.Warn().Str("law_key", law.Key).Int("texte", num).Strs("slugs", slugs).Msg("frpolitics: the PDF of the text does not exist; gating")
			return "", false, nil
		}
		if err != nil {
			return "", false, fmt.Errorf("text %d of legislature %d: %w", num, law.Legislature, err)
		}
		body := extractArticle(text, article)
		if body == "" {
			log.Warn().Str("law_key", law.Key).Int("texte", num).Str("article", article).Msg("frpolitics: article not found in the text; gating")
			return "", false, nil
		}
		summary, err = r.statement(ctx, bill, article, body)
		if err != nil {
			return "", false, fmt.Errorf("statement of %s of text %d: %w", article, num, err)
		}
		if err := r.store.StoreArticleStatement(ctx, law.Legislature, num, article, summary); err != nil {
			return "", false, err
		}
	}
	if summary.Confidence < articleStatementMinConfidence {
		log.Warn().Str("law_key", law.Key).Int("texte", num).Str("article", article).Float64("confidence", summary.Confidence).
			Msg("frpolitics: no confident statement of the article; gating")
		return "", false, nil
	}
	return fmt.Sprintf("%s (« %s »)", article, strings.TrimSuffix(summary.SimpleStatement, ".")), true, nil
}

// seanceText returns the text the séance of law voted on: its number and
// the page slugs to try. Two sources name it:
//   - the amendments voted in the same séance on the same bill
//     (texteLegislatifRef). The bill is the tail of an amendment title ("...
//     à l'article 7 de la proposition de loi relative ..."), so the match is a
//     suffix: a bill whose title extends another's does not pass for it. An
//     amendment number can name several texts (the same number in several
//     texts, sorted the same day): the text of the séance is the one every
//     amendment of it names;
//   - the transcript of the séance, which lists the texts it discussed by
//     number. It narrows the amendments' texts to the ones discussed, and
//     names the text alone when the séance voted no amendment.
//
// ok is false when the two sources leave no text, or several.
func (r *articleResolver) seanceText(ctx context.Context, law Law, bill string) (num int, slugs []string, ok bool, err error) {
	if law.SeanceRef == "" {
		log.Warn().Str("law_key", law.Key).Msg("frpolitics: article vote without a séance; gating")
		return 0, nil, false, nil
	}
	billKey := billTitleKey(bill)
	// common holds the texts every amendment so far names; nil before the first.
	var common map[string]bool
	for _, sibling := range r.bySeance[law.SeanceRef] {
		if sibling.Legislature != law.Legislature {
			continue
		}
		switch ClassifyScrutinObject(sibling.Title) {
		case ObjectAmendement, ObjectSousAmendement:
		default:
			continue
		}
		if !strings.HasSuffix(billTitleKey(sibling.Title), billKey) {
			continue
		}
		m := scrutinNumeroRe.FindStringSubmatch(sibling.Title)
		if m == nil {
			continue
		}
		numero, _ := strconv.Atoi(m[1])
		refs, err := r.lookup(ctx, sibling.Legislature, numero, sibling.VoteDate)
		if err != nil {
			return 0, nil, false, err
		}
		if len(refs) == 0 {
			continue // not in the archive yet: no signal
		}
		named := map[string]bool{}
		for _, ref := range refs {
			named[ref] = true
		}
		if common == nil {
			common = named
			continue
		}
		for ref := range common {
			if !named[ref] {
				delete(common, ref)
			}
		}
	}
	if len(common) != 1 {
		// The transcript of the séance: the texts it discussed, by number.
		nums, err := r.crTexts(ctx, law.Legislature, law.SeanceRef)
		if err != nil {
			return 0, nil, false, err
		}
		discussed := map[int]bool{}
		for _, n := range nums {
			if v, err := strconv.Atoi(n); err == nil {
				discussed[v] = true
			}
		}
		switch {
		case len(common) > 1:
			for ref := range common {
				if n, _, ok := textPage(ref); !ok || !discussed[n] {
					delete(common, ref)
				}
			}
		case len(common) == 0 && len(discussed) == 1:
			for n := range discussed {
				return n, textSlugs, true, nil
			}
		}
	}
	switch len(common) {
	case 1:
		for ref := range common {
			n, slug, ok := textPage(ref)
			if !ok {
				log.Warn().Str("law_key", law.Key).Str("texte_ref", ref).Msg("frpolitics: the text reference of the séance has no known page; gating")
				return 0, nil, false, nil
			}
			return n, []string{slug}, true, nil
		}
	case 0:
		log.Warn().Str("law_key", law.Key).Str("seance", law.SeanceRef).Msg("frpolitics: neither the amendments nor the transcript of the séance name its text; gating the article vote")
	default:
		log.Warn().Str("law_key", law.Key).Str("seance", law.SeanceRef).Int("texts", len(common)).Msg("frpolitics: the séance names several texts; gating the article vote")
	}
	return 0, nil, false, nil
}

// transcriptTexts returns the numbers of the texts the transcript of the
// séance discussed (the ordre du jour points of the compte rendu). The index
// of a legislature is built from the transcripts archive on the first need.
func (r *articleResolver) transcriptTexts(ctx context.Context, legislature int, seanceRef string) ([]string, error) {
	idx, ok := r.crIndex[legislature]
	if !ok {
		body, err := downloadCached(ctx, fmt.Sprintf(debatsArchiveURLTpl, legislature), r.cacheDir, debatsCacheMaxAge)
		if err != nil {
			return nil, fmt.Errorf("transcripts of legislature %d: %w", legislature, err)
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			return nil, fmt.Errorf("transcripts of legislature %d: %w", legislature, err)
		}
		idx = map[string][]string{}
		for _, f := range zr.File {
			if !strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			cr, err := parseCompteRendu(rc)
			rc.Close()
			if err != nil || cr.SeanceRef == "" {
				continue
			}
			seen := map[string]bool{}
			for _, p := range cr.Points {
				if p.TexteNum != "" && !seen[p.TexteNum] {
					seen[p.TexteNum] = true
					idx[cr.SeanceRef] = append(idx[cr.SeanceRef], p.TexteNum)
				}
			}
		}
		r.crIndex[legislature] = idx
		log.Info().Int("legislature", legislature).Int("seances", len(idx)).Msg("frpolitics: transcripts indexed by séance for the article votes")
	}
	return idx[seanceRef], nil
}

// billTitleKey reduces a bill title to the words that identify the bill, for
// a containment test: lower case, without the reading in parentheses and
// the punctuation. "la proposition de loi relative au droit à l'aide à mourir
// (première lecture)." gives "proposition de loi relative au droit à l aide à
// mourir".
func billTitleKey(title string) string {
	t := strings.ToLower(title)
	if i := strings.Index(t, "("); i >= 0 {
		t = t[:i]
	}
	t = strings.NewReplacer("'", " ", "’", " ", ".", " ", ",", " ").Replace(t)
	for _, p := range []string{"la ", "le ", "l "} {
		t = strings.TrimPrefix(strings.TrimSpace(t), p)
	}
	return strings.Join(strings.Fields(t), " ")
}

// texteRefRe parses a text reference: the family (PION, PRJL), the
// legislature, the kind (TC: texte de la commission, TA: texte adopté, none:
// texte déposé) and the number. "PIONANR5L17BTC1364".
var texteRefRe = regexp.MustCompile(`^([A-Z]{4})ANR5L(\d+)B(TC|TA)?(\d+)$`)

// textPage returns the number and the page slug of a text reference, as the
// Assemblée names its pages: l17b1364_texte-adopte-commission. ok is false
// for a reference of an unknown shape or family.
func textPage(ref string) (num int, slug string, ok bool) {
	m := texteRefRe.FindStringSubmatch(ref)
	if m == nil {
		return 0, "", false
	}
	num, _ = strconv.Atoi(m[4])
	switch m[3] {
	case "TC":
		return num, "texte-adopte-commission", true
	case "TA":
		return num, "texte-adopte-seance", true
	}
	switch m[1] {
	case "PION":
		return num, "proposition-loi", true
	case "PRJL":
		return num, "projet-loi", true
	}
	return 0, "", false
}

// textPDFURL is the PDF of a text: the pages of the texts adopted in séance
// are numbered with a t, the others with a b.
func textPDFURL(legislature, num int, slug string) string {
	letter := "b"
	if slug == "texte-adopte-seance" {
		letter = "t"
	}
	return fmt.Sprintf("https://www.assemblee-nationale.fr/dyn/%d/textes/l%d%s%04d_%s.pdf", legislature, legislature, letter, num, slug)
}

// pdfText downloads the PDF at url (cached for a year: a published text does
// not change) and returns its text, through pdftotext with the layout kept.
func pdfText(ctx context.Context, cacheDir, url string) (string, error) {
	body, err := downloadCached(ctx, url, cacheDir, 365*24*time.Hour)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "texte-*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	var out, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "pdftotext", "-layout", f.Name(), "-")
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftotext: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

// articleHeadingRe matches a line that opens an article or a division of the
// text: "Article 7", "Article 1er bis A", "Article 4 (nouveau)", "Article
// premier", "TITRE II", "Chapitre III", "Section 2".
var articleHeadingRe = regexp.MustCompile(`^\s*(Article\s+\S+(\s+(bis|ter|quater|quinquies|sexies|septies|octies|nonies|decies|[A-Z]))*(\s*\([^)]*\))?|TITRE\s+\S+|Titre\s+\S+|CHAPITRE\s+\S+|Chapitre\s+\S+|Section\s+\S+)\s*$`)

// headingNoteRe matches the note after an article heading: "(nouveau)",
// "(Supprimé)", "(Non modifié)".
var headingNoteRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)

// pageMarkRe matches the page number line of a PDF: "– 8 –".
var pageMarkRe = regexp.MustCompile(`^\s*[–-]\s*\d+\s*[–-]\s*$`)

// extractArticle returns the body of article ("l'article 7", "l'article 21
// bis", "l'article premier") in the text of a bill: from its heading line to
// the next heading, without the page marks, in one paragraph. "" when the
// article has no heading in the text.
func extractArticle(text, article string) string {
	ids := articleIDs(article)
	if len(ids) == 0 {
		return ""
	}
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		h := headingNoteRe.ReplaceAllString(strings.Join(strings.Fields(line), " "), "")
		for _, id := range ids {
			if strings.EqualFold(h, "Article "+id) {
				start = i
				break
			}
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return ""
	}
	var parts []string
	for _, line := range lines[start+1:] {
		if articleHeadingRe.MatchString(line) {
			break
		}
		if pageMarkRe.MatchString(line) || strings.TrimSpace(line) == "" {
			continue
		}
		parts = append(parts, strings.Join(strings.Fields(line), " "))
	}
	body := strings.Join(parts, " ")
	if len(body) > articleTextMaxChars {
		body = body[:articleTextMaxChars] + " […]"
	}
	return body
}

// articleIDs returns the headings an article can have in a text: "l'article
// 7" is "7"; "l'article premier" is "1er" or "premier"; "l'article 24 (examen
// prioritaire)" is "24".
func articleIDs(article string) []string {
	a := strings.TrimSpace(article)
	for _, p := range []string{"l'article ", "l’article ", "L'article ", "L’article "} {
		a = strings.TrimPrefix(a, p)
	}
	if i := strings.Index(a, "("); i >= 0 {
		a = a[:i]
	}
	a = strings.Join(strings.Fields(a), " ")
	if a == "" {
		return nil
	}
	switch strings.ToLower(a) {
	case "premier":
		return []string{"1er", "premier"}
	case "unique":
		return []string{"unique"}
	}
	return []string{a}
}

// articleStatement asks the model what an article of a bill does: one French
// sentence with the direction of the measure, and a confidence. The reasoning
// comes first and the decision last, as in every judge prompt.
func articleStatement(ctx context.Context, bill, article, text string) (LawSummary, error) {
	prompt := fmt.Sprintf(`You summarize ONE ARTICLE of a French bill into a brief neutral statement, in FRENCH, with an EXPLICIT direction (crée, supprime, étend, restreint, renforce, allège, autorise, interdit, fixe...). The statement is embedded verbatim in the sentence "X a voté POUR l'article N (« ... ») du texte de loi".

Rules:
- Describe what THIS ARTICLE does, not the bill. One sentence, 25 words at most, no subject ("Permet l'administration de la substance létale en dehors du domicile").
- The statement NEVER carries a remark about the text, the title or your certainty. A doubt goes in the confidence.
- confidence: 0.8-1.0 when the article's effect is explicit; 0.5-0.7 when inferred; below 0.5 when the text does not tell what the article does (a renumbering, a coordination of references, a text cut by the extraction).

Return ONLY strict JSON, with the keys in this order:
{"reasoning": "<one sentence: what the article changes and where>", "statement": "<the French statement>", "confidence": <number>}

Bill: %s
Article: %s
Text of the article:
%s`, bill, article, text)

	client, err := llm.NewClientFromEnv("frpolitics-article")
	if err != nil {
		return LawSummary{}, err
	}
	content, _, err := client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.1,
		MaxTokens:      400,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
		Task:           llm.TaskLawSummary,
	})
	if err != nil {
		return LawSummary{}, err
	}
	var reply struct {
		Reasoning  string  `json:"reasoning"`
		Statement  string  `json:"statement"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(content), &reply); err != nil {
		return LawSummary{}, fmt.Errorf("parse article statement: %w", err)
	}
	if strings.TrimSpace(reply.Statement) == "" {
		return LawSummary{}, errors.New("article statement: empty statement")
	}
	summary := LawSummary{SimpleStatement: strings.TrimSpace(reply.Statement), Confidence: reply.Confidence}
	if metaStatement(summary.SimpleStatement) {
		log.Warn().Str("article", article).Str("statement", summary.SimpleStatement).Msg("frpolitics: the article statement carries a remark on the text; confidence 0")
		summary.Confidence = 0
	}
	return summary, nil
}
