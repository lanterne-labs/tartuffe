package frpolitics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
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
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

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
	// document identity is (cr_uid, actor_id), so tuning it never
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
	// PrioritizePress orders documents by the speaker's press presence first
	// (persons with news/twitter facts, most first), then by date: the
	// backfill spends its window where transcript facts can meet press facts.
	PrioritizePress bool
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
	SeanceRef      string // the Assemblée's séance id, shared with scrutin.seanceRef
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

// docResult is what ingesting one document yields.
type docResult struct {
	Inserted, Skipped, DroppedSubject int
}

// RunDebats ingests séance transcripts for tracked persons: parses the
// archive, builds one document per (speaker, séance) not yet processed,
// extracts facts with the shared extractor and inserts them through the
// normal FactInserter path (dedupe, stance routing, analyze). Idempotent:
// documents live in the shared document state machine (core.SourceDocument),
// keyed by (cr_uid, actor_id), so a processed one is never rebuilt.
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

	store := NewStore(storer)
	persons, err := store.GetPersonsByAssembleeID(ctx)
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

	// Fetch step of the document state machine: every document not yet
	// known is saved with its rendered transcript, keyed by its identity
	// (compte-rendu uid + actor id), which is stable across republished
	// séances where the permalink's anchor moves.
	keys := make([]string, len(docs))
	for i := range docs {
		keys[i] = docs[i].documentKey()
	}
	known, err := storer.KnownDocumentURLs(ctx, debatsSourceType, keys)
	if err != nil {
		return stats, fmt.Errorf("debats: filtering processed documents: %w", err)
	}
	var fresh []core.SourceDocument
	for i := range docs {
		if !known[keys[i]] {
			fresh = append(fresh, docs[i].toSourceDocument())
		}
	}
	if _, err := storer.SaveFetchedDocuments(ctx, fresh); err != nil {
		return stats, fmt.Errorf("debats: saving documents: %w", err)
	}
	log.Info().Int("documents", len(docs)).Int("saved", len(fresh)).Msg("debats: new documents saved")

	extractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return stats, fmt.Errorf("debats: extractor: %w", err)
	}
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return stats, fmt.Errorf("debats: embedder: %w", err)
	}
	stanceMgr, err := core.NewStanceManagerFromEnv(storer)
	if err != nil {
		return stats, fmt.Errorf("debats: stance manager: %w", err)
	}
	inserter := core.NewFactInserter(storer, stanceMgr)
	inserter.EnableDedupe(storer)

	var priority map[int64]int
	if opts.PrioritizePress {
		priority, err = storer.GetPersonFactCountsBySourceTypes(ctx, pressSourceTypes)
		if err != nil {
			return stats, fmt.Errorf("debats: loading press fact counts: %w", err)
		}
	}
	extract := func(ctx context.Context, batch []core.SourceDocument) (map[int64]int, error) {
		res, err := ingestDebateDocument(ctx, extractor, embedder, inserter, batch[0])
		stats.FactsSkipped += res.Skipped
		stats.FactsDroppedSubject += res.DroppedSubject
		if err != nil {
			return nil, err
		}
		if res.Inserted == 0 {
			stats.NoFacts++
		}
		return map[int64]int{batch[0].ID: res.Inserted}, nil
	}
	ps, runErr := core.ProcessDocuments(ctx, storer, debatsSourceType, 0, extract, core.ProcessOptions{
		Deadline:               opts.Deadline,
		PublishedSince:         opts.Since,
		MaxDocs:                opts.MaxDocs,
		MaxConsecutiveFailures: debatsMaxConsecutiveFailures,
		Order:                  debateDocumentOrder(opts, priority),
	})
	stats.Pending, stats.Attempted, stats.Processed, stats.Facts, stats.Errors, stats.DeadlineHit =
		ps.Pending, ps.Attempted, ps.Extracted, ps.Facts, ps.Failed, ps.DeadlineHit
	stats.PendingAfter = stats.Pending - stats.Processed
	stats.ElapsedSeconds = time.Since(start).Seconds()
	log.Info().
		Int("attempted", stats.Attempted).Int("processed", stats.Processed).Int("facts", stats.Facts).
		Int("skipped", stats.FactsSkipped).Int("dropped_subject", stats.FactsDroppedSubject).
		Int("no_facts", stats.NoFacts).Int("errors", stats.Errors).Int("pending_after", stats.PendingAfter).
		Bool("deadline_hit", stats.DeadlineHit).Dur("elapsed", time.Since(start)).
		Msg("debats: run finished")
	return stats, runErr
}

// debateDocumentOrder is the processing order of the unfinished documents:
// speakers with the most press facts first when opts.PrioritizePress (the
// backfill spends its window where transcript facts can meet press facts),
// then by séance date, newest first when opts.NewestFirst.
func debateDocumentOrder(opts DebatsOptions, priority map[int64]int) func(a, b core.SourceDocument) bool {
	return func(a, b core.SourceDocument) bool {
		if opts.PrioritizePress {
			pa, pb := priority[a.PersonID], priority[b.PersonID]
			if pa != pb {
				return pa > pb
			}
		}
		da, db := docTime(a), docTime(b)
		if opts.NewestFirst {
			return da.After(db)
		}
		return da.Before(db)
	}
}

func docTime(d core.SourceDocument) time.Time {
	if d.PublishedAt != nil {
		return *d.PublishedAt
	}
	return d.FetchedAt
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
// ingestDebateDocument is the extract step for one stored transcript
// document: LLM extraction, the speaker-only subject filter, embeddings and
// the normal FactInserter path. The document's metadata carries what the
// facts' provenance needs (séance, actor, paragraphs, textes, articles,
// amendements), written by toSourceDocument at fetch time.
func ingestDebateDocument(ctx context.Context, extractor *categorizer.OpenAIExtractor, embedder *embedding.OpenAIEmbeddingGenerator, inserter *core.FactInserter, doc core.SourceDocument) (docResult, error) {
	var res docResult
	meta := debateMeta(doc)
	seanceDate := docTime(doc)
	facts, err := extractor.ExtractFactsWithConfig(ctx, promptrules.FactExtractorConfig{
		TargetName:         meta.speaker,
		SourceURL:          meta.sourceURL,
		Content:            doc.Content,
		PluginInstructions: debatsExtractionInstructions,
		Now:                seanceDate,
	})
	if err != nil {
		return res, fmt.Errorf("extracting: %w", err)
	}

	kept := make([]backend.Fact, 0, len(facts))
	for _, f := range facts {
		if subjectMatches(f, meta.speaker, meta.personName) {
			kept = append(kept, f)
		} else {
			res.DroppedSubject++
			log.Info().Str("subject", f.SubjectEntity).Str("speaker", meta.speaker).Str("fact", util.TruncateString(f.NormalizedFactText, 100)).
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

	for i, fact := range kept {
		if len(embs[i]) == 0 {
			return res, fmt.Errorf("embedding: empty vector for fact %d", i)
		}
		input := core.StoreFactInput{
			SubjectEntity:        meta.personName,
			SubjectEntityType:    "human",
			NormalizedFactText:   fact.NormalizedFactText,
			NormalizedFactTextEn: fact.NormalizedFactTextEn,
			Lang:                 fact.Lang,
			EmbeddingText:        fact.EmbeddingText,
			Embedding:            embs[i],
			OriginalQuote:        fact.OriginalQuote,
			StatementDate:        fact.StatementDate,
			CanonicalVerb:        fact.CanonicalVerb,
			InvolvedPeople:       fact.InvolvedPeople,
			SourceURL:            meta.sourceURL,
			PublishedAt:          &seanceDate,
			SourceType:           debatsSourceType,
			Metadata: map[string]any{
				"cr_uid":        meta.crUID,
				"seance_date":   seanceDate.Format("2006-01-02"),
				"actor_id":      meta.actorID,
				"paragraph_ids": meta.paragraphIDs,
				"textes":        meta.textes,
				"articles":      meta.articles,
				"amendements":   meta.amendements,
			},
		}
		if input.Lang == "" {
			input.Lang = "fr"
		}
		if meta.seanceRef != "" {
			input.Metadata["seance_ref"] = meta.seanceRef
		}
		r, err := inserter.InsertFacts(ctx, []core.StoreFactInput{input})
		if err != nil {
			return res, fmt.Errorf("inserting fact %d/%d: %w", i+1, len(kept), err)
		}
		res.Inserted += r.Inserted
		res.Skipped += r.Skipped
	}
	return res, nil
}

// documentKey is the document's identity in the document store: the
// compte-rendu uid and the speaker's actor id, stable across republished
// séances and threshold changes (the permalink's anchor is not).
func (d *debateDocument) documentKey() string {
	return debatsDocumentKey(d.CR.UID, d.ActorID)
}

func debatsDocumentKey(crUID, actorID string) string {
	return "debats:" + crUID + "/" + actorID
}

// toSourceDocument is the stored form of a (speaker, séance) document: the
// rendered transcript as content, the provenance the facts need as metadata.
func (d *debateDocument) toSourceDocument() core.SourceDocument {
	paragraphIDs := make([]string, len(d.Paragraphs))
	textes, articles, amendements := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i, p := range d.Paragraphs {
		paragraphIDs[i] = p.IDSyceron
		if pt, ok := d.CR.Points[p.PtsODJ]; ok && pt.TexteNum != "" {
			textes[pt.TexteNum] = true
		}
		if a := strings.TrimSpace(p.Article); a != "" {
			articles[a] = true
		}
		if a := strings.TrimSpace(p.Amendement); a != "" {
			amendements[a] = true
		}
	}
	seanceDate := d.CR.SeanceDate
	return core.SourceDocument{
		SourceType:  debatsSourceType,
		URL:         d.documentKey(),
		PersonID:    d.Person.ID,
		Title:       fmt.Sprintf("%s — %s", d.Speaker, d.CR.DateSeanceJour),
		Content:     renderDebateDocument(d),
		PublishedAt: &seanceDate,
		Metadata: map[string]any{
			"source_url":    d.SourceURL,
			"cr_uid":        d.CR.UID,
			"seance_ref":    strings.TrimSpace(d.CR.SeanceRef),
			"actor_id":      d.ActorID,
			"speaker":       d.Speaker,
			"person_name":   d.Person.Name,
			"paragraphs":    len(d.Paragraphs),
			"chars":         d.Chars,
			"paragraph_ids": paragraphIDs,
			"textes":        sortedKeys(textes),
			"articles":      sortedKeys(articles),
			"amendements":   sortedKeys(amendements),
		},
	}
}

// debateDocMeta is the provenance read back from a stored document.
type debateDocMeta struct {
	sourceURL, crUID, seanceRef, actorID, speaker, personName string
	paragraphIDs, textes, articles, amendements               []string
}

func debateMeta(doc core.SourceDocument) debateDocMeta {
	str := func(k string) string { v, _ := doc.Metadata[k].(string); return v }
	strs := func(k string) []string {
		raw, _ := doc.Metadata[k].([]any)
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return debateDocMeta{
		sourceURL: str("source_url"), crUID: str("cr_uid"), seanceRef: str("seance_ref"), actorID: str("actor_id"),
		speaker: str("speaker"), personName: str("person_name"),
		paragraphIDs: strs("paragraph_ids"), textes: strs("textes"), articles: strs("articles"), amendements: strs("amendements"),
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// subjectMatches keeps only facts the extractor attributed to the speaker:
// a transcript is first-person speech, so a fact about someone else is the
// speaker quoting or attacking a third party, not the speaker's own position.
func subjectMatches(fact backend.Fact, speaker, personName string) bool {
	subj := normalizeForMatch(fact.SubjectEntity)
	if subj == "" {
		return false
	}
	return subj == normalizeForMatch(speaker) || subj == normalizeForMatch(personName)
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
			case t.Name.Local == "seanceRef" && depth == 2:
				metaField = "seanceRef"
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
				case "seanceRef":
					cr.SeanceRef += v
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

// EnqueueSameSeancePairs asks the contradiction detector to judge, for every
// person, each (transcript fact, vote fact) pair whose sources carry the same
// Assemblée séance id — what the person said in a sitting versus how they
// voted in that sitting — regardless of embedding distance. Idempotent; the
// daily pipeline calls it right before detection.
func EnqueueSameSeancePairs(ctx context.Context, storer *storage.PostgresStorer) (int, error) {
	return storer.EnqueueFactPairsBySharedSourceKey(ctx, debatsSourceType, "assemblee_law_page", "seance_ref", "frpolitics:same-seance")
}

// BackfillVoteSeanceRefs stamps seance_ref on vote sources ingested before the
// key existed, from the Scrutins archive (law_key = legislature-numero).
// Returns the number of sources updated.
func BackfillVoteSeanceRefs(ctx context.Context, storer *storage.PostgresStorer, legislature int) (int, error) {
	if legislature <= 0 {
		legislature = 17
	}
	body, err := downloadCached(ctx, fmt.Sprintf("https://data.assemblee-nationale.fr/static/openData/repository/%d/loi/scrutins/Scrutins.json.zip", legislature), getCacheDir(), 24*time.Hour)
	if err != nil {
		return 0, fmt.Errorf("downloading scrutins: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return 0, fmt.Errorf("reading scrutins archive: %w", err)
	}
	byLawKey := map[string]string{}
	for _, f := range zr.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("frpolitics: cannot open scrutin entry")
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, 10<<20))
		rc.Close()
		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("frpolitics: cannot read scrutin entry")
			continue
		}
		var sj scrutinJSON
		if err := json.Unmarshal(data, &sj); err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("frpolitics: cannot parse scrutin entry")
			continue
		}
		if ref := strings.TrimSpace(sj.Scrutin.SeanceRef); ref != "" {
			byLawKey[fmt.Sprintf("%d-%d", int(sj.Scrutin.Legislature), int(sj.Scrutin.Numero))] = ref
		}
	}
	return storer.SetFactSourceMetadataByKey(ctx, "assemblee_law_page", "law_key", "seance_ref", byLawKey)
}
