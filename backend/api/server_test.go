package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStorer records the arguments the handlers pass to storage and returns
// canned data; every method not exercised by a test returns zero values.
type fakeStorer struct {
	memPolls

	history []storage.HistoryPoint
	pingErr error

	rankedArgs struct {
		limit, offset, minFacts  int
		entityType, search, sort string
	}
	ranked []storage.PersonSummary

	members    []storage.MemberInfo
	membersErr error

	stances      []core.StanceDocument
	stanceCounts map[int64]int
	groupCounts  map[int64]int
}

func (f *fakeStorer) Ping(context.Context) error { return f.pingErr }

func (f *fakeStorer) GetPersonsRankedByScore(_ context.Context, limit, offset int, entityType, search, sort string, minFacts int) ([]storage.PersonSummary, error) {
	f.rankedArgs.limit, f.rankedArgs.offset, f.rankedArgs.minFacts = limit, offset, minFacts
	f.rankedArgs.entityType, f.rankedArgs.search, f.rankedArgs.sort = entityType, search, sort
	return f.ranked, nil
}

func (f *fakeStorer) GetPersonByID(context.Context, int64) (*storage.PersonSummary, error) {
	return nil, nil
}

func (f *fakeStorer) GetPersonFacts(context.Context, int64, int, int) (*storage.PaginatedFacts, error) {
	return nil, nil
}

func (f *fakeStorer) GetPersonContradictions(context.Context, int64, int, int) (*storage.PaginatedContradictions, error) {
	return nil, nil
}

func (f *fakeStorer) GetPersonMembers(context.Context, int64) ([]storage.MemberInfo, error) {
	return f.members, f.membersErr
}

func (f *fakeStorer) GetPersonHistory(context.Context, int64) ([]storage.HistoryPoint, error) {
	return f.history, nil
}

func (f *fakeStorer) GetFactPersons(context.Context, int64, int, int) ([]storage.PersonSummary, error) {
	return nil, nil
}

func (f *fakeStorer) GetStanceDocumentsForPerson(context.Context, int64) ([]core.StanceDocument, error) {
	return f.stances, nil
}

func (f *fakeStorer) GetStanceContradictionCounts(context.Context, int64) (map[int64]int, error) {
	return f.stanceCounts, nil
}

func (f *fakeStorer) GetStanceGroupCounts(context.Context, int64) (map[int64]int, error) {
	return f.groupCounts, nil
}

func (f *fakeStorer) GetStanceContradictionGroups(context.Context, int64) ([]storage.ContradictionGroup, error) {
	return nil, nil
}

func (f *fakeStorer) GetFactsInStance(context.Context, int64) ([]storage.FactInStance, error) {
	return nil, nil
}

func (f *fakeStorer) GetMaxEventID(context.Context) (int64, error) { return 0, nil }

func (f *fakeStorer) GetEventsAfter(context.Context, int64, int) ([]storage.Event, error) {
	return nil, nil
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	routes, err := NewServer(&fakeStorer{}, nil).Routes()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"GET /health",
		"GET /api/persons",
		"GET /api/person/{id}",
		"GET /api/person/{id}/facts",
		"GET /api/person/{id}/contradictions",
		"GET /api/person/{id}/members",
		"GET /api/person/{id}/history",
		"GET /api/facts/{id}/persons",
		"GET /api/person/{id}/stances",
		"GET /api/stances/{id}/contradictions",
		"GET /api/stances/{id}/facts",
		"GET /api/events",
		"GET /api/auth/providers",
		"POST /api/auth/challenge/{provider}",
		"POST /api/auth/signin/{provider}",
		"GET /api/auth/me",
		"DELETE /api/auth/me",
		"GET /api/me/votes",
		"POST /api/auth/signout",
		"GET /api/polls",
		"GET /api/polls/{slug}",
		"GET /api/polls/{slug}/ballot",
		"POST /api/polls/{slug}/ballot",
		"POST /api/polls/{slug}/votes",
		"DELETE /api/polls/{slug}/votes/{personID}",
		"GET /api/person/{id}/score",
		"GET /api/feed",
		"GET /api/share/{kind}/{id}",
		"GET /api/corrections",
		"POST /api/reports",
		"GET /api/me/reports",
	}, routes)
}

func TestHealth(t *testing.T) {
	rec := get(t, NewServer(&fakeStorer{}, nil).Handler(), "/health")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())

	rec = get(t, NewServer(&fakeStorer{pingErr: errors.New("down")}, nil).Handler(), "/health")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"status":"error"}`, rec.Body.String())
}

func TestSecurityHeaders(t *testing.T) {
	rec := get(t, NewServer(&fakeStorer{}, nil).Handler(), "/health")
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "strict-origin-when-cross-origin", rec.Header().Get("Referrer-Policy"))
	assert.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
	assert.Empty(t, rec.Header().Get("Strict-Transport-Security"), "HSTS only behind https")
}

func TestGetPersons_MinFactsFloorAndPagination(t *testing.T) {
	name := "party"
	fs := &fakeStorer{ranked: []storage.PersonSummary{{ID: 7, Name: "X", EntityType: &name, HonestyScore: 0.9, FactCount: 12}}}
	h := NewServer(fs, nil).Handler()

	rec := get(t, h, "/api/persons")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 50, fs.rankedArgs.limit)
	assert.Equal(t, 0, fs.rankedArgs.offset)
	assert.Equal(t, publicMinFacts, fs.rankedArgs.minFacts, "default floor")
	assert.Equal(t, "public, max-age=60, stale-while-revalidate=300", rec.Header().Get("Cache-Control"))

	var got []storage.PersonSummary
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 1)
	assert.Equal(t, int64(7), got[0].ID)

	// A caller may raise the floor, never lower it; out-of-range paging falls back.
	get(t, h, "/api/persons?min_facts=1&limit=5000&offset=-3")
	assert.Equal(t, publicMinFacts, fs.rankedArgs.minFacts)
	assert.Equal(t, 50, fs.rankedArgs.limit)
	assert.Equal(t, 0, fs.rankedArgs.offset)

	get(t, h, "/api/persons?min_facts=10&limit=20&offset=40&entity_type=human&search=ma&sort=honesty_asc")
	assert.Equal(t, 10, fs.rankedArgs.minFacts)
	assert.Equal(t, 20, fs.rankedArgs.limit)
	assert.Equal(t, 40, fs.rankedArgs.offset)
	assert.Equal(t, "human", fs.rankedArgs.entityType)
	assert.Equal(t, "ma", fs.rankedArgs.search)
	assert.Equal(t, "honesty_asc", fs.rankedArgs.sort)
}

func TestGetPersonMembers(t *testing.T) {
	img := "https://img/x.png"
	fs := &fakeStorer{members: []storage.MemberInfo{{ID: 3, Name: "Dep", EntityType: "human", HonestyScore: 0.5, ImageURL: &img}}}
	h := NewServer(fs, nil).Handler()

	rec := get(t, h, "/api/person/1/members")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `[{"id":3,"name":"Dep","entity_type":"human","honesty_score":0.5,"image_url":"https://img/x.png"}]`, rec.Body.String())

	rec = get(t, h, "/api/person/abc/members")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	fs.membersErr = errors.New("boom")
	rec = get(t, h, "/api/person/1/members")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "Internal server error\n", rec.Body.String(), "internal details must not leak")
}

func TestGetPersonStances_ScoreAndCounts(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeStorer{
		stances: []core.StanceDocument{
			{ID: 10, ClusterLabel: "A", Summary: "sa", FactCount: 20, LastUpdatedAt: now},
			{ID: 11, ClusterLabel: "B", LabelEn: "B-en", Summary: "sb", SummaryEn: "sb-en", FactCount: 5, LastUpdatedAt: now},
		},
		stanceCounts: map[int64]int{10: 3},
		groupCounts:  map[int64]int{10: 2},
	}
	rec := get(t, NewServer(fs, nil).Handler(), "/api/person/1/stances")
	require.Equal(t, http.StatusOK, rec.Code)

	var got []StanceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)

	// Stance with contradictions: one bundle + 2 extras, per stanceScore.
	assert.Equal(t, 2, got[0].ContradictionCount, "badge shows the group count")
	assert.InDelta(t, core.ComputeHonestyScore(20, core.ContradictionStats{Bundles: 1, Total: 3}), got[0].Score, 1e-12)
	assert.Equal(t, "", got[0].LabelEn)

	// Clean stance: score 1.0, zero counts, translations passed through.
	assert.Equal(t, 0, got[1].ContradictionCount)
	assert.InDelta(t, 1.0, got[1].Score, 1e-12)
	assert.Equal(t, "B-en", got[1].LabelEn)
	assert.Equal(t, "sb-en", got[1].SummaryEn)
}

func TestStanceScore(t *testing.T) {
	assert.Equal(t, 1.0, stanceScore(10, 0))
	assert.Equal(t, 1.0, stanceScore(0, 5), "no facts: benefit of the doubt")
	one := stanceScore(10, 1)
	three := stanceScore(10, 3)
	assert.Less(t, one, 1.0)
	assert.Less(t, three, one, "extras add a repetition penalty")
	assert.False(t, math.IsNaN(three))
}
