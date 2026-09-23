package core

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
)

// ClaimGroup is a group parent (an active fact) and the person it belongs to.
type ClaimGroup struct {
	ParentID   int64
	PersonID   int64
	PersonName string
	Text       string
}

// GroupReviewStorer is the storage view the claim-group review needs.
// *storage.PostgresStorer implements it.
type GroupReviewStorer interface {
	// ListClaimGroups returns the groups that gained a member at or after
	// since, oldest parent first; onlyIDs, when non-empty, restricts the
	// listing to those parents.
	ListClaimGroups(ctx context.Context, since time.Time, onlyIDs []int64) ([]ClaimGroup, error)
	// GetGroupMembers returns the parent's members (id and text).
	GetGroupMembers(ctx context.Context, parentID int64) ([]FactForFold, error)
	// UngroupFact makes a member active again, in the parent's stance.
	UngroupFact(ctx context.Context, parentID, factID int64) error
	RefreshPersonScore(ctx context.Context, personID int64) error
}

// GroupReviewOptions tunes ReviewClaimGroups.
type GroupReviewOptions struct {
	Since time.Time
	// OnlyIDs restricts the review to these group parents.
	OnlyIDs []int64
	// Apply un-groups the members that no longer belong. Without it the
	// review only reports.
	Apply bool
}

// GroupReviewStats are the counters of one review.
type GroupReviewStats struct {
	Groups       int64
	Intact       int64 // every member still belongs
	Trimmed      int64 // some members un-grouped, the group keeps at least one
	Dissolved    int64 // no member belongs: every member un-grouped
	Ungrouped    int64 // members made active again (Apply)
	GateFailures int64 // groups skipped because a gate call failed
}

// ReviewClaimGroups re-judges groups with the current same-position gate.
// The parent — an actual statement — anchors the group; members are taken
// in id order and each stays when the gate confirms it against at least as
// many kept facts as it rejects. A majority, not a clique: an over-grouped
// member (a different claim) fails against every kept fact, while a member
// the gate is borderline on against ONE sibling still agrees with the rest.
// A "different" verdict only counts when it holds with the two statements
// swapped (order-swap agreement, as the auditor's eviction gate). Members
// that fall out are un-grouped; nothing is deleted. A gate error skips the
// group: it is never trimmed on partial information.
func ReviewClaimGroups(ctx context.Context, storer GroupReviewStorer, judge SamePositionJudge, opts GroupReviewOptions) (GroupReviewStats, error) {
	var st GroupReviewStats
	groups, err := storer.ListClaimGroups(ctx, opts.Since, opts.OnlyIDs)
	if err != nil {
		return st, fmt.Errorf("listing claim groups: %w", err)
	}
	log.Info().Int("groups", len(groups)).Time("since", opts.Since).Bool("apply", opts.Apply).Msg("Claim group review started")

	for _, g := range groups {
		members, err := storer.GetGroupMembers(ctx, g.ParentID)
		if err != nil {
			return st, fmt.Errorf("loading members of group %d: %w", g.ParentID, err)
		}
		st.Groups++
		sort.Slice(members, func(a, b int) bool { return members[a].ID < members[b].ID })

		kept := []FactForFold{{ID: g.ParentID, Text: g.Text}}
		var out []FactForFold
		failed := false
	members:
		for _, f := range members {
			agree, disagree := 0, 0
			for _, k := range kept {
				same, err := judge.IsSamePosition(ctx, g.PersonName, k.Text, f.Text)
				if err == nil && !same {
					same, err = judge.IsSamePosition(ctx, g.PersonName, f.Text, k.Text)
				}
				if err != nil {
					log.Warn().Err(err).Int64("parent_fact_id", g.ParentID).Msg("Claim group review: gate failed; group skipped")
					failed = true
					break members
				}
				if same {
					agree++
				} else {
					disagree++
				}
			}
			if disagree > agree {
				out = append(out, f)
				continue
			}
			kept = append(kept, f)
		}
		if failed {
			st.GateFailures++
			continue
		}
		switch {
		case len(out) == 0:
			st.Intact++
			continue
		case len(kept) == 1:
			st.Dissolved++
		default:
			st.Trimmed++
		}
		for _, f := range out {
			log.Info().Int64("parent_fact_id", g.ParentID).Int64("fact_id", f.ID).Str("person", g.PersonName).
				Str("fact_text", f.Text).Str("parent_text", g.Text).Bool("apply", opts.Apply).
				Msg("Claim group review: member no longer belongs")
		}
		if !opts.Apply {
			continue
		}
		for _, f := range out {
			if err := storer.UngroupFact(ctx, g.ParentID, f.ID); err != nil {
				return st, fmt.Errorf("un-grouping fact %d from %d: %w", f.ID, g.ParentID, err)
			}
			st.Ungrouped++
		}
		if err := storer.RefreshPersonScore(ctx, g.PersonID); err != nil {
			log.Warn().Err(err).Int64("person_id", g.PersonID).Msg("Failed to refresh fact count")
		}
	}

	log.Info().Int64("groups", st.Groups).Int64("intact", st.Intact).Int64("trimmed", st.Trimmed).
		Int64("dissolved", st.Dissolved).Int64("ungrouped", st.Ungrouped).Int64("gate_failures", st.GateFailures).
		Msg("Claim group review finished")
	return st, nil
}
