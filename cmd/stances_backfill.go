package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"fmt"
	"strings"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stancesBackfillPerson string
	stancesBackfillDryRun bool
)

var stancesBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Backfill stance documents from existing facts",
	Long: `Groups existing facts by topic and creates stance documents using LLM-based topic grouping.

For each person (or specified person):
1. Loads all active facts with embeddings
2. Groups facts by exact embedding_text match (initial clusters)
3. Uses LLM to merge related topic labels into broader themes
4. For multi-fact clusters: generates LLM summary
5. For single-fact clusters: uses fact text as summary
6. Inserts stance documents and links facts

Examples:
  # Backfill all persons
  ./dm stances backfill

  # Backfill specific person
  ./dm stances backfill --person "Marine Le Pen"

  # Dry run
  ./dm stances backfill --person "Marine Le Pen" --dry-run`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesBackfill,
}

func init() {
	stancesCmd.AddCommand(stancesBackfillCmd)
	stancesBackfillCmd.Flags().StringVar(&stancesBackfillPerson, "person", "", "Specific person name to backfill (optional)")
	stancesBackfillCmd.Flags().BoolVar(&stancesBackfillDryRun, "dry-run", false, "Show what would be created without writing to database")
}

// backfillFact holds the data needed for clustering.
type backfillFact struct {
	ID            int64
	FactText      string
	EmbeddingText string
	CanonicalVerb string
	Embedding     []float32
}

// factCluster represents a group of similar facts.
type factCluster struct {
	facts    []backfillFact
	centroid []float32
	label    string // embedding_text of first fact
}

func runStancesBackfill(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	adapter := storage.NewStanceStorerAdapter(storer)

	var stanceManager *core.StanceManager
	if !stancesBackfillDryRun {
		stanceManager, err = core.NewStanceManagerFromEnv(adapter)
		if err != nil {
			return fmt.Errorf("failed to create stance manager: %w", err)
		}
	}

	// Determine which persons to process
	persons, err := getPersonsForBackfill(ctx, storer, stancesBackfillPerson)
	if err != nil {
		return err
	}

	log.Info().Int("person_count", len(persons)).Bool("dry_run", stancesBackfillDryRun).Msg("Starting stance backfill")

	totalCreated := 0
	totalSkipped := 0

	for _, p := range persons {
		created, skipped, err := backfillStancesForPerson(ctx, storer, adapter, stanceManager, p.ID, p.Name, stancesBackfillDryRun)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to backfill stances")
			continue
		}
		totalCreated += created
		totalSkipped += skipped
	}

	fmt.Println()
	fmt.Println("=== STANCE BACKFILL RESULTS ===")
	fmt.Printf("Persons processed:     %d\n", len(persons))
	fmt.Printf("Stances created:       %d\n", totalCreated)
	fmt.Printf("Stances skipped:       %d (already exist)\n", totalSkipped)
	if stancesBackfillDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}

	return nil
}

func getPersonsForBackfill(ctx context.Context, storer *storage.PostgresStorer, personName string) ([]storage.PersonInfo, error) {
	if personName != "" {
		result := core.ResolveTargetName(ctx, storer, personName)
		if result.Error != nil {
			return nil, fmt.Errorf("resolving person name: %w", result.Error)
		}
		if result.ExactMatch == nil {
			if len(result.FuzzyMatches) > 0 {
				top := result.FuzzyMatches[0]
				return nil, fmt.Errorf("no exact match for %q (best: %s at %.2f). Use exact name", personName, top.Target.Name, top.Score)
			}
			return nil, fmt.Errorf("person %q not found in database", personName)
		}
		return []storage.PersonInfo{
			{ID: *result.ExactMatch.PersonID, Name: result.ExactMatch.Name, EntityType: result.ExactMatch.EntityType},
		}, nil
	}

	return storer.GetAllPersons(ctx)
}

func backfillStancesForPerson(
	ctx context.Context,
	storer *storage.PostgresStorer,
	adapter *storage.StanceStorerAdapter,
	stanceManager *core.StanceManager,
	personID int64,
	personName string,
	dryRun bool,
) (created int, skipped int, err error) {
	// Load all active facts with embeddings
	facts, err := loadFactsForBackfill(ctx, storer, personID)
	if err != nil {
		return 0, 0, fmt.Errorf("loading facts: %w", err)
	}

	if len(facts) == 0 {
		log.Info().Str("person", personName).Msg("No facts with embeddings found, skipping")
		return 0, 0, nil
	}

	log.Info().Str("person", personName).Int("facts", len(facts)).Msg("Loaded facts for clustering")

	// Step 1: Group facts by exact embedding_text match
	initialClusters := clusterFactsByExactLabel(facts)
	log.Info().Str("person", personName).Int("initial_clusters", len(initialClusters)).Msg("Grouped facts by exact embedding_text")

	// Step 2: Use LLM to group related labels into broader themes
	labels := make([]string, 0, len(initialClusters))
	for label := range initialClusters {
		labels = append(labels, label)
	}

	var mergedClusters []factCluster
	if dryRun || stanceManager == nil {
		// In dry-run mode, use initial clusters as-is
		for label, clusterFacts := range initialClusters {
			mergedClusters = append(mergedClusters, factCluster{
				facts:    clusterFacts,
				centroid: computeCentroid(clusterFacts),
				label:    label,
			})
		}
	} else {
		grouping, err := stanceManager.GroupTopicLabels(ctx, labels)
		if err != nil {
			log.Error().Err(err).Msg("LLM topic grouping failed, falling back to exact-match clusters")
			for label, clusterFacts := range initialClusters {
				mergedClusters = append(mergedClusters, factCluster{
					facts:    clusterFacts,
					centroid: computeCentroid(clusterFacts),
					label:    label,
				})
			}
		} else {
			mergedClusters = applyTopicGrouping(initialClusters, grouping)
			log.Info().Str("person", personName).Int("merged_clusters", len(mergedClusters)).Msg("Merged clusters via LLM grouping")
		}
	}

	// Check existing stances to skip duplicates
	existingStances, err := adapter.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		return 0, 0, fmt.Errorf("getting existing stances: %w", err)
	}
	existingLabels := make(map[string]bool, len(existingStances))
	for _, s := range existingStances {
		existingLabels[s.ClusterLabel] = true
	}

	for _, cluster := range mergedClusters {
		label := cluster.label
		if existingLabels[label] {
			log.Debug().Str("label", label).Str("person", personName).Msg("Stance already exists, skipping")
			skipped++
			continue
		}

		if dryRun {
			fmt.Printf("  [DRY-RUN] Would create stance: %q (%d facts)\n", truncateForDisplay(label, 80), len(cluster.facts))
			created++
			continue
		}

		// Generate summary
		var summary string
		var keyFactIDs []int64

		if len(cluster.facts) == 1 {
			summary = cluster.facts[0].FactText
			keyFactIDs = []int64{cluster.facts[0].ID}
		} else {
			stanceFactInfos := make([]core.StanceFactInfo, len(cluster.facts))
			for i, f := range cluster.facts {
				stanceFactInfos[i] = core.StanceFactInfo{
					ID:                 f.ID,
					NormalizedFactText: f.FactText,
					EmbeddingText:      f.EmbeddingText,
					CanonicalVerb:      f.CanonicalVerb,
				}
			}

			summary, keyFactIDs, err = stanceManager.GenerateSummary(ctx, stanceFactInfos)
			if err != nil {
				log.Error().Err(err).Str("label", label).Msg("Failed to generate summary, using first fact text")
				summary = cluster.facts[0].FactText
				keyFactIDs = []int64{cluster.facts[0].ID}
			}
		}

		clusterLabel := strings.TrimSpace(label)
		if len(clusterLabel) > 120 {
			clusterLabel = clusterLabel[:120]
		}

		doc := core.StanceDocument{
			PersonID:         personID,
			ClusterLabel:     clusterLabel,
			ClusterEmbedding: cluster.centroid,
			Summary:          summary,
			KeyFactIDs:       keyFactIDs,
			FactCount:        len(cluster.facts),
		}

		stanceID, err := adapter.InsertStanceDocument(ctx, doc)
		if err != nil {
			log.Error().Err(err).Str("label", label).Msg("Failed to insert stance document")
			continue
		}

		for _, f := range cluster.facts {
			if err := adapter.AddFactToStance(ctx, stanceID, f.ID); err != nil {
				log.Error().Err(err).Int64("stance_id", stanceID).Int64("fact_id", f.ID).Msg("Failed to link fact to stance")
			}
		}

		log.Info().
			Int64("stance_id", stanceID).
			Str("label", truncateForDisplay(clusterLabel, 60)).
			Int("fact_count", len(cluster.facts)).
			Str("person", personName).
			Msg("Created stance document")

		created++
	}

	return created, skipped, nil
}

func loadFactsForBackfill(ctx context.Context, storer *storage.PostgresStorer, personID int64) ([]backfillFact, error) {
	rows, err := storer.Pool().Query(ctx, `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''), COALESCE(f.canonical_verb, ''), f.embedding
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		AND NOT EXISTS (
			SELECT 1 FROM fact_relations fr
			WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		)
		GROUP BY f.id
		ORDER BY f.id`, personID)
	if err != nil {
		return nil, fmt.Errorf("querying facts: %w", err)
	}
	defer rows.Close()

	var facts []backfillFact
	for rows.Next() {
		var f backfillFact
		var vec pgvector.Vector
		if err := rows.Scan(&f.ID, &f.FactText, &f.EmbeddingText, &f.CanonicalVerb, &vec); err != nil {
			return nil, fmt.Errorf("scanning fact: %w", err)
		}
		f.Embedding = vec.Slice()
		if len(f.Embedding) == 0 {
			continue // skip facts without embeddings
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating facts: %w", err)
	}

	return facts, nil
}

// clusterFactsByExactLabel groups facts by exact embedding_text match.
func clusterFactsByExactLabel(facts []backfillFact) map[string][]backfillFact {
	clusters := make(map[string][]backfillFact)
	for _, fact := range facts {
		label := fact.EmbeddingText
		if label == "" {
			label = fmt.Sprintf("unnamed-fact-%d", fact.ID)
		}
		clusters[label] = append(clusters[label], fact)
	}
	return clusters
}

// applyTopicGrouping merges initial clusters based on LLM grouping results.
// Any labels not assigned by the LLM are added to the most closely named group or kept standalone.
func applyTopicGrouping(initialClusters map[string][]backfillFact, grouping *core.TopicGroupingResponse) []factCluster {
	// Build reverse map: original label → group name
	labelToGroup := make(map[string]string)
	for _, g := range grouping.Groups {
		for _, label := range g.Labels {
			labelToGroup[label] = g.GroupName
		}
	}

	// Collect group names for matching unassigned labels
	groupNames := make([]string, 0, len(grouping.Groups))
	for _, g := range grouping.Groups {
		groupNames = append(groupNames, g.GroupName)
	}

	// Merge facts into groups
	groupFacts := make(map[string][]backfillFact)
	for label, facts := range initialClusters {
		groupName, ok := labelToGroup[label]
		if !ok {
			// Try to match unassigned label to an existing group by substring overlap
			groupName = findBestGroupMatch(label, groupNames)
			if groupName != "" {
				log.Info().Str("label", label).Str("matched_group", groupName).Msg("Matched unassigned label to group by keyword overlap")
			} else {
				groupName = label
				log.Warn().Str("label", label).Msg("Label not assigned to any group by LLM, keeping standalone")
			}
		}
		groupFacts[groupName] = append(groupFacts[groupName], facts...)
	}

	// Build final clusters
	clusters := make([]factCluster, 0, len(groupFacts))
	for groupName, facts := range groupFacts {
		clusters = append(clusters, factCluster{
			facts:    facts,
			centroid: computeCentroid(facts),
			label:    groupName,
		})
	}

	return clusters
}

// findBestGroupMatch finds the best group name for an unassigned label by checking
// if the label shares significant keywords with any group name.
func findBestGroupMatch(label string, groupNames []string) string {
	labelLower := strings.ToLower(label)
	labelWords := strings.FieldsFunc(labelLower, func(r rune) bool {
		return r == '-' || r == '_' || r == ' '
	})

	bestGroup := ""
	bestOverlap := 0

	for _, group := range groupNames {
		groupLower := strings.ToLower(group)
		overlap := 0
		for _, word := range labelWords {
			if len(word) >= 4 && strings.Contains(groupLower, word) {
				overlap++
			}
		}
		if overlap > bestOverlap {
			bestOverlap = overlap
			bestGroup = group
		}
	}

	// Require at least 2 keyword matches to avoid false positives
	if bestOverlap >= 2 {
		return bestGroup
	}
	return ""
}

// computeCentroid calculates the average embedding of a set of facts.
func computeCentroid(facts []backfillFact) []float32 {
	if len(facts) == 0 {
		return nil
	}
	dim := len(facts[0].Embedding)
	centroid := make([]float32, dim)
	for _, f := range facts {
		for i, v := range f.Embedding {
			centroid[i] += v
		}
	}
	n := float32(len(facts))
	for i := range centroid {
		centroid[i] /= n
	}
	return centroid
}
