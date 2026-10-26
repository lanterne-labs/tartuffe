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
proofs, and recomputes the results. It exits non-zero on any problem.`,
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

// auditPassport verifies a registration's proof as of its own date: the
// SDK's freshness check takes the validity it is given.
type auditPassport struct {
	z     *providers.ZKPassport
	scope string
}

func (a auditPassport) VerifyRegistration(ctx context.Context, proof json.RawMessage, customData string) (string, error) {
	var r struct {
		ProofDate time.Time `json:"proof_date"`
	}
	_ = json.Unmarshal(proof, &r)
	extra := map[string]any{"minAge": 18, "nationality": "FRA", "documentType": "passport", "scope": a.scope}
	return a.z.VerifyBound(ctx, proof, customData, extra)
}

func runVoteVerify(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()
	base, _ := f.GetString("api")
	key, _ := f.GetString("key")
	vkPath, _ := f.GetString("vk")
	skipPassports, _ := f.GetBool("skip-passports")
	slug := args[0]

	// The event's id: from the API's event list.
	raw, err := apiGet(ctx, base, "/api/polls")
	if err != nil {
		return err
	}
	var events []struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		return fmt.Errorf("events: %w", err)
	}
	var eventID int64
	for _, e := range events {
		if e.Slug == slug {
			eventID = e.ID
		}
	}
	if eventID == 0 {
		return fmt.Errorf("no event %q at %s", slug, base)
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
	rep, err := vote.Audit(ctx, apiSource{base}, opt)
	if err != nil {
		return err
	}
	fmt.Printf("log: %d entries, %d checkpoints\nevent %s: %d registrations, %d ballots, %d rejects, %d tallies\n",
		rep.Entries, rep.Checkpoints, slug, rep.Registrations, rep.Ballots, rep.Rejects, rep.Tallies)
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
