package cmd

import (
	"context"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/storage"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"golang.org/x/text/unicode/norm"
)

const (
	auditCheckpointFile = "facts_audit_progress.json"
	auditLogFile        = "facts_audit_log.jsonl"
)

type auditCheckpoint struct {
	LastID int64 `json:"last_id"`
}

// auditDecision is one row in the JSONL audit log.
type auditDecision struct {
	Timestamp    string `json:"ts"`
	FactID       int64  `json:"fact_id"`
	FactSourceID int64  `json:"fact_source_id"`
	SourceURL    string `json:"source_url"`
	QuotePrefix  string `json:"quote_prefix"`
	Decision     string `json:"decision"` // KEEP, DELETE_SOURCE, DELETE_FACT_CASCADE, SKIP_SCRAPE_FAILED
	Reason       string `json:"reason,omitempty"`
}

var factsAuditAttributionCmd = &cobra.Command{
	Use:   "audit-attribution",
	Short: "One-shot: re-scrape every fact_source and delete sources whose original_quote is absent from current page content.",
	Long: `Iterates fact_sources in id order, re-scrapes each source_url via the cleaning
pipeline (main_content_only + local sidebar strip), and deletes the row if its
stored original_quote can no longer be found in the cleaned content. Facts that
end up with zero remaining sources are deleted via DeleteFactsAndCleanup so
honesty scores and stance documents stay consistent.

No dry-run mode — every action is appended to facts_audit_log.jsonl with full
context. The run is resumable: facts_audit_progress.json tracks the last
processed fact_source.id. Delete that file to restart from scratch.

Run carefully — this mutates production data.`,
	RunE: runAuditAttribution,
}

func init() {
	factsCmd.AddCommand(factsAuditAttributionCmd)
	factsAuditAttributionCmd.Flags().Int("batch-size", 50, "Number of fact_sources to fetch per page")
	factsAuditAttributionCmd.Flags().Int("delay-ms", 200, "Sleep between webcrawlerapi requests (ms)")
	factsAuditAttributionCmd.Flags().Bool("reset", false, "Ignore checkpoint file and start from id=0")
}

func runAuditAttribution(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env not found or error loading")
	}

	batchSize, _ := cmd.Flags().GetInt("batch-size")
	delayMs, _ := cmd.Flags().GetInt("delay-ms")
	reset, _ := cmd.Flags().GetBool("reset")

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("storage init: %w", err)
	}
	defer storer.Close()

	ext, err := extractor.NewWebcrawlerAPIExtractorFromEnv()
	if err != nil {
		return fmt.Errorf("webcrawlerapi init: %w", err)
	}
	if ext == nil {
		return fmt.Errorf("WEBCRAWLERAPI_KEY not set — required for audit-attribution")
	}

	cp := auditCheckpoint{}
	if !reset {
		if loaded, err := loadCheckpoint(); err == nil {
			cp = loaded
			log.Info().Int64("last_id", cp.LastID).Msg("resuming from checkpoint")
		}
	}

	logFile, err := os.OpenFile(auditLogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer logFile.Close()
	logEnc := json.NewEncoder(logFile)

	touchedFacts := map[int64]struct{}{}
	var stats struct {
		Processed, Kept, DeletedSource, ScrapeFailed int
	}

	for {
		rows, err := storer.IterateFactSources(ctx, cp.LastID, batchSize)
		if err != nil {
			return fmt.Errorf("iterate: %w", err)
		}
		if len(rows) == 0 {
			break
		}

		for _, r := range rows {
			decision := processOne(ctx, ext, storer, r)
			decision.Timestamp = time.Now().UTC().Format(time.RFC3339)
			if err := logEnc.Encode(decision); err != nil {
				log.Warn().Err(err).Msg("audit log write failed")
			}

			switch decision.Decision {
			case "KEEP":
				stats.Kept++
			case "DELETE_SOURCE":
				stats.DeletedSource++
				touchedFacts[r.FactID] = struct{}{}
			case "SKIP_SCRAPE_FAILED":
				stats.ScrapeFailed++
			}
			stats.Processed++

			cp.LastID = r.ID
			if err := saveCheckpoint(cp); err != nil {
				log.Warn().Err(err).Msg("checkpoint write failed")
			}

			if delayMs > 0 {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
			}
		}

		log.Info().
			Int("processed", stats.Processed).
			Int("kept", stats.Kept).
			Int("deleted_source", stats.DeletedSource).
			Int("scrape_failed", stats.ScrapeFailed).
			Int64("checkpoint_id", cp.LastID).
			Msg("audit-attribution: batch done")
	}

	// Sweep orphan facts (zero remaining sources after deletes).
	orphans := []int64{}
	for fid := range touchedFacts {
		orphans = append(orphans, fid)
	}
	cascadeDeleted := 0
	if len(orphans) > 0 {
		toDelete, err := storer.FindOrphanFacts(ctx, orphans)
		if err != nil {
			log.Error().Err(err).Msg("find orphan facts failed")
		} else if len(toDelete) > 0 {
			res, err := storer.DeleteFactsAndCleanup(ctx, toDelete)
			if err != nil {
				log.Error().Err(err).Msg("DeleteFactsAndCleanup failed")
			} else {
				cascadeDeleted = res.FactsDeleted
				for _, fid := range toDelete {
					_ = logEnc.Encode(auditDecision{
						Timestamp: time.Now().UTC().Format(time.RFC3339),
						FactID:    fid,
						Decision:  "DELETE_FACT_CASCADE",
						Reason:    "orphan after source deletion",
					})
				}
			}
		}
	}

	fmt.Printf("\n=== audit-attribution complete ===\n")
	fmt.Printf("  processed:        %d\n", stats.Processed)
	fmt.Printf("  kept:             %d\n", stats.Kept)
	fmt.Printf("  deleted (source): %d\n", stats.DeletedSource)
	fmt.Printf("  deleted (fact):   %d  (orphan facts cleaned up)\n", cascadeDeleted)
	fmt.Printf("  scrape failed:    %d\n", stats.ScrapeFailed)
	fmt.Printf("  audit log:        %s\n", auditLogFile)
	fmt.Printf("  checkpoint:       %s (last id=%d)\n", auditCheckpointFile, cp.LastID)
	return nil
}

func processOne(ctx context.Context, ext *extractor.WebcrawlerAPIExtractor, storer *storage.PostgresStorer, r storage.FactSourceAuditRow) auditDecision {
	d := auditDecision{
		FactID:       r.FactID,
		FactSourceID: r.ID,
		SourceURL:    r.SourceURL,
		QuotePrefix:  truncateForAudit(r.OriginalQuote, 80),
	}

	// Skip if the stored quote is empty — we can't verify what isn't there.
	// Rare; treat as KEEP rather than risk losing legitimate facts.
	if strings.TrimSpace(r.OriginalQuote) == "" {
		d.Decision = "KEEP"
		d.Reason = "no original_quote stored"
		return d
	}

	content, err := ext.ExtractContent(r.SourceURL)
	if err != nil {
		d.Decision = "SKIP_SCRAPE_FAILED"
		d.Reason = err.Error()
		return d
	}

	if quoteInContent(r.OriginalQuote, content.Content) {
		d.Decision = "KEEP"
		return d
	}

	if err := storer.DeleteFactSourceByID(ctx, r.ID); err != nil {
		d.Decision = "SKIP_SCRAPE_FAILED"
		d.Reason = "delete failed: " + err.Error()
		return d
	}
	d.Decision = "DELETE_SOURCE"
	d.Reason = "quote not in cleaned content"
	return d
}

// quoteInContent does a normalized substring check. Normalization folds accents,
// lowercases, unifies smart quotes, and collapses whitespace — making the
// comparison robust to scraper-side punctuation differences.
func quoteInContent(quote, content string) bool {
	nq := normalizeForMatch(quote)
	nc := normalizeForMatch(content)
	if nq == "" || nc == "" {
		return false
	}
	// Strip leading/trailing ellipses so partial-quote citations still match.
	nq = strings.Trim(nq, ".")
	if len(nq) < 10 {
		return false
	}
	return strings.Contains(nc, nq)
}

// normalizeForMatch applies the transformations needed to compare a stored
// `original_quote` against re-scraped content tolerantly: NFD-fold and drop
// combining marks (so "déclare" matches "declare" in a stripped corpus),
// lowercase, unify smart quotes / guillemets / ellipsis, collapse whitespace.
func normalizeForMatch(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ToLower(s)
	// Unify quotes.
	r := strings.NewReplacer(
		"‘", "'", "’", "'", "‚", "'", "‛", "'",
		"“", `"`, "”", `"`, "„", `"`, "‟", `"`,
		"«", `"`, "»", `"`,
		"…", "...",
	)
	s = r.Replace(s)
	// NFD then strip Mn (combining marks).
	decomposed := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, ru := range decomposed {
		if unicode.Is(unicode.Mn, ru) {
			continue
		}
		b.WriteRune(ru)
	}
	s = b.String()
	// Collapse whitespace.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func truncateForAudit(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func loadCheckpoint() (auditCheckpoint, error) {
	var cp auditCheckpoint
	b, err := os.ReadFile(auditCheckpointFile)
	if err != nil {
		return cp, err
	}
	return cp, json.Unmarshal(b, &cp)
}

func saveCheckpoint(cp auditCheckpoint) error {
	b, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return os.WriteFile(auditCheckpointFile, b, 0644)
}
