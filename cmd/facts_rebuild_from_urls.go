package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

const (
	rebuildCheckpointFile = "facts_rebuild_progress.json"
	rebuildLogFile        = "facts_rebuild_log.jsonl"
)

type rebuildCheckpoint struct {
	DonePersonURL map[string]bool `json:"done_person_url"`
}

type rebuildLogEntry struct {
	Timestamp         string `json:"ts"`
	Person            string `json:"person"`
	URL               string `json:"url"`
	DeletedSourceIDs  []int64 `json:"deleted_source_ids,omitempty"`
	FactsExtracted    int    `json:"facts_extracted"`
	FactsSkippedDedup int    `json:"facts_skipped_dedup"`
	SkippedThin       int    `json:"skipped_thin"`
	ErrorCount        int    `json:"error_count"`
	Errors            []string `json:"errors,omitempty"`
}

var factsRebuildFromURLsCmd = &cobra.Command{
	Use:   "rebuild-from-urls",
	Short: "Re-run the news extraction pipeline against URLs that previously produced facts for given persons.",
	Long: `For each --person, finds the distinct news source_urls that previously produced
fact_mentions for that person, deletes those fact_sources (cascading mentions),
sweeps orphan facts, then re-scrapes each URL via the cleaning pipeline and
extracts facts as the daily pipeline would. Stance routing and contradiction
detection run inline via the normal FactInserter path.

Resumable via facts_rebuild_progress.json. Append-only audit at facts_rebuild_log.jsonl.

Example (test phase):
  ./dm facts rebuild-from-urls --person "Emmanuel Macron" --person "Marine Le Pen" --person "Donald Trump"`,
	RunE: runRebuildFromURLs,
}

func init() {
	factsCmd.AddCommand(factsRebuildFromURLsCmd)
	factsRebuildFromURLsCmd.Flags().StringSlice("person", nil, "Person name(s) to rebuild facts for (repeatable)")
	factsRebuildFromURLsCmd.Flags().Bool("reset", false, "Ignore checkpoint file and start from scratch")
	factsRebuildFromURLsCmd.Flags().Int("max-urls-per-person", 0, "Optional cap on URLs processed per person (0 = no cap)")
	factsRebuildFromURLsCmd.Flags().String("urls-from-log", "", "Recovery mode: read (person, url) pairs from this rebuild-log JSONL instead of querying the DB. Each line must have 'person' and 'url' fields. Use to re-process URLs whose fact_sources were already deleted by a previous run.")
}

func runRebuildFromURLs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env not found or error loading")
	}

	personNames, _ := cmd.Flags().GetStringSlice("person")
	reset, _ := cmd.Flags().GetBool("reset")
	maxURLsPerPerson, _ := cmd.Flags().GetInt("max-urls-per-person")
	urlsFromLog, _ := cmd.Flags().GetString("urls-from-log")
	if len(personNames) == 0 && urlsFromLog == "" {
		return fmt.Errorf("either --person or --urls-from-log is required")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("storage init: %w", err)
	}
	defer storer.Close()

	factExtractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return fmt.Errorf("fact extractor init: %w", err)
	}
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("embedder init: %w", err)
	}
	rawExtractor, err := extractor.NewExtractorFromType("webcrawler")
	if err != nil {
		return fmt.Errorf("content extractor init: %w", err)
	}
	batchExtractor := core.NewBatchExtractorFromEnv(rawExtractor)

	stanceAdapter := storage.NewStanceStorerAdapter(storer)
	stanceMgr, err := core.NewStanceManagerFromEnv(stanceAdapter)
	if err != nil {
		return fmt.Errorf("stance manager init: %w", err)
	}

	provider := &news.Provider{
		ContentExtractor: batchExtractor,
		FactExtractor:    factExtractor,
		Embedder:         embedder,
		Storer:           storer,
		StanceManager:    stanceMgr,
	}

	opts := news.Options{
		RevisitHours: 24 * 365 * 10,
	}

	cp := rebuildCheckpoint{DonePersonURL: map[string]bool{}}
	if !reset {
		if loaded, err := loadRebuildCheckpoint(); err == nil {
			cp = loaded
			log.Info().Int("already_done", len(cp.DonePersonURL)).Msg("rebuild: resuming from checkpoint")
		}
	}

	logFile, err := os.OpenFile(rebuildLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open rebuild log: %w", err)
	}
	defer logFile.Close()
	logEnc := json.NewEncoder(logFile)

	touchedFacts := map[int64]struct{}{}
	totalProcessed := 0
	totalExtracted := 0

	// Build the work list: either from --urls-from-log (recovery mode) or from the DB per-person.
	var work []rebuildWorkItem
	if urlsFromLog != "" {
		var err error
		work, err = readWorkFromLog(urlsFromLog)
		if err != nil {
			return fmt.Errorf("read urls-from-log: %w", err)
		}
		log.Info().Int("pairs", len(work)).Str("file", urlsFromLog).Msg("rebuild: loaded (person, url) pairs from log")
	} else {
		for _, name := range personNames {
			personID, _, err := resolvePersonTarget(ctx, storer, name)
			if err != nil {
				log.Error().Err(err).Str("person", name).Msg("could not resolve person, skipping")
				continue
			}
			urls, err := storer.NewsURLsForPerson(ctx, personID)
			if err != nil {
				log.Error().Err(err).Str("person", name).Msg("NewsURLsForPerson failed, skipping")
				continue
			}
			if maxURLsPerPerson > 0 && len(urls) > maxURLsPerPerson {
				urls = urls[:maxURLsPerPerson]
			}
			for _, u := range urls {
				work = append(work, rebuildWorkItem{Person: name, URL: u})
			}
		}
	}

	// Group work by person for nicer logging (and to resolve person_id once per person).
	personIDCache := map[string]int64{}
	for _, item := range work {
		if _, ok := personIDCache[item.Person]; ok {
			continue
		}
		id, _, err := resolvePersonTarget(ctx, storer, item.Person)
		if err != nil {
			log.Error().Err(err).Str("person", item.Person).Msg("could not resolve person, skipping its URLs")
			personIDCache[item.Person] = -1
			continue
		}
		personIDCache[item.Person] = id
	}

	log.Info().Int("total_pairs", len(work)).Msg("rebuild: starting")

	for _, item := range work {
		name := item.Person
		url := item.URL
		personID := personIDCache[name]
		if personID < 0 {
			continue
		}
		target := news.SearchTarget{
			Name:     name,
			Type:     news.TargetPerson,
			PersonID: &personID,
		}

		key := name + "|" + url
		if cp.DonePersonURL[key] {
			continue
		}

		// Delete existing news fact_sources for this URL (cascades fact_mentions).
		deletedIDs, derr := storer.DeleteFactSourcesByURL(ctx, url, "news")
		if derr != nil {
			log.Warn().Err(derr).Str("url", url).Msg("delete existing fact_sources failed; skipping URL")
			_ = logEnc.Encode(rebuildLogEntry{
				Timestamp: time.Now().UTC().Format(time.RFC3339),
				Person:    name, URL: url,
				Errors: []string{"delete: " + derr.Error()},
			})
			continue
		}
		for _, fid := range deletedIDs {
			touchedFacts[fid] = struct{}{}
		}

		// Re-scrape + LLM-extract + dedup-insert via the normal pipeline path.
		result, rerr := provider.RebuildFromURLs(ctx, target, []string{url}, opts)
		entry := rebuildLogEntry{
			Timestamp:        time.Now().UTC().Format(time.RFC3339),
			Person:           name,
			URL:              url,
			DeletedSourceIDs: deletedIDs,
		}
		if result != nil {
			entry.FactsExtracted = result.FactsExtracted
			entry.FactsSkippedDedup = result.FactsSkippedDedup
			entry.SkippedThin = result.SkippedThinContent
			entry.ErrorCount = len(result.Errors)
			if len(result.Errors) > 0 {
				for _, e := range result.Errors {
					entry.Errors = append(entry.Errors, e.Error())
				}
			}
			totalExtracted += result.FactsExtracted
		}
		if rerr != nil {
			entry.Errors = append(entry.Errors, "rebuild: "+rerr.Error())
		}
		_ = logEnc.Encode(entry)

		cp.DonePersonURL[key] = true
		if cperr := saveRebuildCheckpoint(cp); cperr != nil {
			log.Warn().Err(cperr).Msg("checkpoint save failed")
		}
		totalProcessed++

		log.Info().
			Str("person", name).
			Str("url", url).
			Int("extracted", entry.FactsExtracted).
			Int("dedup", entry.FactsSkippedDedup).
			Int("skipped_thin", entry.SkippedThin).
			Int("errors", entry.ErrorCount).
			Int64("touched_facts_total", int64(len(touchedFacts))).
			Msg("rebuild: URL done")
	}

	// Sweep orphan facts (those whose ONLY sources were deleted and no new sources were created).
	cascadeDeleted := 0
	if len(touchedFacts) > 0 {
		var ids []int64
		for id := range touchedFacts {
			ids = append(ids, id)
		}
		orphans, oerr := storer.FindOrphanFacts(ctx, ids)
		if oerr != nil {
			log.Error().Err(oerr).Msg("FindOrphanFacts failed")
		} else if len(orphans) > 0 {
			res, derr := storer.DeleteFactsAndCleanup(ctx, orphans)
			if derr != nil {
				log.Error().Err(derr).Msg("DeleteFactsAndCleanup failed")
			} else {
				cascadeDeleted = res.FactsDeleted
				_ = logEnc.Encode(rebuildLogEntry{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Errors:    []string{fmt.Sprintf("orphan-sweep: deleted %d facts, %d stances cleaned", res.FactsDeleted, res.StancesDeleted)},
				})
			}
		}
	}

	fmt.Printf("\n=== rebuild-from-urls complete ===\n")
	fmt.Printf("  persons:           %d\n", len(personNames))
	fmt.Printf("  urls processed:    %d\n", totalProcessed)
	fmt.Printf("  facts extracted:   %d\n", totalExtracted)
	fmt.Printf("  orphan facts swept: %d\n", cascadeDeleted)
	fmt.Printf("  log file:          %s\n", rebuildLogFile)
	fmt.Printf("  checkpoint:        %s\n", rebuildCheckpointFile)
	return nil
}

// rebuildWorkItem is one (person, url) pair to process. Used by both the
// from-DB and from-log code paths.
type rebuildWorkItem struct {
	Person string
	URL    string
}

// readWorkFromLog parses a previous rebuild-log JSONL file and returns the unique
// (person, url) pairs found in it, in the order they appear. Used by the
// --urls-from-log recovery flag.
func readWorkFromLog(path string) ([]rebuildWorkItem, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []rebuildWorkItem
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var e struct {
			Person string `json:"person"`
			URL    string `json:"url"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		if e.Person == "" || e.URL == "" {
			continue
		}
		key := e.Person + "|" + e.URL
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, rebuildWorkItem{Person: e.Person, URL: e.URL})
	}
	return out, scanner.Err()
}

// resolvePersonTarget looks up a person by name and returns their ID plus an
// empty placeholder for party affiliations (which RebuildFromURLs loads itself
// via Provider.getAffiliatedParties).
func resolvePersonTarget(ctx context.Context, storer *storage.PostgresStorer, name string) (int64, []string, error) {
	normalized := util.NormalizeString(name)
	id, err := storer.GetPersonIDByName(ctx, normalized)
	if err != nil {
		return 0, nil, fmt.Errorf("person %q not found: %w", name, err)
	}
	return id, nil, nil
}

func loadRebuildCheckpoint() (rebuildCheckpoint, error) {
	var cp rebuildCheckpoint
	b, err := os.ReadFile(rebuildCheckpointFile)
	if err != nil {
		return cp, err
	}
	if err := json.Unmarshal(b, &cp); err != nil {
		return cp, err
	}
	if cp.DonePersonURL == nil {
		cp.DonePersonURL = map[string]bool{}
	}
	return cp, nil
}

func saveRebuildCheckpoint(cp rebuildCheckpoint) error {
	b, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return os.WriteFile(rebuildCheckpointFile, b, 0644)
}

// Ensure imports used.
var _ = strings.TrimSpace
