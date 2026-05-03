package embedding

import "context"

// Purpose identifies what an embedding is being computed for.
//
// Different purposes can map to different instruction prefixes (Qwen-style
// "Instruct: ...\nQuery: ..." prompts). Today every callsite is storage-side
// (the resulting vector is written to facts.embedding and later used as both
// query and document side of symmetric KNN searches), so a single Purpose is
// sufficient. Adding query-time re-embedding would justify additional
// purposes.
type Purpose string

const (
	// PurposeFactStorage tags embeddings of political claims that are stored
	// in facts.embedding and later searched for similarity, contradiction, or
	// stance clustering. All KNN comparisons in this codebase happen between
	// vectors produced under this purpose, so swapping its instruction
	// requires a full re-embed (see `dm embeddings invalidate` +
	// `dm embeddings regenerate`).
	PurposeFactStorage Purpose = "fact_storage"

	// PurposeContradictionBundle tags ad-hoc query embeddings used by the
	// contradiction Bundler (backend/contradiction/bundler.go). The text being
	// embedded is a contradiction summary ("claim A vs. claim B — reason"), and
	// the vector is compared against per-person stance centroids to find which
	// existing bundle (if any) this new contradiction extends. The instruction
	// asks the model to project on the *underlying inconsistency* rather than
	// the topic of either claim, which differs from PurposeFactStorage where a
	// single claim is being represented for "find related claims" retrieval.
	PurposeContradictionBundle Purpose = "contradiction_bundle"
)

// EmbeddingGenerator generates vector embeddings for text under a stated Purpose.
type EmbeddingGenerator interface {
	// GenerateEmbeddings embeds each text with the instruction associated with
	// purpose. The returned slice has the same length and order as texts.
	GenerateEmbeddings(ctx context.Context, purpose Purpose, texts []string) ([][]float32, error)
}
