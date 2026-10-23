package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
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

// foldOperation names the fold in the progress records.
const foldOperation = "fold"

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
	total := len(persons)
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
	// The persons are folded as many at once as the endpoint serves
	// (llm.ForEach): each person's clusters are independent of the others'.
	// The persons start in order, so at the deadline every person before the
	// first one skipped is done.
	var (
		mu                         sync.Mutex
		totalClusters, totalFolded int
		firstSkipped               = -1
		// The persons before --start-after were done by the run it resumes.
		personsDone = total - len(persons)
	)
	// progress records how far the whole fold is (Grafana, Operations); a
	// dry run is not the operation.
	progress := func(ctx context.Context) {
		if !factsFoldApply || factsFoldPerson != "" {
			return
		}
		if err := storer.RecordProgress(ctx, foldOperation, personsDone, total); err != nil {
			log.Warn().Err(err).Msg("Fold: recording the progress failed")
		}
	}
	index := make(map[int64]int, len(persons))
	for i, p := range persons {
		index[p.ID] = i
	}
	err = llm.ForEach(ctx, persons, func(ctx context.Context, p core.PersonRecord) error {
		if !deadline.IsZero() && time.Now().After(deadline) {
			mu.Lock()
			if i := index[p.ID]; firstSkipped < 0 || i < firstSkipped {
				firstSkipped = i
			}
			mu.Unlock()
			return nil
		}
		defer func() {
			mu.Lock()
			personsDone++
			progress(ctx)
			mu.Unlock()
		}()
		clusters, err := core.FoldClustersForPerson(ctx, storer, judge, p, opts)
		if errors.Is(err, llm.ErrUnavailable) {
			return err // every person left would fail the same way
		}
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Fold clustering failed; skipping person")
			return nil
		}
		for _, cluster := range clusters {
			mu.Lock()
			totalClusters++
			fmt.Fprintf(os.Stderr, "\n%s — cluster of %d:\n", p.Name, len(cluster.Facts))
			for _, f := range cluster.Facts {
				fmt.Fprintf(os.Stderr, "  [%d] %s\n", f.ID, util.TruncateString(f.Text, 130))
			}
			mu.Unlock()
			if !factsFoldApply {
				continue
			}
			if _, err := core.ApplyFoldCluster(ctx, storer, p, cluster); err != nil {
				log.Error().Err(err).Str("person", p.Name).Msg("Failed to apply fold cluster")
				continue
			}
			mu.Lock()
			totalFolded++
			mu.Unlock()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if firstSkipped >= 0 {
		var lastDone int64
		if firstSkipped > 0 {
			lastDone = persons[firstSkipped-1].ID
		}
		log.Info().Int("done", firstSkipped).Int("left", len(persons)-firstSkipped).Int64("last_person_id", lastDone).
			Msg("Fold: deadline reached; the rest stays pending (resume with --start-after)")
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
