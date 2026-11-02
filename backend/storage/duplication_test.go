package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"digital-mirror/backend/embedding"
	"digital-mirror/backend/util"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	pgvector "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Structs and Loading ---

// CorpusEntry represents an entry in the ground truth JSON file.
type CorpusEntry struct {
	ID         int    `json:"id"`          // Unique ID from the corpus file
	Text       string `json:"text"`        // The text content of the fact
	GroupID    string `json:"group_id"`    // Identifier for facts that should match
	PersonName string `json:"person_name"` // Name of the person associated with the fact
}

// loadCorpus loads the ground truth data from the specified JSON file.
func loadCorpus(t *testing.T, filePath string) []CorpusEntry {
	t.Helper()
	data, err := os.ReadFile(filePath)
	require.NoError(t, err, "Failed to read corpus file: %s", filePath)

	var corpus []CorpusEntry
	err = json.Unmarshal(data, &corpus)
	require.NoError(t, err, "Failed to unmarshal corpus JSON from: %s", filePath)
	require.NotEmpty(t, corpus, "Corpus data is empty")
	return corpus
}

// ProcessedEntry holds data needed during the test after processing a CorpusEntry.
type ProcessedEntry struct {
	CorpusID       int       // Original ID from the corpus file
	GroupID        string    // Group ID for matching
	PersonName     string    // Normalized person name
	PersonDBID     int64     // Database ID of the person
	Text           string    // Fact text
	Embedding      []float32 // Generated embedding for the fact text
	DBFactID       int64     // Database ID of the inserted fact
	DBFactSourceID int64     // Database ID of the inserted fact source
}

// --- Helper Functions ---

// findPersonNameByFactDBID attempts to find the normalized person name associated with a fact's database ID.
// It uses pre-built maps for efficiency.
func findPersonNameByFactDBID(factDBID int64, factDBIDToPersonDBID map[int64]int64, personDBIDToName map[int64]string) string {
	if personDBID, ok := factDBIDToPersonDBID[factDBID]; ok {
		if name, okName := personDBIDToName[personDBID]; okName {
			return name // Return normalized name
		}
	}
	return "<unknown>"
}

// --- Test Function ---

// TestPersonSimilarity evaluates the FindNeighborFactsForPerson performance
// using the ground truth corpus adapted with person names.
func TestPersonSimilarity(t *testing.T) {
	err := godotenv.Overload(filepath.Join("..", "..", ".env"))
	if err != nil {
		t.Log("INFO: .env file not found or failed to load, relying on environment variables.")
	} else {
		t.Log("INFO: Loaded environment variables from .env file")
	}

	dbURL := os.Getenv("DATABASE_URL")
	openAIKey := os.Getenv("OPEN_AI_KEY")
	if dbURL == "" || openAIKey == "" {
		t.Skip("Skipping similarity test: DATABASE_URL or OPEN_AI_KEY not set")
	}

	// Set up console logger only when the test will actually run (avoids data race
	// with background goroutines spawned by other tests that read log.Logger).
	log.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.RFC3339,
		NoColor:    false,
	})
	zerolog.SetGlobalLevel(zerolog.DebugLevel)
	log.Debug().Msg("Console logger forced for TestPersonSimilarity")

	// --- Test Dependencies Initialization ---
	ctx := context.Background()

	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	require.NoError(t, err, "Failed to create embedder")

	storer, err := NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err, "Failed to connect to test database")
	defer storer.Close()

	// --- Database Setup and Cleanup ---
	cleanup := func(pool *pgxpool.Pool) {
		log.Debug().Msg("Cleaning up test tables...")
		_, err := pool.Exec(context.Background(), "TRUNCATE TABLE fact_mentions, fact_sources, facts, persons RESTART IDENTITY CASCADE")
		if err != nil {
			log.Warn().Err(err).Msg("Failed to truncate test tables during cleanup")
		}
	}
	cleanup(storer.Pool())       // Cleanup before test
	defer cleanup(storer.Pool()) // Cleanup after test

	// --- Load Corpus & Generate Embeddings ---
	corpusPath := filepath.Join("..", "..", "testdata", "corpus", "person_similarity_ground_truth.json")
	corpus := loadCorpus(t, corpusPath)

	texts := make([]string, 0, len(corpus))
	corpusPersonNames := make([]string, 0, len(corpus))
	validCorpusEntries := make([]CorpusEntry, 0, len(corpus))

	// Filter out invalid entries and prepare for embedding
	for _, entry := range corpus {
		if entry.Text == "" || entry.PersonName == "" {
			log.Warn().Int("corpus_id", entry.ID).Msg("Skipping corpus entry: empty text or person name")
			continue
		}
		texts = append(texts, entry.Text)
		corpusPersonNames = append(corpusPersonNames, entry.PersonName)
		validCorpusEntries = append(validCorpusEntries, entry)
	}

	if len(texts) == 0 {
		t.Fatal("No valid entries found in the corpus to process.")
	}

	embeddings, err := embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, texts)
	require.NoError(t, err, "Failed to generate embeddings")
	require.Equal(t, len(validCorpusEntries), len(embeddings), "Mismatch between valid corpus size and embeddings count")

	// --- Populate Test Database ---
	// Maps to store relationships between corpus IDs, DB IDs, and other data
	processedEntries := make(map[int]ProcessedEntry) // Map Corpus ID -> ProcessedEntry
	personNameToID := make(map[string]int64)         // Map Normalized Person Name -> DB Person ID
	personDBIDToName := make(map[int64]string)       // Map DB Person ID -> Normalized Person Name
	factDBIDToCorpusID := make(map[int64]int)        // Map DB Fact ID -> Corpus ID
	factDBIDToGroupID := make(map[int64]string)      // Map DB Fact ID -> Group ID
	factDBIDToPersonDBID := make(map[int64]int64)    // Map DB Fact ID -> DB Person ID

	txPopulate, err := storer.Pool().Begin(ctx)
	require.NoError(t, err, "Failed to begin transaction for population")

	// 1. Insert Persons (unique normalized names)
	for _, name := range corpusPersonNames {
		normalizedName := util.NormalizeString(name)
		if _, exists := personNameToID[normalizedName]; !exists {
			personID, inserted, err := storer.AddPerson(ctx, name) // Uses original name for insertion
			require.NoError(t, err, "Failed to add/find person %s", name)
			personNameToID[normalizedName] = personID
			personDBIDToName[personID] = normalizedName
			t.Logf("Processed person: %s (Normalized: %s, ID: %d, Inserted: %t)", name, normalizedName, personID, inserted)
		}
	}

	// 2. Insert Facts, Sources, Mentions (using valid entries and corresponding embeddings)
	for i, entry := range validCorpusEntries {
		normalizedPersonName := util.NormalizeString(entry.PersonName)
		personDBID, ok := personNameToID[normalizedPersonName]
		if !ok {
			t.Fatalf("Internal error: Person ID not found for normalized name: %s (corpus ID: %d)", normalizedPersonName, entry.ID)
		}

		currentEmbedding := embeddings[i]

		// Insert Fact
		var factDBID int64
		insertFactStmt := `INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`
		err = txPopulate.QueryRow(ctx, insertFactStmt, entry.Text, pgvector.NewHalfVector(currentEmbedding)).Scan(&factDBID)
		require.NoError(t, err, "Failed to insert fact for corpus entry %d", entry.ID)

		// Insert Fact Source
		var factSourceDBID int64
		dummyURL := fmt.Sprintf("corpus://id/%d", entry.ID)
		insertSourceStmt := `INSERT INTO fact_sources (fact_id, source_url, original_quote) VALUES ($1, $2, $3) RETURNING id`
		err = txPopulate.QueryRow(ctx, insertSourceStmt, factDBID, dummyURL, entry.Text).Scan(&factSourceDBID)
		require.NoError(t, err, "Failed to insert fact source for corpus entry %d", entry.ID)

		// Insert Fact Mention
		insertMentionStmt := `INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`
		_, err = txPopulate.Exec(ctx, insertMentionStmt, factSourceDBID, personDBID)
		require.NoError(t, err, "Failed to insert fact mention for corpus entry %d", entry.ID)

		// Store processed data and mappings
		procEntry := ProcessedEntry{
			CorpusID:       entry.ID,
			GroupID:        entry.GroupID,
			PersonName:     normalizedPersonName,
			PersonDBID:     personDBID,
			Text:           entry.Text,
			Embedding:      currentEmbedding,
			DBFactID:       factDBID,
			DBFactSourceID: factSourceDBID,
		}
		processedEntries[entry.ID] = procEntry
		factDBIDToCorpusID[factDBID] = entry.ID
		factDBIDToGroupID[factDBID] = entry.GroupID
		factDBIDToPersonDBID[factDBID] = personDBID
	}

	err = txPopulate.Commit(ctx)
	require.NoError(t, err, "Failed to commit transaction for population")
	t.Logf("Successfully populated test DB with %d valid entries", len(validCorpusEntries))

	// --- Evaluate Person-Specific Similarity Detection ---
	tp := 0                                         // True Positives
	fp := 0                                         // False Positives
	predictedPositivePairs := make(map[[2]int]bool) // Store pairs of (corpus_id_1, corpus_id_2)

	// Iterate through each processed entry and find its neighbors
	for _, entryI := range processedEntries {
		txSearch, err := storer.Pool().Begin(ctx)
		require.NoError(t, err, "Failed to begin search transaction for corpus entry %d", entryI.CorpusID)

		neighborFacts, err := storer.FindNeighborFactsForPerson(ctx, txSearch, entryI.PersonDBID, entryI.Embedding, entryI.DBFactID)
		_ = txSearch.Rollback(ctx)
		require.NoError(t, err, "Error calling FindNeighborFactsForPerson for corpus entry %d (FactID: %d, PersonID: %d)", entryI.CorpusID, entryI.DBFactID, entryI.PersonDBID)

		// DEBUG LOG: Log neighbors found for specific entries
		if entryI.CorpusID == 1 || entryI.CorpusID == 4 || entryI.CorpusID == 5 {
			t.Logf("DEBUG: Found %d neighbors for CorpusID %d (Person: %s, FactID: %d, Group: %s)",
				len(neighborFacts), entryI.CorpusID, entryI.PersonName, entryI.DBFactID, entryI.GroupID)
			for _, nf := range neighborFacts {
				neighborCorpusID := factDBIDToCorpusID[nf.ID]
				neighborPersonName := findPersonNameByFactDBID(nf.ID, factDBIDToPersonDBID, personDBIDToName)
				neighborGroupID := factDBIDToGroupID[nf.ID]
				t.Logf("  - Neighbor CorpusID: %d (FactID: %d, Person: %s, Group: %s), Distance: %.4f",
					neighborCorpusID, nf.ID, neighborPersonName, neighborGroupID, nf.Distance)
			}
		}

		// Evaluate each neighbor found
		for _, neighborFact := range neighborFacts {
			neighborCorpusID, ok := factDBIDToCorpusID[neighborFact.ID]
			if !ok {
				t.Errorf("Internal error: Could not find original corpus ID for neighbor FactID %d", neighborFact.ID)
				continue
			}

			// Create canonical pair (min_id, max_id) to avoid double counting
			pair := [2]int{min(entryI.CorpusID, neighborCorpusID), max(entryI.CorpusID, neighborCorpusID)}

			if !predictedPositivePairs[pair] {
				predictedPositivePairs[pair] = true // Mark this pair as found

				groupIDI := entryI.GroupID
				groupIDJ := factDBIDToGroupID[neighborFact.ID]

				// Check if the predicted match is correct (same group ID)
				if groupIDI == groupIDJ {
					tp++ // True Positive: Found a neighbor with the same person and same group
				} else {
					fp++ // False Positive: Found a neighbor for the same person, but different group
					neighborPersonName := findPersonNameByFactDBID(neighborFact.ID, factDBIDToPersonDBID, personDBIDToName)
					t.Logf("False Positive: CorpusID %d (Person: %s, Group: %s) matched with CorpusID %d (Person: %s, Group: %s) Distance: %.4f",
						entryI.CorpusID, entryI.PersonName, groupIDI,
						neighborCorpusID, neighborPersonName, groupIDJ,
						neighborFact.Distance)
				}
			}
		}
	}

	// --- Calculate Ground Truth and Metrics ---

	// Calculate Total True Positive Pairs from the ground truth
	// (Pairs of entries with the Same Person AND Same Group ID)
	truePositivePairs := make(map[[2]int]bool)
	processedSlice := make([]ProcessedEntry, 0, len(processedEntries)) // Need slice for pairwise comparison
	for _, entry := range processedEntries {
		processedSlice = append(processedSlice, entry)
	}
	for i := 0; i < len(processedSlice); i++ {
		for j := i + 1; j < len(processedSlice); j++ {
			entryI := processedSlice[i]
			entryJ := processedSlice[j]
			if entryI.PersonDBID == entryJ.PersonDBID && entryI.GroupID == entryJ.GroupID {
				pair := [2]int{min(entryI.CorpusID, entryJ.CorpusID), max(entryI.CorpusID, entryJ.CorpusID)}
				truePositivePairs[pair] = true
			}
		}
	}

	// Calculate False Negatives (Ground truth pairs that were NOT predicted)
	fn := 0
	for pair := range truePositivePairs {
		if !predictedPositivePairs[pair] {
			fn++
			p1, ok1 := processedEntries[pair[0]]
			// p2, ok2 := processedEntries[pair[1]] // We only need one entry to log details
			if ok1 {
				t.Logf("False Negative: Pair CorpusIDs (%d, %d) with GroupID %s for Person %s (DBID %d) was not matched.",
					pair[0], pair[1], p1.GroupID, p1.PersonName, p1.PersonDBID)
			} else {
				t.Logf("False Negative: Pair CorpusIDs (%d, %d) was not matched (data lookup failed).", pair[0], pair[1])
			}
		}
	}

	// Calculate Precision, Recall, F1
	precision := 0.0
	if (tp + fp) > 0 {
		precision = float64(tp) / float64(tp+fp)
	}

	recall := 0.0
	if len(truePositivePairs) > 0 {
		recall = float64(tp) / float64(len(truePositivePairs))
	}

	f1 := 0.0
	if (precision + recall) > 0 {
		f1 = 2 * (precision * recall) / (precision + recall)
	}

	// --- Log Results and Assertions ---
	t.Logf("Person-Specific Similarity Results (Threshold: %.2f, MaxCandidates: %d):", embeddingSimilarityThreshold, maxCandidatesToCheck)
	t.Logf("  True Positives (Correctly matched pairs for same person): %d", tp)
	t.Logf("  False Positives (Incorrectly matched pairs for same person): %d", fp)
	t.Logf("  False Negatives (Should have matched pairs for same person): %d", fn)
	t.Logf("  Total True Positive Pairs (Ground Truth - Same Person & Group): %d", len(truePositivePairs))
	t.Logf("  Precision: %.4f", precision)
	t.Logf("  Recall: %.4f", recall)
	t.Logf("  F1 Score: %.4f", f1)

	// Assertions: Check if the metrics meet minimum expectations
	assert.True(t, recall >= 0.9, "Recall should be high for person-specific duplicates (Recall=%.4f)", recall)
	assert.True(t, precision > 0.7, "Precision should be reasonable (Precision=%.4f)", precision)
}
