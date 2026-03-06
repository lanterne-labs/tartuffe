package contradiction

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/storage"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
)

// Listener encapsulates DB listening and contradiction processing.
type Listener struct {
	Pool     *pgxpool.Pool
	Storer   *storage.PostgresStorer
	Detector ContradictionDetector
}

// ListenAndProcess blocks, listening on the facts_changed channel and processing contradictions.
func (l *Listener) ListenAndProcess(ctx context.Context) error {
	if l.Pool == nil || l.Storer == nil || l.Detector == nil {
		return fmt.Errorf("listener not initialized")
	}
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "LISTEN facts_changed"); err != nil {
		return fmt.Errorf("LISTEN: %w", err)
	}
	log.Info().Msg("contradiction.Listener: listening on facts_changed")
	for {
		n, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			log.Error().Err(err).Msg("listener error; backing off")
			time.Sleep(2 * time.Second)
			continue
		}
		factID, perr := parseInt64(n.Payload)
		if perr != nil {
			log.Warn().Str("payload", n.Payload).Msg("invalid payload")
			continue
		}
		if err := l.processFact(ctx, factID); err != nil {
			log.Error().Err(err).Int64("fact_id", factID).Msg("contradiction processing failed")
		}
	}
}

func (l *Listener) processFact(ctx context.Context, newFactID int64) error {
	// Check if the new fact is a duplicate - skip if yes
	isDup, err := l.Storer.IsDuplicate(ctx, newFactID)
	if err != nil {
		return fmt.Errorf("failed to check duplicate status: %w", err)
	}
	if isDup {
		log.Debug().Int64("fact_id", newFactID).Msg("Skipping contradiction check for duplicate fact")
		return nil
	}

	// Use a read-only transaction for queries
	tx, err := l.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	newText, err := l.Storer.GetFactTextByID(ctx, tx, newFactID)
	if err != nil {
		return err
	}
	newDate, err := l.Storer.GetFactDateByID(ctx, tx, newFactID)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
        SELECT DISTINCT fm.person_id
        FROM fact_sources fs
        JOIN fact_mentions fm ON fm.fact_source_id = fs.id
        WHERE fs.fact_id = $1`, newFactID)
	if err != nil {
		return err
	}
	var pids []int64
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return err
		}
		pids = append(pids, pid)
	}
	rows.Close()

	emb, err := getEmbeddingForFact(ctx, tx, newFactID)
	if err != nil {
		return err
	}

	// Get new fact's canonical verb for verb-aware filtering
	newVerb, _ := l.Storer.GetFactCanonicalVerb(ctx, newFactID)

	for _, pid := range pids {
		neighbors, err := l.Storer.FindNeighborFactsForPerson(ctx, tx, pid, emb, newFactID)
		if err != nil {
			return err
		}

		// Collect valid pairs after verb-aware filtering
		type candidatePair struct {
			factAText string
			factADate *time.Time
			factBID   int64
			factBText string
			factBDate *time.Time
		}
		var pairs []candidatePair

		for _, nb := range neighbors {
			// Skip same-verb or both-external pairs with high similarity (>0.85) — paraphrases, not contradictions
			if newVerb != "" && nb.CanonicalVerb != "" &&
				(1.0-nb.Distance) > 0.85 {
				sameVerb := newVerb == nb.CanonicalVerb
				bothExternal := !categorizer.IsDirectClaimVerb(newVerb) && !categorizer.IsDirectClaimVerb(nb.CanonicalVerb)
				if sameVerb || bothExternal {
					log.Debug().
						Int64("new_fact", newFactID).
						Int64("neighbor", nb.ID).
						Str("new_verb", newVerb).
						Str("neighbor_verb", nb.CanonicalVerb).
						Bool("both_external", bothExternal).
						Float64("similarity", 1.0-nb.Distance).
						Msg("Skipping paraphrase pair in listener")
					continue
				}
			}

			// Skip pairs where neither fact has a direct claim verb
			if !categorizer.IsDirectClaimVerb(newVerb) && !categorizer.IsDirectClaimVerb(nb.CanonicalVerb) {
				log.Debug().
					Int64("new_fact", newFactID).
					Int64("neighbor", nb.ID).
					Str("new_verb", newVerb).
					Str("neighbor_verb", nb.CanonicalVerb).
					Msg("Skipping external-only pair in listener")
				continue
			}

			// Walk UP the implication chain to find the most general ancestor
			targetID := nb.ID
			targetText := nb.Text
			targetDate := nb.PublishedAt

			ancestorID, err := l.Storer.GetMostGeneralAncestor(ctx, nb.ID)
			if err != nil {
				log.Warn().Err(err).Int64("fact_id", nb.ID).Msg("Failed to get ancestor, using original fact")
			} else if ancestorID != nb.ID {
				ancestorText, err := l.Storer.GetFactTextByID(ctx, tx, ancestorID)
				if err != nil {
					log.Warn().Err(err).Int64("ancestor_id", ancestorID).Msg("Failed to get ancestor text, using original")
				} else {
					targetID = ancestorID
					targetText = ancestorText
					ancestorDate, err := l.Storer.GetFactDateByID(ctx, tx, ancestorID)
					if err == nil {
						targetDate = ancestorDate
					}
				}
			}

			pairs = append(pairs, candidatePair{
				factAText: newText,
				factADate: newDate,
				factBID:   targetID,
				factBText: targetText,
				factBDate: targetDate,
			})
		}

		// Process pairs in batches using AreContradictoryBatch
		for i := 0; i < len(pairs); i += MaxPairsPerBatch {
			end := i + MaxPairsPerBatch
			if end > len(pairs) {
				end = len(pairs)
			}
			batch := pairs[i:end]

			// Build FactPair slice for batch detector
			factPairs := make([]FactPair, len(batch))
			for j, p := range batch {
				factPairs[j] = FactPair{
					TextA: p.factAText,
					DateA: p.factADate,
					TextB: p.factBText,
					DateB: p.factBDate,
				}
			}

			// Use concrete type assertion for batch method (not on interface)
			batchDetector, ok := l.Detector.(*OpenAIContradictionDetector)
			if !ok {
				// Fallback: call AreContradictory per pair
				for j, p := range batch {
					isContra, reason, derr := l.Detector.AreContradictory(ctx, p.factAText, p.factADate, p.factBText, p.factBDate)
					if derr != nil {
						log.Warn().Err(derr).Msg("detector error")
						continue
					}
					if isContra {
						if err := l.Storer.RecordContradictionImmediate(ctx, pid, newFactID, p.factBID, reason); err != nil {
							log.Error().Err(err).Int64("fact_a", newFactID).Int64("fact_b", p.factBID).Msg("Failed to record contradiction")
						} else {
							log.Info().Int64("fact_a", newFactID).Int64("fact_b", p.factBID).Int64("person_id", pid).Msg("Contradiction recorded and committed")
						}
					}
					_ = j
				}
				continue
			}

			results := batchDetector.AreContradictoryBatch(ctx, factPairs)
			if err != nil {
				log.Warn().Err(err).Int("batch_size", len(batch)).Msg("Batch contradiction detection error")
				continue
			}

			for j, br := range results {
				if br.IsContradictory {
					p := batch[j]
					if err := l.Storer.RecordContradictionImmediate(ctx, pid, newFactID, p.factBID, br.Reason); err != nil {
						log.Error().Err(err).Int64("fact_a", newFactID).Int64("fact_b", p.factBID).Msg("Failed to record contradiction")
					} else {
						log.Info().Int64("fact_a", newFactID).Int64("fact_b", p.factBID).Int64("person_id", pid).Msg("Contradiction recorded and committed")
					}
				}
			}
		}
	}

	return nil
}

func parseInt64(s string) (int64, error) { var v int64; _, err := fmt.Sscan(s, &v); return v, err }

func getEmbeddingForFact(ctx context.Context, tx pgx.Tx, factID int64) ([]float32, error) {
	var emb pgvector.Vector
	if err := tx.QueryRow(ctx, `SELECT embedding FROM facts WHERE id = $1`, factID).Scan(&emb); err != nil {
		return nil, fmt.Errorf("failed to scan embedding for fact %d: %w", factID, err)
	}
	return emb.Slice(), nil
}
