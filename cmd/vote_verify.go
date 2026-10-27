package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote"

	"github.com/spf13/cobra"
)

var voteVerifyCmd = &cobra.Command{
	Use:   "verify [slug]",
	Short: "Audit the public log of a vote, as anyone can (SPEC §12)",
	Long: `Downloads the log and the checkpoints of the site (--api) and checks,
without trusting it: every checkpoint's signature (--key, the log's verifier
key) and tree, the setup, every registration's passport proof, the voter
tree, every ballot's proof and rules, every tally's sums and decryption
proofs, and recomputes the results. With --relays it reads the relays too
and judges every event there by its acknowledgement (SPEC §12.6): an
acknowledged event the log does not list is withholding, proven; an event
no one acknowledged is listed as a finding. An archive of the relays
(dm vote archive, --archive) adds the first sightings that attribute the
latter. It exits non-zero on any problem.`,
	Example:      `  dm vote verify presidentielle-2027 --api https://tartuffe.me --key 'tartuffe.me/vote-log+…' --vk vk`,
	Args:         cobra.ExactArgs(1),
	RunE:         runVoteVerify,
	SilenceUsage: true,
}

func init() {
	voteCmd.AddCommand(voteVerifyCmd)
	f := voteVerifyCmd.Flags()
	f.String("api", "https://tartuffe.me", "base URL of the site's API")
	f.String("key", "", "the log's verifier key; default: the one the site serves (which only proves the site signed its own log)")
	f.String("vk", "", "the circuit's verification key, to check the ballot proofs (default: skipped, with a warning)")
	f.Bool("skip-passports", false, "do not verify the ZKPassport proofs of the registrations")
	f.String("relays", "", "read the relays: 'setup' for the setup's, or wss:// URLs, comma-separated")
	f.String("archive", "", "an archive of the relays (dm vote archive): read instead of the relays, with first sightings")
	f.String("snapshot", "", "the mirror's snapshot.json, to check the relays against the close snapshot")
}

// apiSource reads the log from the site's API.
type apiSource struct{ base string }

func (a apiSource) Entries(ctx context.Context, after int64, limit int) ([]polls.LogEntry, error) {
	raw, err := apiGet(ctx, a.base, fmt.Sprintf("/api/vote-log?after=%d&limit=%d", after, limit))
	if err != nil {
		return nil, err
	}
	var out []polls.LogEntry
	return out, json.Unmarshal(raw, &out)
}

func (a apiSource) Checkpoints(ctx context.Context) ([]vote.Checkpoint, error) {
	raw, err := apiGet(ctx, a.base, "/api/vote2/checkpoints")
	if err != nil {
		return nil, err
	}
	var out []vote.Checkpoint
	return out, json.Unmarshal(raw, &out)
}

// auditPassport verifies a registration's proof as of its own date (the
// expiry check is against the proof's day, not the audit's).
type auditPassport struct {
	z     *providers.ZKPassport
	scope string
}

func (a auditPassport) VerifyRegistration(ctx context.Context, proof json.RawMessage, customData string, proofDate time.Time) (string, error) {
	return vote.Passport{Z: a.z, Scope: a.scope}.VerifyRegistration(ctx, proof, customData, proofDate)
}

func runVoteVerify(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()
	base, _ := f.GetString("api")
	key, _ := f.GetString("key")
	vkPath, _ := f.GetString("vk")
	skipPassports, _ := f.GetBool("skip-passports")
	slug := args[0]

	eventID, err := eventIDOf(ctx, base, slug)
	if err != nil {
		return err
	}
	if key == "" {
		raw, err := apiGet(ctx, base, "/api/vote-log/key")
		if err != nil {
			return err
		}
		key = strings.TrimSpace(string(raw))
		fmt.Printf("log key (served by the site, not pinned): %s\n", key)
	}
	opt := vote.Options{EventID: eventID, LogKey: key}
	if vkPath != "" {
		vk, err := os.ReadFile(vkPath)
		if err != nil {
			return err
		}
		repo, _ := filepath.Abs(".")
		tools, err := vote.FindTools(repo)
		if err != nil {
			return err
		}
		if opt.Verifier, err = vote.NewVerifier(tools.BB, vk); err != nil {
			return err
		}
	}
	if !skipPassports {
		zk, err := providers.NewZKPassportFromEnv()
		if err != nil {
			return fmt.Errorf("%w (or --skip-passports)", err)
		}
		if z, ok := zk.(*providers.ZKPassport); ok {
			opt.Passport = auditPassport{z: z, scope: "tartuffe-vote/2:" + slug}
		}
	}
	relayList, _ := f.GetString("relays")
	archiveDir, _ := f.GetString("archive")
	snapshotFile, _ := f.GetString("snapshot")
	src := apiSource{base}
	switch {
	case archiveDir != "":
		events, err := vote.ReadArchive(archiveDir)
		if err != nil {
			return err
		}
		opt.Relays = events
		fmt.Printf("archive: %d events\n", len(events))
	case relayList != "":
		urls, err := relayURLs(ctx, src, eventID, relayList)
		if err != nil {
			return err
		}
		fmt.Printf("reading %d relays…\n", len(urls))
		events, errs := vote.DumpRelays(ctx, slug, urls)
		for _, e := range errs {
			fmt.Printf("warning: %v\n", e)
		}
		opt.Relays = events
		fmt.Printf("relays: %d events\n", len(events))
	}
	if snapshotFile != "" {
		raw, err := os.ReadFile(snapshotFile)
		if err != nil {
			return err
		}
		var list vote.SnapshotList
		if err := json.Unmarshal(raw, &list); err != nil {
			return fmt.Errorf("%s: %w", snapshotFile, err)
		}
		opt.Snapshot = &list
	}
	rep, err := vote.Audit(ctx, src, opt)
	if err != nil {
		return err
	}
	fmt.Printf("log: %d entries, %d checkpoints\nevent %s: %d registrations, %d ballots, %d rejects, %d tallies\n",
		rep.Entries, rep.Checkpoints, slug, rep.Registrations, rep.Ballots, rep.Rejects, rep.Tallies)
	if opt.Relays != nil {
		fmt.Printf("relays: %d protocol events, %d acknowledgements, %d acknowledged by no one, %d withheld, %d complaints\n",
			rep.RelayEvents, rep.Acks, rep.Unacknowledged, rep.Withheld, rep.Complaints)
	}
	if rep.Results != nil {
		fmt.Printf("results: %d voters\n", rep.Results.Voters)
		for _, o := range rep.Results.Options {
			name := o.Name
			if o.Kind == "blank" {
				name = "vote blanc"
			}
			fmt.Printf("  %d. %-24s first %d  ranked %d\n", o.Rank, name, o.First, o.Ranked)
		}
		if rep.Results.Winner != nil {
			fmt.Printf("  Condorcet winner: slot %d\n", *rep.Results.Winner)
		}
	}
	for _, w := range rep.Warnings {
		fmt.Printf("warning: %s\n", w)
	}
	for _, p := range rep.Problems {
		fmt.Printf("PROBLEM: %s\n", p)
	}
	if !rep.OK() {
		return fmt.Errorf("%d problem(s)", len(rep.Problems))
	}
	fmt.Println("ok: the log checks out")
	return nil
}

// eventIDOf finds an event's id in the API's event list.
func eventIDOf(ctx context.Context, base, slug string) (int64, error) {
	raw, err := apiGet(ctx, base, "/api/polls")
	if err != nil {
		return 0, err
	}
	var events []struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		return 0, fmt.Errorf("events: %w", err)
	}
	for _, e := range events {
		if e.Slug == slug {
			return e.ID, nil
		}
	}
	return 0, fmt.Errorf("no event %q at %s", slug, base)
}

// relayURLs resolves --relays: the active relays of the event's setup, or
// the URLs given.
func relayURLs(ctx context.Context, src vote.LogSource, eventID int64, flag string) ([]string, error) {
	if flag != "setup" {
		var urls []string
		for _, u := range strings.Split(flag, ",") {
			if u = strings.TrimSpace(u); u != "" {
				urls = append(urls, u)
			}
		}
		return urls, nil
	}
	setups, err := vote.SetupsOf(ctx, src, eventID)
	if err != nil {
		return nil, err
	}
	if len(setups) == 0 {
		return nil, fmt.Errorf("no setup logged for event %d", eventID)
	}
	return vote.ActiveBoxes(setups), nil
}

// verifyClient reads the site's API.
var verifyClient = &http.Client{Timeout: time.Minute}

func apiGet(ctx context.Context, base, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := verifyClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}
