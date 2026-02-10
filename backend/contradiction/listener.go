package contradiction

import (
	"context"
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

	for _, pid := range pids {
		neighbors, err := l.Storer.FindNeighborFactsForPerson(ctx, tx, pid, emb, newFactID)
		if err != nil {
			return err
		}
		for _, nb := range neighbors {
			// Walk UP the implication chain to find the most general ancestor
			targetID := nb.ID
			targetText := nb.Text
			targetDate := nb.PublishedAt

			ancestorID, err := l.Storer.GetMostGeneralAncestor(ctx, nb.ID)
			if err != nil {
				log.Warn().Err(err).Int64("fact_id", nb.ID).Msg("Failed to get ancestor, using original fact")
			} else if ancestorID != nb.ID {
				// Fetch ancestor text and date for comparison
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

			// Check for contradiction with the (possibly walked-up) fact
			isContra, reason, derr := l.Detector.AreContradictory(ctx, newText, newDate, targetText, targetDate)
			if derr != nil {
				log.Warn().Err(derr).Msg("detector error")
				continue
			}
			if isContra {
				if err := l.Storer.RecordContradictionImmediate(ctx, pid, newFactID, targetID, reason); err != nil {
					log.Error().Err(err).Int64("fact_a", newFactID).Int64("fact_b", targetID).Msg("Failed to record contradiction")
				} else {
					log.Info().Int64("fact_a", newFactID).Int64("fact_b", targetID).Int64("person_id", pid).Msg("Contradiction recorded and committed")
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
