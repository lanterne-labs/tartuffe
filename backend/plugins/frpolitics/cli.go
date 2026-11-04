package frpolitics

import (
	"context"
	"fmt"
	"strings"
	"time"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// CLIOptions are the flags of `dm plugin frpolitics`; each mode reads the
// ones it needs.
type CLIOptions struct {
	Mode string
	// Since and Legislatures bound the scrutins (votes, rearticle,
	// remotion) or the séances (debats).
	Since        *time.Time
	Legislatures []int
	// votes
	MaxLaws        int
	PartyThreshold float64
	// rearticle
	Rejudge bool
	// reamend
	DryRun bool
	// redate: write (the mode is a dry run without it)
	Apply bool
	// debats
	MaxDocs         int
	NewestFirst     bool
	PrioritizePress bool
}

// cliMode is one --mode of the command.
type cliMode struct {
	name string
	help string
	run  func(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error
}

// cliModes are the modes of the command, the default first.
var cliModes = []cliMode{
	{"votes", "process official votes", runVotesMode},
	{"debats", "séance transcripts", runDebatsMode},
	{"backfill-seance-ref", "stamp seance_ref on old vote sources + queue same-séance pairs", runSeanceRefMode},
	{"rearticle", "rewrite the ballots of the article scrutins with what the article does", runRearticleMode},
	{"remotion", "name the motion in the stored ballots of the motion scrutins; reopens the pairs of the motions other than censure", runRemotionMode},
	{"reamend", "complete the quoted object of the amendment ballots cut inside the guillemets (--dry-run to count)", runReamendMode},
	{"reword", "give the stored amendment ballots what the amendment does (an LLM statement per amendment, cached) instead of the quoted exposé (--dry-run to count, --rejudge to reopen their pairs)", runRewordMode},
	{"redate", "give the stored ballots the scrutin date suffix and the current abstention wording, no LLM (dry run unless --apply)", runRedateMode},
	{"resummarize", "give the amendment ballots whose bill summary describes the amendment the bill's summary (--dry-run to count, --rejudge to reopen their pairs)", runResummarizeMode},
}

// ModeHelp describes the modes for the --mode flag.
func ModeHelp() string {
	parts := make([]string, len(cliModes))
	for i, m := range cliModes {
		parts[i] = fmt.Sprintf("'%s' (%s)", m.name, m.help)
	}
	return "operation mode: " + strings.Join(parts, ", ")
}

// RunMode runs the mode opts.Mode of the command.
func RunMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	names := make([]string, len(cliModes))
	for i, m := range cliModes {
		if m.name == opts.Mode {
			return m.run(ctx, storer, opts)
		}
		names[i] = "'" + m.name + "'"
	}
	return fmt.Errorf("invalid mode %q: must be one of %s", opts.Mode, strings.Join(names, ", "))
}

func runVotesMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	sinceLog := ""
	if opts.Since != nil {
		sinceLog = opts.Since.Format("2006-01-02")
	}
	log.Info().Str("since", sinceLog).Int("max_laws", opts.MaxLaws).Float64("party_threshold", opts.PartyThreshold).
		Ints("legislatures", opts.Legislatures).Msg("Starting frpolitics plugin")
	if _, err := Run(ctx, storer, Options{
		Since:          opts.Since,
		MaxLaws:        opts.MaxLaws,
		PartyThreshold: opts.PartyThreshold,
		Legislatures:   opts.Legislatures,
		RevisitHours:   -1, // negative = always visitable: retry laws whose store failed (0 would coerce to the 24h default)
	}); err != nil {
		return fmt.Errorf("frpolitics plugin failed: %w", err)
	}
	log.Info().Msg("Frpolitics plugin completed successfully")
	return nil
}

// runDebatsMode runs the séance transcripts: a separate engine that shares
// the AN actor ids with the votes path. Since bounds the séance dates;
// MaxDocs / NewestFirst drive a manual backfill chunk.
func runDebatsMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	dopts := DebatsOptions{MaxDocs: opts.MaxDocs, NewestFirst: opts.NewestFirst, PrioritizePress: opts.PrioritizePress}
	if opts.Since != nil {
		dopts.Since = *opts.Since
	}
	if len(opts.Legislatures) > 1 {
		return fmt.Errorf("--mode debats processes one legislature per run, got %v", opts.Legislatures)
	}
	if len(opts.Legislatures) == 1 {
		dopts.Legislature = opts.Legislatures[0]
	}
	stats, err := RunDebats(ctx, storer, dopts)
	if err != nil {
		return err
	}
	fmt.Printf("\n=== DEBATS RESULTS ===\nSéances in range: %d\nDocuments: %d (pending before: %d)\nAttempted: %d, processed: %d, facts: %d (skipped %d, dropped non-speaker subject %d), no-fact docs: %d, errors: %d\nPending after: %d, deadline hit: %v, elapsed: %.0fs\n",
		stats.Seances, stats.Documents, stats.Pending, stats.Attempted, stats.Processed, stats.Facts, stats.FactsSkipped, stats.FactsDroppedSubject, stats.NoFacts, stats.Errors, stats.PendingAfter, stats.DeadlineHit, stats.ElapsedSeconds)
	return nil
}

// runSeanceRefMode is a one-off: it stamps seance_ref on the vote sources
// ingested before the key existed, then queues the same-séance pairs.
func runSeanceRefMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	leg := 17
	if len(opts.Legislatures) == 1 {
		leg = opts.Legislatures[0]
	}
	n, err := BackfillVoteSeanceRefs(ctx, storer, leg)
	if err != nil {
		return err
	}
	q, err := EnqueueSameSeancePairs(ctx, storer)
	if err != nil {
		return err
	}
	fmt.Printf("seance_ref backfilled on %d vote sources; %d same-séance pairs queued\n", n, q)
	return nil
}

// runRearticleMode rewrites the ballots of the article scrutins stored
// before the articles were resolved, which name the article alone, with what
// the article does. Discovery reads the scrutins of Since / Legislatures.
func runRearticleMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	res, err := BackfillArticles(ctx, storer, Options{Since: opts.Since, Legislatures: opts.Legislatures}, opts.Rejudge)
	if err != nil {
		return err
	}
	fmt.Printf("%d article scrutins rewritten, %d ballots; %d scrutins gated (%s); %d contradictions reopened\n",
		res.Scrutins, res.Ballots, len(res.Gated), strings.Join(res.Gated, ", "), res.Reopened)
	return nil
}

// runReamendMode completes the quoted object of the amendment ballots that
// lost their closing » (purposePhrase before 2026-11-01).
func runReamendMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	res, err := BackfillAmendments(ctx, storer, Options{Since: opts.Since, Legislatures: opts.Legislatures}, opts.DryRun)
	if err != nil {
		return err
	}
	verb := "rewritten"
	if opts.DryRun {
		verb = "to rewrite (dry run)"
	}
	fmt.Printf("%d amendment scrutins, %d ballots %s; %d scrutins gated (%s)\n",
		res.Scrutins, res.Ballots, verb, len(res.Gated), strings.Join(res.Gated, ", "))
	return nil
}

// runRewordMode rewrites the stored amendment ballots with the amendment's
// statement (RewordAmendmentBallots).
func runRewordMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	res, err := BackfillAmendmentStatements(ctx, storer, Options{Since: opts.Since, Legislatures: opts.Legislatures}, opts.Rejudge, opts.DryRun)
	if err != nil {
		return err
	}
	verb := "rewritten"
	if opts.DryRun {
		verb = "to rewrite (dry run)"
	}
	fmt.Printf("%d amendment scrutins, %d ballots %s; %d scrutins keep the quoted exposé (%s); %d contradictions reopened\n",
		res.Scrutins, res.Ballots, verb, len(res.Kept), strings.Join(res.Kept, ", "), res.Reopened)
	return nil
}

// runRedateMode gives the stored ballots the « (scrutin du …) » suffix and
// the abstention wording of the current code (RedateBallots). A dry run
// unless --apply.
func runRedateMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	res, err := RedateBallots(ctx, NewStore(storer), !opts.Apply)
	if err != nil {
		return err
	}
	for _, ex := range res.Examples {
		fmt.Printf("%d\n  old: %s\n  new: %s\n", ex.ID, ex.Old, ex.New)
	}
	mode := "dry run: nothing written (use --apply)"
	if opts.Apply {
		mode = fmt.Sprintf("%d written", res.Written)
	}
	fmt.Printf("%d ballots read; %d to rewrite: %d given the date suffix, %d abstention wordings fixed; %s\n",
		res.Ballots, res.Rewritten, res.Suffixed, res.Abstention, mode)
	return nil
}

// runResummarizeMode gives the ballots of the amendment scrutins summarized
// from the amendment's title (before 1298d12) the bill's summary.
func runResummarizeMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	res, err := BackfillSummaries(ctx, storer, Options{Since: opts.Since, Legislatures: opts.Legislatures}, opts.DryRun, opts.Rejudge)
	if err != nil {
		return err
	}
	verb := "rewritten"
	if opts.DryRun {
		verb = "to rewrite (dry run)"
	}
	fmt.Printf("%d amendment scrutins with a stale summary, %d ballots %s; %d scrutins gated (%s); %d contradictions reopened\n",
		res.Scrutins, res.Ballots, verb, len(res.Gated), strings.Join(res.Gated, ", "), res.Reopened)
	return nil
}

// runRemotionMode names the motion in the ballots of the motion scrutins
// stored with the bill phrase, which read as votes on the bill. Discovery
// reads the scrutins of Since / Legislatures.
func runRemotionMode(ctx context.Context, storer *storage.PostgresStorer, opts CLIOptions) error {
	res, err := BackfillMotions(ctx, storer, Options{Since: opts.Since, Legislatures: opts.Legislatures})
	if err != nil {
		return err
	}
	fmt.Printf("%d motions rewritten, %d ballots; %d contradictions reopened\n", res.Scrutins, res.Ballots, res.Reopened)
	return nil
}
