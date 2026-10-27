package cmd

import (
	"bufio"
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	factsAuditPerson                                                      string
	factsAuditPersonID                                                    int64
	factsAuditFactID                                                      int64
	factsAuditMax                                                         int
	factsAuditRandom                                                      bool
	factsAuditFormat                                                      string
	factsAuditApply                                                       bool
	factsAuditYes                                                         bool
	factsAuditCreatedFrom, factsAuditCreatedBefore, factsAuditDeleteRules string
	factsAuditEndpoint                                                    string
	factsAuditModel                                                       string
	factsAuditAPIKey                                                      string
)

var factsAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "LLM-audit stored facts against the live extraction rules.",
	Long: `Walks stored facts and asks an LLM whether each one would still be extracted
under the current rules in promptrules.CoreFactExtractionPrompt. The auditor sees the
WHOLE extraction prompt verbatim, so the rules cannot drift between extractor
and auditor.

By default the command is read-only: it writes one JSONL row per fact to stdout
(the summary goes to stderr) and recommends nothing. With --apply, after the
audit completes the command prompts for confirmation and then deletes every
fact that received a "delete" verdict, cascading the cleanup:

  - contradictions referencing the fact are dropped (FK CASCADE),
  - the fact's stance memberships are dropped (FK CASCADE),
  - any stance that becomes empty as a result is removed,
  - honesty score is recomputed for every person mentioned by a deleted fact.

For local model probing over Tailscale, point --endpoint at e.g.
http://cachy:4000/v1 and override --model. --apikey lets you set a different
API key (some local gateways use a fixed token). Defaults come from
OPEN_AI_API / OPEN_AI_CHAT_MODEL / OPEN_AI_KEY.

Examples:
  # Dry run, sample 200 random facts for one person
  ./dm facts audit --person "Donald Trump" --random --max 200

  # Same, against a local model on cachy via Tailscale
  ./dm facts audit --person "Donald Trump" --random --max 200 \
    --endpoint http://cachy:4000/v1 --model gemma-3-27b-it

  # Apply: prompts before deleting (use --yes to skip the prompt)
  ./dm facts audit --person "Donald Trump" --apply`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsAudit,
}

func init() {
	factsCmd.AddCommand(factsAuditCmd)
	factsAuditCmd.Flags().StringVarP(&factsAuditPerson, "person", "p", "", "Restrict audit to a single person by name")
	factsAuditCmd.Flags().Int64Var(&factsAuditPersonID, "person-id", 0, "Restrict audit to a single person by numeric ID (overrides --person)")
	factsAuditCmd.Flags().Int64Var(&factsAuditFactID, "fact-id", 0, "Audit a single fact by id (overrides --person/--person-id, ignores --max/--random). Useful for spot-checking a specific verdict.")
	factsAuditCmd.Flags().IntVar(&factsAuditMax, "max", 0, "Maximum facts to audit (0 = no cap)")
	factsAuditCmd.Flags().BoolVar(&factsAuditRandom, "random", false, "Sample facts in random order (default: newest-first)")
	factsAuditCmd.Flags().StringVar(&factsAuditFormat, "format", "jsonl", "Output format: jsonl or text")
	factsAuditCmd.Flags().BoolVar(&factsAuditApply, "apply", false, "After audit, delete facts with verdict=delete (cascades stance/contradiction cleanup + recomputes honesty score)")
	factsAuditCmd.Flags().BoolVar(&factsAuditYes, "yes", false, "Skip the confirmation prompt before deletion (only with --apply)")
	factsAuditCmd.Flags().StringVar(&factsAuditCreatedFrom, "created-from", "", "Only facts created on or after this day (YYYY-MM-DD)")
	factsAuditCmd.Flags().StringVar(&factsAuditCreatedBefore, "created-before", "", "Only facts created before this day (YYYY-MM-DD)")
	factsAuditCmd.Flags().StringVar(&factsAuditDeleteRules, "delete-rules", "", "With --apply, delete only the facts whose rule is in this comma-separated list (e.g. not_about_entity,journalist_characterization); default: every delete verdict")
	factsAuditCmd.Flags().StringVar(&factsAuditEndpoint, "endpoint", "", "Override OPEN_AI_API for this run (e.g. http://cachy:4000/v1)")
	factsAuditCmd.Flags().StringVar(&factsAuditModel, "model", "", "Override OPEN_AI_CHAT_MODEL for this run")
	factsAuditCmd.Flags().StringVar(&factsAuditAPIKey, "apikey", "", "Override OPEN_AI_KEY for this run (some local gateways use a fixed token)")
}

// factAuditPerson is the JSON shape of one mentioned person in the audit row.
type factAuditPerson struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// factAuditOutputLine is the per-fact JSONL row written to stdout.
type factAuditOutputLine struct {
	FactID         int64             `json:"fact_id"`
	Persons        []factAuditPerson `json:"persons,omitempty"`
	FactText       string            `json:"fact_text"`
	EmbeddingText  string            `json:"embedding_text,omitempty"`
	CanonicalVerb  string            `json:"canonical_verb,omitempty"`
	OriginalQuote  string            `json:"original_quote,omitempty"`
	SourceURL      string            `json:"source_url,omitempty"`
	Verdict        string            `json:"verdict"`
	Rule           string            `json:"rule,omitempty"`
	AuditReasoning string            `json:"audit_reasoning"`
	Error          string            `json:"error,omitempty"`
}

func runFactsAudit(cmd *cobra.Command, args []string) (runErr error) {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	format := strings.ToLower(strings.TrimSpace(factsAuditFormat))
	if format != "jsonl" && format != "text" {
		return fmt.Errorf("invalid --format %q: expected 'jsonl' or 'text'", factsAuditFormat)
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	var personIDPtr *int64
	var factIDPtr *int64
	switch {
	case factsAuditFactID > 0:
		id := factsAuditFactID
		factIDPtr = &id
	case factsAuditPersonID > 0:
		id := factsAuditPersonID
		personIDPtr = &id
	case factsAuditPerson != "":
		person, err := core.ResolvePersonID(ctx, storer, factsAuditPerson, core.ExactMatchOnly)
		if err != nil {
			return err
		}
		personIDPtr = &person.ID
	}

	scope := storage.FactAuditScope{PersonID: personIDPtr, FactID: factIDPtr}
	for _, b := range []struct {
		flag string
		dst  *time.Time
	}{{factsAuditCreatedFrom, &scope.CreatedFrom}, {factsAuditCreatedBefore, &scope.CreatedBefore}} {
		if b.flag == "" {
			continue
		}
		t, err := time.Parse("2006-01-02", b.flag)
		if err != nil {
			return fmt.Errorf("invalid date %q (YYYY-MM-DD): %w", b.flag, err)
		}
		*b.dst = t
	}
	deleteRules := map[string]bool{}
	for _, r := range strings.Split(factsAuditDeleteRules, ",") {
		if r = strings.TrimSpace(r); r != "" {
			deleteRules[r] = true
		}
	}
	rows, err := storer.ListFactsForAudit(ctx, scope, factsAuditRandom, factsAuditMax)
	if err != nil {
		return fmt.Errorf("failed to list facts: %w", err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(os.Stderr, "No facts to audit.")
		return nil
	}

	auditor, err := core.NewOpenAIFactAuditor(factsAuditAPIKey, factsAuditEndpoint, factsAuditModel)
	if err != nil {
		return fmt.Errorf("failed to create auditor: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)

	startedAt := time.Now()
	log.Info().
		Int("total", len(rows)).
		Bool("random", factsAuditRandom).
		Str("scope", factsAuditScopeDescription()).
		Str("model", auditor.Model()).
		Str("endpoint", auditor.Endpoint()).
		Bool("apply", factsAuditApply).
		Msg("Starting fact audit")

	jobs := make([]core.FactAuditJob, len(rows))
	rowByID := make(map[int64]storage.FactAuditRow, len(rows))
	for i, row := range rows {
		factCreatedAt := row.FactCreatedAt
		names := make([]string, len(row.Persons))
		for j, p := range row.Persons {
			names[j] = p.Name
		}
		jobs[i] = core.FactAuditJob{FactID: row.ID, Input: core.FactAuditInput{
			NormalizedFact:    row.FactText,
			CanonicalVerb:     row.CanonicalVerb,
			EmbeddingText:     row.EmbeddingText,
			SubjectEntity:     core.FactAuditSubjectLabel(names),
			OriginalQuote:     row.FirstOriginalQuote,
			SourceURL:         row.FirstSourceURL,
			SourcePublishedAt: row.FirstSourcePublishedAt,
			FactCreatedAt:     &factCreatedAt,
		}}
		rowByID[row.ID] = row
	}

	ruleByID := map[int64]string{}
	tally, err := core.RunFactAudit(ctx, auditor, jobs, func(o core.FactAuditOutcome) error {
		row := rowByID[o.FactID]
		ruleByID[o.FactID] = o.Result.Rule
		line := factAuditOutputLine{
			FactID:         row.ID,
			Persons:        toAuditInputPersons(row.Persons),
			FactText:       row.FactText,
			EmbeddingText:  row.EmbeddingText,
			CanonicalVerb:  row.CanonicalVerb,
			OriginalQuote:  row.FirstOriginalQuote,
			SourceURL:      row.FirstSourceURL,
			Verdict:        string(o.Result.Verdict),
			Rule:           o.Result.Rule,
			AuditReasoning: o.Result.Reasoning,
		}
		if o.Result.Err != nil {
			line.Error = o.Result.Err.Error()
		}
		if format == "jsonl" {
			if err := enc.Encode(line); err != nil {
				return fmt.Errorf("failed to encode JSONL line: %w", err)
			}
			return nil
		}
		printFactAuditTextRow(os.Stdout, line)
		return nil
	})
	// An LLM that cannot serve stops the run: the verdicts given still apply,
	// and the command fails so the facts left are audited again later.
	stopped := errors.Is(err, llm.ErrUnavailable)
	if err != nil && !stopped {
		return err
	}
	if stopped {
		fmt.Fprintf(os.Stderr, "\nThe audit stopped, the LLM cannot serve: %v\n", err)
		defer func() {
			if runErr == nil {
				runErr = fmt.Errorf("fact audit stopped before the end: %w", err)
			}
		}()
	}

	printFactAuditSummary(os.Stderr, len(rows), tally.Keep, tally.Delete, tally.Unsure, tally.Errors, time.Since(startedAt))

	if !factsAuditApply {
		if tally.Delete > 0 {
			fmt.Fprintf(os.Stderr, "\n%d facts marked for deletion. Re-run with --apply to delete them.\n", tally.Delete)
		}
		return nil
	}

	deleteIDs := tally.DeleteIDs
	if len(deleteRules) > 0 {
		// Only the deletions under the rules asked for; the others stay in
		// the JSONL output for a review.
		kept := deleteIDs[:0:0]
		for _, id := range deleteIDs {
			if deleteRules[ruleByID[id]] {
				kept = append(kept, id)
			}
		}
		fmt.Fprintf(os.Stderr, "%d of %d deletions are under --delete-rules\n", len(kept), len(deleteIDs))
		deleteIDs = kept
	}
	if len(deleteIDs) == 0 {
		fmt.Fprintln(os.Stderr, "Nothing to apply.")
		return nil
	}

	if !factsAuditYes {
		ok, err := promptYesNo(fmt.Sprintf("\nDelete %d facts (cascading contradictions + empty stances + recomputing scores)?", len(deleteIDs)))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return nil
		}
	}

	cleanup, err := storer.DeleteFactsAndCleanup(ctx, deleteIDs)
	if err != nil {
		return fmt.Errorf("failed to apply deletions: %w", err)
	}

	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "=== APPLY RESULTS ===")
	fmt.Fprintf(os.Stderr, "Facts deleted:                  %d\n", cleanup.FactsDeleted)
	fmt.Fprintf(os.Stderr, "Stances dropped (now empty):    %d\n", cleanup.StancesDeleted)
	fmt.Fprintf(os.Stderr, "Persons rescored:               %d\n", len(cleanup.PersonsAffected))
	return nil
}

func factsAuditScopeDescription() string {
	if factsAuditPersonID > 0 {
		return fmt.Sprintf("person-id=%d", factsAuditPersonID)
	}
	if factsAuditPerson != "" {
		return factsAuditPerson
	}
	return "all persons"
}

func toAuditInputPersons(persons []storage.FactAuditPerson) []factAuditPerson {
	out := make([]factAuditPerson, 0, len(persons))
	for _, p := range persons {
		out = append(out, factAuditPerson{ID: p.ID, Name: p.Name})
	}
	return out
}

func promptYesNo(question string) (bool, error) {
	fmt.Fprint(os.Stderr, question, " [y/N]: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("failed to read confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func printFactAuditTextRow(w *os.File, line factAuditOutputLine) {
	color := ansiGray
	switch line.Verdict {
	case string(core.FactVerdictKeep):
		color = ansiGreen
	case string(core.FactVerdictDelete):
		color = ansiRed
	case string(core.FactVerdictUnsure):
		color = ansiYellow
	}
	verdict := strings.ToUpper(line.Verdict)
	if verdict == "" {
		verdict = "ERROR"
	}
	fmt.Fprintf(w, "%s[%s]%s fact #%d\n", color, verdict, ansiReset, line.FactID)
	if len(line.Persons) > 0 {
		names := make([]string, 0, len(line.Persons))
		for _, p := range line.Persons {
			names = append(names, p.Name)
		}
		fmt.Fprintf(w, "  Persons:  %s\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(w, "  Fact:     %s\n", line.FactText)
	if line.EmbeddingText != "" {
		fmt.Fprintf(w, "  Topic:    %s\n", line.EmbeddingText)
	}
	if line.OriginalQuote != "" {
		fmt.Fprintf(w, "  Quote:    %s\n", line.OriginalQuote)
	}
	if line.Rule != "" {
		fmt.Fprintf(w, "  Rule:     %s\n", line.Rule)
	}
	fmt.Fprintf(w, "  Audit:    %s\n", line.AuditReasoning)
	if line.Error != "" {
		fmt.Fprintf(w, "  ERROR:    %s\n", line.Error)
	}
	fmt.Fprintln(w)
}

func printFactAuditSummary(w *os.File, total, keep, del, unsure, errors int, elapsed time.Duration) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "=== FACT AUDIT SUMMARY ===")
	fmt.Fprintf(w, "Audited:    %d (in %s)\n", total, elapsed.Round(time.Second))
	pct := func(n int) string {
		if total == 0 {
			return ""
		}
		return fmt.Sprintf("  (%.1f%%)", 100*float64(n)/float64(total))
	}
	fmt.Fprintf(w, "Keep:       %d%s\n", keep, pct(keep))
	fmt.Fprintf(w, "Delete:     %d%s\n", del, pct(del))
	fmt.Fprintf(w, "Unsure:     %d%s\n", unsure, pct(unsure))
	fmt.Fprintf(w, "Errors:     %d\n", errors)
}
