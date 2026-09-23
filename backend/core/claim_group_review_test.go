package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type reviewStorer struct {
	groups    []ClaimGroup
	members   map[int64][]FactForFold
	ungrouped [][2]int64
}

func (r *reviewStorer) ListClaimGroups(ctx context.Context, since time.Time, onlyIDs []int64) ([]ClaimGroup, error) {
	return r.groups, nil
}
func (r *reviewStorer) GetGroupMembers(ctx context.Context, parentID int64) ([]FactForFold, error) {
	return r.members[parentID], nil
}
func (r *reviewStorer) UngroupFact(ctx context.Context, parentID, factID int64) error {
	r.ungrouped = append(r.ungrouped, [2]int64{parentID, factID})
	return nil
}
func (r *reviewStorer) RefreshPersonScore(ctx context.Context, personID int64) error { return nil }

// prefixJudge: two texts are the same position when they share their first
// word; "err" makes the gate fail; an "(odd)" text is a compound the gate
// is borderline on (agrees only with "tax cut A"); a "(flip)" text is judged
// different only as the second statement (order-dependent).
type prefixJudge struct{}

func (prefixJudge) IsSamePosition(ctx context.Context, subject, a, b string) (bool, error) {
	if a == "err" || b == "err" {
		return false, errors.New("gate down")
	}
	if strings.Contains(b, "(flip)") {
		return false, nil
	}
	if strings.Contains(a, "(odd)") || strings.Contains(b, "(odd)") {
		return a == "tax cut A" || b == "tax cut A", nil
	}
	return strings.Fields(a)[0] == strings.Fields(b)[0], nil
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
		},
		members: map[int64][]FactForFold{
			1: {{ID: 11, Text: "tax cut B"}},                                 // intact
			2: {{ID: 21, Text: "war must end"}, {ID: 22, Text: "tax cut C"}}, // trimmed: 21 out
			3: {{ID: 31, Text: "war must end"}},                              // dissolved
			4: {{ID: 41, Text: "tax cut"}},                                   // gate failure
			// Majority: the compound 52 gets in on a 1-1 tie (parent yes, 51 no);
			// 53 then agrees with the parent and 51 and fails only against 52.
			5: {{ID: 51, Text: "tax cut B"}, {ID: 52, Text: "tax cut (odd)"}, {ID: 53, Text: "tax cut D"}},
			// Order-swap: rejected as the second statement, accepted as the first.
			6: {{ID: 61, Text: "tax cut (flip)"}},
		},
	}

	st, err := ReviewClaimGroups(context.Background(), store, prefixJudge{}, GroupReviewOptions{})
	if err != nil {
		t.Fatal(err)
	}
	want := GroupReviewStats{Groups: 6, Intact: 3, Trimmed: 1, Dissolved: 1, GateFailures: 1}
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
	if st.Ungrouped != 2 || len(store.ungrouped) != 2 || store.ungrouped[0] != [2]int64{2, 21} || store.ungrouped[1] != [2]int64{3, 31} {
		t.Fatalf("apply: ungrouped=%v stats=%+v, want 21 out of group 2 and 31 out of group 3", store.ungrouped, st)
	}
}
