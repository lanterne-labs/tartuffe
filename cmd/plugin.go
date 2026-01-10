package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/plugins/frpolitics"
	"digital-mirror/backend/storage"

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
	frpoliticsCmd.Flags().String("since", "", "process laws voted on or after YYYY-MM-DD")
	frpoliticsCmd.Flags().Int("max-laws", 10, "maximum laws to process")
	frpoliticsCmd.Flags().Float64("party-threshold", 0.8, "majority threshold for party-level facts (0..1)")
	frpoliticsCmd.Flags().String("legislatures", "16", "comma-separated legislature numbers to fetch")
}

func runFrpoliticsPlugin(cmd *cobra.Command, args []string) error {
	// Load environment variables from .env file
	err := godotenv.Load()
	if err != nil {
		log.Warn().Msg("No .env file found, relying on environment variables")
	}

	ctx := context.Background()

	// Initialize database connection
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Parse flags
	sinceStr, _ := cmd.Flags().GetString("since")
	maxLaws, _ := cmd.Flags().GetInt("max-laws")
	threshold, _ := cmd.Flags().GetFloat64("party-threshold")
	legsStr, _ := cmd.Flags().GetString("legislatures")

	// Parse since date
	var since *time.Time
	if sinceStr != "" {
		if t, err := time.Parse("2006-01-02", sinceStr); err == nil {
			since = &t
		} else {
			log.Warn().Str("since", sinceStr).Msg("invalid --since format, expected YYYY-MM-DD")
		}
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

	// Set up options
	opts := frpolitics.Options{
		Since:          since,
		MaxLaws:        maxLaws,
		PartyThreshold: threshold,
		Legislatures:   legs,
		RevisitHours:   0, // Always process new data
	}

	log.Info().
		Int("max_laws", maxLaws).
		Float64("party_threshold", threshold).
		Ints("legislatures", legs).
		Msg("Starting frpolitics plugin")

	// Run the plugin
	if err := frpolitics.Run(ctx, storer, opts); err != nil {
		return fmt.Errorf("frpolitics plugin failed: %w", err)
	}

	log.Info().Msg("Frpolitics plugin completed successfully")
	return nil
}
