package contradiction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/require"
)

// chatStub answers every chat call with status and, on 200, content.
func chatStub(t *testing.T, status int, content string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":{"message":"Insufficient credits"}}`))
			return
		}
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// auditFixture stores one contradiction of a fresh person and returns the
// storer and the person id; everything is removed at cleanup.
func auditFixture(t *testing.T) (*storage.PostgresStorer, int64) {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping audit run tests")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	t.Cleanup(storer.Close)
	pool := storer.Pool()

	const name = "audit outage person"
	_, err = pool.Exec(ctx, `DELETE FROM persons WHERE name = $1`, name)
	require.NoError(t, err)
	var personID int64
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ($1, 'human') RETURNING id`, name).Scan(&personID))
	var factIDs []int64
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM contradictions WHERE person_id = $1`, personID)
		_, _ = pool.Exec(ctx, `DELETE FROM facts WHERE id = ANY($1)`, factIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM persons WHERE id = $1`, personID)
	})
	for _, text := range []string{"veut la retraite à 62 ans", "vote la retraite à 64 ans"} {
		var id, sourceID int64
		require.NoError(t, pool.QueryRow(ctx, `INSERT INTO facts (fact_text) VALUES ($1) RETURNING id`, text).Scan(&id))
		factIDs = append(factIDs, id)
		require.NoError(t, pool.QueryRow(ctx,
			`INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, $2, 'test') RETURNING id`,
			id, fmt.Sprintf("http://ex/audit-outage/%d", id)).Scan(&sourceID))
		_, err := pool.Exec(ctx, `INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)`, sourceID, personID)
		require.NoError(t, err)
	}
	_, _, err = storer.RecordContradictionImmediate(ctx, personID, factIDs[0], factIDs[1], "62 then 64", "")
	require.NoError(t, err)
	return storer, personID
}

// auditMetricSince reports whether a contradiction-audit job metric was
// recorded after since (the row the pipeline's 5-day cadence reads).
func auditMetricSince(t *testing.T, storer *storage.PostgresStorer, since time.Time) bool {
	t.Helper()
	last, err := storer.GetLatestJobRunTime(context.Background(), "contradiction-audit")
	require.NoError(t, err)
	return last != nil && !last.Before(since)
}

// TestRunAudit_CadenceOnlyAfterJudgedRows: an audit stopped by an
// unavailable LLM, or whose every verdict failed, returns an error and does
// not record the cadence row, so the next run audits again; an audit that
// judged its rows records it.
func TestRunAudit_CadenceOnlyAfterJudgedRows(t *testing.T) {
	storer, personID := auditFixture(t)
	ctx := context.Background()
	opts := AuditOptions{PersonID: &personID, RecordMetric: true}
	t.Setenv("OPEN_AI_KEY", "k")

	t.Run("LLM unavailable", func(t *testing.T) {
		t.Setenv("OPEN_AI_API", "http://127.0.0.1:1/v1")
		t.Setenv("FALLBACK_URL", chatStub(t, http.StatusPaymentRequired, ""))
		t.Setenv("FALLBACK_CHAT_MODEL", "stub")
		llm.MarkPrimaryDown("audit outage test")
		t.Cleanup(llm.MarkPrimaryUp)

		start := time.Now()
		_, err := RunAudit(ctx, storer, opts)
		require.ErrorIs(t, err, llm.ErrUnavailable)
		require.False(t, auditMetricSince(t, storer, start), "a stopped audit must not reset the cadence")
	})

	t.Run("every verdict failed", func(t *testing.T) {
		t.Setenv("OPEN_AI_API", chatStub(t, http.StatusOK, "no verdict here"))
		start := time.Now()
		res, err := RunAudit(ctx, storer, opts)
		require.Error(t, err)
		require.False(t, errors.Is(err, llm.ErrUnavailable))
		require.Equal(t, 1, res.Errors)
		require.False(t, auditMetricSince(t, storer, start), "an audit that judged nothing must not reset the cadence")
	})

	t.Run("control: rows judged", func(t *testing.T) {
		t.Setenv("OPEN_AI_API", chatStub(t, http.StatusOK, "REASONING: real reversal.\nRULE: none\nVERDICT: keep"))
		start := time.Now()
		res, err := RunAudit(ctx, storer, opts)
		require.NoError(t, err)
		require.Equal(t, 1, res.Keep)
		require.True(t, auditMetricSince(t, storer, start), "a judged audit records the cadence row")
	})
}

// TestAudit_RemoveNeedsFullQuorum: a remove backed by 2 of AuditVotes=5
// samples, the others unparseable, is a plurality of the samples that
// answered, not a majority of the vote: it must come back as an error (no
// decision), never as a remove the caller would act on.
func TestAudit_RemoveNeedsFullQuorum(t *testing.T) {
	first := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string } `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		content := "garbage"
		if len(req.Messages) == 1 { // not a corrective retry
			first++
			if first == 1 || first == 5 {
				content = "REASONING: x.\nRULE: accusation_not_finding\nVERDICT: remove"
			}
		}
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_API", srv.URL+"/v1")
	a, err := NewOpenAIContradictionAuditorFromEnv()
	require.NoError(t, err)

	res := a.Audit(context.Background(), promptrules.Statement{Text: "a"}, promptrules.Statement{Text: "b"}, nil, nil, "r")
	require.Equal(t, 5, first, "every sample is drawn")
	require.Error(t, res.Err, "a 2-of-5 remove must not be a decision (verdict %q)", res.Verdict)
}

// TestRunAudit_RemoveNotCountedWhenConfirmUnavailable: a row the first pass
// flags for removal, whose confirming pass the LLM cannot serve, is neither
// deleted nor counted as a remove: the audit stops and re-judges it next time.
func TestRunAudit_RemoveNotCountedWhenConfirmUnavailable(t *testing.T) {
	storer, personID := auditFixture(t)
	ctx := context.Background()
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_API", "http://127.0.0.1:1/v1")
	t.Setenv("FALLBACK_CHAT_MODEL", "stub")

	// The first pass sees the facts in stored order (62 before 64) and says
	// remove; the order-swapped confirming pass gets a 402.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Index(string(body), "62 ans") > strings.Index(string(body), "64 ans") {
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte(`{"error":{"message":"Insufficient credits"}}`))
			return
		}
		content := "REASONING: two different ages.\nRULE: complementary_facts\nVERDICT: remove"
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": content}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FALLBACK_URL", srv.URL+"/v1")
	llm.MarkPrimaryDown("audit confirm outage test")
	t.Cleanup(llm.MarkPrimaryUp)

	res, err := RunAudit(ctx, storer, AuditOptions{PersonID: &personID, Apply: true})
	require.ErrorIs(t, err, llm.ErrUnavailable)
	require.Zero(t, res.Remove)
	require.Empty(t, res.RemoveIDs)
	var left int
	require.NoError(t, storer.Pool().QueryRow(ctx, `SELECT count(*) FROM contradictions WHERE person_id = $1`, personID).Scan(&left))
	require.Equal(t, 1, left, "the contradiction stays")
}
