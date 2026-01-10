package embedding

import (
	"context"
	"os"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to load .env for tests
func loadEnv(t *testing.T) {
	t.Helper()
	err := godotenv.Load("../../.env")
	if err != nil {
		t.Log("INFO: .env file not found or failed to load, tests needing env vars might be skipped.")
	}
}

// TestOpenAIEmbeddingGenerator_GenerateEmbeddings tests the embedding generation.
// It requires OPEN_AI_KEY environment variable to be set (e.g., in .env).
func TestOpenAIEmbeddingGenerator_GenerateEmbeddings(t *testing.T) {
	loadEnv(t)
	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		t.Skip("Skipping Embedding test: OPEN_AI_KEY not set.")
	}

	generator, err := NewOpenAIEmbeddingGeneratorFromEnv()
	require.NoError(t, err, "Failed to create OpenAIEmbeddingGenerator")

	// Test with a small batch of simple texts
	testTexts := []string{
		"This is the first sentence.",
		"Here is another sentence for testing.",
		"A third one.",
	}

	ctx := context.Background()
	embeddings, err := generator.GenerateEmbeddings(ctx, testTexts)

	require.NoError(t, err, "GenerateEmbeddings failed")
	require.Len(t, embeddings, len(testTexts), "Incorrect number of embeddings returned")

	expectedDimensions := 1024 // default embed dimensions used in DB schema
	for i, emb := range embeddings {
		t.Logf("Embedding %d: %d dimensions (first few: %v...)", i+1, len(emb), emb[:min(5, len(emb))])
		assert.Len(t, emb, expectedDimensions, "Embedding %d has incorrect dimensions", i+1)
		// Check that the embedding is not all zeros (basic sanity check)
		assert.NotEqual(t, make([]float32, expectedDimensions), []float32(emb), "Embedding %d should not be all zeros", i+1)
	}
}

// Test with empty input
func TestOpenAIEmbeddingGenerator_GenerateEmbeddings_EmptyInput(t *testing.T) {
	loadEnv(t)
	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		t.Skip("Skipping Embedding test: OPEN_AI_KEY not set.")
	}

	generator, err := NewOpenAIEmbeddingGeneratorFromEnv()
	require.NoError(t, err)

	embeddings, err := generator.GenerateEmbeddings(context.Background(), []string{})
	require.NoError(t, err)
	assert.Empty(t, embeddings, "Should return empty slice for empty input")
}

// Helper for logging embeddings
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
