// Package viepublique is the vie-publique.fr source: the speeches,
// interviews, press conferences and communiqués of the executive that the
// DILA publishes as open data ("discours publics", Etalab licence v2). The
// index names the speakers and dates of each text; the page holds the text.
// A fact is the person's own words, quoted verbatim, dated the day they were
// spoken.
package viepublique

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/util"

	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

const (
	sourceType = "viepublique"
	// attribution is the mention the licence asks for, in the rendered
	// text; a fact carries the full one (factAttribution).
	attribution = "Source : DILA, vie-publique.fr (Licence Ouverte v2)"
	// minOwnChars leaves out the texts where the person only greets or
	// thanks.
	minOwnChars = 200
	// maxQuestionChars bounds the question kept as context of an answer.
	maxQuestionChars = 700
	// pagesAtOnce is how many pages of a person are asked at once; the site
	// paces them (site.go).
	pagesAtOnce = 4
	// defaultDays is the window of a run without dates.
	defaultDays = 3
	// maxConsecutiveFailures stops a run whose documents fail back to back:
	// an outage, not bad documents.
	maxConsecutiveFailures = 10
)

// Plugin is the vie-publique.fr source.
type Plugin struct{}

// Name implements plugins.Plugin.
func (Plugin) Name() string { return "viepublique" }

// HistoryFetched implements plugins.HistoryReader: every day of the stretch
// is in a fetched window of every target.
func (Plugin) HistoryFetched(ctx context.Context, d *deps.Deps, targets []plugins.Target, since, until time.Time) (bool, error) {
	return NewStore(d.Storer).WindowsCover(ctx, plugins.TargetIDs(targets), since, until)
}

// Ingest implements plugins.Ingester: the speeches of each target given or
// put online in the window are fetched, saved and extracted.
func (Plugin) Ingest(ctx context.Context, d *deps.Deps, targets []plugins.Target, opts plugins.IngestOptions) (plugins.IngestStats, error) {
	var stats plugins.IngestStats
	until := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
	if opts.Until != nil {
		until = *opts.Until
	}
	since := until.AddDate(0, 0, -defaultDays)
	if opts.Since != nil {
		since = *opts.Since
	}
	keys := map[string]bool{}
	var persons []plugins.Target
	for _, t := range targets {
		if t.IsParty() {
			stats.Done = append(stats.Done, t.PersonID)
			continue
		}
		keys[util.PersonNameKey(t.Name)] = true
		persons = append(persons, t)
	}
	if len(persons) == 0 {
		return stats, nil
	}

	f, err := openIndex(ctx)
	if err != nil {
		return stats, fmt.Errorf("viepublique: %w", err)
	}
	var indexDate time.Time
	if info, err := f.Stat(); err == nil {
		indexDate = info.ModTime().UTC()
	}
	speeches, err := SpeechesOf(f, keys, since, until)
	f.Close()
	if err != nil {
		return stats, fmt.Errorf("viepublique: %w", err)
	}

	ing, err := newIngester(d)
	if err != nil {
		return stats, fmt.Errorf("viepublique: %w", err)
	}
	ing.indexDate = indexDate
	extractOnly := map[int64]bool{}
	for _, id := range opts.ExtractOnly {
		extractOnly[id] = true
	}
	var firstErr error
	for _, t := range persons {
		if err := ctx.Err(); err != nil {
			return stats, errors.Join(firstErr, fmt.Errorf("viepublique stopped before %s: %w", t.Name, err))
		}
		list := speeches[util.PersonNameKey(t.Name)]
		st, err := ing.target(ctx, t, list, since, until, opts, extractOnly[t.PersonID])
		stats.Add(st)
		if err != nil {
			log.Error().Err(err).Str("person", t.Name).Msg("viepublique: collection failed")
			stats.Details = append(stats.Details, t.Name+": "+err.Error())
			if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
				return stats, errors.Join(firstErr, err)
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	log.Info().Int("persons", len(persons)).Int("speeches", stats.Items).Int("facts", stats.Facts).
		Msg("viepublique: collection completed")
	return stats, firstErr
}

// ingester holds the shared components of a run.
type ingester struct {
	d         *deps.Deps
	extractor *categorizer.OpenAIExtractor
	embedder  *embedding.OpenAIEmbeddingGenerator
	inserter  *core.FactInserter
	// indexDate is the date of the index file the speeches come from.
	indexDate time.Time
}

func newIngester(d *deps.Deps) (*ingester, error) {
	extractor, err := d.FactExtractor()
	if err != nil {
		return nil, fmt.Errorf("fact extractor: %w", err)
	}
	embedder, err := d.Embedder()
	if err != nil {
		return nil, fmt.Errorf("embedder: %w", err)
	}
	stanceMgr, err := d.StanceManager()
	if err != nil {
		return nil, fmt.Errorf("stance manager: %w", err)
	}
	inserter := core.NewFactInserter(d.Storer, stanceMgr)
	inserter.EnableDedupe(d.Storer)
	return &ingester{d: d, extractor: extractor, embedder: embedder, inserter: inserter}, nil
}

// target fetches the speeches of one person not stored yet, logs the window
// once all of them are, and extracts every unfinished document of the person.
func (ing *ingester) target(ctx context.Context, t plugins.Target, list []Speech, since, until time.Time, opts plugins.IngestOptions, extractOnly bool) (plugins.IngestStats, error) {
	stats := plugins.IngestStats{Targets: 1}
	var fetchErr error
	if !extractOnly {
		var n int
		n, fetchErr = ing.fetch(ctx, t, list, opts)
		stats.Items = n
		if ctx.Err() != nil {
			return stats, fmt.Errorf("viepublique stopped fetching for %s: %w", t.Name, ctx.Err())
		}
		if opts.DryRun {
			stats.Done = []int64{t.PersonID}
			return stats, fetchErr
		}
		if fetchErr == nil {
			if err := NewStore(ing.d.Storer).LogWindow(ctx, t.PersonID, since, until, len(list)); err != nil {
				fetchErr = err
			}
		}
	}

	extract := func(ctx context.Context, batch []core.SourceDocument) (map[int64]int, error) {
		n, err := ing.extract(ctx, t, batch[0])
		if err != nil {
			return nil, err
		}
		return map[int64]int{batch[0].ID: n}, nil
	}
	ps, err := core.ProcessDocuments(ctx, ing.d.Storer, sourceType, t.PersonID, extract, core.ProcessOptions{
		MaxConsecutiveFailures: maxConsecutiveFailures,
	})
	stats.Facts = ps.Facts
	stats.Errors = ps.Failed
	if errors.Is(err, llm.ErrUnavailable) {
		stats.Fetched = []int64{t.PersonID}
		return stats, errors.Join(fetchErr, err)
	}
	stats.Done = []int64{t.PersonID}
	return stats, errors.Join(fetchErr, err)
}

// fetch reads the pages of the speeches not stored yet and saves them. A
// page that is gone is saved empty, so it is not asked again; any other
// failure (the site down, its check changed) stops the fetch, and the window
// stays open.
func (ing *ingester) fetch(ctx context.Context, t plugins.Target, list []Speech, opts plugins.IngestOptions) (int, error) {
	sort.Slice(list, func(i, j int) bool { return list[i].Spoken.Before(list[j].Spoken) })
	if opts.MaxItems > 0 && len(list) > opts.MaxItems {
		list = list[len(list)-opts.MaxItems:]
	}
	keys := make([]string, len(list))
	for i, s := range list {
		keys[i] = documentKey(s, t.PersonID)
	}
	known, err := ing.d.Storer.KnownDocumentURLs(ctx, sourceType, keys)
	if err != nil {
		return 0, fmt.Errorf("filtering stored speeches: %w", err)
	}
	var todo []Speech
	for i, s := range list {
		if known[keys[i]] {
			continue
		}
		if opts.DryRun {
			log.Info().Str("person", t.Name).Str("url", s.URL).Str("title", util.TruncateString(s.Title, 120)).Msg("viepublique: would fetch")
			continue
		}
		todo = append(todo, s)
	}
	// The pages are asked a few at once, in order; after a failure other than
	// a gone page (the site down, its check changed) no new page is asked.
	docs := make([]*core.SourceDocument, len(todo))
	errs := make([]error, len(todo))
	var failed atomic.Bool
	var g errgroup.Group
	g.SetLimit(pagesAtOnce)
	for i, s := range todo {
		if failed.Load() || ctx.Err() != nil {
			break
		}
		g.Go(func() error {
			turns, err := fetchPage(ctx, s.URL, t.Name)
			switch {
			case errors.Is(err, errGone):
				log.Warn().Err(err).Str("person", t.Name).Msg("viepublique: page gone")
				d := document(s, t, nil, ing.indexDate)
				docs[i] = &d
			case err != nil:
				errs[i] = err
				failed.Store(true)
			default:
				d := document(s, t, turns, ing.indexDate)
				docs[i] = &d
			}
			return nil
		})
	}
	g.Wait()
	var fresh []core.SourceDocument
	var fetchErr error
	for i := range todo {
		if docs[i] != nil {
			fresh = append(fresh, *docs[i])
		}
		if errs[i] != nil && fetchErr == nil {
			fetchErr = errs[i]
		}
	}
	if len(fresh) > 0 {
		if _, err := ing.d.Storer.SaveFetchedDocuments(ctx, fresh); err != nil {
			return 0, errors.Join(fetchErr, fmt.Errorf("saving speeches: %w", err))
		}
	}
	log.Info().Str("person", t.Name).Int("speeches", len(list)).Int("fetched", len(fresh)).Msg("viepublique: speeches fetched")
	return len(fresh), fetchErr
}

func documentKey(s Speech, personID int64) string {
	return fmt.Sprintf("viepublique:%s/%d", s.ID, personID)
}

// document is the stored form of the person's part of a speech: the rendered
// turns as content, empty when the person says too little in it.
func document(s Speech, t plugins.Target, turns []Turn, indexDate time.Time) core.SourceDocument {
	content := render(s, t.Name, ownTurns(turns, s, t.Name))
	spoken := s.Spoken
	if spoken.IsZero() {
		spoken = s.Online
	}
	return core.SourceDocument{
		SourceType:  sourceType,
		URL:         documentKey(s, t.PersonID),
		PersonID:    t.PersonID,
		Title:       s.Title,
		Content:     content,
		PublishedAt: &spoken,
		Metadata: map[string]any{
			"source_url":  s.URL,
			"speech_id":   s.ID,
			"speech_type": s.Type,
			"media":       s.Media,
			"person_name": t.Name,
			"index_date":  indexDate.Format("2006-01-02"),
		},
	}
}

// factAttribution is the mention the licence asks for: the author, the URL of
// the download, the file and its date.
func factAttribution(indexDate string) string {
	return fmt.Sprintf("DILA, vie-publique.fr, Licence Ouverte v2.0 ; %s, fichier vp_discours.json du %s", indexURL, indexDate)
}

// render is the text handed to the extractor: a header naming the speech,
// then each turn of the person, after the question it answers.
func render(s Speech, person string, exchanges []exchange) string {
	own := 0
	for _, ex := range exchanges {
		own += len([]rune(ex.Answer.Text))
	}
	if own < minOwnChars {
		return ""
	}
	var b strings.Builder
	kind := s.Type
	if kind == "" {
		kind = "Texte"
	}
	fmt.Fprintf(&b, "%s de %s", kind, person)
	for _, sp := range s.Speakers {
		if util.PersonNameKey(sp.Name) == util.PersonNameKey(person) && sp.Role != "" {
			fmt.Fprintf(&b, " (%s)", sp.Role)
		}
	}
	if s.Media != "" {
		fmt.Fprintf(&b, ", %s", s.Media)
	}
	if !s.Spoken.IsZero() {
		// In words: the extractor copies the date it reads into the facts.
		fmt.Fprintf(&b, ", le %s", frenchDate(s.Spoken))
	}
	fmt.Fprintf(&b, ".\nTitre : %s\n%s\n", s.Title, attribution)
	for _, ex := range exchanges {
		if q := strings.TrimSpace(ex.Question.Text); q != "" {
			fmt.Fprintf(&b, "\nQuestion (%s) : %s\n", ex.Question.Label, util.TruncateString(q, maxQuestionChars))
		}
		label := ex.Answer.Label
		if label == "" {
			label = person
		}
		fmt.Fprintf(&b, "\n%s : %s\n", label, ex.Answer.Text)
	}
	return b.String()
}

const extractionInstructions = `The content is the official transcript, published by the French government's DILA on vie-publique.fr, of a speech, interview, press conference or communiqué of the TARGET. The lines that start with the TARGET's name are the TARGET's own words, spoken publicly on the date given in the header. Extract the positions, commitments, demands, claims and denials the TARGET states in their own voice. A line that starts with "Question" is what a journalist asked: it is there only so that a short answer ("Oui", "Absolument", "Non, jamais") can be understood — never extract a fact from a question, and never attribute the journalist's words or premises to the TARGET unless the TARGET takes them up. Ignore greetings and thanks. Every fact's date is the date in the header, and every quote must be the TARGET's exact words.`

// extract is the extract step of one stored document: LLM extraction, the
// facts about the person only, embeddings and the normal insertion path.
func (ing *ingester) extract(ctx context.Context, t plugins.Target, doc core.SourceDocument) (int, error) {
	if strings.TrimSpace(doc.Content) == "" {
		return 0, nil
	}
	sourceURL, _ := doc.Metadata["source_url"].(string)
	var spoken time.Time
	if doc.PublishedAt != nil {
		spoken = *doc.PublishedAt
	}
	facts, err := ing.extractor.ExtractFactsWithConfig(ctx, promptrules.FactExtractorConfig{
		TargetName:         t.Name,
		SourceURL:          sourceURL,
		Content:            doc.Content,
		PluginInstructions: extractionInstructions,
		Now:                spoken,
	})
	if err != nil {
		return 0, fmt.Errorf("extracting %s: %w", sourceURL, err)
	}
	kept := make([]backend.Fact, 0, len(facts))
	for _, f := range facts {
		if util.PersonNameKey(f.SubjectEntity) == util.PersonNameKey(t.Name) {
			kept = append(kept, f)
			continue
		}
		log.Info().Str("subject", f.SubjectEntity).Str("person", t.Name).Str("fact", util.TruncateString(f.NormalizedFactText, 100)).
			Msg("viepublique: dropping a fact whose subject is not the speaker")
	}
	if len(kept) == 0 {
		return 0, nil
	}
	texts := make([]string, len(kept))
	for i, f := range kept {
		texts[i] = f.EmbeddingText
	}
	embs, err := ing.embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, texts)
	if err != nil {
		return 0, fmt.Errorf("embedding %d facts of %s: %w", len(kept), sourceURL, err)
	}
	if len(embs) != len(kept) {
		return 0, fmt.Errorf("embedding: got %d vectors for %d facts", len(embs), len(kept))
	}
	inserted := 0
	for i, f := range kept {
		lang := f.Lang
		if lang == "" {
			lang = "fr"
		}
		input := core.StoreFactInput{
			SubjectEntity:        t.Name,
			SubjectEntityType:    "human",
			NormalizedFactText:   f.NormalizedFactText,
			NormalizedFactTextEn: f.NormalizedFactTextEn,
			Lang:                 lang,
			EmbeddingText:        f.EmbeddingText,
			Embedding:            embs[i],
			OriginalQuote:        f.OriginalQuote,
			CanonicalVerb:        f.CanonicalVerb,
			InvolvedPeople:       f.InvolvedPeople,
			SourceURL:            sourceURL,
			PublishedAt:          doc.PublishedAt,
			SourceType:           sourceType,
			Metadata: map[string]any{
				"speech_id":   doc.Metadata["speech_id"],
				"speech_type": doc.Metadata["speech_type"],
				"media":       doc.Metadata["media"],
				"attribution": factAttribution(fmt.Sprint(doc.Metadata["index_date"])),
			},
		}
		r, err := ing.inserter.InsertFacts(ctx, []core.StoreFactInput{input})
		if core.FailsDocument(ctx, err) {
			return inserted, fmt.Errorf("storing a fact of %s: %w", sourceURL, err)
		}
		if err != nil {
			log.Warn().Err(err).Str("fact", util.TruncateString(f.NormalizedFactText, 80)).Msg("viepublique: failed to store a fact")
			continue
		}
		inserted += r.Inserted + r.Skipped
	}
	return inserted, nil
}

var frenchMonths = [...]string{"janvier", "février", "mars", "avril", "mai", "juin", "juillet", "août", "septembre", "octobre", "novembre", "décembre"}

// frenchDate writes t as "3 mars 2025" ("1er" for the first day).
func frenchDate(t time.Time) string {
	day := strconv.Itoa(t.Day())
	if t.Day() == 1 {
		day = "1er"
	}
	return fmt.Sprintf("%s %s %d", day, frenchMonths[t.Month()-1], t.Year())
}
