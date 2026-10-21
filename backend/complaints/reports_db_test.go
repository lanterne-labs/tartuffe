package complaints

import (
	"context"
	"os"
	"testing"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJudgeReportsDB runs the whole loop on the database: a user reports a
// contradiction, the judge finds the report right, the contradiction goes
// for good (its pair is dismissed), the score rises and the correction is
// public.
func TestJudgeReportsDB(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()
	st, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	defer st.Close()
	pool := st.Pool()
	_, err = pool.Exec(ctx, `TRUNCATE user_reports RESTART IDENTITY`)
	require.NoError(t, err)

	var person int64
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO persons (name, entity_type) VALUES ('judged person', 'human') RETURNING id`).Scan(&person))
	defer pool.Exec(ctx, `DELETE FROM persons WHERE id = $1`, person)
	var facts [2]int64
	for i, text := range []string{"Il soutient la loi.", "Il combat la loi."} {
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO facts (fact_text) VALUES ($1) RETURNING id`, text).Scan(&facts[i]))
		_, err := pool.Exec(ctx, `
			WITH src AS (INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type) VALUES ($1::bigint, 'http://ex/' || $1::text, $2, 'test') RETURNING id)
			INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $3 FROM src`, facts[i], text, person)
		require.NoError(t, err)
	}
	defer pool.Exec(ctx, `DELETE FROM facts WHERE id = ANY($1)`, facts[:])
	cid, _, err := st.RecordContradictionImmediate(ctx, person, facts[0], facts[1], "soutient puis combat", "")
	require.NoError(t, err)
	var before float64
	require.NoError(t, pool.QueryRow(ctx, `SELECT honesty_score FROM persons WHERE id = $1`, person).Scan(&before))

	u, err := st.UpsertAppUser(ctx, "noop", "judged-reporter", "")
	require.NoError(t, err)
	defer st.DeleteAppUser(ctx, u.ID)
	_, err = st.CreateUserReport(ctx, u.ID, storage.ReportKindContradiction, cid, "Ce ne sont pas les mêmes lois : RIGHT")
	require.NoError(t, err)

	j := &fakeJudge{verdicts: map[string]Verdict{"RIGHT": VerdictDeleteTarget}}
	n, err := JudgeReports(ctx, st, j, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	var left int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM contradictions WHERE id = $1`, cid).Scan(&left))
	assert.Zero(t, left)
	var dismissed bool
	a, b := min(facts[0], facts[1]), max(facts[0], facts[1])
	require.NoError(t, pool.QueryRow(ctx, `SELECT dismissed FROM fact_pair_checks WHERE person_id = $1 AND fact_a_id = $2 AND fact_b_id = $3`, person, a, b).Scan(&dismissed))
	assert.True(t, dismissed, "the pair is not detected again")
	var after float64
	require.NoError(t, pool.QueryRow(ctx, `SELECT honesty_score FROM persons WHERE id = $1`, person).Scan(&after))
	assert.Greater(t, after, before)

	corrections, err := st.ListCorrections(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, corrections, 1)
	assert.Equal(t, "because RIGHT", corrections[0].VerdictReason)
	assert.Equal(t, "Il soutient la loi. ⟷ Il combat la loi.", corrections[0].TargetText)
}
