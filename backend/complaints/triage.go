package complaints

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// Storer is the subset of storage operations triage needs. Defined as an
// interface so tests can supply a fake.
type Storer interface {
	GetFactByID(ctx context.Context, factID int64) (*core.FactForDedup, error)
	GetFactSourceURLs(ctx context.Context, factID int64) ([]string, error)
	DeleteFact(ctx context.Context, factID int64) error

	GetContradictionByID(ctx context.Context, id int64) (*storage.ContradictionWithFacts, error)
	DeleteContradiction(ctx context.Context, id int64) error

	GetStanceDocumentByID(ctx context.Context, id int64) (*core.StanceDocument, error)
	GetFactsForStance(ctx context.Context, stanceID int64) ([]core.StanceFactInfo, error)
	DeleteStanceDocument(ctx context.Context, id int64) error
}

// TallyDeleter is the Tally-side surface triage needs. The full TallyClient
// satisfies it; tests can use a fake.
type TallyDeleter interface {
	DeleteSubmission(ctx context.Context, submissionID string) error
}

// Outcome is the per-complaint result of a triage pass.
type Outcome struct {
	Complaint Complaint
	Verdict   Verdict
	Reason    string
	// TargetDeleted is true when a fact / contradiction / stance was actually
	// removed from the DB (false on dry-run, dismiss, ambiguous, or error).
	TargetDeleted bool
	// ComplaintDeleted is true when the Tally submission was removed (false on
	// dry-run, ambiguous, or if the delete call failed).
	ComplaintDeleted bool
	Err              error
}

// Report aggregates outcomes from one triage run.
type Report struct {
	Outcomes []Outcome
}

// Counts returns per-bucket counts. Errors are counted separately and may
// overlap with verdict buckets when the verdict was made but the apply step
// failed.
type Counts struct {
	FactsDeleted          int
	ContradictionsDeleted int
	StancesDeleted        int
	ComplaintsDismissed   int
	Ambiguous             int
	Errors                int
	Total                 int
}

// Counts walks the report and tallies outcomes.
func (r Report) Counts() Counts {
	var c Counts
	for _, o := range r.Outcomes {
		c.Total++
		if o.Err != nil {
			c.Errors++
		}
		switch o.Verdict {
		case VerdictDeleteTarget:
			if o.TargetDeleted {
				switch o.Complaint.Kind {
				case KindFact:
					c.FactsDeleted++
				case KindContradiction:
					c.ContradictionsDeleted++
				case KindStance:
					c.StancesDeleted++
				}
			}
		case VerdictDismissComplaint:
			if o.ComplaintDeleted {
				c.ComplaintsDismissed++
			}
		case VerdictAmbiguous:
			c.Ambiguous++
		}
	}
	return c
}

// Triage iterates complaints, builds a kind-specific prompt, asks the judge,
// and (unless dryRun) applies the verdict. Each kind has its own handler so
// the dispatch is explicit — no shared "entity" abstraction.
func Triage(ctx context.Context, store Storer, tally TallyDeleter, judge Judge, complaints []Complaint, dryRun bool) Report {
	report := Report{Outcomes: make([]Outcome, 0, len(complaints))}
	for _, c := range complaints {
		var outcome Outcome
		switch c.Kind {
		case KindFact:
			outcome = triageFactComplaint(ctx, store, tally, judge, c, dryRun)
		case KindContradiction:
			outcome = triageContradictionComplaint(ctx, store, tally, judge, c, dryRun)
		case KindStance:
			outcome = triageStanceComplaint(ctx, store, tally, judge, c, dryRun)
		default:
			outcome = Outcome{Complaint: c, Err: fmt.Errorf("unknown kind %q", c.Kind)}
		}
		report.Outcomes = append(report.Outcomes, outcome)
	}
	return report
}

// --- per-kind handlers ---

func triageFactComplaint(ctx context.Context, store Storer, tally TallyDeleter, judge Judge, c Complaint, dryRun bool) Outcome {
	out := Outcome{Complaint: c}

	fact, err := store.GetFactByID(ctx, c.FactID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return staleTarget(ctx, tally, c, "fact no longer exists in database", dryRun)
		}
		out.Err = fmt.Errorf("load fact %d: %w", c.FactID, err)
		return out
	}

	urls, err := store.GetFactSourceURLs(ctx, c.FactID)
	if err != nil {
		log.Warn().Err(err).Int64("fact_id", c.FactID).Msg("Failed to load fact sources for triage prompt")
		// Continue with empty URLs — the LLM can still judge from the text.
	}

	prompt := buildFactPrompt(fact, urls, c.Description)
	verdict, err := judge.Judge(ctx, prompt)
	if err != nil {
		out.Err = fmt.Errorf("judge fact complaint: %w", err)
		return out
	}
	out.Verdict = verdict.Verdict
	out.Reason = verdict.Reason

	if dryRun {
		return out
	}

	switch verdict.Verdict {
	case VerdictDeleteTarget:
		if err := store.DeleteFact(ctx, c.FactID); err != nil {
			out.Err = fmt.Errorf("delete fact %d: %w", c.FactID, err)
			return out
		}
		out.TargetDeleted = true
		if err := tally.DeleteSubmission(ctx, c.SubmissionID); err != nil {
			out.Err = fmt.Errorf("delete tally submission after fact delete: %w", err)
			return out
		}
		out.ComplaintDeleted = true
	case VerdictDismissComplaint:
		if err := tally.DeleteSubmission(ctx, c.SubmissionID); err != nil {
			out.Err = fmt.Errorf("delete tally submission: %w", err)
			return out
		}
		out.ComplaintDeleted = true
	case VerdictAmbiguous:
		// Leave both for human follow-up.
	}
	return out
}

func triageContradictionComplaint(ctx context.Context, store Storer, tally TallyDeleter, judge Judge, c Complaint, dryRun bool) Outcome {
	out := Outcome{Complaint: c}

	contr, err := store.GetContradictionByID(ctx, c.ContradictionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return staleTarget(ctx, tally, c, "contradiction no longer exists in database", dryRun)
		}
		out.Err = fmt.Errorf("load contradiction %d: %w", c.ContradictionID, err)
		return out
	}

	prompt := buildContradictionPrompt(contr, c.Description)
	verdict, err := judge.Judge(ctx, prompt)
	if err != nil {
		out.Err = fmt.Errorf("judge contradiction complaint: %w", err)
		return out
	}
	out.Verdict = verdict.Verdict
	out.Reason = verdict.Reason

	if dryRun {
		return out
	}

	switch verdict.Verdict {
	case VerdictDeleteTarget:
		if err := store.DeleteContradiction(ctx, c.ContradictionID); err != nil {
			out.Err = fmt.Errorf("delete contradiction %d: %w", c.ContradictionID, err)
			return out
		}
		out.TargetDeleted = true
		if err := tally.DeleteSubmission(ctx, c.SubmissionID); err != nil {
			out.Err = fmt.Errorf("delete tally submission after contradiction delete: %w", err)
			return out
		}
		out.ComplaintDeleted = true
	case VerdictDismissComplaint:
		if err := tally.DeleteSubmission(ctx, c.SubmissionID); err != nil {
			out.Err = fmt.Errorf("delete tally submission: %w", err)
			return out
		}
		out.ComplaintDeleted = true
	case VerdictAmbiguous:
	}
	return out
}

func triageStanceComplaint(ctx context.Context, store Storer, tally TallyDeleter, judge Judge, c Complaint, dryRun bool) Outcome {
	out := Outcome{Complaint: c}

	stance, err := store.GetStanceDocumentByID(ctx, c.StanceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return staleTarget(ctx, tally, c, "stance no longer exists in database", dryRun)
		}
		out.Err = fmt.Errorf("load stance %d: %w", c.StanceID, err)
		return out
	}

	facts, err := store.GetFactsForStance(ctx, c.StanceID)
	if err != nil {
		log.Warn().Err(err).Int64("stance_id", c.StanceID).Msg("Failed to load facts for stance triage prompt")
	}

	prompt := buildStancePrompt(stance, facts, c.Description)
	verdict, err := judge.Judge(ctx, prompt)
	if err != nil {
		out.Err = fmt.Errorf("judge stance complaint: %w", err)
		return out
	}
	out.Verdict = verdict.Verdict
	out.Reason = verdict.Reason

	if dryRun {
		return out
	}

	switch verdict.Verdict {
	case VerdictDeleteTarget:
		if err := store.DeleteStanceDocument(ctx, c.StanceID); err != nil {
			out.Err = fmt.Errorf("delete stance %d: %w", c.StanceID, err)
			return out
		}
		out.TargetDeleted = true
		if err := tally.DeleteSubmission(ctx, c.SubmissionID); err != nil {
			out.Err = fmt.Errorf("delete tally submission after stance delete: %w", err)
			return out
		}
		out.ComplaintDeleted = true
	case VerdictDismissComplaint:
		if err := tally.DeleteSubmission(ctx, c.SubmissionID); err != nil {
			out.Err = fmt.Errorf("delete tally submission: %w", err)
			return out
		}
		out.ComplaintDeleted = true
	case VerdictAmbiguous:
	}
	return out
}

// staleTarget handles the case where the complained-about row is already gone.
// We just drop the complaint as a dismiss with a synthetic reason.
func staleTarget(ctx context.Context, tally TallyDeleter, c Complaint, reason string, dryRun bool) Outcome {
	out := Outcome{
		Complaint: c,
		Verdict:   VerdictDismissComplaint,
		Reason:    reason,
	}
	if dryRun {
		return out
	}
	if err := tally.DeleteSubmission(ctx, c.SubmissionID); err != nil {
		out.Err = fmt.Errorf("delete stale tally submission: %w", err)
		return out
	}
	out.ComplaintDeleted = true
	return out
}

// --- prompt builders ---

const verdictInstruction = `Decide one of three verdicts:
- "delete_target": the user's complaint is correct; the database row is wrong, fabricated, misattributed, or otherwise should be removed.
- "dismiss_complaint": the user's complaint is mistaken; the database row is well-supported by its sources and should stay.
- "ambiguous": the complaint has merit but isn't clear-cut; keep both for human review.

Respond strictly as a JSON object with no markdown, prose, or commentary:
{"reason": "<one short sentence>", "verdict": "delete_target" | "dismiss_complaint" | "ambiguous"}`

// factRules are the rules of the fact and contradiction prompts: what a fact
// of a person is, and who carries the burden of proof.
const factRules = `Rules:
- A fact of a person is the person's own position, claim or act, or an authoritative finding about the person (a court ruling, a vote). The opinion of other people about the person is not a fact of the person: a complaint about such a row is delete_target.
- The burden of proof is on the complaint. A complaint that only denies a sourced fact, with no evidence, is dismiss_complaint: the source stands.

`

func buildFactPrompt(fact *core.FactForDedup, urls []string, description string) string {
	var b strings.Builder
	b.WriteString("You are reviewing a user complaint about a FACT stored in a fact-checking database for political figures.\n\n")
	b.WriteString(factRules)
	fmt.Fprintf(&b, "Fact #%d: %q\n", fact.ID, fact.NormalizedFactText)
	if fact.OriginalQuote != "" {
		fmt.Fprintf(&b, "Original quote: %q\n", fact.OriginalQuote)
	}
	if len(urls) > 0 {
		b.WriteString("Sources:\n")
		for _, u := range urls {
			fmt.Fprintf(&b, "  - %s\n", u)
		}
	} else {
		b.WriteString("Sources: none recorded.\n")
	}
	b.WriteString("\nThe user reports the following problem with this fact:\n")
	fmt.Fprintf(&b, "%q\n\n", description)
	b.WriteString(verdictInstruction)
	return b.String()
}

func buildContradictionPrompt(contr *storage.ContradictionWithFacts, description string) string {
	var b strings.Builder
	b.WriteString("You are reviewing a user complaint about a CONTRADICTION recorded between two facts in a fact-checking database.\n\n")
	b.WriteString(factRules)
	fmt.Fprintf(&b, "Contradiction #%d.\n", contr.ID)
	fmt.Fprintf(&b, "Fact A (#%d, dated %s): %q\n", contr.FactAID, formatDate(contr.FactADate), contr.FactAText)
	if contr.FactAQuote != "" {
		fmt.Fprintf(&b, "  Source quote for A: %q\n", contr.FactAQuote)
	}
	if len(contr.FactASourceURLs) > 0 {
		fmt.Fprintf(&b, "  Sources for A: %s\n", strings.Join(contr.FactASourceURLs, ", "))
	}
	fmt.Fprintf(&b, "Fact B (#%d, dated %s): %q\n", contr.FactBID, formatDate(contr.FactBDate), contr.FactBText)
	if contr.FactBQuote != "" {
		fmt.Fprintf(&b, "  Source quote for B: %q\n", contr.FactBQuote)
	}
	if len(contr.FactBSourceURLs) > 0 {
		fmt.Fprintf(&b, "  Sources for B: %s\n", strings.Join(contr.FactBSourceURLs, ", "))
	}
	fmt.Fprintf(&b, "Detection reason recorded by the system: %q\n\n", contr.Reason)
	b.WriteString("The user reports the following problem with this contradiction (e.g. that the two facts don't actually contradict, or that one of the underlying facts is wrong):\n")
	fmt.Fprintf(&b, "%q\n\n", description)
	b.WriteString("If the user is right that there is no real contradiction, choose delete_target — the contradiction record will be removed but the underlying facts will be kept.\n\n")
	b.WriteString(verdictInstruction)
	return b.String()
}

func buildStancePrompt(stance *core.StanceDocument, facts []core.StanceFactInfo, description string) string {
	var b strings.Builder
	b.WriteString("You are reviewing a user complaint about a STANCE — a clustered group of facts representing a person's position on a topic — in a fact-checking database.\n\n")
	fmt.Fprintf(&b, "Stance #%d: %q\n", stance.ID, stance.ClusterLabel)
	if stance.Summary != "" {
		fmt.Fprintf(&b, "Summary: %s\n", stance.Summary)
	}
	fmt.Fprintf(&b, "Fact count: %d\n", stance.FactCount)
	if len(facts) > 0 {
		b.WriteString("Linked facts (sample):\n")
		max := len(facts)
		if max > 10 {
			max = 10
		}
		for i := 0; i < max; i++ {
			fmt.Fprintf(&b, "  - #%d: %q\n", facts[i].ID, facts[i].NormalizedFactText)
		}
		if len(facts) > max {
			fmt.Fprintf(&b, "  …and %d more.\n", len(facts)-max)
		}
	}
	b.WriteString("\nThe user reports the following problem with this stance (e.g. that the label/summary mischaracterizes the linked facts, or the cluster groups unrelated positions):\n")
	fmt.Fprintf(&b, "%q\n\n", description)
	b.WriteString("delete_target removes only the stance grouping; the underlying facts are kept. Choose it when the cluster itself is wrong (mislabeled, mixed topics, etc.).\n\n")
	b.WriteString(verdictInstruction)
	return b.String()
}

func formatDate(t *time.Time) string {
	if t == nil {
		return "unknown"
	}
	return t.Format("2006-01-02")
}
