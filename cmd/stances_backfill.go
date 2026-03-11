package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"fmt"
	"math"
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
	Long: `Groups existing facts by embedding similarity and creates stance documents.

For each person (or specified person):
1. Loads all active facts with embeddings
2. Groups facts by embedding_text similarity (exact match first, then cosine distance < 0.2)
3. For multi-fact clusters: generates LLM summary
4. For single-fact clusters: uses fact text as summary
5. Inserts stance documents and links facts

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

	// Cluster facts by embedding similarity
	clusters := clusterFacts(facts)
	log.Info().Str("person", personName).Int("clusters", len(clusters)).Msg("Clustered facts")

	// Check existing stances to skip duplicates
	existingStances, err := adapter.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		return 0, 0, fmt.Errorf("getting existing stances: %w", err)
	}
	existingLabels := make(map[string]bool, len(existingStances))
	for _, s := range existingStances {
		existingLabels[s.ClusterLabel] = true
	}

	for _, cluster := range clusters {
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
			// Build StanceFactInfo list for LLM
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

		// Truncate label to 100 chars
		clusterLabel := strings.TrimSpace(label)
		if len(clusterLabel) > 100 {
			clusterLabel = clusterLabel[:100]
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

		// Link all facts to the stance
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

func clusterFacts(facts []backfillFact) []factCluster {
	var clusters []factCluster

	for _, fact := range facts {
		matched := false
		for i := range clusters {
			// Check exact embedding_text match first
			if clusters[i].label == fact.EmbeddingText {
				addToCluster(&clusters[i], fact)
				matched = true
				break
			}
			// Then check cosine distance
			if cosineDistance(clusters[i].centroid, fact.Embedding) < 0.2 {
				addToCluster(&clusters[i], fact)
				matched = true
				break
			}
		}

		if !matched {
			clusters = append(clusters, factCluster{
				facts:    []backfillFact{fact},
				centroid: copyEmbedding(fact.Embedding),
				label:    fact.EmbeddingText,
			})
		}
	}

	return clusters
}

func addToCluster(c *factCluster, fact backfillFact) {
	oldCount := len(c.facts)
	c.facts = append(c.facts, fact)

	// Update centroid as weighted average
	newCount := float32(oldCount + 1)
	for i := range c.centroid {
		c.centroid[i] = (c.centroid[i]*float32(oldCount) + fact.Embedding[i]) / newCount
	}
}

func copyEmbedding(emb []float32) []float32 {
	cp := make([]float32, len(emb))
	copy(cp, emb)
	return cp
}

func cosineDistance(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 2.0 // max distance
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 2.0
	}
	return 1.0 - dot/(math.Sqrt(normA)*math.Sqrt(normB))
}
