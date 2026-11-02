package frpolitics

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pressSourceTypes are the sources whose facts the débats backfill wants to
// meet: transcripts of persons who also speak to the press are where "said in
// the hemicycle vs said to the press" contradictions live.
var pressSourceTypes = []string{"news", "twitter"}

func init() {
	core.RequireQuoteForSourceType(debatsSourceType)
}

// Store is the plugin's own database access: the tables only frpolitics reads
// and writes (processed_laws, skipped_laws) and the person lookups keyed by
// Assemblée actor id. Transcript documents live in the shared document state
// machine (source_documents).
type Store struct{ pool *pgxpool.Pool }

// NewStore binds a plugin store to the shared connection pool.
func NewStore(s *storage.PostgresStorer) *Store { return &Store{pool: s.Pool()} }

// SettledLawKeys returns which of keys are settled: fully processed (all
// facts stored) or skipped for a verdict on the law itself whose skip_reason
// is one of skipReasons ('keyword_match', 'low_ai_confidence'); a run with a
// filter disabled leaves that filter's verdicts out. Settled laws never
// re-enter a run.
func (s *Store) SettledLawKeys(ctx context.Context, keys, skipReasons []string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT law_key FROM processed_laws WHERE law_key = ANY($1)
		UNION
		SELECT law_key FROM skipped_laws WHERE law_key = ANY($1) AND skip_reason = ANY($2)`, keys, skipReasons)
	if err != nil {
		return nil, fmt.Errorf("failed to load settled laws: %w", err)
	}
	defer rows.Close()
	settled := make(map[string]bool)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("failed to scan settled law: %w", err)
		}
		settled[key] = true
	}
	return settled, rows.Err()
}

// MarkLawAsProcessed records that a law has been fully processed (all facts stored).
// This is the "commit" marker that prevents reprocessing the law on retry.
func (s *Store) MarkLawAsProcessed(ctx context.Context, lawKey string, legislature int, title string, factsCount int) error {
	query := `
		INSERT INTO processed_laws (law_key, legislature, title, facts_count)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (law_key) DO UPDATE
		SET processed_at = CURRENT_TIMESTAMP,
		    facts_count = EXCLUDED.facts_count
	`
	_, err := s.pool.Exec(ctx, query, lawKey, legislature, title, factsCount)
	if err != nil {
		return fmt.Errorf("failed to mark law %s as processed: %w", lawKey, err)
	}
	return nil
}

// StoredLawSubjects returns which of subjects already have a ballot stored
// for a law. A law whose store failed part-way resumes after them: every
// ballot is its own fact, one per voter (deputy or party), and each insert is
// atomic, so the ballots already stored are skipped rather than deleted and
// redone. The key is the voter, not the fact text: the text embeds the LLM
// summary of the law, which a retry regenerates differently. A subject is
// matched the way the inserter resolves it to a person
// (util.NormalizePersonName, then the accent- and case-folded name).
func (s *Store) StoredLawSubjects(ctx context.Context, lawKey string, subjects []string) (map[string]bool, error) {
	normalized := make([]string, len(subjects))
	for i, name := range subjects {
		normalized[i] = util.NormalizePersonName(name)
	}
	rows, err := s.pool.Query(ctx, `
		WITH stored AS (
			SELECT DISTINCT lower(immutable_unaccent(p.name)) AS name
			FROM fact_sources fs
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			JOIN persons p ON p.id = fm.person_id
			WHERE fs.source_type = 'assemblee_law_page' AND fs.metadata->>'law_key' = $1
		)
		SELECT n.idx
		FROM unnest($2::text[]) WITH ORDINALITY AS n(name, idx)
		JOIN stored ON stored.name = lower(immutable_unaccent(n.name))`,
		lawKey, normalized)
	if err != nil {
		return nil, fmt.Errorf("failed to load stored ballots for law %s: %w", lawKey, err)
	}
	defer rows.Close()
	stored := make(map[string]bool)
	for rows.Next() {
		var idx int
		if err := rows.Scan(&idx); err != nil {
			return nil, fmt.Errorf("failed to scan stored ballot for law %s: %w", lawKey, err)
		}
		stored[subjects[idx-1]] = true
	}
	return stored, rows.Err()
}

// StoreSkippedLaw stores information about a law that was skipped during processing
func (s *Store) StoreSkippedLaw(ctx context.Context, lawKey, title string, legislature int, voteDate *time.Time, skipReason, matchedPattern string, aiConfidence *float64) error {
	query := `
		INSERT INTO skipped_laws (law_key, title, legislature, vote_date, skip_reason, matched_pattern, ai_confidence)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (law_key) DO NOTHING`

	_, err := s.pool.Exec(ctx, query, lawKey, title, legislature, voteDate, skipReason, matchedPattern, aiConfidence)
	if err != nil {
		return fmt.Errorf("failed to store skipped law %s: %w", lawKey, err)
	}
	return nil
}

// GetPersonsByAssembleeID returns every person carrying an Assemblée actor id
// (attributes.assemblee_id, "PA..."), keyed by that id. The débats ingestion
// joins transcript paragraphs to persons on this id — no name matching. When
// two rows carry the same id (un-merged duplicates), the one with more facts
// wins, then the lowest id, so the choice is deterministic.
func (s *Store) GetPersonsByAssembleeID(ctx context.Context) (map[string]storage.PersonInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (attributes->>'assemblee_id')
		       id, name, COALESCE(entity_type, 'human'), attributes->>'assemblee_id'
		FROM persons
		WHERE attributes ? 'assemblee_id'
		ORDER BY attributes->>'assemblee_id', fact_count DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("querying persons by assemblee_id: %w", err)
	}
	defer rows.Close()
	out := map[string]storage.PersonInfo{}
	for rows.Next() {
		var p storage.PersonInfo
		var aid string
		if err := rows.Scan(&p.ID, &p.Name, &p.EntityType, &aid); err != nil {
			return nil, fmt.Errorf("scanning person by assemblee_id: %w", err)
		}
		if aid != "" {
			out[aid] = p
		}
	}
	return out, rows.Err()
}

// StoredVoteSubject returns the vote subject already stored on a ballot of
// scope, the oldest one, or "" when no ballot of scope has one.
func (s *Store) StoredVoteSubject(ctx context.Context, scope subjectScope) (string, error) {
	query := `
		SELECT metadata->>'vote_subject' FROM fact_sources
		WHERE source_type = 'assemblee_law_page' AND source_url = $1 AND metadata->>'vote_subject' <> ''
		ORDER BY id LIMIT 1`
	if scope.byScrutin {
		query = `
		SELECT metadata->>'vote_subject' FROM fact_sources
		WHERE source_type = 'assemblee_law_page' AND metadata->>'law_key' = $1 AND metadata->>'vote_subject' <> ''
		ORDER BY id LIMIT 1`
	}
	var subject string
	err := s.pool.QueryRow(ctx, query, scope.key).Scan(&subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("loading the stored vote subject of %s: %w", scope.key, err)
	}
	return subject, nil
}

// VoteSubjectStances returns, per person, the stance that holds the person's
// ballots with the vote subject subject (the vote_subject of the source
// metadata). When the ballots of a person are in more than one stance, the
// stance with the most of them wins, then the lowest id.
func (s *Store) VoteSubjectStances(ctx context.Context, subject string) (map[int64]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (sd.person_id) sd.person_id, sd.id
		FROM fact_sources fs
		JOIN stance_document_facts sdf ON sdf.fact_id = fs.fact_id
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		WHERE fs.source_type = 'assemblee_law_page' AND fs.metadata->>'vote_subject' = $1
		GROUP BY sd.person_id, sd.id
		ORDER BY sd.person_id, count(*) DESC, sd.id`, subject)
	if err != nil {
		return nil, fmt.Errorf("loading the stances of vote subject %q: %w", subject, err)
	}
	defer rows.Close()
	stances := make(map[int64]int64)
	for rows.Next() {
		var personID, stanceID int64
		if err := rows.Scan(&personID, &stanceID); err != nil {
			return nil, fmt.Errorf("scanning the stance of vote subject %q: %w", subject, err)
		}
		stances[personID] = stanceID
	}
	return stances, rows.Err()
}

// StoredArticleStatement returns the statement of article in text texte_num
// of the legislature, when the model stated it before.
func (s *Store) StoredArticleStatement(ctx context.Context, legislature, texteNum int, article string) (LawSummary, bool, error) {
	var summary LawSummary
	err := s.pool.QueryRow(ctx, `
		SELECT statement, confidence FROM vote_articles
		WHERE legislature = $1 AND texte_num = $2 AND article = $3`, legislature, texteNum, article).Scan(&summary.SimpleStatement, &summary.Confidence)
	if errors.Is(err, pgx.ErrNoRows) {
		return LawSummary{}, false, nil
	}
	if err != nil {
		return LawSummary{}, false, fmt.Errorf("loading the statement of %s of text %d: %w", article, texteNum, err)
	}
	return summary, true, nil
}

// StoreArticleStatement keeps the statement of an article.
func (s *Store) StoreArticleStatement(ctx context.Context, legislature, texteNum int, article string, summary LawSummary) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO vote_articles (legislature, texte_num, article, statement, confidence)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (legislature, texte_num, article) DO UPDATE SET statement = EXCLUDED.statement, confidence = EXCLUDED.confidence`,
		legislature, texteNum, article, summary.SimpleStatement, summary.Confidence)
	if err != nil {
		return fmt.Errorf("storing the statement of %s of text %d: %w", article, texteNum, err)
	}
	return nil
}

// BallotsNamingArticle counts the ballots of the scrutin lawKey whose text
// names the article alone ("l'article 7 du texte de loi : « ... »"). The
// scrutin is the law_key of the source: the source URL is the dossier's,
// shared by every scrutin of the bill.
func (s *Store) BallotsNamingArticle(ctx context.Context, lawKey, article string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		WHERE fs.metadata->>'law_key' = $1 AND f.fact_text LIKE '% ' || $2 || ' du texte de loi : «%'`, lawKey, article).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting the ballots of %s naming %s: %w", lawKey, article, err)
	}
	return n, nil
}

// RearticleBallots rewrites the ballots of the scrutin lawKey that name the
// article alone with object, the article and what it does, and returns the
// ids of the facts it rewrote. The English text is reset: the translation
// repair fills it again from the new text.
func (s *Store) RearticleBallots(ctx context.Context, lawKey, article, object string) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE facts f
		SET fact_text = replace(f.fact_text, ' ' || $2 || ' du texte de loi : «', ' ' || $3 || ' du texte de loi : «'),
		    fact_text_en = NULL, updated_at = now()
		FROM fact_sources fs
		WHERE fs.fact_id = f.id AND fs.metadata->>'law_key' = $1 AND f.fact_text LIKE '% ' || $2 || ' du texte de loi : «%'
		RETURNING f.id`, lawKey, article, object)
	if err != nil {
		return nil, fmt.Errorf("rewriting the ballots of %s: %w", lawKey, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("rewriting the ballots of %s: %w", lawKey, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// TruncatedAmendmentRe matches, in a ballot, the amendment object whose
// quoted exposé was cut inside the guillemets ("dont l'objet est : « … ,
// déposé sur"): the object, from "l'amendement n° N" to the ellipsis, with
// the ", déposé sur" that follows (ReamendBallots puts it back). An exposé
// may quote words in « » of its own (« chaque fois que cela est possible »,
// 1,697 ballots left by the first run, review of 2026-11-01): a closed inner
// pair is part of the object, and the cut may fall inside one. The pattern
// is POSIX ARE and RE2 at once: the test compiles it with Go's regexp.
const TruncatedAmendmentRe = `(l'amendement|le sous-amendement) n° [0-9]+( \([^)]*\))? dont (l'objet|le dispositif) est : « ([^«»]|«[^«»]*»)*(«[^«»]*)?…, déposé sur`

// TruncatedAmendmentBallots counts the ballots of the scrutin lawKey whose
// amendment object lost its closing » (TruncatedAmendmentRe).
func (s *Store) TruncatedAmendmentBallots(ctx context.Context, lawKey string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		WHERE fs.metadata->>'law_key' = $1 AND f.fact_text ~ $2`, lawKey, TruncatedAmendmentRe).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting the truncated ballots of %s: %w", lawKey, err)
	}
	return n, nil
}

// ReamendBallots rewrites the truncated amendment object of the ballots of
// the scrutin lawKey with object (the amendment and its purpose, closed) and
// returns the ids of the facts it rewrote. The pair verdicts stay: the vote
// is the same, the quote is only completed. The English text is reset for
// the translation repair.
func (s *Store) ReamendBallots(ctx context.Context, lawKey, object string) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE facts f
		SET fact_text = regexp_replace(f.fact_text, $2, $3),
		    fact_text_en = NULL, updated_at = now()
		FROM fact_sources fs
		WHERE fs.fact_id = f.id AND fs.metadata->>'law_key' = $1 AND f.fact_text ~ $2
		RETURNING f.id`, lawKey, TruncatedAmendmentRe, strings.ReplaceAll(object, `\`, `\\`)+", déposé sur")
	if err != nil {
		return nil, fmt.Errorf("rewriting the truncated ballots of %s: %w", lawKey, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("rewriting the truncated ballots of %s: %w", lawKey, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RemotionBallots rewrites the ballots of the motion lawKey that name the
// bill phrase ("a voté CONTRE le texte de loi : « ... »") with the motion,
// and returns the ids of the facts it rewrote. The bill phrase ends the
// ballot text, the party ballots' too; with keepPurpose its quote (" : « ...
// »") stays after the motion. The English text is reset: the translation
// repair fills it again.
func (s *Store) RemotionBallots(ctx context.Context, lawKey, object string, keepPurpose bool) ([]int64, error) {
	replacement := " " + object
	if keepPurpose {
		replacement += `\1`
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE facts f
		SET fact_text = regexp_replace(f.fact_text, ' le texte de loi( : «.*»)?$', $2),
		    fact_text_en = NULL, updated_at = now()
		FROM fact_sources fs
		WHERE fs.fact_id = f.id AND fs.metadata->>'law_key' = $1 AND f.fact_text ~ ' le texte de loi( : «.*»)?$'
		RETURNING f.id`, lawKey, replacement)
	if err != nil {
		return nil, fmt.Errorf("rewriting the ballots of %s: %w", lawKey, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("rewriting the ballots of %s: %w", lawKey, err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ReopenPairsOf removes the contradictions on the facts and the verdicts of
// their pairs, so the detector judges the pairs again, and returns the
// persons whose contradictions changed with the number of contradictions
// removed.
func (s *Store) ReopenPairsOf(ctx context.Context, factIDs []int64) (persons []int64, removed int, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, fmt.Errorf("reopening pairs: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		DELETE FROM contradictions WHERE fact_a_id = ANY($1) OR fact_b_id = ANY($1) RETURNING person_id`, factIDs)
	if err != nil {
		return nil, 0, fmt.Errorf("reopening pairs: removing the contradictions: %w", err)
	}
	seen := map[int64]bool{}
	for rows.Next() {
		var pid int64
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			return nil, 0, fmt.Errorf("reopening pairs: %w", err)
		}
		removed++
		if !seen[pid] {
			seen[pid] = true
			persons = append(persons, pid)
		}
	}
	rows.Close()
	if _, err := tx.Exec(ctx, `DELETE FROM fact_pair_checks WHERE fact_a_id = ANY($1) OR fact_b_id = ANY($1)`, factIDs); err != nil {
		return nil, 0, fmt.Errorf("reopening pairs: removing the verdicts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, fmt.Errorf("reopening pairs: %w", err)
	}
	return persons, removed, nil
}

// StoredLawSummaries returns the summary already given to the facts of each
// law key that has facts: a law whose facts are built in two runs keeps one
// wording (scrutin 17-7980 had 301 facts with one summary and 246 with
// another, audit of 2026-10-31). A stored summary passed the AI filter, so
// its confidence is 1.
func (s *Store) StoredLawSummaries(ctx context.Context, keys []string) (map[string]LawSummary, error) {
	out := make(map[string]LawSummary, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (metadata->>'law_key') metadata->>'law_key', COALESCE(metadata->>'law_short_title', ''), metadata->>'law_summary'
		FROM fact_sources
		WHERE source_type = 'assemblee_law_page' AND metadata->>'law_key' = ANY($1)
		  AND COALESCE(metadata->>'law_summary', '') <> ''
		ORDER BY metadata->>'law_key', id`, keys)
	if err != nil {
		return nil, fmt.Errorf("loading the stored law summaries: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var sum LawSummary
		if err := rows.Scan(&key, &sum.ShortTitle, &sum.SimpleStatement); err != nil {
			return nil, fmt.Errorf("scanning a stored law summary: %w", err)
		}
		sum.Confidence = 1
		out[key] = sum
	}
	return out, rows.Err()
}
