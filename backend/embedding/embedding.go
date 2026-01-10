package embedding

import "context"

// EmbeddingGenerator defines the interface for generating vector embeddings for text.
type EmbeddingGenerator interface {
	// GenerateEmbeddings takes a slice of texts and returns a corresponding slice of embeddings.
	// Each inner slice ([]float32) represents the vector embedding for the text at the same index.
	// Returns an error if the generation process fails for any text.
	GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error)
}
