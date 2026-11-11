package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Report kinds and statuses (user_reports).
const (
	ReportKindFact          = "fact"
	ReportKindContradiction = "contradiction"

	ReportPending  = "pending"  // the AI has not read it yet
	ReportReview   = "review"   // the AI gave its advice, a person decides
	ReportAccepted = "accepted" // a person accepted it: the target was removed
	ReportRejected = "rejected" // a person refused it: the target stays
	ReportStale    = "stale"    // the target was already gone
)

// ErrAlreadyReported is returned when the user already reported the target.
var ErrAlreadyReported = errors.New("already reported")

// ErrReportTarget is returned when the reported fact or contradiction does
// not exist.
var ErrReportTarget = errors.New("no such fact or contradiction")

// ErrTooManyReports is returned when the user has too many reports waiting
// for the judge.
var ErrTooManyReports = errors.New("too many reports waiting")

// MaxPendingReportsPerUser caps the reports of one user that wait for the
// judge: each costs an LLM call.
const MaxPendingReportsPerUser = 20

// UserReport is one row of user_reports.
type UserReport struct {
	ID            int64  `json:"id"`
	TargetKind    string `json:"target_kind"`
	TargetID      int64  `json:"target_id"`
	PersonID int64 `json:"person_id"`
	// PersonSlug names the person's page (/person/<slug>); nil once the
	// person is gone (the report keeps their name).
	PersonSlug    *string `json:"person_slug,omitempty"`
	PersonName    string  `json:"person_name"`
	TargetText    string  `json:"target_text"`
	Reason        string  `json:"reason"`
	Status        string `json:"status"`
	VerdictReason string `json:"verdict_reason"`
	// AIVerdict is the judge's advice (delete_target, dismiss_complaint,
	// ambiguous), nil before it judged or when it failed.
	AIVerdict *string `json:"ai_verdict,omitempty"`
	// Enabled is the person's decision: true accepted (the target was
	// removed), false refused, nil not decided yet.
	Enabled   *bool      `json:"enabled"`
	Attempts  int        `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
	JudgedAt  *time.Time `json:"judged_at,omitempty"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
}

const userReportColumns = `id, target_kind, target_id, person_id, person_name, target_text, reason, status, verdict_reason, ai_verdict, enabled, attempts, created_at, judged_at, decided_at`

// scanUserReport scans userReportColumns into r, then the columns a query
// adds after them into extra.
func scanUserReport(row pgx.Row, r *UserReport, extra ...any) error {
	dest := append([]any{&r.ID, &r.TargetKind, &r.TargetID, &r.PersonID, &r.PersonName, &r.TargetText,
		&r.Reason, &r.Status, &r.VerdictReason, &r.AIVerdict, &r.Enabled, &r.Attempts, &r.CreatedAt, &r.JudgedAt, &r.DecidedAt}, extra...)
	return row.Scan(dest...)
}

func collectUserReports(rows pgx.Rows, err error) ([]UserReport, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserReport{}
	for rows.Next() {
		var r UserReport
		if err := scanUserReport(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CreateUserReport records the user's report on a fact or a contradiction,
// with the target's text and person as they are now. It returns
// ErrReportTarget when the target does not exist, ErrAlreadyReported when
// the user reported it before, and ErrTooManyReports when the user has
// MaxPendingReportsPerUser reports waiting.
func (s *PostgresStorer) CreateUserReport(ctx context.Context, userID int64, kind string, targetID int64, reason string) (*UserReport, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin report: %w", err)
	}
	defer tx.Rollback(ctx)

	// A lock per user serializes their reports, so two tabs cannot pass the
	// cap (an advisory lock: the accounts' table is not the mirror's).
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('user_reports:' || $1::bigint::text, 0))`, userID); err != nil {
		return nil, fmt.Errorf("lock reports of user %d: %w", userID, err)
	}
	var pending int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM user_reports WHERE user_id = $1 AND status = 'pending'`, userID).Scan(&pending); err != nil {
		return nil, fmt.Errorf("count pending reports: %w", err)
	}
	if pending >= MaxPendingReportsPerUser {
		return nil, ErrTooManyReports
	}

	var personID int64
	var personName, text string
	switch kind {
	case ReportKindContradiction:
		err = tx.QueryRow(ctx, `
			SELECT c.person_id, p.name, fa.fact_text || ' ⟷ ' || fb.fact_text
			FROM contradictions c
			JOIN persons p ON p.id = c.person_id
			JOIN facts fa ON fa.id = c.fact_a_id
			JOIN facts fb ON fb.id = c.fact_b_id
			WHERE c.id = $1`, targetID).Scan(&personID, &personName, &text)
	case ReportKindFact:
		err = tx.QueryRow(ctx, `
			SELECT p.id, p.name, f.fact_text
			FROM facts f
			JOIN LATERAL (
				SELECT fm.person_id FROM fact_sources fs
				JOIN fact_mentions fm ON fm.fact_source_id = fs.id
				WHERE fs.fact_id = f.id ORDER BY fm.id LIMIT 1
			) m ON true
			JOIN persons p ON p.id = m.person_id
			WHERE f.id = $1`, targetID).Scan(&personID, &personName, &text)
	default:
		return nil, fmt.Errorf("report kind %q: %w", kind, ErrReportTarget)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrReportTarget
	}
	if err != nil {
		return nil, fmt.Errorf("load reported %s %d: %w", kind, targetID, err)
	}

	var r UserReport
	err = scanUserReport(tx.QueryRow(ctx, `
		INSERT INTO user_reports (user_id, target_kind, target_id, person_id, person_name, target_text, reason)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+userReportColumns, userID, kind, targetID, personID, personName, text, reason), &r)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrAlreadyReported
		}
		return nil, fmt.Errorf("insert report: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit report: %w", err)
	}
	return &r, nil
}

// GetUserReports lists the user's reports, newest first.
func (s *PostgresStorer) GetUserReports(ctx context.Context, userID int64) ([]UserReport, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+userReportColumns+`, (SELECT slug FROM persons p WHERE p.id = user_reports.person_id)
		FROM user_reports WHERE user_id = $1 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("reports of user %d: %w", userID, err)
	}
	defer rows.Close()
	out := []UserReport{}
	for rows.Next() {
		var r UserReport
		if err := scanUserReport(rows, &r, &r.PersonSlug); err != nil {
			return nil, fmt.Errorf("scan report of user %d: %w", userID, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reports of user %d: %w", userID, err)
	}
	return out, nil
}

// GetUserReportedTargets returns the targets the user reported, as
// "kind:id" keys, so the page can show them as reported.
func (s *PostgresStorer) GetUserReportedTargets(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT target_kind || ':' || target_id FROM user_reports WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("reported targets of user %d: %w", userID, err)
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("reported targets of user %d: %w", userID, err)
	}
	if keys == nil {
		keys = []string{}
	}
	return keys, nil
}

// ListPendingUserReports returns the oldest pending reports, at most limit.
func (s *PostgresStorer) ListPendingUserReports(ctx context.Context, limit int) ([]UserReport, error) {
	out, err := collectUserReports(s.pool.Query(ctx, `
		SELECT `+userReportColumns+` FROM user_reports WHERE status = 'pending'
		ORDER BY created_at, id LIMIT $1`, limit))
	if err != nil {
		return nil, fmt.Errorf("pending reports: %w", err)
	}
	return out, nil
}

// ListUserReports returns the reports of a status, newest first (status "":
// every status).
func (s *PostgresStorer) ListUserReports(ctx context.Context, status string, limit int) ([]UserReport, error) {
	out, err := collectUserReports(s.pool.Query(ctx, `
		SELECT `+userReportColumns+` FROM user_reports WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC, id DESC LIMIT $2`, status, limit))
	if err != nil {
		return nil, fmt.Errorf("reports: %w", err)
	}
	return out, nil
}

// GetUserReport returns one report (pgx.ErrNoRows when there is none).
func (s *PostgresStorer) GetUserReport(ctx context.Context, id int64) (*UserReport, error) {
	var r UserReport
	if err := scanUserReport(s.pool.QueryRow(ctx, `SELECT `+userReportColumns+` FROM user_reports WHERE id = $1`, id), &r); err != nil {
		return nil, fmt.Errorf("report %d: %w", id, err)
	}
	return &r, nil
}

// RecordUserReportVerdict records the judge's advice on a report: status is
// review (a person decides) or stale (the target was already gone).
// aiVerdict is empty when the judge gave none.
func (s *PostgresStorer) RecordUserReportVerdict(ctx context.Context, id int64, status, aiVerdict, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE user_reports SET status = $2, ai_verdict = NULLIF($3, ''), verdict_reason = $4, judged_at = now()
		WHERE id = $1`, id, status, aiVerdict, reason)
	if err != nil {
		return fmt.Errorf("record verdict of report %d: %w", id, err)
	}
	return nil
}

// DecideUserReport records a person's decision on a report: accepted (its
// target was removed), rejected, or stale (the target was gone when it was
// accepted). A reason replaces the judge's in the public log. An accepted
// report takes the other undecided reports on the same target with it.
func (s *PostgresStorer) DecideUserReport(ctx context.Context, id int64, status, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin report decision: %w", err)
	}
	defer tx.Rollback(ctx)
	var kind string
	var targetID int64
	err = tx.QueryRow(ctx, `
		UPDATE user_reports SET status = $2,
			enabled = CASE $2 WHEN 'accepted' THEN true WHEN 'rejected' THEN false END,
			verdict_reason = COALESCE(NULLIF($3, ''), verdict_reason),
			decided_at = now()
		WHERE id = $1 RETURNING target_kind, target_id`, id, status, reason).Scan(&kind, &targetID)
	if err != nil {
		return fmt.Errorf("decide report %d: %w", id, err)
	}
	if status == ReportAccepted || status == ReportStale {
		if _, err := tx.Exec(ctx, `
			UPDATE user_reports SET status = $3, enabled = CASE $3 WHEN 'accepted' THEN true END,
				verdict_reason = (SELECT verdict_reason FROM user_reports WHERE id = $4), decided_at = now()
			WHERE target_kind = $1 AND target_id = $2 AND status IN ('pending', 'review')`, kind, targetID, status, id); err != nil {
			return fmt.Errorf("close the other reports on %s %d: %w", kind, targetID, err)
		}
	}
	return tx.Commit(ctx)
}

// CountUserReportAttempt counts a failed judging of the report; past
// maxAttempts the report goes to review without the judge's advice.
func (s *PostgresStorer) CountUserReportAttempt(ctx context.Context, id int64, lastError string, maxAttempts int) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE user_reports SET attempts = attempts + 1,
			status = CASE WHEN attempts + 1 >= $3 THEN 'review' ELSE status END,
			verdict_reason = CASE WHEN attempts + 1 >= $3 THEN 'judge failed: ' || $2 ELSE verdict_reason END,
			judged_at = CASE WHEN attempts + 1 >= $3 THEN now() ELSE judged_at END
		WHERE id = $1`, id, lastError, maxAttempts)
	if err != nil {
		return fmt.Errorf("count attempt of report %d: %w", id, err)
	}
	return nil
}

// Correction is one accepted report in the public log: what was removed,
// the reason, and when. Neither the reporter nor their own words
// are shown.
type Correction struct {
	ID            int64     `json:"id"`
	TargetKind    string    `json:"target_kind"`
	PersonID int64 `json:"person_id"`
	// PersonSlug names the person's page (/person/<slug>); nil once the
	// person is gone.
	PersonSlug    *string   `json:"person_slug,omitempty"`
	PersonName    string    `json:"person_name"`
	TargetText    string    `json:"target_text"`
	VerdictReason string    `json:"verdict_reason"`
	ReportedAt    time.Time `json:"reported_at"`
	CorrectedAt   time.Time `json:"corrected_at"`
}

// ListCorrections returns the accepted reports, one per target (its first
// report), newest report first, before the given id (0: from the newest).
func (s *PostgresStorer) ListCorrections(ctx context.Context, beforeID int64, limit int) ([]Correction, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, target_kind, person_id, (SELECT slug FROM persons p WHERE p.id = firsts.person_id), person_name,
		       target_text, verdict_reason, created_at, COALESCE(decided_at, judged_at)
		FROM (
			SELECT DISTINCT ON (target_kind, target_id) *
			FROM user_reports WHERE status = 'accepted'
			ORDER BY target_kind, target_id, created_at
		) firsts
		WHERE $1 = 0 OR id < $1
		ORDER BY id DESC LIMIT $2`, beforeID, limit)
	if err != nil {
		return nil, fmt.Errorf("corrections: %w", err)
	}
	defer rows.Close()
	out := []Correction{}
	for rows.Next() {
		var c Correction
		if err := rows.Scan(&c.ID, &c.TargetKind, &c.PersonID, &c.PersonSlug, &c.PersonName, &c.TargetText,
			&c.VerdictReason, &c.ReportedAt, &c.CorrectedAt); err != nil {
			return nil, fmt.Errorf("scan correction: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
