package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/plugins/frpolitics"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// pluginCmd represents the plugin command
var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Commands for running plugins",
	Long:  `Plugin commands for running various data processing plugins.`,
}

// frpoliticsCmd represents the frpolitics plugin command
var frpoliticsCmd = &cobra.Command{
	Use:   "frpolitics",
	Short: "Run the French politics plugin",
	Long:  `Runs the French politics plugin to process Assemblée Nationale data.`,
	RunE:  runFrpoliticsPlugin,
}

func init() {
	rootCmd.AddCommand(pluginCmd)
	pluginCmd.AddCommand(frpoliticsCmd)

	// Flags for frpolitics
	frpoliticsCmd.Flags().String("mode", "votes", "operation mode: 'votes' (process official votes), 'debats' (séance transcripts), or 'backfill-seance-ref' (stamp seance_ref on old vote sources + queue same-séance pairs)")
	frpoliticsCmd.Flags().Int("max-docs", 0, "debats mode: max (speaker, séance) documents to extract this run (0 = unlimited)")
	frpoliticsCmd.Flags().Bool("newest-first", false, "debats mode: process the most recent séances first")
	frpoliticsCmd.Flags().Bool("prioritize-press", false, "debats mode: speakers with news/twitter facts first (backfill order)")
	frpoliticsCmd.Flags().String("since", "", "process laws voted on or after YYYY-MM-DD (auto-derives legislatures)")
	frpoliticsCmd.Flags().Int("last-years", 0, "process votes from the last N years, e.g. --last-years 2 (auto-derives since date and legislatures)")
	frpoliticsCmd.Flags().Int("from-year", 0, "process votes starting from Jan 1 of this year, e.g. --from-year 2022 (auto-derives legislatures)")
	frpoliticsCmd.Flags().Int("max-laws", 10, "maximum laws to process")
	frpoliticsCmd.Flags().Float64("party-threshold", 0.8, "majority threshold for party-level facts (0..1)")
	frpoliticsCmd.Flags().String("legislatures", "", "comma-separated legislature numbers to fetch (default: auto-detect from date range or current)")
}

func runFrpoliticsPlugin(cmd *cobra.Command, args []string) error {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Warn().Msg("No .env file found, relying on environment variables")
	}

	ctx := context.Background()

	// Initialize database connection
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Parse flags
	mode, _ := cmd.Flags().GetString("mode")
	sinceStr, _ := cmd.Flags().GetString("since")
	lastYears, _ := cmd.Flags().GetInt("last-years")
	fromYear, _ := cmd.Flags().GetInt("from-year")
	maxLaws, _ := cmd.Flags().GetInt("max-laws")
	threshold, _ := cmd.Flags().GetFloat64("party-threshold")
	legsStr, _ := cmd.Flags().GetString("legislatures")

	// Validate mode
	if mode != "votes" && mode != "debats" && mode != "backfill-seance-ref" {
		return fmt.Errorf("invalid mode %q: must be 'votes', 'debats' or 'backfill-seance-ref'", mode)
	}

	// Parse since date (--since takes priority; --last-years and --from-year are convenience shorthands)
	var since *time.Time
	if sinceStr != "" {
		t, err := time.Parse("2006-01-02", sinceStr)
		if err != nil {
			return fmt.Errorf("invalid --since %q: expected YYYY-MM-DD", sinceStr)
		}
		since = &t
	} else if lastYears > 0 {
		t := time.Now().AddDate(-lastYears, 0, 0)
		since = &t
		log.Info().Int("last_years", lastYears).Str("since", t.Format("2006-01-02")).Msg("derived --since from --last-years")
	} else if fromYear > 0 {
		t := time.Date(fromYear, time.January, 1, 0, 0, 0, 0, time.UTC)
		since = &t
		log.Info().Int("from_year", fromYear).Str("since", t.Format("2006-01-02")).Msg("derived --since from --from-year")
	}

	// Parse legislatures
	var legs []int
	if legsStr != "" {
		for _, p := range strings.Split(legsStr, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if n, err := strconv.Atoi(p); err == nil {
				legs = append(legs, n)
			} else {
				log.Warn().Str("leg", p).Msg("invalid legislature")
			}
		}
	}

	// One-off: stamp seance_ref on vote sources ingested before the key
	// existed, then queue the same-séance pairs.
	if mode == "backfill-seance-ref" {
		leg := 17
		if len(legs) == 1 {
			leg = legs[0]
		}
		n, err := frpolitics.BackfillVoteSeanceRefs(ctx, storer, leg)
		if err != nil {
			return err
		}
		q, err := frpolitics.EnqueueSameSeancePairs(ctx, storer)
		if err != nil {
			return err
		}
		fmt.Printf("seance_ref backfilled on %d vote sources; %d same-séance pairs queued\n", n, q)
		return nil
	}

	// Séance transcripts: separate engine, shares the AN actor ids with the
	// votes path. --since bounds the séance dates; --max-docs / --newest-first
	// drive a manual backfill chunk.
	if mode == "debats" {
		maxDocs, _ := cmd.Flags().GetInt("max-docs")
		newestFirst, _ := cmd.Flags().GetBool("newest-first")
		prioritizePress, _ := cmd.Flags().GetBool("prioritize-press")
		dopts := frpolitics.DebatsOptions{MaxDocs: maxDocs, NewestFirst: newestFirst, PrioritizePress: prioritizePress}
		if since != nil {
			dopts.Since = *since
		}
		if len(legs) > 1 {
			return fmt.Errorf("--mode debats processes one legislature per run, got %v", legs)
		}
		if len(legs) == 1 {
			dopts.Legislature = legs[0]
		}
		stats, err := frpolitics.RunDebats(ctx, storer, dopts)
		if err != nil {
			return err
		}
		fmt.Printf("\n=== DEBATS RESULTS ===\nSéances in range: %d\nDocuments: %d (pending before: %d)\nAttempted: %d, processed: %d, facts: %d (skipped %d, dropped non-speaker subject %d), no-fact docs: %d, errors: %d\nPending after: %d, deadline hit: %v, elapsed: %.0fs\n",
			stats.Seances, stats.Documents, stats.Pending, stats.Attempted, stats.Processed, stats.Facts, stats.FactsSkipped, stats.FactsDroppedSubject, stats.NoFacts, stats.Errors, stats.PendingAfter, stats.DeadlineHit, stats.ElapsedSeconds)
		return nil
	}

	// Set up options
	opts := frpolitics.Options{
		Since:          since,
		MaxLaws:        maxLaws,
		PartyThreshold: threshold,
		Legislatures:   legs,
		RevisitHours:   -1, // negative = always visitable: retry laws whose store failed (0 would coerce to the 24h default)
	}

	sinceLog := ""
	if since != nil {
		sinceLog = since.Format("2006-01-02")
	}
	log.Info().
		Str("mode", mode).
		Str("since", sinceLog).
		Int("max_laws", maxLaws).
		Float64("party_threshold", threshold).
		Ints("legislatures", legs).
		Msg("Starting frpolitics plugin")

	// Run the plugin
	if _, err := frpolitics.Run(ctx, storer, opts); err != nil {
		return fmt.Errorf("frpolitics plugin failed: %w", err)
	}

	log.Info().Msg("Frpolitics plugin completed successfully")
	return nil
}
