package sweep

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"digital-mirror/backend/entityvalidator"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rejectNamed rejects the listed names, defers everything else (so the
// validator's DefaultDecision decides).
type rejectNamed struct {
	junk map[string]bool
	mu   sync.Mutex
	seen []string
}

func (f *rejectNamed) Name() string { return "stub" }

func (f *rejectNamed) Check(_ context.Context, c entityvalidator.Candidate) (entityvalidator.FilterResult, error) {
	f.mu.Lock()
	f.seen = append(f.seen, c.Name)
	f.mu.Unlock()
	if f.junk[c.Name] {
		return entityvalidator.FilterResult{Decision: entityvalidator.Reject, Reason: "junk"}, nil
	}
	return entityvalidator.FilterResult{Decision: entityvalidator.Defer}, nil
}

type fakeStorer struct {
	persons    []storage.PersonInfo
	personsErr error
	counts     map[int64]int
	countsErr  error
	deleteErr  map[int64]error
	deleted    []int64
}

func (f *fakeStorer) GetAllPersons(context.Context) ([]storage.PersonInfo, error) {
	return f.persons, f.personsErr
}

func (f *fakeStorer) GetFactCountsByPersonIDs(context.Context, []int64) (map[int64]int, error) {
	return f.counts, f.countsErr
}

func (f *fakeStorer) DeletePersonByID(_ context.Context, id int64) error {
	if err := f.deleteErr[id]; err != nil {
		return err
	}
	f.deleted = append(f.deleted, id)
	return nil
}

func persons() []storage.PersonInfo {
	return []storage.PersonInfo{
		{ID: 1, Name: "Alice", EntityType: "human"},
		{ID: 2, Name: "Bookstore", EntityType: "organization"},
		{ID: 3, Name: "Carol", EntityType: "human"},
		{ID: 4, Name: "Dave", EntityType: "human"},
	}
}

func TestRun_HasFactsGuardAndRejectDefault(t *testing.T) {
	filter := &rejectNamed{junk: map[string]bool{"Bookstore": true}}
	v := entityvalidator.New(filter)
	v.DefaultDecision = entityvalidator.Reject
	fs := &fakeStorer{persons: persons(), counts: map[int64]int{1: 12}}

	report, err := Run(context.Background(), fs, v, Options{Concurrency: 3, OnlyWithoutFacts: true})
	require.NoError(t, err)

	assert.Equal(t, 1, report.Skipped, "Alice has facts")
	assert.Equal(t, 3, report.Validated)
	assert.Equal(t, 0, report.Approved, "deferred candidates fall to Reject")
	assert.Equal(t, 3, report.Rejected)
	require.Len(t, report.Rows, 4)
	assert.True(t, report.Rows[0].Skipped)
	assert.Equal(t, "has 12 existing facts", report.Rows[0].SkipReason)
	assert.Equal(t, "stub", report.Rows[1].Filter)
	assert.Equal(t, "default", report.Rows[2].Filter)
	assert.NotContains(t, filter.seen, "Alice", "skipped rows never reach the validator")

	rejected := report.RejectedRows()
	ids := make([]int64, 0, len(rejected))
	for _, r := range rejected {
		ids = append(ids, r.ID)
	}
	assert.Equal(t, []int64{2, 3, 4}, ids)
}

func TestRun_LimitAndApprove(t *testing.T) {
	filter := &rejectNamed{junk: map[string]bool{"Bookstore": true}}
	v := entityvalidator.New(filter) // DefaultDecision Approve
	fs := &fakeStorer{persons: persons()}

	report, err := Run(context.Background(), fs, v, Options{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, report.Rows, 2)
	assert.Equal(t, 1, report.Approved)
	assert.Equal(t, 1, report.Rejected)
	assert.Equal(t, 0, report.Skipped)
	assert.Len(t, filter.seen, 2)
}

// A fact-count failure aborts the sweep: validating everyone as zero-facts
// would spend the search and LLM credits the skip exists to protect.
func TestRun_FactCountErrorAborts(t *testing.T) {
	filter := &rejectNamed{}
	fs := &fakeStorer{persons: persons(), countsErr: errors.New("db down")}
	_, err := Run(context.Background(), fs, entityvalidator.New(filter), Options{OnlyWithoutFacts: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fact counts")
	assert.Empty(t, filter.seen, "no person must be validated when the skip list cannot be built")
}

func TestRun_ListError(t *testing.T) {
	_, err := Run(context.Background(), &fakeStorer{personsErr: errors.New("boom")}, entityvalidator.New(), Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list persons")
}

func TestDeleteRejected_ContinuesOnFailure(t *testing.T) {
	fs := &fakeStorer{deleteErr: map[int64]error{3: errors.New("fk")}}
	stats := DeleteRejected(context.Background(), fs, []Row{{ID: 2}, {ID: 3}, {ID: 4}})
	assert.Equal(t, DeleteStats{Deleted: 2, Failed: 1}, stats)
	assert.Equal(t, []int64{2, 4}, fs.deleted)
}

func TestWriteJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.jsonl")
	rows := []Row{
		{ID: 1, Name: "Alice", EntityType: "human", Skipped: true, SkipReason: "has 2 existing facts"},
		{ID: 2, Name: "Bookstore", EntityType: "organization", Real: false, Filter: "stub", Reason: "junk"},
	}
	require.NoError(t, WriteJSONL(path, rows))

	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	var got []Row
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r Row
		require.NoError(t, json.Unmarshal(sc.Bytes(), &r))
		got = append(got, r)
	}
	require.NoError(t, sc.Err())
	assert.Equal(t, rows, got)

	require.Error(t, WriteJSONL(filepath.Join(t.TempDir(), "missing", "x.jsonl"), rows))
}
