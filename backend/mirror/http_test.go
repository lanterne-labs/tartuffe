package mirror

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"testing"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetPersons_MinFactsFloorAndPagination(t *testing.T) {
	name := "party"
	fs := &fakeStore{ranked: []storage.PersonSummary{{ID: 7, Name: "X", EntityType: &name, HonestyScore: 0.9, FactCount: 12}}}
	h := routerFor(fs)

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
	fs := &fakeStore{
		members: []storage.MemberInfo{{ID: 3, Slug: "dep", Name: "Dep", EntityType: "human", HonestyScore: 0.5, ImageURL: &img}},
		slugs:   map[string]int64{"la-parti": 1},
	}
	h := routerFor(fs)

	rec := get(t, h, "/api/person/1/members")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `[{"id":3,"slug":"dep","name":"Dep","entity_type":"human","honesty_score":0.5,"image_url":"https://img/x.png"}]`, rec.Body.String())

	// A person is named by id or by slug.
	rec = get(t, h, "/api/person/la-parti/members")
	require.Equal(t, http.StatusOK, rec.Code, "by slug")
	assert.JSONEq(t, `[{"id":3,"slug":"dep","name":"Dep","entity_type":"human","honesty_score":0.5,"image_url":"https://img/x.png"}]`, rec.Body.String())

	rec = get(t, h, "/api/person/nobody/members")
	assert.Equal(t, http.StatusNotFound, rec.Code, "an unknown slug")

	rec = get(t, h, "/api/person/Abc!/members")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "neither an id nor a slug")

	fs.membersErr = errors.New("boom")
	rec = get(t, h, "/api/person/1/members")
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "Internal server error\n", rec.Body.String(), "internal details must not leak")
}

func TestGetPersonStances_ScoreAndCounts(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeStore{
		stances: []core.StanceDocument{
			{ID: 10, ClusterLabel: "A", Summary: "sa", FactCount: 20, LastUpdatedAt: now},
			{ID: 11, ClusterLabel: "B", LabelEn: "B-en", Summary: "sb", SummaryEn: "sb-en", FactCount: 5, LastUpdatedAt: now},
		},
		stanceCounts: map[int64]int{10: 3},
		groupCounts:  map[int64]int{10: 2},
	}
	rec := get(t, routerFor(fs), "/api/person/1/stances")
	require.Equal(t, http.StatusOK, rec.Code)

	var got []StanceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got, 2)

	// Stance with contradictions: one bundle + 2 extras, per stanceScore.
	assert.Equal(t, 2, got[0].ContradictionCount, "badge shows the group count")
	assert.InDelta(t, core.ComputeHonestyScore(20, core.ContradictionStats{Bundles: 1, Total: 3}, 0), got[0].Score, 1e-12)
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

// TestGetPersons_CandidateFilter: the candidates, the others, or everyone.
func TestGetPersons_CandidateFilter(t *testing.T) {
	fs := &fakeStore{}
	h := routerFor(fs)
	get(t, h, "/api/persons")
	assert.Equal(t, "", fs.rankedArgs.candidate)
	get(t, h, "/api/persons?candidate=true")
	assert.Equal(t, "true", fs.rankedArgs.candidate)
	get(t, h, "/api/persons?candidate=false")
	assert.Equal(t, "false", fs.rankedArgs.candidate)
	assert.Equal(t, http.StatusBadRequest, get(t, h, "/api/persons?candidate=maybe").Code)
}

// TestPodium: without a week (or for the current one) the live podium; for a
// past week its snapshot; the past weeks are listed latest first.
func TestPodium(t *testing.T) {
	current := storage.WeekStart(time.Now())
	last := current.AddDate(0, 0, -7).Format(time.DateOnly)
	older := current.AddDate(0, 0, -14).Format(time.DateOnly)
	fs := &fakeStore{
		livePodium: []storage.PodiumEntry{{Rank: 1, ID: 1, Slug: "a", Name: "A", HonestyScore: 0.2, FactCount: 900}},
		podiums: map[string][]storage.PodiumEntry{
			last:  {{Rank: 1, ID: 2, Slug: "b", Name: "B", HonestyScore: 0.3, FactCount: 800}},
			older: {{Rank: 1, ID: 3, Slug: "c", Name: "C", HonestyScore: 0.4, FactCount: 700}},
		},
	}
	h := routerFor(fs)

	var got podiumResponse
	rec := get(t, h, "/api/podium")
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.True(t, got.Live)
	assert.Equal(t, current.Format(time.DateOnly), got.Week)
	assert.Equal(t, []string{last, older}, got.Weeks)
	require.Len(t, got.Persons, 1)
	assert.Equal(t, int64(1), got.Persons[0].ID)

	// Any day of a past week names it.
	rec = get(t, h, "/api/podium?week="+current.AddDate(0, 0, -5).Format(time.DateOnly))
	require.Equal(t, http.StatusOK, rec.Code)
	got = podiumResponse{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.False(t, got.Live)
	assert.Equal(t, last, got.Week)
	require.Len(t, got.Persons, 1)
	assert.Equal(t, int64(2), got.Persons[0].ID)

	// A week ahead is the live podium; a bad date is refused.
	rec = get(t, h, "/api/podium?week="+current.AddDate(0, 0, 21).Format(time.DateOnly))
	got = podiumResponse{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.True(t, got.Live)
	assert.Equal(t, http.StatusBadRequest, get(t, h, "/api/podium?week=last").Code)
}
