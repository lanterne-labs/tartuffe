package news

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"digital-mirror/backend/plugins"

	"github.com/rs/zerolog/log"
)

const (
	// RebuildCheckpointFile records the (person, url) pairs a rebuild has
	// finished so an interrupted run resumes where it stopped.
	RebuildCheckpointFile = "facts_rebuild_progress.json"
	// RebuildLogFile is the append-only JSONL audit of every rebuilt URL.
	RebuildLogFile = "facts_rebuild_log.jsonl"
)

type rebuildCheckpoint struct {
	DonePersonURL map[string]bool `json:"done_person_url"`
}

type rebuildLogEntry struct {
	Timestamp         string   `json:"ts"`
	Person            string   `json:"person"`
	URL               string   `json:"url"`
	DeletedSourceIDs  []int64  `json:"deleted_source_ids,omitempty"`
	FactsExtracted    int      `json:"facts_extracted"`
	FactsSkippedDedup int      `json:"facts_skipped_dedup"`
	SkippedThin       int      `json:"skipped_thin"`
	ErrorCount        int      `json:"error_count"`
	Errors            []string `json:"errors,omitempty"`
}

// RebuildItem is one (target, url) pair to re-extract.
type RebuildItem struct {
	Target plugins.Target
	URL    string
}

// RebuildLogPair is a (person name, url) pair read back from a rebuild log.
type RebuildLogPair struct {
	Person string
	URL    string
}

// RebuildOptions configures RebuildFromURLs.
type RebuildOptions struct {
	// Reset ignores an existing checkpoint and starts from scratch.
	Reset bool
}

// RebuildSummary is what RebuildFromURLs reports.
type RebuildSummary struct {
	// URLsProcessed is the number of (target, url) pairs run this time
	// (checkpointed pairs are not counted).
	URLsProcessed int
	// FactsExtracted is the number of facts inserted.
	FactsExtracted int
	// OrphansSwept is the number of facts deleted because every source they
	// had was deleted and the re-extraction produced no new one.
	OrphansSwept int
}

// NewsURLsForTarget returns the distinct news URLs that previously produced
// facts mentioning the target, capped at max when max > 0.
func (p *Provider) NewsURLsForTarget(ctx context.Context, target plugins.Target, max int) ([]string, error) {
	urls, err := p.store.NewsURLsForPerson(ctx, target.PersonID)
	if err != nil {
		return nil, err
	}
	if max > 0 && len(urls) > max {
		urls = urls[:max]
	}
	return urls, nil
}

// ReadRebuildLog parses a rebuild log (RebuildLogFile format) and returns its
// unique (person, url) pairs in order of first appearance. Lines that are not
// JSON or lack either field (the orphan-sweep line, delete failures) are
// skipped.
func ReadRebuildLog(path string) ([]RebuildLogPair, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	var out []RebuildLogPair
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
		out = append(out, RebuildLogPair{Person: e.Person, URL: e.URL})
	}
	return out, scanner.Err()
}

// RebuildFromURLs re-runs the news extraction pipeline over items. For each
// (target, url) pair not yet in the checkpoint it deletes the URL's news
// fact_sources (cascading mentions), re-scrapes and re-extracts the URL as the
// daily pipeline would (stance routing and contradiction detection inline),
// appends an audit line to RebuildLogFile and checkpoints the pair. Facts
// whose every source was deleted and that gained no new one are swept at the
// end.
func (p *Provider) RebuildFromURLs(ctx context.Context, items []RebuildItem, opts RebuildOptions) (RebuildSummary, error) {
	var summary RebuildSummary

	cp := rebuildCheckpoint{DonePersonURL: map[string]bool{}}
	if !opts.Reset {
		if loaded, err := loadRebuildCheckpoint(); err == nil {
			cp = loaded
			log.Info().Int("already_done", len(cp.DonePersonURL)).Msg("rebuild: resuming from checkpoint")
		}
	}

	logFile, err := os.OpenFile(RebuildLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return summary, fmt.Errorf("open rebuild log: %w", err)
	}
	defer logFile.Close()
	logEnc := json.NewEncoder(logFile)

	touchedFacts := map[int64]struct{}{}

	log.Info().Int("total_pairs", len(items)).Msg("rebuild: starting")

	for _, item := range items {
		name := item.Target.Name
		url := item.URL

		key := name + "|" + url
		if cp.DonePersonURL[key] {
			continue
		}

		// Delete existing news fact_sources for this URL (cascades fact_mentions).
		deletedIDs, derr := p.Storer.DeleteFactSourcesByURL(ctx, url, "news")
		if derr != nil {
			log.Warn().Err(derr).Str("url", url).Msg("delete existing fact_sources failed; skipping URL")
			appendRebuildLog(logEnc, rebuildLogEntry{
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
		result, rerr := p.replayURLs(ctx, item.Target, []string{url}, Options{})
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
			for _, e := range result.Errors {
				entry.Errors = append(entry.Errors, e.Error())
			}
			summary.FactsExtracted += result.FactsExtracted
		}
		if rerr != nil {
			entry.Errors = append(entry.Errors, "rebuild: "+rerr.Error())
		}
		appendRebuildLog(logEnc, entry)

		cp.DonePersonURL[key] = true
		if cperr := saveRebuildCheckpoint(cp); cperr != nil {
			log.Warn().Err(cperr).Msg("checkpoint save failed")
		}
		summary.URLsProcessed++

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
	if len(touchedFacts) > 0 {
		var ids []int64
		for id := range touchedFacts {
			ids = append(ids, id)
		}
		orphans, oerr := p.Storer.FindOrphanFacts(ctx, ids)
		if oerr != nil {
			log.Error().Err(oerr).Msg("FindOrphanFacts failed")
		} else if len(orphans) > 0 {
			res, derr := p.Storer.DeleteFactsAndCleanup(ctx, orphans)
			if derr != nil {
				log.Error().Err(derr).Msg("DeleteFactsAndCleanup failed")
			} else {
				summary.OrphansSwept = res.FactsDeleted
				appendRebuildLog(logEnc, rebuildLogEntry{
					Timestamp: time.Now().UTC().Format(time.RFC3339),
					Errors:    []string{fmt.Sprintf("orphan-sweep: deleted %d facts, %d stances cleaned", res.FactsDeleted, res.StancesDeleted)},
				})
			}
		}
	}

	return summary, nil
}

// appendRebuildLog writes one audit line; a failed write is reported, not
// fatal, since the checkpoint is what resumption relies on.
func appendRebuildLog(enc *json.Encoder, entry rebuildLogEntry) {
	if err := enc.Encode(entry); err != nil {
		log.Warn().Err(err).Str("url", entry.URL).Msg("rebuild: audit log write failed")
	}
}

func loadRebuildCheckpoint() (rebuildCheckpoint, error) {
	var cp rebuildCheckpoint
	b, err := os.ReadFile(RebuildCheckpointFile)
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
	return os.WriteFile(RebuildCheckpointFile, b, 0644)
}
