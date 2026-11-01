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
	frpoliticsCmd.Flags().String("mode", "votes", frpolitics.ModeHelp())
	frpoliticsCmd.Flags().Bool("dry-run", false, "reamend mode: count the ballots to rewrite, write nothing")
	frpoliticsCmd.Flags().Bool("rejudge", false, "rearticle mode: remove the contradictions on the rewritten ballots and the verdicts of their pairs, so they are judged again")
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

	rejudge, _ := cmd.Flags().GetBool("rejudge")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	maxDocs, _ := cmd.Flags().GetInt("max-docs")
	newestFirst, _ := cmd.Flags().GetBool("newest-first")
	prioritizePress, _ := cmd.Flags().GetBool("prioritize-press")
	return frpolitics.RunMode(ctx, storer, frpolitics.CLIOptions{
		Mode:            mode,
		Since:           since,
		Legislatures:    legs,
		MaxLaws:         maxLaws,
		PartyThreshold:  threshold,
		Rejudge:         rejudge,
		DryRun:          dryRun,
		MaxDocs:         maxDocs,
		NewestFirst:     newestFirst,
		PrioritizePress: prioritizePress,
	})
}
