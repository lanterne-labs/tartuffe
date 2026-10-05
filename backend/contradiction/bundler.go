// Package contradiction — bundler.go implements the LLM-verified upgrade path for a
// contradiction's bundle_stance_id when the fact-level fast path is inconclusive.
//
// Background. Contradictions are bundled by stance: if both facts of a contradiction
// belong to the same stance_documents row, that stance is the bundle id (recorded in
// contradictions.bundle_stance_id at insert time, see RecordContradiction). Same-bundle
// contradictions count as one in the honesty-score formula plus a logarithmic penalty
// for repetitions; this is the whole point of the bundling layer.
//
// Two cases need fallback handling:
//  1. The two facts are in *different* stances (e.g. promise stance vs. retraction
//     stance). The fact-level JOIN at insert time leaves bundle_stance_id NULL. But
//     the contradiction itself has a topic — "broken promise about X" — which often
//     maps to one of those stances or a third one. We want to assign that stance so
//     the row joins the bundle.
//  2. One or both facts have no stance row at all (predates the stance backfill, or
//     ingestion bypassed ProcessNewFact). Same NULL outcome, same need for repair.
//
// The Bundler resolves both cases the same way:
//   - embed the contradiction summary ("claim A | claim B | reason") under
//     PurposeContradictionBundle (instruction tells the model to project on the
//     *underlying inconsistency*, not the topic of either claim — see embedding.go);
//   - retrieve the top-K stances by cosine distance for that person;
//   - ask the LLM yes/no whether each candidate stance is the topic of this
//     contradiction;
//   - on the first YES, write that stance via SetContradictionBundleStance, which
//     also recomputes the honesty score in the same transaction.
//
// If no candidate matches, the contradiction stays NULL and is treated as a singleton
// in the score formula. This is the conservative choice: better one extra solo bundle
// than a wrong merge that hides a real disagreement.
package contradiction

import (
	"context"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/storage"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// BundlerStorer is the subset of storage operations the Bundler needs.
// Kept narrow so the Bundler is trivially mockable.
type BundlerStorer interface {
	GetFactStanceMap(ctx context.Context, personID int64, factIDs []int64) (map[int64]int64, error)
	FindTopStancesForPerson(ctx context.Context, personID int64, embedding []float32, limit int) ([]storage.StanceCandidate, error)
	SetContradictionBundleStance(ctx context.Context, contradictionID int64, bundleStanceID int64) error
}

// Bundler upgrades a contradiction's bundle_stance_id when the fact-level shared-stance
// fast path didn't find one. See package doc for the full rationale.
type Bundler struct {
	storer     BundlerStorer
	embedder   embedding.EmbeddingGenerator
	client     *llm.Client
	topK       int
	maxYesDist float64
}

// DefaultBundlerTopK is how many candidate stances we ask the LLM about.
// Three is enough that the right stance is almost always in the top-3 by embedding
// distance, but small enough that the LLM cost stays one cheap call per contradiction.
const DefaultBundlerTopK = 3

// DefaultBundlerMaxDistance gates the LLM call: candidates farther than this in cosine
// distance from the contradiction embedding are filtered out before we even ask the LLM,
// because at that distance the answer is essentially always NO and we'd waste the call.
// Tuned empirically against PurposeContradictionBundle vectors; not load-bearing for
// correctness (an over-tight gate just means more solo bundles, never wrong merges).
const DefaultBundlerMaxDistance = 1.2

// NewBundlerFromEnv constructs a Bundler using the same OPEN_AI_* env vars as the rest
// of the LLM stack. Returns an error if the LLM env is misconfigured; the embedder must
// be supplied by the caller (it's already constructed elsewhere in the pipeline).
func NewBundlerFromEnv(storer BundlerStorer, embedder embedding.EmbeddingGenerator) (*Bundler, error) {
	client, err := llm.NewClientFromEnv("contradiction-bundler")
	if err != nil {
		return nil, err
	}
	return &Bundler{
		storer:     storer,
		embedder:   embedder,
		client:     client,
		topK:       DefaultBundlerTopK,
		maxYesDist: DefaultBundlerMaxDistance,
	}, nil
}

// Resolve attempts to assign a bundle_stance_id to the contradiction. Idempotent and
// safe to call after RecordContradictionImmediate. Returns nil on success including
// the no-match case (the row simply remains a solo bundle).
func (b *Bundler) Resolve(ctx context.Context, personID int64, contradictionID int64,
	factAID, factBID int64, factAText, factBText, reason string) error {

	// Fast path: if both facts share a stance, the SQL CTE in RecordContradiction has
	// already populated bundle_stance_id. Re-checking here is cheap and lets us skip
	// the embedding+LLM round-trip entirely.
	stances, err := b.storer.GetFactStanceMap(ctx, personID, []int64{factAID, factBID})
	if err != nil {
		return fmt.Errorf("bundler: fact-stance lookup: %w", err)
	}
	sa, okA := stances[factAID]
	sb, okB := stances[factBID]
	if okA && okB && sa == sb {
		// Already bundled by the SQL fast path; nothing to do.
		return nil
	}

	// Build the bundle query string. The format is read by the LLM as well as embedded;
	// keeping both consistent matches the PurposeContradictionBundle instruction's framing
	// ("claim A versus claim B and the reason they conflict").
	bundleText := fmt.Sprintf("Claim A: %s\nClaim B: %s\nReason they conflict: %s",
		strings.TrimSpace(factAText), strings.TrimSpace(factBText), strings.TrimSpace(reason))

	embeddings, err := b.embedder.GenerateEmbeddings(ctx, embedding.PurposeContradictionBundle, []string{bundleText})
	if err != nil {
		return fmt.Errorf("bundler: embedding contradiction summary: %w", err)
	}
	if len(embeddings) != 1 {
		return fmt.Errorf("bundler: expected 1 embedding, got %d", len(embeddings))
	}

	candidates, err := b.storer.FindTopStancesForPerson(ctx, personID, embeddings[0], b.topK)
	if err != nil {
		return fmt.Errorf("bundler: finding top stances for person %d: %w", personID, err)
	}
	if len(candidates) == 0 {
		log.Debug().Int64("person_id", personID).Int64("contradiction_id", contradictionID).
			Msg("Bundler: no stance candidates; leaving as solo bundle")
		return nil
	}

	for _, cand := range candidates {
		if cand.Distance > b.maxYesDist {
			// Distance-gated: anything beyond this is a near-certain NO; skip the LLM call.
			log.Debug().Int64("person_id", personID).Int64("stance_id", cand.ID).
				Float64("distance", cand.Distance).
				Msg("Bundler: candidate beyond distance gate, skipping LLM")
			continue
		}
		ok, judgeErr := b.askLLMRelated(ctx, bundleText, cand)
		if errors.Is(judgeErr, llm.ErrUnavailable) {
			// Declining the remaining candidates would leave the row solo on
			// the outage's account.
			return fmt.Errorf("bundler: relatedness check for stance %d: %w", cand.ID, judgeErr)
		}
		if judgeErr != nil {
			// Don't fail the whole resolution on one LLM hiccup; try the next candidate.
			log.Warn().Err(judgeErr).Int64("stance_id", cand.ID).
				Msg("Bundler: LLM relatedness check failed; trying next candidate")
			continue
		}
		if !ok {
			continue
		}
		// First YES wins. SetContradictionBundleStance also recomputes the honesty score
		// (and emits SSE) in one transaction.
		if err := b.storer.SetContradictionBundleStance(ctx, contradictionID, cand.ID); err != nil {
			return fmt.Errorf("bundler: assigning bundle stance %d to contradiction %d: %w",
				cand.ID, contradictionID, err)
		}
		log.Info().Int64("person_id", personID).Int64("contradiction_id", contradictionID).
			Int64("stance_id", cand.ID).Str("stance_label", cand.ClusterLabel).
			Float64("distance", cand.Distance).
			Msg("Bundler: bundled contradiction into stance via LLM verification")
		metrics.IncrementContradictionChecks("bundled")
		return nil
	}

	log.Debug().Int64("person_id", personID).Int64("contradiction_id", contradictionID).
		Int("candidates", len(candidates)).
		Msg("Bundler: no candidate accepted by LLM; leaving as solo bundle")
	return nil
}

const bundlerLLMPromptTpl = `You judge whether a political contradiction belongs to an existing topic cluster (a "stance") for the same person.

CONTRADICTION (between two of this person's statements):
%s

CANDIDATE STANCE for this same person:
Label: %s
Summary: %s

Answer YES only if the contradiction is fundamentally about the same underlying issue, promise, or position as the stance — i.e. an additional instance of the same broken promise / reversal / inconsistency, not merely a tangential overlap of vocabulary.

Reply with exactly two lines, in this order:
REASONING: <one sentence: is the contradiction about the stance's issue?>
VERDICT: YES or NO`

// askLLMRelated runs a single yes/no LLM check. Tight prompt, low temperature, small
// max-tokens — this is a classifier call, not generation.
func (b *Bundler) askLLMRelated(ctx context.Context, bundleText string, cand storage.StanceCandidate) (bool, error) {
	prompt := fmt.Sprintf(bundlerLLMPromptTpl, bundleText, cand.ClusterLabel, cand.Summary)

	content, _, err := b.client.Chat(ctx, llm.ChatRequest{
		Messages:    []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature: 0.0,
		// The reasoning line comes before the verdict (CLAUDE.md: reasoning
		// first, verdict last), so the reply needs room for one sentence.
		MaxTokens: 256,
	})
	if err != nil {
		return false, fmt.Errorf("bundler LLM call: %w", err)
	}
	content = strings.ToUpper(strings.TrimSpace(content))
	// Robust to small surrounding noise: look for "VERDICT: YES" / "VERDICT: NO".
	switch {
	case strings.Contains(content, "VERDICT: YES") || strings.Contains(content, "VERDICT:YES"):
		return true, nil
	case strings.Contains(content, "VERDICT: NO") || strings.Contains(content, "VERDICT:NO"):
		return false, nil
	default:
		return false, fmt.Errorf("bundler: unparseable LLM verdict: %q", content)
	}
}
