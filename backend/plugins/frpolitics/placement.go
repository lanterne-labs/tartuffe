package frpolitics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"digital-mirror/backend/core"
)

// voteStancer is the part of the stance manager that places ballots.
type voteStancer interface {
	SubjectLabel(ctx context.Context, text string) (string, error)
	AssignStance(ctx context.Context, personID, factID int64, text string, embedding []float32) (core.StanceDecision, error)
	ApplyStanceDecision(ctx context.Context, personID, factID int64, text string, embedding []float32, d core.StanceDecision) (*core.StanceProcessResult, error)
}

// voteSubjectStore is the storage that the placement reads (*Store).
type voteSubjectStore interface {
	StoredVoteSubject(ctx context.Context, scope subjectScope) (string, error)
	VoteSubjectStances(ctx context.Context, subject string) (map[int64]int64, error)
}

// subjectScope is the set of ballots that share one vote subject: all the
// ballots of an ordinary law (key: the dossier URL, the source URL of each
// ballot), or the ballots of one scrutin of a budget law (key: the scrutin
// key, law_key).
type subjectScope struct {
	byScrutin bool
	key       string
}

// isBudgetLaw reports whether a scrutin title is on a budget law: a loi de
// finances (initial, rectificative, de fin de gestion) or a loi de
// financement de la sécurité sociale. A budget law covers many subjects, so
// each of its scrutins gets its own vote subject. The title is enough: a
// scrutin title names the text voted on ("l'amendement n° 12 de M. X à
// l'article 3 du projet de loi de finances pour 2026").
func isBudgetLaw(title string) bool {
	t := strings.ToLower(title)
	return strings.Contains(t, "loi de finances") || strings.Contains(t, "financement de la sécurité sociale")
}

// voteSubjectOf returns the scope of the ballots of one scrutin and the text
// that SubjectLabel reads for it: the voted object for a budget law, the
// bill for an ordinary law.
func voteSubjectOf(rec FactRecord) (subjectScope, string, error) {
	if isBudgetLaw(rec.LawTitle) {
		if rec.LawKey == "" {
			return subjectScope{}, "", fmt.Errorf("ballot %q of a budget law has no scrutin key", rec.FactText)
		}
		return subjectScope{byScrutin: true, key: rec.LawKey}, rec.EmbeddingText, nil
	}
	if rec.SourceURL == "" || strings.TrimSpace(rec.LawSummary) == "" {
		return subjectScope{}, "", fmt.Errorf("ballot of law %s has no dossier URL or no law summary", rec.LawKey)
	}
	return subjectScope{key: rec.SourceURL}, prepareLawPhrase(rec.LawSummary), nil
}

// votePlacer places the ballots of one scrutin in the stances of their
// voters (a core.StanceProcessor). The ballots of the scrutin share one vote
// subject. When a stance of the voter already holds a ballot with this
// subject, the ballot joins that stance without an LLM call. Otherwise the
// assignment (core AssignStance) decides, so a stance of statements on the
// subject can get the ballot. No ballot gets the stance analysis: VOTED_* is
// a direct claim, and the contradiction detection judges the vote pairs.
type votePlacer struct {
	stancer voteStancer
	subject string
	known   map[int64]int64 // person id → the stance with a ballot on subject
}

// newVotePlacer gets the vote subject of the scrutin of rec and the stances
// that already hold a ballot with this subject. The subject is computed once
// per scope: the subject stored on a ballot of the scope is used again, and
// SubjectLabel (1 LLM call) runs only when there is none. An LLM label is not
// stable across the scrutins of one law, so a label per scrutin would put
// the ballots of one law in different stances.
func newVotePlacer(ctx context.Context, store voteSubjectStore, stancer voteStancer, rec FactRecord) (*votePlacer, error) {
	scope, text, err := voteSubjectOf(rec)
	if err != nil {
		return nil, err
	}
	subject, err := store.StoredVoteSubject(ctx, scope)
	if err != nil {
		return nil, err
	}
	if subject == "" {
		if subject, err = stancer.SubjectLabel(ctx, text); err != nil {
			return nil, fmt.Errorf("vote subject of %q: %w", text, err)
		}
	}
	known, err := store.VoteSubjectStances(ctx, subject)
	if err != nil {
		return nil, err
	}
	return &votePlacer{stancer: stancer, subject: subject, known: known}, nil
}

// ProcessNewFact places one ballot and writes its stance membership.
func (p *votePlacer) ProcessNewFact(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, _ string, _ *time.Time) (*core.StanceProcessResult, error) {
	d := core.StanceDecision{StanceID: p.known[personID], Verdict: core.VerdictNewInfo}
	if d.StanceID == 0 {
		var err error
		if d, err = p.stancer.AssignStance(ctx, personID, factID, factText, embedding); err != nil {
			return nil, fmt.Errorf("assigning the ballot of person %d: %w", personID, err)
		}
	}
	return p.stancer.ApplyStanceDecision(ctx, personID, factID, factText, embedding, d)
}
