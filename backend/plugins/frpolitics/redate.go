package frpolitics

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// The ballots stored before voteDateSuffix (review of 2026-11-01) carry no
// date: two texts of one title voted a year apart read as one text. The
// ballots stored before buildVoteStatement worded the abstention without a
// participle say « s'est abstenu », wrong for every woman deputy (audit of
// 2026-10-31). RedateBallots gives the stored ballots the wording the current
// code writes, from the stored scrutin date: no LLM, no new fetch.

// oldAbstention is the abstention wording of the ballots stored before
// 2026-10-31: individual and collective, with or without the feminine.
var oldAbstention = regexp.MustCompile(`\bs'est (collectivement )?abstenue? (lors du vote sur|sur) `)

// redateBallotText is the stored ballot text as the current code would write
// it for a scrutin voted on voteDate: the abstention worded as
// buildVoteStatement / buildCollectiveVoteStatement do, then the date suffix
// of voteDateSuffix, added once (a text that already names the date, as a
// censure motion does, gets none).
func redateBallotText(text string, voteDate time.Time) string {
	out := oldAbstention.ReplaceAllStringFunc(text, func(m string) string {
		if strings.Contains(m, "collectivement") {
			return "a collectivement choisi l'abstention lors du vote sur "
		}
		return "a choisi l'abstention lors du vote sur "
	})
	if strings.Contains(out, "(scrutin du ") {
		return out
	}
	date := frenchDate(voteDate)
	if strings.Contains(out, date) {
		return out
	}
	return strings.TrimRight(out, " ") + " (scrutin du " + date + ")"
}

// RedateResult counts what RedateBallots found and wrote.
type RedateResult struct {
	Ballots    int // ballots read
	Rewritten  int // ballots whose text changes
	Suffixed   int // ballots given the date suffix
	Abstention int // ballots whose abstention wording was fixed
	Written    int // ballots written (0 in a dry run)
	Examples   []RedateExample
}

// RedateExample is one rewrite, for the dry-run report.
type RedateExample struct {
	ID       int64
	Old, New string
}

const redateBatch = 1000

// RedateBallots rewrites every stored ballot (assemblee_law_page fact) whose
// text differs from what redateBallotText makes of it, in batches of
// redateBatch rows, one transaction per batch. With dryRun nothing is
// written: it counts and keeps 10 examples. A rewritten ballot loses its
// English text (fact_text_en), as the other ballot repairs do; its embedding
// text is the voted object alone and does not change.
func RedateBallots(ctx context.Context, store *Store, dryRun bool) (RedateResult, error) {
	var res RedateResult
	var after int64
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		rows, err := store.pool.Query(ctx, `
			SELECT f.id, f.fact_text, MIN(fs.published_at)
			FROM facts f JOIN fact_sources fs ON fs.fact_id = f.id
			WHERE fs.source_type = 'assemblee_law_page' AND f.id > $1 AND fs.published_at IS NOT NULL
			GROUP BY f.id, f.fact_text
			ORDER BY f.id
			LIMIT $2`, after, redateBatch)
		if err != nil {
			return res, fmt.Errorf("listing the ballots after %d: %w", after, err)
		}
		var ids []int64
		var texts []string
		n := 0
		for rows.Next() {
			var id int64
			var text string
			var at time.Time
			if err := rows.Scan(&id, &text, &at); err != nil {
				rows.Close()
				return res, fmt.Errorf("scanning a ballot: %w", err)
			}
			n++
			after = id
			// The scrutin date is stored at midnight UTC (ParseScrutin reads a
			// date-only dateScrutin as UTC), as frenchDate reads it.
			fresh := redateBallotText(text, at.UTC())
			if fresh == text {
				continue
			}
			res.Rewritten++
			if oldAbstention.MatchString(text) {
				res.Abstention++
			}
			if !strings.Contains(text, "(scrutin du ") && strings.Contains(fresh, "(scrutin du ") {
				res.Suffixed++
			}
			if len(res.Examples) < 10 {
				res.Examples = append(res.Examples, RedateExample{ID: id, Old: text, New: fresh})
			}
			ids = append(ids, id)
			texts = append(texts, fresh)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, fmt.Errorf("listing the ballots after %d: %w", after, err)
		}
		res.Ballots += n
		if !dryRun && len(ids) > 0 {
			written, err := store.rewriteBallotTexts(ctx, ids, texts)
			if err != nil {
				return res, err
			}
			res.Written += written
			log.Info().Int64("up_to_id", after).Int("written", written).Int("total", res.Written).Msg("frpolitics redate: batch written")
		}
		if n < redateBatch {
			return res, nil
		}
	}
}

// rewriteBallotTexts writes the new texts of the ballots ids in one
// transaction.
func (s *Store) rewriteBallotTexts(ctx context.Context, ids []int64, texts []string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("rewriting %d ballots: %w", len(ids), err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE facts f
		SET fact_text = v.text, fact_text_en = NULL, updated_at = now()
		FROM unnest($1::bigint[], $2::text[]) AS v(id, text)
		WHERE f.id = v.id AND f.fact_text <> v.text`, ids, texts)
	if err != nil {
		return 0, fmt.Errorf("rewriting %d ballots: %w", len(ids), err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("rewriting %d ballots: %w", len(ids), err)
	}
	return int(tag.RowsAffected()), nil
}
