package mirror

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/accountstest"
	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
)

var do = accountstest.Do

// fakeStore records the arguments the handlers pass to storage and returns
// canned data; every method not exercised by a test returns zero values.
type fakeStore struct {
	history []storage.HistoryPoint

	rankedArgs struct {
		limit, offset, minFacts  int
		entityType, search, sort string
		candidate                string
	}
	ranked []storage.PersonSummary

	livePodium []storage.PodiumEntry
	podiums    map[string][]storage.PodiumEntry // by week, YYYY-MM-DD

	members    []storage.MemberInfo
	membersErr error

	// slugs maps a person's slug to their id (GetPersonIDBySlug).
	slugs map[string]int64

	stances      []core.StanceDocument
	stanceCounts map[int64]int
	groupCounts  map[int64]int

	reports []storage.UserReport
	// targets are the facts and contradictions that exist, as "kind:id".
	targets map[string]bool
	feed    []storage.FeedItem
	// lastFeedFilter is the filter of the last GetFeed.
	lastFeedFilter storage.FeedFilter
	share          map[string]*storage.ShareTarget
	scores         map[int64]*storage.ScoreExplanation
	// likes are the users who liked each target, as "kind:id".
	likes map[string]map[int64]bool
}

func (f *fakeStore) GetPersonsRankedByScore(_ context.Context, limit, offset int, entityType, search, sort string, minFacts int, candidate string) ([]storage.PersonSummary, error) {
	f.rankedArgs.candidate = candidate
	f.rankedArgs.limit, f.rankedArgs.offset, f.rankedArgs.minFacts = limit, offset, minFacts
	f.rankedArgs.entityType, f.rankedArgs.search, f.rankedArgs.sort = entityType, search, sort
	return f.ranked, nil
}

func (f *fakeStore) GetPersonByID(context.Context, int64) (*storage.PersonSummary, error) {
	return nil, nil
}

func (f *fakeStore) GetPersonIDBySlug(_ context.Context, slug string) (int64, error) {
	return f.slugs[slug], nil
}

func (f *fakeStore) GetPersonFacts(context.Context, int64, int, int) (*storage.PaginatedFacts, error) {
	return nil, nil
}

func (f *fakeStore) GetPersonContradictions(context.Context, int64, int, int) (*storage.PaginatedContradictions, error) {
	return nil, nil
}

func (f *fakeStore) GetPersonMembers(context.Context, int64) ([]storage.MemberInfo, error) {
	return f.members, f.membersErr
}

func (f *fakeStore) GetPersonHistory(context.Context, int64) ([]storage.HistoryPoint, error) {
	return f.history, nil
}

func (f *fakeStore) GetPersonCoverage(context.Context, int64) (*storage.Coverage, error) {
	return &storage.Coverage{Years: []storage.CoverageYear{}}, nil
}

func (f *fakeStore) GetFactPersons(context.Context, int64, int, int) ([]storage.PersonSummary, error) {
	return nil, nil
}

func (f *fakeStore) GetStanceDocumentsForPerson(context.Context, int64) ([]core.StanceDocument, error) {
	return f.stances, nil
}

func (f *fakeStore) GetStanceContradictionCounts(context.Context, int64) (map[int64]int, error) {
	return f.stanceCounts, nil
}

func (f *fakeStore) GetStanceGroupCounts(context.Context, int64) (map[int64]int, error) {
	return f.groupCounts, nil
}

func (f *fakeStore) GetStanceContradictionGroups(context.Context, int64, int64) ([]storage.ContradictionGroup, error) {
	return nil, nil
}

func (f *fakeStore) GetFactsInStance(context.Context, int64, int64) ([]storage.FactInStance, error) {
	return nil, nil
}

func (f *fakeStore) GetMaxEventID(context.Context) (int64, error) { return 0, nil }

func (f *fakeStore) GetEventsAfter(context.Context, int64, int) ([]storage.Event, error) {
	return nil, nil
}

// newFixture is an empty store and the router of the mirror with the
// accounts (noop).
func newFixture() (*fakeStore, http.Handler) {
	fs := &fakeStore{}
	acc := accounts.NewService(accountstest.NewMemStore(), providers.NewRegistry(providers.Noop{}))
	return fs, accountstest.Router(acc, NewService(fs, acc).Routes)
}

// routerFor is the router of the mirror on the store.
func routerFor(fs *fakeStore) http.Handler {
	acc := accounts.NewService(accountstest.NewMemStore(), providers.NewRegistry(providers.Noop{}))
	return accountstest.Router(acc, NewService(fs, acc).Routes)
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func (f *fakeStore) LivePodium(context.Context) ([]storage.PodiumEntry, error) {
	return f.livePodium, nil
}

func (f *fakeStore) PodiumSnapshot(_ context.Context, week time.Time) ([]storage.PodiumEntry, error) {
	if e, ok := f.podiums[week.Format(time.DateOnly)]; ok {
		return e, nil
	}
	return []storage.PodiumEntry{}, nil
}

func (f *fakeStore) PodiumWeeks(_ context.Context, before time.Time) ([]time.Time, error) {
	weeks := []time.Time{}
	for k := range f.podiums {
		if w, _ := time.Parse(time.DateOnly, k); w.Before(before) {
			weeks = append(weeks, w)
		}
	}
	sort.Slice(weeks, func(i, j int) bool { return weeks[i].After(weeks[j]) })
	return weeks, nil
}
