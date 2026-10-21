package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/util"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	factsFoldPerson     string
	factsFoldApply      bool
	factsFoldFloor      float64
	factsFoldMaxCluster int
	factsFoldUntil      string
	factsFoldStartAfter int64
	factsFoldAskAll     bool
)

var factsFoldCmd = &cobra.Command{
	Use:   "fold",
	Short: "Group the facts that report one occurrence under their most concrete wording.",
	Long: `Backfill pass over existing facts: clusters the facts of each person that
report one occurrence (candidates by embedding or text similarity, then the
same evidence rule and LLM gate as insertion), makes each cluster one claim
group whose parent is its most concrete wording (the others are kept as
members), moves their contradictions onto the parent (collapsing duplicate
rows), and recomputes scores. Insertion-time grouping handles new facts.

A pair of two facts inserted under the current grouping rule (since
2026-10-12) keeps its insert-time verdict; --ask-all asks the gate about it too.

READ-ONLY by default: prints the clusters it would fold. Pass --apply to write.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsFold,
}

func init() {
	factsCmd.AddCommand(factsFoldCmd)
	factsFoldCmd.Flags().StringVar(&factsFoldPerson, "person", "", "Restrict to one person (default: all persons)")
	factsFoldCmd.Flags().BoolVar(&factsFoldApply, "apply", false, "Apply the folds (default is a read-only report)")
	factsFoldCmd.Flags().Float64Var(&factsFoldFloor, "floor", core.DefaultFoldOptions.Floor, "Cosine-similarity floor for grouping candidates")
	factsFoldCmd.Flags().IntVar(&factsFoldMaxCluster, "max-cluster", core.DefaultFoldOptions.MaxCluster, "Skip clusters larger than this (defense against runaway chaining)")
	factsFoldCmd.Flags().StringVar(&factsFoldUntil, "until", "", "Stop starting persons after this RFC 3339 time (a long backfill must not overlap the nightly)")
	factsFoldCmd.Flags().BoolVar(&factsFoldAskAll, "ask-all", false, "Also ask the gate about pairs of facts inserted under the current grouping rule (by default their insert-time verdict stands)")
	factsFoldCmd.Flags().Int64Var(&factsFoldStartAfter, "start-after", 0, "Skip persons up to and including this person id (resume a cut run)")
}

func runFactsFold(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	judge, err := core.NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		return fmt.Errorf("occurrence gate required for folding: %w", err)
	}

	persons, err := personsForCommand(ctx, storer, factsFoldPerson, storer.GetAllPersons)
	if err != nil {
		return err
	}

	var deadline time.Time
	if factsFoldUntil != "" {
		if deadline, err = time.Parse(time.RFC3339, factsFoldUntil); err != nil {
			return fmt.Errorf("invalid --until (expected RFC 3339, e.g. 2026-09-22T00:45:00Z): %w", err)
		}
	}
	if factsFoldStartAfter != 0 {
		for i, p := range persons {
			if p.ID == factsFoldStartAfter {
				persons = persons[i+1:]
				break
			}
		}
	}

	opts := core.FoldOptions{Floor: factsFoldFloor, TextFloor: core.DefaultFoldOptions.TextFloor, MaxCluster: factsFoldMaxCluster, JudgedSince: core.DefaultFoldOptions.JudgedSince}
	if factsFoldAskAll {
		opts.JudgedSince = time.Time{}
	}
	totalClusters, totalFolded := 0, 0
	var lastDone int64
	for i, p := range persons {
		if !deadline.IsZero() && time.Now().After(deadline) {
			log.Info().Int("done", i).Int("left", len(persons)-i).Int64("last_person_id", lastDone).
				Msg("Fold: deadline reached; the rest stays pending (resume with --start-after)")
			break
		}
		lastDone = p.ID
		clusters, err := core.FoldClustersForPerson(ctx, storer, judge, p, opts)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Fold clustering failed; skipping person")
			continue
		}
		for _, cluster := range clusters {
			totalClusters++
			fmt.Fprintf(os.Stderr, "\n%s — cluster of %d:\n", p.Name, len(cluster.Facts))
			for _, f := range cluster.Facts {
				fmt.Fprintf(os.Stderr, "  [%d] %s\n", f.ID, util.TruncateString(f.Text, 130))
			}
			if !factsFoldApply {
				continue
			}
			if _, err := core.ApplyFoldCluster(ctx, storer, p, cluster); err != nil {
				log.Error().Err(err).Str("person", p.Name).Msg("Failed to apply fold cluster")
				continue
			}
			totalFolded++
		}
	}

	if factsFoldApply && totalFolded > 0 {
		// Folding changes active-fact counts and collapses contradictions; settle
		// every affected score in one deterministic pass.
		if _, err := storer.RecomputeAllHonestyScores(ctx, false); err != nil {
			log.Error().Err(err).Msg("Failed to recompute honesty scores after folding")
		}
	}
	if factsFoldApply {
		fmt.Fprintf(os.Stderr, "\nGrouped %d/%d clusters.\n", totalFolded, totalClusters)
	} else {
		fmt.Fprintf(os.Stderr, "\n[DRY RUN] %d fold clusters found. Re-run with --apply to write.\n", totalClusters)
	}
	return nil
}
