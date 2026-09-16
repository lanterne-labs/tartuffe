package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"digital-mirror/backend/util"
)

// fakePersonResolver serves a fixed person list; fuzzy matching runs the real
// util scorer over it, as the storage implementation does. tableLoads counts
// the full-table reads (GetAllPersonRecords).
type fakePersonResolver struct {
	persons    []PersonRecord
	err        error
	tableLoads int
}

// Exact lookup on the canonical person form, as the indexed SQL lookup does.
func (f *fakePersonResolver) GetPersonRecordByName(ctx context.Context, name string) (PersonRecord, bool, error) {
	if f.err != nil {
		return PersonRecord{}, false, f.err
	}
	key := util.NormalizePersonName(name)
	for _, p := range f.persons {
		if util.NormalizePersonName(p.Name) == key {
			return p, true, nil
		}
	}
	return PersonRecord{}, false, nil
}

func (f *fakePersonResolver) GetAllPersonRecords(ctx context.Context) ([]PersonRecord, error) {
	f.tableLoads++
	if f.err != nil {
		return nil, f.err
	}
	return f.persons, nil
}

func (f *fakePersonResolver) FindPersonsByFuzzyName(ctx context.Context, query string, threshold float64, maxResults int) ([]util.PersonMatch, error) {
	if f.err != nil {
		return nil, f.err
	}
	candidates := make([]struct {
		ID         int64
		Name       string
		EntityType string
	}, len(f.persons))
	for i, p := range f.persons {
		candidates[i].ID, candidates[i].Name, candidates[i].EntityType = p.ID, p.Name, p.EntityType
	}
	return util.FindBestMatches(query, candidates, threshold, maxResults), nil
}

func newFakeResolver() *fakePersonResolver {
	return &fakePersonResolver{persons: []PersonRecord{
		{ID: 1, Name: "marine le pen", EntityType: "human"},
		{ID: 2, Name: "jean-luc melenchon", EntityType: "human"},
		{ID: 3, Name: "rassemblement national", EntityType: "party"},
	}}
}

func TestResolvePersonIDExactMatch(t *testing.T) {
	ctx := context.Background()
	r := newFakeResolver()
	// Accents and case differ from the stored form: the indexed lookup hits
	// and the persons table is never loaded.
	got, err := ResolvePersonID(ctx, r, "Jean-Luc Mélenchon", ExactMatchOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != 2 || got.Name != "jean-luc melenchon" || got.EntityType != "human" {
		t.Fatalf("unexpected record: %+v", got)
	}
	if r.tableLoads != 0 {
		t.Fatalf("exact hit must not load the persons table, loaded %d times", r.tableLoads)
	}
}

// An article-insensitive match ("Marine Pen" for "marine le pen") is not an
// indexed hit; it still resolves exactly through the full-table scan.
func TestResolvePersonIDArticleInsensitiveMatchScansTable(t *testing.T) {
	ctx := context.Background()
	r := newFakeResolver()
	got, err := ResolvePersonID(ctx, r, "Marine Pen", ExactMatchOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != 1 {
		t.Fatalf("expected person 1, got %+v", got)
	}
	if r.tableLoads != 1 {
		t.Fatalf("article-insensitive match must come from one table scan, loaded %d times", r.tableLoads)
	}
}

func TestResolvePersonIDFuzzyFloor(t *testing.T) {
	ctx := context.Background()
	r := newFakeResolver()

	// "melenchon" is a whole-word match on "jean-luc melenchon" (score 0.8).
	got, err := ResolvePersonID(ctx, r, "melenchon", 0.8)
	if err != nil {
		t.Fatalf("expected the 0.8 word match to be accepted: %v", err)
	}
	if got.ID != 2 {
		t.Fatalf("expected person 2, got %+v", got)
	}

	// The same candidate is rejected when the floor is exact-only, and the
	// typed error carries it so a caller can offer it to the user.
	_, err = ResolvePersonID(ctx, r, "melenchon", ExactMatchOnly)
	var notFound *PersonNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *PersonNotFoundError, got %v", err)
	}
	if notFound.Name != "melenchon" || len(notFound.Candidates) == 0 || notFound.Candidates[0].Target.Name != "jean-luc melenchon" {
		t.Fatalf("unexpected not-found error: %+v", notFound)
	}
	if !strings.Contains(err.Error(), "jean-luc melenchon (0.80)") {
		t.Fatalf("error should list the closest candidate: %s", err)
	}
}

func TestResolvePersonIDNoCandidates(t *testing.T) {
	ctx := context.Background()
	_, err := ResolvePersonID(ctx, newFakeResolver(), "zzzz qqqq", 0.8)
	var notFound *PersonNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *PersonNotFoundError, got %v", err)
	}
	if len(notFound.Candidates) != 0 {
		t.Fatalf("expected no candidates, got %+v", notFound.Candidates)
	}
	if err.Error() != `person "zzzz qqqq" not found in database` {
		t.Fatalf("unexpected message: %s", err)
	}
}

func TestResolvePersonIDLookupError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")
	_, err := ResolvePersonID(ctx, &fakePersonResolver{err: boom}, "marine le pen", 0.8)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the lookup error to be wrapped, got %v", err)
	}
	var notFound *PersonNotFoundError
	if errors.As(err, &notFound) {
		t.Fatalf("a lookup failure must not read as not-found: %v", err)
	}
}

func TestResolvePersonsSkipsUnresolvedBlankAndDuplicates(t *testing.T) {
	ctx := context.Background()
	names := []string{" Marine Le Pen ", "", "nobody at all", "marine le pen", "Rassemblement National"}
	got, err := ResolvePersons(ctx, newFakeResolver(), names, ExactMatchOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 3 {
		t.Fatalf("expected persons [1 3], got %+v", got)
	}
}

func TestResolvePersonsAbortsOnLookupError(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("db down")
	_, err := ResolvePersons(ctx, &fakePersonResolver{err: boom}, []string{"marine le pen"}, ExactMatchOnly)
	if !errors.Is(err, boom) {
		t.Fatalf("expected the lookup error, got %v", err)
	}
}
