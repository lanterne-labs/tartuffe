package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/auth"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (m *memPolls) CreateUserReport(_ context.Context, userID int64, kind string, targetID int64, reason string) (*storage.UserReport, error) {
	if !m.targets[fmt.Sprintf("%s:%d", kind, targetID)] {
		return nil, storage.ErrReportTarget
	}
	pending := 0
	for _, r := range m.reports {
		if r.ID/1000 == userID && r.TargetKind == kind && r.TargetID == targetID {
			return nil, storage.ErrAlreadyReported
		}
		if r.ID/1000 == userID && r.Status == storage.ReportPending {
			pending++
		}
	}
	if pending >= storage.MaxPendingReportsPerUser {
		return nil, storage.ErrTooManyReports
	}
	// The fake keeps the user in the id: user*1000 + n.
	r := storage.UserReport{ID: userID*1000 + int64(len(m.reports)), TargetKind: kind, TargetID: targetID,
		Reason: reason, Status: storage.ReportPending, CreatedAt: time.Now()}
	m.reports = append(m.reports, r)
	return &r, nil
}

func (m *memPolls) GetUserReports(_ context.Context, userID int64) ([]storage.UserReport, error) {
	out := []storage.UserReport{}
	for _, r := range m.reports {
		if r.ID/1000 == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memPolls) ListCorrections(context.Context, int64, int) ([]storage.Correction, error) {
	out := []storage.Correction{}
	for _, r := range m.reports {
		if r.Status == storage.ReportAccepted {
			out = append(out, storage.Correction{ID: r.ID, TargetKind: r.TargetKind, VerdictReason: r.VerdictReason})
		}
	}
	return out, nil
}

func (m *memPolls) GetFeed(_ context.Context, f storage.FeedFilter, after *storage.FeedCursor, limit, _ int) ([]storage.FeedItem, error) {
	m.lastFeedFilter = f
	out := []storage.FeedItem{}
	for _, it := range m.feed {
		if f.Kind != "" && it.Kind != f.Kind {
			continue
		}
		if after != nil && !it.At.Before(after.At) {
			continue
		}
		out = append(out, it)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *memPolls) GetShareTarget(_ context.Context, kind string, id, _ int64) (*storage.ShareTarget, error) {
	if t, ok := m.share[fmt.Sprintf("%s:%d", kind, id)]; ok {
		return t, nil
	}
	return nil, storage.ErrNoShareTarget
}

func (m *memPolls) GetScoreExplanation(_ context.Context, id int64) (*storage.ScoreExplanation, error) {
	if e, ok := m.scores[id]; ok {
		return e, nil
	}
	return nil, pgx.ErrNoRows
}

// TestReports: only a signed-in user reports, once per target, with a
// reason; the user sees their reports.
func TestReports(t *testing.T) {
	fs := pollFixture()
	fs.targets = map[string]bool{"contradiction:7": true, "fact:9": true}
	h := NewServer(fs, auth.NewRegistry(auth.Noop{})).Handler()
	body := func(kind string, id int64, reason string) map[string]any {
		return map[string]any{"kind": kind, "target_id": id, "reason": reason}
	}
	const why = "Les deux faits parlent de deux lois différentes."

	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPost, "/api/reports", "", body("contradiction", 7, why)).Code)

	alice := signIn(t, h, "alice")
	for _, c := range []struct {
		name string
		body map[string]any
		code int
		err  string
	}{
		{"reason too short", body("contradiction", 7, "  faux   "), http.StatusBadRequest, codeInvalidReport},
		{"reason too long", body("contradiction", 7, strings.Repeat("é", maxReportReason+1)), http.StatusBadRequest, codeInvalidReport},
		{"unknown kind", body("stance", 7, why), http.StatusBadRequest, codeInvalidReport},
		{"no such target", body("fact", 8, why), http.StatusNotFound, codeNoTarget},
		{"reported", body("contradiction", 7, why), http.StatusCreated, ""},
		{"once per target", body("contradiction", 7, why+" Encore."), http.StatusConflict, codeAlreadyReported},
		{"another kind, same id space", body("fact", 9, why), http.StatusCreated, ""},
	} {
		rec := do(t, h, http.MethodPost, "/api/reports", alice, c.body)
		assert.Equal(t, c.code, rec.Code, "%s: %s", c.name, rec.Body.String())
		if c.err != "" {
			assert.Contains(t, rec.Body.String(), `"code":"`+c.err+`"`, c.name)
		}
	}

	bruno := signIn(t, h, "bruno")
	assert.Equal(t, http.StatusCreated, do(t, h, http.MethodPost, "/api/reports", bruno, body("contradiction", 7, why)).Code,
		"another user reports the same target")

	var mine myReports
	rec := do(t, h, http.MethodGet, "/api/me/reports", alice, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &mine))
	assert.ElementsMatch(t, []string{"contradiction:7", "fact:9"}, mine.Reported)
	assert.Len(t, mine.Reports, 2)
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/api/me/reports", "", nil).Code)
}

// TestReportCap: a user cannot queue more than MaxPendingReportsPerUser.
func TestReportCap(t *testing.T) {
	fs := pollFixture()
	fs.targets = map[string]bool{}
	for i := range storage.MaxPendingReportsPerUser + 1 {
		fs.targets[fmt.Sprintf("fact:%d", i+1)] = true
	}
	h := NewServer(fs, auth.NewRegistry(auth.Noop{})).Handler()
	alice := signIn(t, h, "alice")
	for i := range storage.MaxPendingReportsPerUser {
		require.Equal(t, http.StatusCreated, do(t, h, http.MethodPost, "/api/reports", alice,
			map[string]any{"kind": "fact", "target_id": i + 1, "reason": "Ce fait est mal attribué."}).Code)
	}
	rec := do(t, h, http.MethodPost, "/api/reports", alice,
		map[string]any{"kind": "fact", "target_id": storage.MaxPendingReportsPerUser + 1, "reason": "Ce fait est mal attribué."})
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Contains(t, rec.Body.String(), codeTooManyReports)
}

// TestFeedPages: the feed pages with an opaque cursor, and refuses a bad one.
func TestFeedPages(t *testing.T) {
	fs := pollFixture()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		kind := storage.FeedFact
		if i%2 == 0 {
			kind = storage.FeedContradiction
		}
		fs.feed = append(fs.feed, storage.FeedItem{Kind: kind, ID: int64(i + 1), At: t0.Add(-time.Duration(i) * time.Minute)})
	}
	h := NewServer(fs, nil).Handler()

	var page feedPage
	rec := do(t, h, http.MethodGet, "/api/feed?limit=2", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	require.Len(t, page.Items, 2)
	require.NotEmpty(t, page.Next)

	var ids []int64
	for page.Next != "" {
		ids = append(ids, page.Items[0].ID, page.Items[1].ID)
		rec = do(t, h, http.MethodGet, "/api/feed?limit=2&after="+page.Next, "", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		page = feedPage{}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
		if len(page.Items) < 2 {
			for _, it := range page.Items {
				ids = append(ids, it.ID)
			}
		}
	}
	assert.Equal(t, []int64{1, 2, 3, 4, 5}, ids)

	rec = do(t, h, http.MethodGet, "/api/feed?kind=fact", "", nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.Len(t, page.Items, 2)

	rec = do(t, h, http.MethodGet, "/api/feed?person=12&from=2020-01-01&to=2020-12-31", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	ff := fs.lastFeedFilter
	assert.Equal(t, int64(12), ff.PersonID)
	require.NotNil(t, ff.From)
	require.NotNil(t, ff.To)
	assert.Equal(t, "2020-01-01", ff.From.Format("2006-01-02"))
	assert.Equal(t, "2021-01-01", ff.To.Format("2006-01-02"), "to includes its whole day")
	for _, bad := range []string{"person=x", "person=-1", "from=2020-13-01", "to=yesterday", "from=2021-01-01&to=2020-01-01"} {
		assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodGet, "/api/feed?"+bad, "", nil).Code, bad)
	}

	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodGet, "/api/feed?after=nope", "", nil).Code)
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodGet, "/api/feed?kind=stance", "", nil).Code)
}

func TestFeedCursorRoundTrip(t *testing.T) {
	it := storage.FeedItem{Kind: storage.FeedContradiction, ID: 42, At: time.Date(2026, 10, 1, 12, 0, 0, 123456789, time.UTC)}
	c, err := decodeFeedCursor(encodeFeedCursor(it))
	require.NoError(t, err)
	assert.True(t, c.At.Equal(it.At))
	assert.Equal(t, storage.FeedContradiction, c.Kind)
	assert.Equal(t, int64(42), c.ID)
	for _, bad := range []string{"", "1.fact", "x.fact.1", "1.stance.1", "1.fact.x"} {
		_, err := decodeFeedCursor(bad)
		assert.Error(t, err, bad)
	}
}

func TestShareAndScore(t *testing.T) {
	fs := pollFixture()
	sid := int64(3)
	fs.share = map[string]*storage.ShareTarget{"contradiction:7": {Kind: "contradiction", ID: 7, PersonID: 10, StanceID: &sid}}
	fs.scores = map[int64]*storage.ScoreExplanation{10: {PersonID: 10, Facts: 80, Bundles: 2, Score: 0.9}}
	h := NewServer(fs, nil).Handler()

	rec := do(t, h, http.MethodGet, "/api/share/contradiction/7", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"stance_id":3`)
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/api/share/fact/7", "", nil).Code)
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodGet, "/api/share/fact/x", "", nil).Code)

	rec = do(t, h, http.MethodGet, "/api/person/10/score", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"bundles":2`)
	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodGet, "/api/person/11/score", "", nil).Code)
}
