package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/api"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

var pollCmd = &cobra.Command{
	Use:   "poll",
	Short: "Pre-polls: the events voters rank candidates in (the presidential election first)",
	Long: `A pre-poll event lists its candidates among the tracked persons. Each
voter ranks up to max_choices of them; the site orders them by the Condorcet
method of Schulze, and by first choices as the one-round majority vote would,
next to each candidate's honesty score.`,
}

var pollListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List the events",
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runPollList,
}

var pollEventCmd = &cobra.Command{
	Use:   "event",
	Short: "Create or change an event",
}

var pollEventSetCmd = &cobra.Command{
	Use:   "set [slug]",
	Short: "Create the event of that slug, or change the fields given as flags",
	Long: `Create the event (then --title-fr and --title-en are required) or change
only the fields given as flags. Times are RFC 3339 (2027-04-18T18:00:00Z).
--featured makes it the event the home page promotes.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runPollEventSet,
}

var pollBlackoutCmd = &cobra.Command{
	Use:   "blackout",
	Short: "The periods when an event's results are hidden (polling law)",
}

var pollBlackoutAddCmd = &cobra.Command{
	Use:         "add [slug]",
	Short:       "Hide the results over [--from, --to)",
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runPollBlackoutAdd,
}

var pollBlackoutClearCmd = &cobra.Command{
	Use:         "clear [slug]",
	Short:       "Remove every blackout of the event",
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runPollBlackoutClear,
}

var pollCandidateCmd = &cobra.Command{
	Use:   "candidate",
	Short: "The candidates of an event, among the tracked persons",
}

var pollCandidateAddCmd = &cobra.Command{
	Use:         "add [slug] [person-name...]",
	Short:       "Make tracked persons candidates of the event",
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.MinimumNArgs(2),
	RunE:        runPollCandidateAdd,
}

var pollCandidateRemoveCmd = &cobra.Command{
	Use:         "remove [slug] [person-name...]",
	Short:       "Remove candidates; their place on every ballot goes with them",
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.MinimumNArgs(2),
	RunE:        runPollCandidateRemove,
}

var pollIsoloirSetupCmd = &cobra.Command{
	Use:   "isoloir-setup [slug]",
	Short: "Print the isoloir poll setup of an event (its candidates, close and blackouts)",
	Long: `Print the poll setup (isoloir SPEC §7) of an event, to log with
isoloir submit poll: a ranked poll of the event's candidates (by name), the
blank last, ranked up to the event's max choices with pairwise margins,
encrypted for one authority, results open after every two changed ballots
(k_real 2: a tally waits for two changed ballots) outside the blackouts, linked re-votes. A poll's options never
change once logged: add the candidates first (dm poll candidate add).`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runPollIsoloirSetup,
}

var pollPurgeUsersCmd = &cobra.Command{
	Use:   "purge-users",
	Short: "Delete the users of a sign-in provider",
	Long: `Delete every user of --provider with their sessions. Run it with
--provider noop before a launch, once AUTH_PROVIDERS no longer names noop.
Their ballots are anonymous and cannot be found: dm poll reset-votes clears
the test ballots.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runPollPurgeUsers,
}

func init() {
	rootCmd.AddCommand(pollCmd)
	pollCmd.AddCommand(pollListCmd, pollEventCmd, pollBlackoutCmd, pollCandidateCmd, pollIsoloirSetupCmd, pollPurgeUsersCmd)
	f := pollIsoloirSetupCmd.Flags()
	f.Int("id", 0, "the poll's id in the ledger (required)")
	f.Int("registry", 1, "the registry it draws its voters from")
	f.String("pk", "", "the authority's public key (isoloir keygen authority: pk)")
	f.String("sign-key", "", "the authority's signing key (isoloir keygen authority: sign_key)")
	f.String("circuit", "", "the circuit variant (isoloir check poll prints it), e.g. rankedm12_single_linked_d20")
	f.String("vk-hash", "", "its verification key hash (isoloir circuits prints it)")
	pollEventCmd.AddCommand(pollEventSetCmd)
	pollBlackoutCmd.AddCommand(pollBlackoutAddCmd, pollBlackoutClearCmd)
	pollCandidateCmd.AddCommand(pollCandidateAddCmd, pollCandidateRemoveCmd)

	f = pollEventSetCmd.Flags()
	f.String("title-fr", "", "title in French")
	f.String("title-en", "", "title in English")
	f.String("description-fr", "", "description in French")
	f.String("description-en", "", "description in English")
	f.Int("max-choices", 3, "how many candidates a ballot may rank (1-10)")
	f.Int("min-voters", 50, "below this many ballots the shares are hidden")
	f.String("opens", "", "first moment ballots are taken (default: now, for a new event)")
	f.String("closes", "", "moment ballots stop (\"never\" to keep it open)")
	f.Bool("featured", false, "the event the home page promotes")

	pollBlackoutAddCmd.Flags().String("from", "", "start, RFC 3339 (required)")
	pollBlackoutAddCmd.Flags().String("to", "", "end, RFC 3339 (required)")
	_ = pollBlackoutAddCmd.MarkFlagRequired("from")
	_ = pollBlackoutAddCmd.MarkFlagRequired("to")

	pollPurgeUsersCmd.Flags().String("provider", "", "the provider whose users are deleted (required)")
	pollPurgeUsersCmd.Flags().Bool("yes", false, "confirm the deletion")
	_ = pollPurgeUsersCmd.MarkFlagRequired("provider")
}

func openPollStorer(ctx context.Context) (*storage.PostgresStorer, error) {
	_ = godotenv.Load()
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	return storer, nil
}

// pollStore is the polls' store on the database of storer.
func pollStore(storer *storage.PostgresStorer) *polls.PGStore {
	return polls.NewPGStore(storer.Pool())
}

func mustPollEvent(ctx context.Context, storer *storage.PostgresStorer, slug string) (*polls.Event, error) {
	e, err := pollStore(storer).GetEvent(ctx, slug)
	if err != nil {
		return nil, err
	}
	if e == nil {
		return nil, fmt.Errorf("no event %q (dm poll list)", slug)
	}
	return e, nil
}

func runPollList(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	storer, err := openPollStorer(ctx)
	if err != nil {
		return err
	}
	defer storer.Close()
	events, err := pollStore(storer).ListEvents(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	fmt.Printf("%-24s %-30s %-8s %5s %5s %6s  %s\n", "SLUG", "TITLE", "STATE", "MAX", "MIN", "CANDS", "CLOSES")
	for _, e := range events {
		cands, err := pollStore(storer).CandidateIDs(ctx, e.ID)
		if err != nil {
			return err
		}
		state := "closed"
		if polls.IsOpen(&e, now) {
			state = "open"
		}
		if polls.InBlackout(&e, now) {
			state += "/bl"
		}
		closes := "never"
		if e.ClosesAt != nil {
			closes = e.ClosesAt.UTC().Format(time.RFC3339)
		}
		title := e.TitleFR
		if e.Featured {
			title = "★ " + title
		}
		fmt.Printf("%-24s %-30.30s %-8s %5d %5d %6d  %s\n", e.Slug, title, state, e.MaxChoices, e.MinVoters, len(cands), closes)
	}
	return nil
}

func runPollEventSet(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	storer, err := openPollStorer(ctx)
	if err != nil {
		return err
	}
	defer storer.Close()
	slug := args[0]
	e, err := pollStore(storer).GetEvent(ctx, slug)
	if err != nil {
		return err
	}
	f := cmd.Flags()
	if e == nil {
		if !f.Changed("title-fr") || !f.Changed("title-en") {
			return fmt.Errorf("a new event needs --title-fr and --title-en")
		}
		e = &polls.Event{Slug: slug, MaxChoices: 3, MinVoters: 50, OpensAt: time.Now()}
	}
	str := func(name string, dst *string) {
		if f.Changed(name) {
			*dst, _ = f.GetString(name)
		}
	}
	str("title-fr", &e.TitleFR)
	str("title-en", &e.TitleEN)
	str("description-fr", &e.DescriptionFR)
	str("description-en", &e.DescriptionEN)
	if f.Changed("max-choices") {
		e.MaxChoices, _ = f.GetInt("max-choices")
	}
	if f.Changed("min-voters") {
		e.MinVoters, _ = f.GetInt("min-voters")
	}
	if f.Changed("featured") {
		e.Featured, _ = f.GetBool("featured")
	}
	if f.Changed("opens") {
		v, _ := f.GetString("opens")
		if e.OpensAt, err = time.Parse(time.RFC3339, v); err != nil {
			return fmt.Errorf("--opens: %w", err)
		}
	}
	if f.Changed("closes") {
		v, _ := f.GetString("closes")
		if v == "never" {
			e.ClosesAt = nil
		} else {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return fmt.Errorf("--closes: %w", err)
			}
			e.ClosesAt = &t
		}
	}
	id, err := pollStore(storer).SaveEvent(ctx, *e)
	if err != nil {
		return err
	}
	fmt.Printf("event %s saved (ID %d)\n", slug, id)
	return nil
}

func runPollBlackoutAdd(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	fromStr, _ := cmd.Flags().GetString("from")
	toStr, _ := cmd.Flags().GetString("to")
	from, err := time.Parse(time.RFC3339, fromStr)
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	to, err := time.Parse(time.RFC3339, toStr)
	if err != nil {
		return fmt.Errorf("--to: %w", err)
	}
	if !from.Before(to) {
		return fmt.Errorf("--from must be before --to")
	}
	storer, err := openPollStorer(ctx)
	if err != nil {
		return err
	}
	defer storer.Close()
	e, err := mustPollEvent(ctx, storer, args[0])
	if err != nil {
		return err
	}
	if err := pollStore(storer).AddBlackout(ctx, e.ID, from, to); err != nil {
		return err
	}
	fmt.Printf("results of %s hidden from %s to %s\n", e.Slug, from.Format(time.RFC3339), to.Format(time.RFC3339))
	return nil
}

func runPollBlackoutClear(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	storer, err := openPollStorer(ctx)
	if err != nil {
		return err
	}
	defer storer.Close()
	e, err := mustPollEvent(ctx, storer, args[0])
	if err != nil {
		return err
	}
	n, err := pollStore(storer).ClearBlackouts(ctx, e.ID)
	if err != nil {
		return err
	}
	fmt.Printf("%d blackout(s) removed from %s\n", n, e.Slug)
	return nil
}

func runPollCandidateAdd(cmd *cobra.Command, args []string) error {
	return pollCandidates(args, func(ctx context.Context, storer *storage.PostgresStorer, eventID, personID int64) (string, error) {
		added, err := pollStore(storer).AddCandidate(ctx, eventID, personID)
		if added {
			return "added", err
		}
		return "already a candidate", err
	})
}

func runPollCandidateRemove(cmd *cobra.Command, args []string) error {
	return pollCandidates(args, func(ctx context.Context, storer *storage.PostgresStorer, eventID, personID int64) (string, error) {
		removed, err := pollStore(storer).RemoveCandidate(ctx, eventID, personID)
		if removed {
			return "removed", err
		}
		return "not a candidate", err
	})
}

// pollCandidates resolves each person name of args[1:] and applies change.
func pollCandidates(args []string, change func(context.Context, *storage.PostgresStorer, int64, int64) (string, error)) error {
	ctx := context.Background()
	storer, err := openPollStorer(ctx)
	if err != nil {
		return err
	}
	defer storer.Close()
	e, err := mustPollEvent(ctx, storer, args[0])
	if err != nil {
		return err
	}
	for _, name := range args[1:] {
		person, ok, err := resolvePersonInteractive(ctx, storer, name, false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Printf("%s: skipped\n", name)
			continue
		}
		what, err := change(ctx, storer, e.ID, person.ID)
		if err != nil {
			return err
		}
		fmt.Printf("%s (ID %d): %s\n", person.Name, person.ID, what)
	}
	return nil
}

func runPollIsoloirSetup(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	f := cmd.Flags()
	id, _ := f.GetInt("id")
	registry, _ := f.GetInt("registry")
	pk, _ := f.GetString("pk")
	signKey, _ := f.GetString("sign-key")
	circuit, _ := f.GetString("circuit")
	vkHash, _ := f.GetString("vk-hash")
	if id <= 0 || pk == "" || signKey == "" || circuit == "" || vkHash == "" {
		return fmt.Errorf("--id, --pk, --sign-key, --circuit and --vk-hash are required")
	}
	storer, err := openPollStorer(ctx)
	if err != nil {
		return err
	}
	defer storer.Close()
	e, err := mustPollEvent(ctx, storer, args[0])
	if err != nil {
		return err
	}
	if e.ClosesAt == nil {
		return fmt.Errorf("%s has no close date (dm poll event set)", e.Slug)
	}
	store := polls.NewPGStore(storer.Pool())
	opts, err := polls.NewService(store, api.PersonCards{Storer: storer}).Options(ctx, e)
	if err != nil {
		return err
	}
	var names []string
	for _, o := range opts {
		if o.Kind == "candidate" {
			names = append(names, o.Name)
		}
	}
	if len(names) < 2 {
		return fmt.Errorf("%s has %d candidate(s): add them first (dm poll candidate add)", e.Slug, len(names))
	}
	blackouts := []map[string]int64{}
	for _, b := range e.Blackouts {
		blackouts = append(blackouts, map[string]int64{"from": b.StartsAt.Unix(), "to": b.EndsAt.Unix()})
	}
	opens := e.OpensAt
	if opens.Before(time.Now()) {
		opens = time.Now()
	}
	poll := map[string]any{
		"v": 1, "id": id, "version": 1, "registry": registry, "title": e.TitleFR,
		"kind":       map[string]any{"type": "ranked", "options": names, "blank": true, "depth": e.MaxChoices, "margins": true},
		"encryption": map[string]any{"mode": "elgamal", "pk": pk, "authority": map[string]any{"type": "single", "sign_key": signKey}},
		"publish":    map[string]any{"mode": "open", "k_real": 2, "min_gap": 0, "coin": false},
		"revote":     "linked",
		"opens":      opens.Unix(),
		"closes":     e.ClosesAt.Unix(),
		"circuit":    map[string]any{"name": circuit, "vk_hash": vkHash},
	}
	if len(blackouts) > 0 {
		poll["blackouts"] = blackouts
	}
	out, err := json.MarshalIndent(poll, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

func runPollPurgeUsers(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	provider, _ := cmd.Flags().GetString("provider")
	yes, _ := cmd.Flags().GetBool("yes")
	if strings.TrimSpace(provider) == "" {
		return fmt.Errorf("--provider is empty")
	}
	if !yes {
		return fmt.Errorf("this deletes every %s user: add --yes", provider)
	}
	storer, err := openPollStorer(ctx)
	if err != nil {
		return err
	}
	defer storer.Close()
	n, err := accounts.NewPGStore(storer.Pool()).PurgeUsers(ctx, provider)
	if err != nil {
		return err
	}
	fmt.Printf("%d %s user(s) deleted\n", n, provider)
	return nil
}
