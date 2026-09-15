package frpolitics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// Séance transcripts ("comptes rendus intégraux", Syceron export). One zip for
// the whole legislature, refreshed daily around 02:05 UTC; each file is one
// séance and every <paragraphe> carries the speaker's Assemblée actor id
// (id_acteur = persons.attributes.assemblee_id), the ordre-du-jour point it
// belongs to (which names the texte under discussion) and the article /
// amendement being debated — the structural link to the votes we already
// ingest. Facts extracted here are the person's OWN words, spoken publicly,
// with the official record as the verbatim quote.
const (
	debatsArchiveURLTpl = "https://data.assemblee-nationale.fr/static/openData/repository/%d/vp/syceronbrut/syseron.xml.zip"
	debatsCacheMaxAge   = 12 * time.Hour
	// debatsMinInterventionChars drops heckles and procedural one-liners
	// (median intervention is ~60 chars; p75 ~430). Tune with data — the
	// pending-set identity is (cr_uid, actor_id), so tuning it never
	// re-extracts already processed documents.
	debatsMinInterventionChars = 300
	debatsSourceType           = "assemblee_debat"
	anCompteRenduBaseURL       = "https://www.assemblee-nationale.fr/dyn/%d/comptes-rendus/seance/%s/%s"
	// debatsMaxConsecutiveFailures stops a run whose documents fail back to
	// back — an LLM/embedder outage — instead of walking the whole backlog
	// logging one warning per document.
	debatsMaxConsecutiveFailures = 10
)

// DebatsOptions drives one débats ingestion run.
type DebatsOptions struct {
	Legislature int // 0 = current (17)
	// Since / Until bound the séance dates considered (zero = unbounded).
	Since, Until time.Time
	// MaxDocs caps the number of documents ATTEMPTED this run; 0 = unlimited.
	MaxDocs int
	// Deadline: no new document is started after this instant (zero = none).
	// The daily pipeline uses it to let the backfill run until a wall-clock
	// time instead of a count.
	Deadline time.Time
	// NewestFirst processes the most recent séances first (backfill order).
	NewestFirst bool
	// MinChars overrides debatsMinInterventionChars when > 0.
	MinChars int
}

// DebatsStats summarizes one run.
type DebatsStats struct {
	Seances             int
	Documents           int // candidate documents in the considered date range
	Pending             int // not yet processed before this run
	Attempted           int
	Processed           int
	Facts               int
	FactsSkipped        int // dedupe / validator / empty-quote skips inside FactInserter
	FactsDroppedSubject int // extractor facts whose subject was not the speaker
	NoFacts             int
	Errors              int
	DeadlineHit         bool
	PendingAfter        int
	ElapsedSeconds      float64
}

// compteRendu is the parsed subset of one séance file.
type compteRendu struct {
	UID            string
	Legislature    int
	SeanceDate     time.Time
	DateSeanceJour string // "lundi 26 janvier 2026"
	NumSeanceJour  string // "1", "2", "3" or "Unique"
	Session        string
	Points         map[string]odjPoint // keyed by valeur_ptsodj
	Paragraphs     []crParagraph
}

type odjPoint struct {
	Title    string // "Protéger les mineurs des risques ..."
	TexteNum string // "2341"
}

type crParagraph struct {
	IDSyceron   string
	ActorID     string
	SpeakerName string // "M. Thibault Bazin" (civility stripped later)
	Qualite     string // "rapporteure", "ministre", ...
	RoleDebat   string // "president" for the chair
	CodeGram    string
	PtsODJ      string
	Article     string
	Amendement  string
	SousAmdt    string
	Text        string
}

// debateDocument is one (speaker, séance) extraction unit.
type debateDocument struct {
	CR         *compteRendu
	ActorID    string
	Person     storage.PersonInfo
	Speaker    string // display name from the transcript
	Paragraphs []crParagraph
	SourceURL  string
	Chars      int
}

func (d *debateDocument) key() storage.DebateDocumentKey {
	return storage.DebateDocumentKey{CRUID: d.CR.UID, ActorID: d.ActorID}
}

// docResult is what ingesting one document yields.
type docResult struct {
	Inserted, Skipped, DroppedSubject int
}

// RunDebats ingests séance transcripts for tracked persons: parses the
// archive, builds one document per (speaker, séance) not yet processed,
// extracts facts with the shared extractor and inserts them through the
// normal FactInserter path (dedupe, stance routing, analyze). Idempotent:
// processed documents are recorded in debate_documents by (cr_uid, actor_id).
func RunDebats(ctx context.Context, storer *storage.PostgresStorer, opts DebatsOptions) (DebatsStats, error) {
	start := time.Now()
	var stats DebatsStats
	leg := opts.Legislature
	if leg <= 0 {
		leg = 17
	}
	minChars := opts.MinChars
	if minChars <= 0 {
		minChars = debatsMinInterventionChars
	}

	persons, err := storer.GetPersonsByAssembleeID(ctx)
	if err != nil {
		return stats, fmt.Errorf("debats: loading tracked persons: %w", err)
	}
	if len(persons) == 0 {
		return stats, fmt.Errorf("debats: no person carries an assemblee_id; run the frpolitics votes step first")
	}

	body, err := downloadCached(ctx, fmt.Sprintf(debatsArchiveURLTpl, leg), getCacheDir(), debatsCacheMaxAge)
	if err != nil {
		return stats, fmt.Errorf("debats: downloading archive: %w", err)
	}
	docs, seances, err := buildDebateDocuments(body, persons, opts, minChars)
	if err != nil {
		return stats, err
	}
	body = nil
	stats.Seances = seances
	stats.Documents = len(docs)

	keys := make([]storage.DebateDocumentKey, len(docs))
	byKey := make(map[storage.DebateDocumentKey]*debateDocument, len(docs))
	for i := range docs {
		keys[i] = docs[i].key()
		byKey[keys[i]] = &docs[i]
	}
	pendingKeys, err := storer.FilterUnprocessedDebateDocuments(ctx, keys)
	if err != nil {
		return stats, fmt.Errorf("debats: filtering processed documents: %w", err)
	}
	pending := make([]*debateDocument, 0, len(pendingKeys))
	for _, k := range pendingKeys {
		pending = append(pending, byKey[k])
	}
	stats.Pending = len(pending)
	log.Info().Int("seances", seances).Int("documents", len(docs)).Int("pending", len(pending)).
		Bool("newest_first", opts.NewestFirst).Msg("debats: documents to process")
	if len(pending) == 0 {
		stats.ElapsedSeconds = time.Since(start).Seconds()
		return stats, nil
	}

	extractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return stats, fmt.Errorf("debats: extractor: %w", err)
	}
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return stats, fmt.Errorf("debats: embedder: %w", err)
	}
	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(storer))
	if err != nil {
		return stats, fmt.Errorf("debats: stance manager: %w", err)
	}
	inserter := core.NewFactInserter(storer, stanceMgr)
	inserter.EnableLLMDedupe()

	ingest := func(ctx context.Context, doc *debateDocument) (docResult, error) {
		return ingestDebateDocument(ctx, storer, extractor, embedder, inserter, doc)
	}
	runErr := runDebateDocuments(ctx, pending, opts, ingest, &stats)
	stats.ElapsedSeconds = time.Since(start).Seconds()
	log.Info().
		Int("attempted", stats.Attempted).Int("processed", stats.Processed).Int("facts", stats.Facts).
		Int("skipped", stats.FactsSkipped).Int("dropped_subject", stats.FactsDroppedSubject).
		Int("no_facts", stats.NoFacts).Int("errors", stats.Errors).Int("pending_after", stats.PendingAfter).
		Bool("deadline_hit", stats.DeadlineHit).Dur("elapsed", time.Since(start)).
		Msg("debats: run finished")
	return stats, runErr
}

// runDebateDocuments is the scheduling loop: sort, then ingest documents one
// at a time under the MaxDocs / Deadline caps. A failed document stays
// pending for the next run; a streak of debatsMaxConsecutiveFailures aborts
// the run (outage), and a run that attempted documents without processing
// any is reported as an error so the pipeline step goes red.
func runDebateDocuments(ctx context.Context, pending []*debateDocument, opts DebatsOptions, ingest func(context.Context, *debateDocument) (docResult, error), stats *DebatsStats) error {
	sort.SliceStable(pending, func(i, j int) bool {
		a, b := pending[i].CR.SeanceDate, pending[j].CR.SeanceDate
		if opts.NewestFirst {
			return a.After(b)
		}
		return a.Before(b)
	})
	consecutive := 0
	var lastErr error
	for _, doc := range pending {
		if ctx.Err() != nil {
			break
		}
		if opts.MaxDocs > 0 && stats.Attempted >= opts.MaxDocs {
			break
		}
		if !opts.Deadline.IsZero() && time.Now().After(opts.Deadline) {
			stats.DeadlineHit = true
			break
		}
		stats.Attempted++
		res, err := ingest(ctx, doc)
		if err != nil {
			stats.Errors++
			consecutive++
			lastErr = err
			log.Warn().Err(err).Str("cr_uid", doc.CR.UID).Str("actor_id", doc.ActorID).Str("speaker", doc.Speaker).
				Int("facts_inserted_before_failure", res.Inserted).
				Msg("debats: document failed; left pending for the next run")
			if consecutive >= debatsMaxConsecutiveFailures {
				stats.PendingAfter = stats.Pending - stats.Processed
				return fmt.Errorf("debats: %d consecutive document failures, aborting run (last: %w)", consecutive, err)
			}
			continue
		}
		consecutive = 0
		stats.Processed++
		stats.Facts += res.Inserted
		stats.FactsSkipped += res.Skipped
		stats.FactsDroppedSubject += res.DroppedSubject
		if res.Inserted == 0 {
			stats.NoFacts++
		}
	}
	stats.PendingAfter = stats.Pending - stats.Processed
	if stats.Attempted > 0 && stats.Processed == 0 && stats.Errors > 0 {
		return fmt.Errorf("debats: every attempted document failed (%d): %w", stats.Errors, lastErr)
	}
	return nil
}

// buildDebateDocuments parses every séance in the archive that falls in the
// option's date range and groups the substantive paragraphs of each tracked
// speaker into one document. Only the documents' own paragraphs are kept in
// memory; the rest of each séance is released after grouping.
func buildDebateDocuments(archive []byte, persons map[string]storage.PersonInfo, opts DebatsOptions, minChars int) ([]debateDocument, int, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, 0, fmt.Errorf("debats: reading archive: %w", err)
	}
	var docs []debateDocument
	seances := 0
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("debats: cannot open séance file")
			continue
		}
		cr, err := parseCompteRendu(rc)
		rc.Close()
		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("debats: cannot parse séance file")
			continue
		}
		if !opts.Since.IsZero() && cr.SeanceDate.Before(opts.Since) {
			continue
		}
		if !opts.Until.IsZero() && cr.SeanceDate.After(opts.Until) {
			continue
		}
		seances++
		docs = append(docs, groupDocuments(cr, persons, minChars)...)
		cr.Paragraphs = nil
	}
	return docs, seances, nil
}

// groupDocuments builds one document per tracked speaker of the séance from
// their substantive paragraphs (not the chair, not heckles, long enough).
func groupDocuments(cr *compteRendu, persons map[string]storage.PersonInfo, minChars int) []debateDocument {
	byActor := map[string]*debateDocument{}
	var order []string
	for _, p := range cr.Paragraphs {
		if p.ActorID == "" || p.RoleDebat == "president" || strings.HasPrefix(p.CodeGram, "INTERRUPTION") {
			continue
		}
		person, ok := persons[p.ActorID]
		if !ok {
			continue
		}
		if len([]rune(p.Text)) < minChars {
			continue
		}
		d, ok := byActor[p.ActorID]
		if !ok {
			d = &debateDocument{CR: cr, ActorID: p.ActorID, Person: person, Speaker: stripCivility(p.SpeakerName)}
			byActor[p.ActorID] = d
			order = append(order, p.ActorID)
		}
		d.Paragraphs = append(d.Paragraphs, p)
		d.Chars += len(p.Text)
	}
	out := make([]debateDocument, 0, len(order))
	for _, a := range order {
		d := byActor[a]
		d.SourceURL = compteRenduPermalink(cr, d.Paragraphs[0].IDSyceron)
		out = append(out, *d)
	}
	return out
}

// ingestDebateDocument extracts facts from one document, embeds them in one
// call, inserts them and records the document as processed. On error the
// document is NOT recorded (it stays pending); facts already inserted are
// reported in the result so the caller can log them — the next run's
// re-extraction relies on dedupe for those.
func ingestDebateDocument(ctx context.Context, storer *storage.PostgresStorer, extractor *categorizer.OpenAIExtractor, embedder *embedding.OpenAIEmbeddingGenerator, inserter *core.FactInserter, doc *debateDocument) (docResult, error) {
	var res docResult
	content := renderDebateDocument(doc)
	facts, err := extractor.ExtractFactsWithConfig(ctx, core.FactExtractorConfig{
		TargetName:         doc.Speaker,
		SourceURL:          doc.SourceURL,
		Content:            content,
		PluginInstructions: debatsExtractionInstructions,
		Now:                doc.CR.SeanceDate,
	})
	if err != nil {
		return res, fmt.Errorf("extracting: %w", err)
	}

	kept := make([]backend.Fact, 0, len(facts))
	for _, f := range facts {
		if subjectMatches(f, doc) {
			kept = append(kept, f)
		} else {
			res.DroppedSubject++
			log.Info().Str("subject", f.SubjectEntity).Str("speaker", doc.Speaker).Str("fact", truncateRunes(f.NormalizedFactText, 100)).
				Msg("debats: dropping fact whose subject is not the speaker")
		}
	}

	var embs [][]float32
	if len(kept) > 0 {
		texts := make([]string, len(kept))
		for i, f := range kept {
			texts[i] = f.EmbeddingText
		}
		embs, err = embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, texts)
		if err != nil {
			return res, fmt.Errorf("embedding %d facts: %w", len(kept), err)
		}
		if len(embs) != len(kept) {
			return res, fmt.Errorf("embedding: got %d vectors for %d facts", len(embs), len(kept))
		}
	}

	paragraphIDs := make([]string, len(doc.Paragraphs))
	textes, articles, amendements := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, p := range doc.Paragraphs {
		paragraphIDs[i] = p.IDSyceron
		if pt, ok := doc.CR.Points[p.PtsODJ]; ok && pt.TexteNum != "" {
			textes[pt.TexteNum] = true
		}
		if a := strings.TrimSpace(p.Article); a != "" {
			articles[a] = true
		}
		if a := strings.TrimSpace(p.Amendement); a != "" {
			amendements[a] = true
		}
	}
	seanceDate := doc.CR.SeanceDate

	for i, fact := range kept {
		if len(embs[i]) == 0 {
			return res, fmt.Errorf("embedding: empty vector for fact %d", i)
		}
		input := core.StoreFactInput{
			SubjectEntity:        doc.Person.Name,
			SubjectEntityType:    "human",
			NormalizedFactText:   fact.NormalizedFactText,
			NormalizedFactTextEn: fact.NormalizedFactTextEn,
			Lang:                 fact.Lang,
			EmbeddingText:        fact.EmbeddingText,
			Embedding:            embs[i],
			OriginalQuote:        fact.OriginalQuote,
			CanonicalVerb:        fact.CanonicalVerb,
			InvolvedPeople:       fact.InvolvedPeople,
			SourceURL:            doc.SourceURL,
			PublishedAt:          &seanceDate,
			SourceType:           debatsSourceType,
			Metadata: map[string]any{
				"cr_uid":        doc.CR.UID,
				"seance_date":   seanceDate.Format("2006-01-02"),
				"actor_id":      doc.ActorID,
				"paragraph_ids": paragraphIDs,
				"textes":        sortedKeys(textes),
				"articles":      sortedKeys(articles),
				"amendements":   sortedKeys(amendements),
			},
		}
		if input.Lang == "" {
			input.Lang = "fr"
		}
		r, err := inserter.InsertFacts(ctx, []core.StoreFactInput{input})
		if err != nil {
			return res, fmt.Errorf("inserting fact %d/%d: %w", i+1, len(kept), err)
		}
		res.Inserted += r.Inserted
		res.Skipped += r.Skipped
	}

	if err := storer.RecordDebateDocument(ctx, storage.DebateDocumentRecord{
		DebateDocumentKey: doc.key(), SourceURL: doc.SourceURL, PersonID: doc.Person.ID,
		SeanceDate: seanceDate, Paragraphs: len(doc.Paragraphs), Chars: doc.Chars, FactsCount: res.Inserted, FactsDropped: res.DroppedSubject,
	}); err != nil {
		return res, err
	}
	return res, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// subjectMatches keeps only facts the extractor attributed to the speaker:
// a transcript is first-person speech, so a fact about someone else is the
// speaker quoting or attacking a third party, not the speaker's own position.
func subjectMatches(fact backend.Fact, doc *debateDocument) bool {
	subj := normalizeForMatch(fact.SubjectEntity)
	if subj == "" {
		return false
	}
	return subj == normalizeForMatch(doc.Speaker) || subj == normalizeForMatch(doc.Person.Name)
}

func normalizeForMatch(s string) string {
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, strings.ToLower(strings.TrimSpace(s)))
	if err != nil {
		out = strings.ToLower(strings.TrimSpace(s))
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(out, "-", " ")), " ")
}

const debatsExtractionInstructions = `The content is the verbatim official transcript (compte rendu intégral) of what the TARGET said in the French National Assembly on the date given in the header — every sentence in the body is the target's own words, spoken publicly in the hemicycle. Extract the positions, commitments, demands, claims, denials and votes the target states in their own voice. Ignore procedural sentences (thanks, yielding the floor, points of order), audience reactions in parentheses, and anything the target attributes to someone else — only what the target themselves asserts. Bracketed lines such as "[article 1er, amendement n° 109]" mark which article or amendment the following paragraphs are about: use them to make each fact self-contained (say which text, article or amendment a position is about). Every fact's date is the date of the sitting.`

// renderDebateDocument renders the document handed to the extractor: a header
// naming the speaker and the sitting, then the paragraphs grouped per
// ordre-du-jour point (which names the texte), each preceded by a bracketed
// marker whenever the article / amendement under discussion changes.
func renderDebateDocument(doc *debateDocument) string {
	var b strings.Builder
	cr := doc.CR
	fmt.Fprintf(&b, "Intervention de %s", doc.Speaker)
	if q := strings.TrimSpace(doc.Paragraphs[0].Qualite); q != "" {
		fmt.Fprintf(&b, " (%s)", q)
	}
	fmt.Fprintf(&b, " en séance publique à l'Assemblée nationale, %s (%s).\n", cr.DateSeanceJour, seanceOrdinalLabel(cr.NumSeanceJour))

	lastPt, lastMarker := "\x00", "\x00"
	for _, p := range doc.Paragraphs {
		if p.PtsODJ != lastPt {
			lastPt = p.PtsODJ
			lastMarker = "\x00"
			b.WriteString("\n")
			if pt, ok := cr.Points[p.PtsODJ]; ok && pt.Title != "" {
				fmt.Fprintf(&b, "Débat : %s", pt.Title)
				if pt.TexteNum != "" {
					fmt.Fprintf(&b, " (texte n° %s)", pt.TexteNum)
				}
				b.WriteString(".\n")
			}
		}
		if m := articleMarker(p); m != lastMarker {
			lastMarker = m
			if m != "" {
				fmt.Fprintf(&b, "\n[%s]\n", m)
			}
		}
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(p.Text))
		b.WriteString("\n")
	}
	return b.String()
}

// articleMarker names the article / amendement / sous-amendement a paragraph
// was spoken on, e.g. "article 1er, amendement n° 109"; empty when none.
func articleMarker(p crParagraph) string {
	var parts []string
	if a := strings.TrimSpace(p.Article); a != "" {
		parts = append(parts, "article "+a)
	}
	if a := strings.TrimSpace(p.Amendement); a != "" {
		parts = append(parts, "amendement n° "+a)
	}
	if a := strings.TrimSpace(p.SousAmdt); a != "" {
		parts = append(parts, "sous-amendement n° "+a)
	}
	return strings.Join(parts, ", ")
}

func seanceOrdinalLabel(num string) string {
	switch strings.TrimSpace(num) {
	case "1":
		return "première séance"
	case "2":
		return "deuxième séance"
	case "3":
		return "troisième séance"
	default:
		return "séance unique"
	}
}

var civilityRe = regexp.MustCompile(`^(?:M\.|Mme|Mlle|M)\s+`)

func stripCivility(name string) string {
	return strings.TrimSpace(civilityRe.ReplaceAllString(strings.TrimSpace(name), ""))
}

// compteRenduPermalink builds the public AN page of the séance with the
// paragraph anchor, e.g.
// https://www.assemblee-nationale.fr/dyn/17/comptes-rendus/seance/session-ordinaire-de-2025-2026/deuxieme-seance-du-lundi-26-janvier-2026#3999160
// The session slug comes from the uid (CRSANR5L17S2026O1N126: parliamentary
// year 2026, O=ordinaire / E1,E2=extraordinaire / D=de droit); the séance slug
// from numSeanceJour + dateSeanceJour. Verified against the live site for
// every session kind of the 17th legislature.
func compteRenduPermalink(cr *compteRendu, idSyceron string) string {
	return fmt.Sprintf(anCompteRenduBaseURL, cr.Legislature, sessionSlug(cr.UID), seanceSlug(cr.NumSeanceJour, cr.DateSeanceJour)) + "#" + idSyceron
}

var uidRe = regexp.MustCompile(`S(\d{4})([OED])(\d)N\d+$`)

func sessionSlug(uid string) string {
	m := uidRe.FindStringSubmatch(uid)
	if m == nil {
		return "session-ordinaire"
	}
	year := m[1]
	prev := fmt.Sprintf("%d", atoiOr(year, 0)-1)
	switch m[2] {
	case "D":
		return "session-de-droit-de-" + year
	case "E":
		if m[3] == "1" {
			return fmt.Sprintf("session-extraordinaire-de-%s-%s", prev, year)
		}
		return fmt.Sprintf("%se-session-extraordinaire-de-%s-%s", m[3], prev, year)
	default:
		return fmt.Sprintf("session-ordinaire-de-%s-%s", prev, year)
	}
}

func seanceSlug(numSeanceJour, dateSeanceJour string) string {
	var prefix string
	switch strings.TrimSpace(numSeanceJour) {
	case "1":
		prefix = "premiere-seance-du-"
	case "2":
		prefix = "deuxieme-seance-du-"
	case "3":
		prefix = "troisieme-seance-du-"
	default:
		prefix = "seance-du-"
	}
	return prefix + slugify(dateSeanceJour)
}

func slugify(s string) string {
	return strings.Join(strings.Fields(normalizeForMatch(s)), "-")
}

func atoiOr(s string, def int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return def
		}
		n = n*10 + int(r-'0')
	}
	return n
}

var texteNumRe = regexp.MustCompile(`(\d{1,5})`)

// parseCompteRendu walks one séance file with a token decoder (the nesting of
// <point> / <paragraphe> is irregular) and collects the metadata, the
// ordre-du-jour points that name a texte, and every paragraph with its
// speaker and inline text.
func parseCompteRendu(r io.Reader) (*compteRendu, error) {
	cr := &compteRendu{Points: map[string]odjPoint{}}
	dec := xml.NewDecoder(r)
	var (
		path      []string
		curPara   *crParagraph
		paraDepth int
		inTexte   bool
		textBuf   strings.Builder
		curPoint  *odjPoint
		pointKey  string
		pointDpth int
		inPtTexte bool
		ptBuf     strings.Builder
		metaField string
	)
	attr := func(se xml.StartElement, name string) string {
		for _, a := range se.Attr {
			if a.Name.Local == name {
				return a.Value
			}
		}
		return ""
	}
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			path = append(path, t.Name.Local)
			depth := len(path)
			switch {
			case t.Name.Local == "uid" && depth == 2:
				metaField = "uid"
			case depth == 3 && path[1] == "metadonnees":
				metaField = t.Name.Local
			case t.Name.Local == "point" && attr(t, "code_grammaire") == "TITRE_TEXTE_DISCUSSION":
				curPoint = &odjPoint{}
				pointKey = attr(t, "valeur_ptsodj")
				pointDpth = depth
				if m := texteNumRe.FindStringSubmatch(attr(t, "valeur")); m != nil {
					curPoint.TexteNum = m[1]
				}
			case t.Name.Local == "texte" && curPoint != nil && depth == pointDpth+1:
				inPtTexte = true
				ptBuf.Reset()
			case t.Name.Local == "paragraphe" && curPara == nil:
				curPara = &crParagraph{
					IDSyceron:  attr(t, "id_syceron"),
					ActorID:    attr(t, "id_acteur"),
					RoleDebat:  attr(t, "roledebat"),
					CodeGram:   attr(t, "code_grammaire"),
					PtsODJ:     attr(t, "valeur_ptsodj"),
					Article:    attr(t, "art"),
					Amendement: attr(t, "adt"),
					SousAmdt:   attr(t, "ssadt"),
				}
				paraDepth = depth
				textBuf.Reset()
			case curPara != nil && t.Name.Local == "texte" && depth == paraDepth+1:
				inTexte = true
			case curPara != nil && t.Name.Local == "nom" && depth == paraDepth+3 && path[depth-2] == "orateur":
				metaField = "orateur.nom"
			case curPara != nil && t.Name.Local == "qualite" && depth == paraDepth+3 && path[depth-2] == "orateur":
				metaField = "orateur.qualite"
			}
		case xml.CharData:
			s := string(t)
			switch {
			case inTexte:
				textBuf.WriteString(s)
			case inPtTexte:
				ptBuf.WriteString(s)
			case metaField != "":
				v := strings.TrimSpace(s)
				switch metaField {
				case "uid":
					cr.UID += v
				case "dateSeance":
					if len(v) >= 8 {
						if d, err := time.Parse("20060102", v[:8]); err == nil {
							cr.SeanceDate = d
						}
					}
				case "dateSeanceJour":
					cr.DateSeanceJour += v
				case "numSeanceJour":
					cr.NumSeanceJour += v
				case "session":
					cr.Session += v
				case "legislature":
					cr.Legislature = atoiOr(v, cr.Legislature)
				case "orateur.nom":
					// A paragraph may list several <orateur>s; the first named
					// one is the speaker (the others are empty in practice).
					if curPara != nil && curPara.SpeakerName == "" {
						curPara.SpeakerName = v
					}
				case "orateur.qualite":
					if curPara != nil && curPara.Qualite == "" {
						curPara.Qualite = v
					}
				}
			}
		case xml.EndElement:
			depth := len(path)
			switch {
			case inTexte && t.Name.Local == "texte" && depth == paraDepth+1:
				inTexte = false
			case inPtTexte && t.Name.Local == "texte" && curPoint != nil && depth == pointDpth+1:
				inPtTexte = false
				curPoint.Title = strings.Join(strings.Fields(ptBuf.String()), " ")
			case curPoint != nil && t.Name.Local == "point" && depth == pointDpth:
				if _, exists := cr.Points[pointKey]; !exists {
					cr.Points[pointKey] = *curPoint
				}
				curPoint = nil
			case curPara != nil && t.Name.Local == "paragraphe" && depth == paraDepth:
				curPara.Text = strings.Join(strings.Fields(textBuf.String()), " ")
				cr.Paragraphs = append(cr.Paragraphs, *curPara)
				curPara = nil
			}
			if metaField != "" && (t.Name.Local == metaField || strings.HasSuffix(metaField, "."+t.Name.Local)) {
				metaField = ""
			}
			if depth > 0 {
				path = path[:depth-1]
			}
		}
	}
	if cr.UID == "" || cr.SeanceDate.IsZero() {
		return nil, fmt.Errorf("compte rendu without uid/date")
	}
	if cr.Legislature == 0 {
		cr.Legislature = 17
	}
	return cr, nil
}
