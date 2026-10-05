package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type reviewStorer struct {
	groups    []ClaimGroup
	members   map[int64][]FactForFold
	noOverlap [][2]int64 // fact pairs that do not meet the evidence rule; every other pair does
	ungrouped [][2]int64
}

func (r *reviewStorer) ListClaimGroups(ctx context.Context, since time.Time, onlyIDs []int64) ([]ClaimGroup, error) {
	return r.groups, nil
}
func (r *reviewStorer) GetGroupMembers(ctx context.Context, parentID int64) ([]FactForFold, error) {
	return r.members[parentID], nil
}
func (r *reviewStorer) LoadClaimSources(ctx context.Context, factIDs []int64, sourceTypes []string) (map[int64][]ClaimSource, error) {
	return nil, nil
}
func (r *reviewStorer) ListGroupEvidencePairs(ctx context.Context, pairs [][2]int64, rule GroupEvidence) ([][2]int64, error) {
	var out [][2]int64
	for _, p := range pairs {
		if !slices.Contains(r.noOverlap, p) && !slices.Contains(r.noOverlap, [2]int64{p[1], p[0]}) {
			out = append(out, p)
		}
	}
	return out, nil
}
func (r *reviewStorer) UngroupFact(ctx context.Context, parentID, factID int64) error {
	r.ungrouped = append(r.ungrouped, [2]int64{parentID, factID})
	return nil
}
func (r *reviewStorer) RefreshPersonScore(ctx context.Context, personID int64) error { return nil }

// prefixJudge: two texts are one occurrence when they share their first
// word; "err" makes the gate fail; an "(odd)" text is a compound the gate
// is borderline on (agrees only with "tax cut A").
type prefixJudge struct{}

func (prefixJudge) IsSameOccurrence(ctx context.Context, x, y ClaimEvidence) (OccurrenceVerdict, error) {
	a, b := x.Text, y.Text
	if a == "err" || b == "err" {
		return OccurrenceDifferent, errors.New("gate down")
	}
	if strings.Contains(a, "(odd)") || strings.Contains(b, "(odd)") {
		return verdictOf(a == "tax cut A" || b == "tax cut A"), nil
	}
	return verdictOf(strings.Fields(a)[0] == strings.Fields(b)[0]), nil
}

func TestReviewClaimGroups(t *testing.T) {
	store := &reviewStorer{
		groups: []ClaimGroup{
			{ParentID: 1, PersonName: "p", Text: "tax cut A"},
			{ParentID: 2, PersonName: "p", Text: "tax cut A"},
			{ParentID: 3, PersonName: "p", Text: "tax cut A"},
			{ParentID: 4, PersonName: "p", Text: "err"},
			{ParentID: 5, PersonName: "p", Text: "tax cut A"},
			{ParentID: 6, PersonName: "p", Text: "tax cut A"},
			{ParentID: 7, PersonName: "p", Text: "tax cut A"},
			{ParentID: 8, PersonName: "p", Text: "tax cut A"},
		},
		members: map[int64][]FactForFold{
			1: {{ID: 11, Text: "tax cut B"}},                                 // intact
			2: {{ID: 21, Text: "war must end"}, {ID: 22, Text: "tax cut C"}}, // trimmed: 21 out
			3: {{ID: 31, Text: "war must end"}},                              // dissolved
			4: {{ID: 41, Text: "tax cut"}},                                   // gate failure
			// Majority: the compound 52 gets in on a 1-1 tie (parent yes, 51 no);
			// 53 then agrees with the parent and 51 and fails only against 52.
			5: {{ID: 51, Text: "tax cut B"}, {ID: 52, Text: "tax cut (odd)"}, {ID: 53, Text: "tax cut D"}},
			6: {{ID: 61, Text: "tax cut E"}},
			// No evidence: out without a gate call ("err" would fail the gate).
			7: {{ID: 71, Text: "err"}},
			// 81 joined under an earlier parent: its quote overlaps 82, not
			// the current parent. It stays.
			8: {{ID: 81, Text: "tax cut B"}, {ID: 82, Text: "tax cut C"}},
		},
		noOverlap: [][2]int64{{7, 71}, {8, 81}},
	}

	st, err := ReviewClaimGroups(context.Background(), store, prefixJudge{}, GroupReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := GroupReviewStats{Groups: 8, Intact: 4, Trimmed: 1, Dissolved: 2, GateFailures: 1, NoEvidence: 1}
	if st != want {
		t.Fatalf("dry run stats = %+v, want %+v", st, want)
	}
	if len(store.ungrouped) != 0 {
		t.Fatalf("a dry run must not write: %v", store.ungrouped)
	}

	st, err = ReviewClaimGroups(context.Background(), store, prefixJudge{}, GroupReviewOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Ungrouped != 3 || !slices.Equal(store.ungrouped, [][2]int64{{2, 21}, {3, 31}, {7, 71}}) {
		t.Fatalf("apply: ungrouped=%v stats=%+v, want 21 out of group 2, 31 out of group 3, 71 out of group 7", store.ungrouped, st)
	}
}
