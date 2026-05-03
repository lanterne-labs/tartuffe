package cmd

import (
	"context"
	"digital-mirror/backend/storage"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var dryRunScores bool

var scoresCmd = &cobra.Command{
	Use:   "scores",
	Short: "Commands related to honesty scores.",
}

var scoresRecomputeCmd = &cobra.Command{
	Use:   "recompute",
	Short: "Recompute honesty scores for all persons using the exponential formula.",
	Long:  `Recalculates honesty scores for every person using exp(-4 * contradictions/facts). Use --dry-run to preview changes without writing.`,
	Annotations: map[string]string{
		"needs_db": "true",
	},
	RunE: runScoresRecompute,
}

func init() {
	rootCmd.AddCommand(scoresCmd)
	scoresCmd.AddCommand(scoresRecomputeCmd)
	scoresRecomputeCmd.Flags().BoolVar(&dryRunScores, "dry-run", false, "Preview score changes without writing to database")
}

func runScoresRecompute(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	updates, err := storer.RecomputeAllHonestyScores(ctx, dryRunScores)
	if err != nil {
		return fmt.Errorf("failed to recompute honesty scores: %w", err)
	}

	if dryRunScores {
		fmt.Println("[DRY RUN] No changes written to database.")
	}

	changed := 0
	for _, u := range updates {
		oldPct := u.OldScore * 100
		newPct := u.NewScore * 100
		delta := newPct - oldPct
		marker := ""
		if delta < -0.01 || delta > 0.01 {
			changed++
			marker = fmt.Sprintf(" (%+.1f%%)", delta)
		}

		if u.MembersScore != nil {
			fmt.Printf("%-30s  facts=%-4d bundles=%-3d (raw=%-3d)  own=%.1f%% members=%.1f%% agg=%.1f%% → %.1f%%%s\n",
				u.PersonName, u.Facts, u.Bundles, u.Total, u.OwnScore*100, *u.MembersScore*100, oldPct, newPct, marker)
		} else {
			fmt.Printf("%-30s  facts=%-4d bundles=%-3d (raw=%-3d)  %.1f%% → %.1f%%%s\n",
				u.PersonName, u.Facts, u.Bundles, u.Total, oldPct, newPct, marker)
		}
	}

	fmt.Printf("\n%d person(s) total, %d score(s) changed.\n", len(updates), changed)
	return nil
}
