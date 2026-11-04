package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/accounts/accountstest"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (m *fakeStore) CreateUserReport(_ context.Context, userID int64, kind string, targetID int64, reason string) (*storage.UserReport, error) {
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

func (m *fakeStore) GetUserReports(_ context.Context, userID int64) ([]storage.UserReport, error) {
	out := []storage.UserReport{}
	for _, r := range m.reports {
		if r.ID/1000 == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *fakeStore) ListCorrections(context.Context, int64, int) ([]storage.Correction, error) {
	out := []storage.Correction{}
	for _, r := range m.reports {
		if r.Status == storage.ReportAccepted {
			out = append(out, storage.Correction{ID: r.ID, TargetKind: r.TargetKind, VerdictReason: r.VerdictReason})
		}
	}
	return out, nil
}

func (m *fakeStore) GetFeed(_ context.Context, f storage.FeedFilter, after *storage.FeedCursor, limit, _ int) ([]storage.FeedItem, error) {
	m.lastFeedFilter = f
	out := []storage.FeedItem{}
	skip := f.Offset
	for _, it := range m.feed {
		if f.Kind != "" && it.Kind != f.Kind {
			continue
		}
		if after != nil && !it.At.Before(after.At) {
			continue
		}
		if skip > 0 {
			skip--
			continue
		}
		users := m.likes[fmt.Sprintf("%s:%d", it.Kind, it.ID)]
		it.Likes, it.Liked = len(users), users[f.ViewerID]
		out = append(out, it)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (m *fakeStore) SetLike(_ context.Context, userID int64, kind string, targetID int64, on bool) (int, error) {
	key := fmt.Sprintf("%s:%d", kind, targetID)
	if !m.targets[key] {
		return 0, storage.ErrLikeTarget
	}
	if m.likes == nil {
		m.likes = map[string]map[int64]bool{}
	}
	if m.likes[key] == nil {
		m.likes[key] = map[int64]bool{}
	}
	if on {
		m.likes[key][userID] = true
	} else {
		delete(m.likes[key], userID)
	}
	return len(m.likes[key]), nil
}

func (m *fakeStore) GetShareTarget(_ context.Context, kind string, id, _ int64) (*storage.ShareTarget, error) {
	if t, ok := m.share[fmt.Sprintf("%s:%d", kind, id)]; ok {
		return t, nil
	}
	return nil, storage.ErrNoShareTarget
}

func (m *fakeStore) GetScoreExplanation(_ context.Context, id int64) (*storage.ScoreExplanation, error) {
	if e, ok := m.scores[id]; ok {
		return e, nil
	}
	return nil, pgx.ErrNoRows
}

// TestReports: only a signed-in user reports, once per target, with a
// reason; the user sees their reports.
func TestReports(t *testing.T) {
	fs, h := newFixture()
	fs.targets = map[string]bool{"contradiction:7": true, "fact:9": true}
	body := func(kind string, id int64, reason string) map[string]any {
		return map[string]any{"kind": kind, "target_id": id, "reason": reason}
	}
	const why = "Les deux faits parlent de deux lois différentes."

	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPost, "/api/reports", "", body("contradiction", 7, why)).Code)

	alice := accountstest.SignIn(t, h, "alice")
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

	bruno := accountstest.SignIn(t, h, "bruno")
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
	fs, h := newFixture()
	fs.targets = map[string]bool{}
	for i := range storage.MaxPendingReportsPerUser + 1 {
		fs.targets[fmt.Sprintf("fact:%d", i+1)] = true
	}
	alice := accountstest.SignIn(t, h, "alice")
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
	fs, h := newFixture()
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := range 5 {
		kind := storage.FeedFact
		if i%2 == 0 {
			kind = storage.FeedContradiction
		}
		fs.feed = append(fs.feed, storage.FeedItem{Kind: kind, ID: int64(i + 1), At: t0.Add(-time.Duration(i) * time.Minute)})
	}

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

// TestFeedHot: the hot order pages by offset and stops at maxHotItems.
func TestFeedHot(t *testing.T) {
	fs, h := newFixture()
	for i := range 5 {
		fs.feed = append(fs.feed, storage.FeedItem{Kind: storage.FeedFact, ID: int64(i + 1), At: time.Now().Add(-time.Duration(i) * time.Minute)})
	}

	var page feedPage
	rec := do(t, h, http.MethodGet, "/api/feed?sort=hot&limit=2", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.True(t, fs.lastFeedFilter.Hot)
	assert.Equal(t, "2", page.Next)
	rec = do(t, h, http.MethodGet, "/api/feed?sort=hot&limit=2&after=2", "", nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.Equal(t, 2, fs.lastFeedFilter.Offset)
	assert.Equal(t, []int64{3, 4}, []int64{page.Items[0].ID, page.Items[1].ID})
	assert.Equal(t, "4", page.Next)

	for _, bad := range []string{"sort=top", "sort=hot&after=-1", "sort=hot&after=x", fmt.Sprintf("sort=hot&after=%d", maxHotItems)} {
		assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodGet, "/api/feed?"+bad, "", nil).Code, bad)
	}
	rec = do(t, h, http.MethodGet, fmt.Sprintf("/api/feed?sort=hot&limit=2&after=%d", maxHotItems-2), "", nil)
	page = feedPage{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &page))
	assert.Empty(t, page.Next, "the hot feed ends at maxHotItems")
}

// TestLikes: only a signed-in user gives a +1, once; the feed marks the
// viewer's.
func TestLikes(t *testing.T) {
	fs, h := newFixture()
	fs.targets = map[string]bool{"fact:9": true}
	fs.feed = []storage.FeedItem{{Kind: storage.FeedFact, ID: 9, At: time.Now()}}

	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPut, "/api/likes/fact/9", "", nil).Code)
	alice, bob := accountstest.SignIn(t, h, "alice"), accountstest.SignIn(t, h, "bob")
	var st likeState
	for range 2 {
		rec := do(t, h, http.MethodPut, "/api/likes/fact/9", alice, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
		assert.Equal(t, likeState{Likes: 1, Liked: true}, st, "a second +1 counts once")
	}
	rec := do(t, h, http.MethodPut, "/api/likes/fact/9", bob, nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	assert.Equal(t, 2, st.Likes)

	feedOf := func(token string) storage.FeedItem {
		var page feedPage
		require.NoError(t, json.Unmarshal(do(t, h, http.MethodGet, "/api/feed", token, nil).Body.Bytes(), &page))
		require.Len(t, page.Items, 1)
		return page.Items[0]
	}
	assert.True(t, feedOf(alice).Liked)
	assert.False(t, feedOf("").Liked)
	assert.False(t, feedOf("not-a-session").Liked, "a bad session reads as signed out")
	assert.Equal(t, 2, feedOf("").Likes)

	rec = do(t, h, http.MethodDelete, "/api/likes/fact/9", alice, nil)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st))
	assert.Equal(t, likeState{Likes: 1, Liked: false}, st)
	assert.False(t, feedOf(alice).Liked)

	assert.Equal(t, http.StatusNotFound, do(t, h, http.MethodPut, "/api/likes/fact/10", alice, nil).Code)
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPut, "/api/likes/stance/9", alice, nil).Code)
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPut, "/api/likes/fact/x", alice, nil).Code)
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
	fs, h := newFixture()
	sid := int64(3)
	fs.share = map[string]*storage.ShareTarget{"contradiction:7": {Kind: "contradiction", ID: 7, PersonID: 10, StanceID: &sid}}
	fs.scores = map[int64]*storage.ScoreExplanation{10: {PersonID: 10, Facts: 80, Bundles: 2, Score: 0.9}}

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

// ContradictionRate: one contradiction per 1000 facts.
func (m *fakeStore) ContradictionRate(context.Context) (float64, error) { return 0.001, nil }
