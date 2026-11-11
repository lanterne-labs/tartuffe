package storage

import (
	"context"
	"digital-mirror/backend/core"
	"fmt"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reportFixture is one person with three facts in one stance, a
// contradiction between the first two, and a signed-in user.
type reportFixture struct {
	person, stance, contradiction, user int64
	facts                               []int64
}

func newReportFixture(t *testing.T, storer *PostgresStorer, factCount int) reportFixture {
	t.Helper()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE app_users, user_reports, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	var f reportFixture
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, fact_count) VALUES ('report person', 'human', $1) RETURNING id`, factCount).Scan(&f.person))
	require.NoError(t, storer.pool.QueryRow(ctx, `
		INSERT INTO stance_documents (person_id, cluster_label, cluster_embedding, summary, label_en)
		VALUES ($1, 'Retraites', $2, 'Sa position sur les retraites.', 'Pensions') RETURNING id`,
		f.person, pgvector.NewHalfVector(mockEmbedding())).Scan(&f.stance))
	for i, text := range []string{"Il faut la retraite à 60 ans.", "Il faut la retraite à 65 ans.", "Il a voté la réforme."} {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding, created_at) VALUES ($1, $2, $3) RETURNING id`,
			text, pgvector.NewHalfVector(mockEmbedding()), time.Date(2026, 10, 1, 12, i, 0, 0, time.UTC)).Scan(&id))
		_, err := storer.pool.Exec(ctx, `
			WITH src AS (
				INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type, published_at)
				VALUES ($1, $2, $3, 'test', $4) RETURNING id)
			INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $5 FROM src`,
			id, fmt.Sprintf("http://ex/%d", id), "« "+text+" »", time.Date(2020+i, 1, 1, 0, 0, 0, 0, time.UTC), f.person)
		require.NoError(t, err)
		_, err = storer.pool.Exec(ctx, `INSERT INTO stance_document_facts (stance_document_id, fact_id) VALUES ($1, $2)`, f.stance, id)
		require.NoError(t, err)
		f.facts = append(f.facts, id)
	}
	f.contradiction, _, err = storer.RecordContradictionImmediate(ctx, f.person, f.facts[0], f.facts[1], "60 ans puis 65 ans.", "60 then 65.")
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `UPDATE contradictions SET bundle_stance_id = $1, detected_at = $2 WHERE id = $3`,
		f.stance, time.Date(2026, 10, 1, 12, 1, 0, 0, time.UTC), f.contradiction)
	require.NoError(t, err)
	u, err := testUser(ctx, storer, "reporter")
	require.NoError(t, err)
	f.user = u
	return f
}

// testUser inserts an account row (the accounts' table, which the mirror
// only references) and returns its id.
func testUser(ctx context.Context, storer *PostgresStorer, subject string) (int64, error) {
	var id int64
	err := storer.pool.QueryRow(ctx, `INSERT INTO app_users (provider, subject) VALUES ('noop', $1) RETURNING id`, subject).Scan(&id)
	return id, err
}

// TestUserReports: a report keeps its target's text and person; a user
// reports a target once; an accepted report accepts the others on its
// target and enters the public log once.
func TestUserReports(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := newReportFixture(t, storer, 3)

	r, err := storer.CreateUserReport(ctx, f.user, ReportKindContradiction, f.contradiction, "Ce ne sont pas les mêmes années.")
	require.NoError(t, err)
	assert.Equal(t, ReportPending, r.Status)
	assert.Equal(t, f.person, r.PersonID)
	assert.Equal(t, "report person", r.PersonName)
	assert.Equal(t, "Il faut la retraite à 60 ans. ⟷ Il faut la retraite à 65 ans.", r.TargetText)

	_, err = storer.CreateUserReport(ctx, f.user, ReportKindContradiction, f.contradiction, "Encore une fois, c'est faux.")
	assert.ErrorIs(t, err, ErrAlreadyReported)
	_, err = storer.CreateUserReport(ctx, f.user, ReportKindContradiction, 999999, "Ce ne sont pas les mêmes années.")
	assert.ErrorIs(t, err, ErrReportTarget)

	fr, err := storer.CreateUserReport(ctx, f.user, ReportKindFact, f.facts[2], "Il a voté contre, pas pour.")
	require.NoError(t, err)
	assert.Equal(t, "Il a voté la réforme.", fr.TargetText)

	other, err := testUser(ctx, storer, "other")
	require.NoError(t, err)
	r2, err := storer.CreateUserReport(ctx, other, ReportKindContradiction, f.contradiction, "Deux contextes différents.")
	require.NoError(t, err)

	keys, err := storer.GetUserReportedTargets(ctx, f.user)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{fmt.Sprintf("contradiction:%d", f.contradiction), fmt.Sprintf("fact:%d", f.facts[2])}, keys)

	pending, err := storer.ListPendingUserReports(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	assert.Equal(t, r.ID, pending[0].ID, "oldest first")

	// The judge's verdict is advice: the report waits for a person, nothing
	// is public yet.
	require.NoError(t, storer.RecordUserReportVerdict(ctx, r.ID, ReportReview, "delete_target", "The two facts are years apart."))
	got, err := storer.GetUserReport(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, ReportReview, got.Status)
	require.NotNil(t, got.AIVerdict)
	assert.Equal(t, "delete_target", *got.AIVerdict)
	assert.Nil(t, got.Enabled, "not decided")
	corrections, err := storer.ListCorrections(ctx, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, corrections, "the judge publishes nothing")

	require.NoError(t, storer.DecideUserReport(ctx, r.ID, ReportAccepted, ""))
	mine, err := storer.GetUserReports(ctx, other)
	require.NoError(t, err)
	require.Len(t, mine, 1)
	assert.Equal(t, r2.ID, mine[0].ID)
	assert.Equal(t, ReportAccepted, mine[0].Status, "the other report on the target is accepted with it")
	require.NotNil(t, mine[0].Enabled)
	assert.True(t, *mine[0].Enabled)
	require.NoError(t, storer.RecordUserReportVerdict(ctx, fr.ID, ReportReview, "dismiss_complaint", "The source says he voted for."))
	require.NoError(t, storer.DecideUserReport(ctx, fr.ID, ReportRejected, ""))
	got, err = storer.GetUserReport(ctx, fr.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Enabled)
	assert.False(t, *got.Enabled)

	corrections, err = storer.ListCorrections(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, corrections, 1, "one correction per target")
	assert.Equal(t, r.ID, corrections[0].ID)
	assert.Equal(t, "The two facts are years apart.", corrections[0].VerdictReason)

	// Failed judgings: past the cap the report waits for a person, without
	// advice.
	require.NoError(t, storer.CountUserReportAttempt(ctx, fr.ID, "boom", 2))
	r3, err := storer.CreateUserReport(ctx, other, ReportKindFact, f.facts[2], "Il a voté contre, pas pour.")
	require.NoError(t, err)
	require.NoError(t, storer.CountUserReportAttempt(ctx, r3.ID, "boom", 2))
	require.NoError(t, storer.CountUserReportAttempt(ctx, r3.ID, "boom", 2))
	review, err := storer.ListUserReports(ctx, ReportReview, 10)
	require.NoError(t, err)
	require.Len(t, review, 1)
	assert.Equal(t, r3.ID, review[0].ID)
	assert.Equal(t, "judge failed: boom", review[0].VerdictReason)
	assert.Nil(t, review[0].AIVerdict)

	// Erasing the account keeps the report, without the user.
	_, err = storer.pool.Exec(ctx, `DELETE FROM app_users WHERE id = $1`, f.user)
	require.NoError(t, err)
	corrections, err = storer.ListCorrections(ctx, 0, 10)
	require.NoError(t, err)
	assert.Len(t, corrections, 1)
}

func TestUserReportCap(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := newReportFixture(t, storer, 3)
	for i := range MaxPendingReportsPerUser {
		u, err := testUser(ctx, storer, fmt.Sprintf("u%d", i))
		require.NoError(t, err)
		_, err = storer.CreateUserReport(ctx, u, ReportKindFact, f.facts[0], "Ce fait est mal attribué.")
		require.NoError(t, err, "other users are not capped by one user's reports")
	}
	_, err := storer.pool.Exec(ctx, `
		INSERT INTO user_reports (user_id, target_kind, target_id, person_id, person_name, target_text, reason)
		SELECT $1, 'fact', g, $2, 'x', 'x', 'Ce fait est mal attribué.' FROM generate_series(1000, 999 + $3) g`,
		f.user, f.person, MaxPendingReportsPerUser)
	require.NoError(t, err)
	_, err = storer.CreateUserReport(ctx, f.user, ReportKindFact, f.facts[0], "Ce fait est mal attribué.")
	assert.ErrorIs(t, err, ErrTooManyReports)
}

// TestFeed: contradictions and facts interleave newest first, page by the
// cursor without gaps or repeats, and skip the persons under the floor.
func TestFeed(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := newReportFixture(t, storer, 3)
	// Recording the contradiction recounted the facts: lift the person to the floor.
	_, err := storer.pool.Exec(ctx, `UPDATE persons SET fact_count = 4 WHERE id = $1`, f.person)
	require.NoError(t, err)

	var all []FeedItem
	var after *FeedCursor
	for {
		page, err := storer.GetFeed(ctx, FeedFilter{}, after, 2, 4)
		require.NoError(t, err)
		all = append(all, page...)
		if len(page) < 2 {
			break
		}
		last := page[len(page)-1]
		after = &FeedCursor{At: last.At, Kind: last.Kind, ID: last.ID}
	}
	var got []string
	for _, it := range all {
		got = append(got, fmt.Sprintf("%s:%d", it.Kind, it.ID))
	}
	// facts at 12:00, 12:01, 12:02; the contradiction detected at 12:01.
	assert.Equal(t, []string{
		fmt.Sprintf("fact:%d", f.facts[2]),
		fmt.Sprintf("fact:%d", f.facts[1]),
		fmt.Sprintf("contradiction:%d", f.contradiction),
		fmt.Sprintf("fact:%d", f.facts[0]),
	}, got)

	c := all[2]
	// The contradiction costs its person's score today; a fact among so few
	// does not move it (three facts expect no contradiction to speak of).
	require.NotNil(t, c.ScoreEffect)
	rate, err := storer.ScoreRate(ctx)
	require.NoError(t, err)
	want := 100 * (core.ComputeHonestyScore(3, core.ContradictionStats{Bundles: 1, Total: 1}, rate) - 1)
	assert.InDelta(t, want, *c.ScoreEffect, 1e-9)
	assert.Less(t, *c.ScoreEffect, -1.0)
	assert.Nil(t, all[0].ScoreEffect)
	assert.Equal(t, "report person", c.Person.Name)
	require.NotNil(t, c.StanceLabel)
	assert.Equal(t, "Retraites", *c.StanceLabel)
	assert.Equal(t, "Il faut la retraite à 60 ans.", c.Fact.Text, "the earlier fact first")
	require.NotNil(t, c.Other)
	assert.Equal(t, "Il faut la retraite à 65 ans.", c.Other.Text)
	assert.Equal(t, []string{fmt.Sprintf("http://ex/%d", f.facts[0])}, c.Fact.SourceURLs)
	assert.Equal(t, "60 ans puis 65 ans.", deref(c.Reason))

	only, err := storer.GetFeed(ctx, FeedFilter{Kind: FeedContradiction}, nil, 10, 4)
	require.NoError(t, err)
	assert.Len(t, only, 1)

	// One person, and the statements of a period: facts 2021-2022, the
	// contradiction by its later fact (2021).
	mine, err := storer.GetFeed(ctx, FeedFilter{PersonID: f.person}, nil, 10, 4)
	require.NoError(t, err)
	assert.Len(t, mine, 4)
	nobody, err := storer.GetFeed(ctx, FeedFilter{PersonID: f.person + 1}, nil, 10, 4)
	require.NoError(t, err)
	assert.Empty(t, nobody)
	from, to := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	period, err := storer.GetFeed(ctx, FeedFilter{From: &from, To: &to}, nil, 10, 4)
	require.NoError(t, err)
	var keys []string
	for _, it := range period {
		keys = append(keys, fmt.Sprintf("%s:%d", it.Kind, it.ID))
	}
	assert.ElementsMatch(t, []string{fmt.Sprintf("fact:%d", f.facts[1]), fmt.Sprintf("fact:%d", f.facts[2]), fmt.Sprintf("contradiction:%d", f.contradiction)}, keys)

	hidden, err := storer.GetFeed(ctx, FeedFilter{}, nil, 10, 5)
	require.NoError(t, err)
	assert.Empty(t, hidden, "a person under the floor is not in the feed")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// TestScoreBaseline: the baseline measures the candidates only.
func TestScoreBaseline(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := newReportFixture(t, storer, 3)

	base, err := storer.ScoreBaseline(ctx)
	require.NoError(t, err)
	assert.Zero(t, base.Rate, "no candidate yet")

	_, err = storer.pool.Exec(ctx, `UPDATE persons SET fact_count = 4000, honesty_score = 0.97, attributes = COALESCE(attributes, '{}'::jsonb) || '{"candidate": true}' WHERE id = $1`, f.person)
	require.NoError(t, err)
	base, err = storer.ScoreBaseline(ctx)
	require.NoError(t, err)
	assert.InDelta(t, 0.00025, base.Rate, 1e-9)
	assert.InDelta(t, 12000, base.CoveredFacts, 1e-6)
	assert.Nil(t, base.FloorHonesty, "4000 facts: not covered")

	// The only candidate is covered once 3 contradictions are expected at
	// the rate its own facts set: with 3 bundles over 4000 facts, the rate
	// is 3/4000 and 4000 facts cover. (More facts alone never cover a single
	// candidate: the rate falls with them.)
	for _, pair := range [][2]int64{{f.facts[0], f.facts[2]}, {f.facts[1], f.facts[2]}} {
		id, _, err := storer.RecordContradictionImmediate(ctx, f.person, pair[0], pair[1], "Encore.", "Again.")
		require.NoError(t, err)
		// The three facts share one stance, so the recording bundles the pair
		// with the fixture's contradiction; a bundle is one contradiction of
		// the rate. Each pair stands alone here.
		_, err = storer.pool.Exec(ctx, `UPDATE contradictions SET bundle_stance_id = NULL WHERE id = $1`, id)
		require.NoError(t, err)
	}
	_, err = storer.pool.Exec(ctx, `UPDATE persons SET fact_count = 4000, honesty_score = 0.97 WHERE id = $1`, f.person)
	require.NoError(t, err)
	base, err = storer.ScoreBaseline(ctx)
	require.NoError(t, err)
	assert.InDelta(t, 0.00075, base.Rate, 1e-9)
	assert.InDelta(t, 4000, base.CoveredFacts, 1e-6)
	if assert.NotNil(t, base.FloorHonesty) {
		assert.InDelta(t, 0.97, *base.FloorHonesty, 1e-9)
	}

	// The score compares a person's contradictions with those expected at
	// this rate: the rate of the score is the baseline's, and a recompute
	// writes every stored score at it (the 0.97 set by hand above goes).
	rate, err := storer.ScoreRate(ctx)
	require.NoError(t, err)
	assert.InDelta(t, base.Rate, rate, 1e-12)
	_, err = storer.RecomputeAllHonestyScores(ctx, false)
	require.NoError(t, err)
	var stored float64
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT honesty_score FROM persons WHERE id = $1`, f.person).Scan(&stored))
	assert.InDelta(t, core.ComputeHonestyScore(3, core.ContradictionStats{Bundles: 3, Total: 3}, rate), stored, 1e-9)
	assert.Less(t, stored, core.ComputeHonestyScore(4000, core.ContradictionStats{Bundles: 3, Total: 3}, rate),
		"the same contradictions among the 4000 facts the rate expects 3 of would cost less")

	// The collected period: the facts' dates, the tasks' once one ended.
	p, err := storer.GetPersonByID(ctx, f.person)
	require.NoError(t, err)
	require.NotNil(t, p.CollectedFrom)
	_, err = storer.pool.Exec(ctx, `INSERT INTO collection_tasks (person_id, plugin, since, until, status) VALUES ($1, 'news', '2023-10-01', '2026-11-01', 'done')`, f.person)
	require.NoError(t, err)
	p, err = storer.GetPersonByID(ctx, f.person)
	require.NoError(t, err)
	assert.Equal(t, "2023-10-01", p.CollectedFrom.Format("2006-01-02"))
	assert.Equal(t, "2026-10-31", p.CollectedTo.Format("2006-01-02"), "until is exclusive")
}

func TestShareTargetAndScore(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := newReportFixture(t, storer, 3)

	p, err := storer.GetShareTarget(ctx, "person", f.person, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Contradictions)
	assert.Nil(t, p.StanceID)

	s, err := storer.GetShareTarget(ctx, "stance", f.stance, 0)
	require.NoError(t, err)
	assert.Equal(t, "Sa position sur les retraites.", s.Text)
	assert.Equal(t, f.stance, *s.StanceID)

	c, err := storer.GetShareTarget(ctx, "contradiction", f.contradiction, 0)
	require.NoError(t, err)
	assert.Equal(t, f.stance, *c.StanceID)
	assert.Equal(t, "Il faut la retraite à 60 ans.", c.Text)
	assert.Equal(t, "Il faut la retraite à 65 ans.", *c.Other)

	fa, err := storer.GetShareTarget(ctx, "fact", f.facts[2], f.person+1)
	require.NoError(t, err, "a fact shared from another page opens its own person")
	assert.Equal(t, f.person, fa.PersonID)
	assert.Equal(t, "« Il a voté la réforme. »", fa.Quote)

	for _, bad := range []struct {
		kind string
		id   int64
	}{{"person", 999999}, {"stance", 999999}, {"contradiction", 999999}, {"fact", 999999}, {"group", f.person}} {
		_, err := storer.GetShareTarget(ctx, bad.kind, bad.id, 0)
		assert.ErrorIs(t, err, ErrNoShareTarget, bad.kind)
	}

	e, err := storer.GetScoreExplanation(ctx, f.person)
	require.NoError(t, err)
	assert.Equal(t, 3, e.Facts)
	assert.Equal(t, 1, e.Bundles)
	assert.Equal(t, 1, e.Pairs)
	assert.InDelta(t, 1.0, e.Weighted, 1e-12)
	assert.InDelta(t, e.Rate*3, e.Expected, 1e-12)
	assert.InDelta(t, e.Weighted/(e.Expected+core.HonestyScoreSmoothing), e.Excess, 1e-12)
	var stored float64
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT honesty_score FROM persons WHERE id = $1`, f.person).Scan(&stored))
	assert.InDelta(t, stored, e.Score, 1e-9, "the explanation computes the score shown")
	assert.Nil(t, e.MembersScore)
}
