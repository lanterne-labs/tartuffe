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

	"digital-mirror/backend/polls"

	"github.com/spf13/cobra"
)

var pollVerifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Recount the public vote log of a site, as anyone can",
	Long: `Downloads the public vote log of the site (--api) and checks, without
trusting it:
  - the signed checkpoint (with --key, the log's verifier key; else the key the
    site serves, which only proves the site signed it) and that the log's
    entries make its Merkle tree;
  - every registration, voter tree root, ballot proof and tally, replayed from
    the first entry (polls.Audit);
  - with --relays, that the log holds every valid ballot the Nostr relays hold;
  - every checkpoint stamped on Bitcoin, saved with --stamps DIR as
    checkpoint-<size>.txt and .ots for the ots client ("ots verify -f
    checkpoint-12.txt checkpoint-12.txt.ots").
It exits non-zero when it finds a problem.`,
	Example: `  dm poll verify --api https://tartuffe.me --relays --stamps ./stamps`,
	RunE:    runPollVerify,
}

func init() {
	pollCmd.AddCommand(pollVerifyCmd, pollLogKeyCmd)
	f := pollVerifyCmd.Flags()
	f.String("api", "https://tartuffe.me", "base URL of the site's API")
	f.String("key", "", "the log's verifier key (tartuffe.me/vote-log+...); default: the one the site serves")
	f.Bool("relays", false, "also compare the log with the ballots on the Nostr relays")
	f.StringSlice("relay", polls.DefaultRelays, "the relays to read with --relays")
	f.String("stamps", "", "a directory to save the stamped checkpoints and their .ots proofs in")
	f.Duration("max-delay", 8*time.Hour, "with --relays: how long a ballot may wait before the log must hold it")
}

var pollLogKeyCmd = &cobra.Command{
	Use:   "log-key",
	Short: "Make a new key for the vote log's checkpoints",
	Long: `Prints a new signer key (the API's VOTE_LOG_KEY: keep it secret, set it once,
never change it, or every browser that saw the old checkpoints reports a fault)
and its verifier key (public: publish it, pin it in the frontend as
VITE_VOTE_LOG_KEY).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		skey, vkey, err := polls.GenerateLogKey()
		if err != nil {
			return err
		}
		fmt.Printf("VOTE_LOG_KEY=%s\nverifier key: %s\n", skey, vkey)
		return nil
	},
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

func runPollVerify(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()
	base, _ := f.GetString("api")
	key, _ := f.GetString("key")
	withRelays, _ := f.GetBool("relays")
	relays, _ := f.GetStringSlice("relay")
	stampsDir, _ := f.GetString("stamps")
	maxDelay, _ := f.GetDuration("max-delay")

	if key == "" {
		raw, err := apiGet(ctx, base, "/api/vote-log/key")
		if err != nil {
			return err
		}
		key = strings.TrimSpace(string(raw))
		fmt.Printf("key (served by the site, not pinned): %s\n", key)
	}
	note, err := apiGet(ctx, base, "/api/vote-log/checkpoint")
	if err != nil {
		return err
	}
	head, err := polls.OpenCheckpoint(note, key)
	if err != nil {
		return fmt.Errorf("the checkpoint does not verify: %w", err)
	}
	fmt.Printf("checkpoint: %d entries, signed\n", head.N)

	var entries []polls.LogEntry
	for int64(len(entries)) < head.N {
		raw, err := apiGet(ctx, base, fmt.Sprintf("/api/vote-log?after=%d&limit=1000", len(entries)))
		if err != nil {
			return err
		}
		var page []polls.LogEntry
		if err := json.Unmarshal(raw, &page); err != nil {
			return fmt.Errorf("vote log: %w", err)
		}
		if len(page) == 0 {
			return fmt.Errorf("the site serves %d entries, its checkpoint signs %d", len(entries), head.N)
		}
		entries = append(entries, page...)
	}
	entries = entries[:head.N]
	tree, err := polls.TreeOf(entries)
	if err != nil {
		return fmt.Errorf("vote log: %w", err)
	}
	var problems []string
	if tree != head {
		problems = append(problems, "the entries served do not make the signed tree")
	}

	report := polls.Audit(entries)
	fmt.Printf("replayed: %d registrations, %d roots, %d ballots, %d tallies\n", report.Registrations, report.Roots, report.Ballots, report.Tallies)
	problems = append(problems, report.Problems...)

	if withRelays {
		var found []polls.RelayBallot
		for _, r := range relays {
			got, err := polls.RelayBallots(ctx, r)
			if err != nil {
				fmt.Printf("relay %s: %v\n", r, err)
				continue
			}
			fmt.Printf("relay %s: %d ballots\n", r, len(got))
			found = append(found, got...)
		}
		end := time.Now()
		if len(entries) > 0 {
			end = entries[len(entries)-1].CreatedAt
		}
		problems = append(problems, polls.AuditRelays(report, found, end, maxDelay)...)
	}

	raw, err := apiGet(ctx, base, "/api/vote-log/stamps")
	if err != nil {
		return err
	}
	var stamps []polls.Stamp
	if err := json.Unmarshal(raw, &stamps); err != nil {
		return fmt.Errorf("stamps: %w", err)
	}
	for _, st := range stamps {
		t, err := polls.OpenCheckpoint([]byte(st.Checkpoint), key)
		switch {
		case err != nil:
			problems = append(problems, fmt.Sprintf("stamped checkpoint %d: %v", st.Size, err))
			continue
		case t.N > head.N:
			continue
		}
		prefix, err := polls.TreeOf(entries[:t.N])
		if err != nil || prefix != t {
			problems = append(problems, fmt.Sprintf("stamped checkpoint %d is not the log's first %d entries: the log changed its past", st.Size, t.N))
		}
		if stampsDir != "" {
			if err := os.MkdirAll(stampsDir, 0o755); err != nil {
				return err
			}
			name := filepath.Join(stampsDir, fmt.Sprintf("checkpoint-%d.txt", st.Size))
			if err := os.WriteFile(name, []byte(st.Checkpoint), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(name+".ots", st.OTS, 0o644); err != nil {
				return err
			}
		}
	}
	fmt.Printf("stamps: %d checkpoints stamped on Bitcoin\n", len(stamps))

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Println("PROBLEM:", p)
		}
		return fmt.Errorf("%d problems", len(problems))
	}
	fmt.Println("OK: the log is consistent, every ballot proves, every tally recounts")
	return nil
}
