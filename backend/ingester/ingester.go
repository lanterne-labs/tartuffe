package ingester

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
	"fmt"

	"github.com/rs/zerolog/log"
)

// FactIngester turns normalized facts into DB rows and triggers contradiction checks.
type FactIngester struct {
	Embedder embedding.EmbeddingGenerator
	Store    *storage.PostgresStorer
}

// Ingest embeds given normalized fact texts (and required fields) and saves them.
// Contradiction checks are intentionally not performed here to keep concerns separated.
func (i *FactIngester) Ingest(ctx context.Context, items []storage.StoreFactInput) error {
	if len(items) == 0 {
		return nil
	}
	texts := make([]string, len(items))
	for idx, it := range items {
		texts[idx] = it.NormalizedFactText
	}
	vecs, err := i.Embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, texts)
	if err != nil {
		return err
	}
	if len(vecs) != len(items) {
		return fmt.Errorf("embedding count mismatch: %d vs %d", len(vecs), len(items))
	}
	// Convert to core.StoreFactInput and add embeddings
	coreInputs := make([]core.StoreFactInput, len(items))
	for idx, it := range items {
		coreInputs[idx] = core.StoreFactInput{
			SubjectEntity:        it.SubjectEntity,
			SubjectEntityType:    it.SubjectEntityType,
			NormalizedFactText:   it.NormalizedFactText,
			NormalizedFactTextEn: it.NormalizedFactTextEn,
			Embedding:            vecs[idx],
			OriginalQuote:        it.OriginalQuote,
			CanonicalVerb:        it.CanonicalVerb,
			InvolvedPeople:       it.InvolvedPeople,
			SourceURL:            it.SourceURL,
			PublishedAt:          it.PublishedAt,
			SourceType:           it.SourceType,
			Metadata:             it.Metadata,
			Lang:                 it.Lang,
		}

	}

	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(i.Store))
	if err != nil {
		return fmt.Errorf("stance manager required for fact ingestion: %w", err)
	}
	inserter := core.NewFactInserter(i.Store, stanceMgr)
	inserter.EnableLLMDedupe()
	if synth, sErr := core.NewLLMMetaSynthesizerFromEnv(); sErr != nil {
		log.Warn().Err(sErr).Msg("Meta-fact folding disabled (synthesizer unavailable); same-claim facts are dropped as before")
	} else {
		inserter.EnableMetaFolding(i.Store, synth)
	}
	_, err = inserter.InsertFacts(ctx, coreInputs)
	return err
}
