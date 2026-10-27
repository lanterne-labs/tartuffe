package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote"
	"digital-mirror/backend/vote/ballot"

	"github.com/spf13/cobra"
)

var voteCmd = &cobra.Command{
	Use:   "vote",
	Short: "tartuffe-vote/2: the anonymous vote of an event (docs/vote/SPEC.md)",
}

var voteKeygenCmd = &cobra.Command{
	Use:   "keygen [slug]",
	Short: "Make the election keys of an event, on the tallier",
	Long: `Writes the secret keys to --out (readable by its owner only) and the public
keys with their proofs of knowledge to <out>.pub.json, which the setup takes.
The secret file never leaves the tallier; back it up as SPEC §10 says.`,
	Args: cobra.ExactArgs(1),
	RunE: runVoteKeygen,
}

var voteSetupCmd = &cobra.Command{
	Use:   "setup [slug]",
	Short: "Compile the ballot circuit for the event's keys and log the setup entry",
	Long: `Builds circuits/ballot for the public keys of --keys, writes the artifact and
verification key to --out-dir (what the app and the verifiers load), composes
the setup entry of SPEC §11 from the event (candidates, close, blackouts) and
the flags, checks it, and appends it to the public log.`,
	Args: cobra.ExactArgs(1),
	RunE: runVoteSetup,
}

var voteLogKeyCmd = &cobra.Command{
	Use:   "log-key",
	Short: "Make a new key for the public log's checkpoints",
	Long: `Prints a new signer key (VOTE_LOG_KEY of the API and the ingest: keep it
secret, set it once, never change it, or every browser that saw the old
checkpoints reports a fault) and its verifier key (public: publish it, pin it
in the frontend as VITE_VOTE_LOG_KEY).`,
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

func init() {
	rootCmd.AddCommand(voteCmd)
	voteCmd.AddCommand(voteKeygenCmd, voteSetupCmd, voteLogKeyCmd)
	voteKeygenCmd.Flags().String("out", "", "file for the secret keys (required)")
	_ = voteKeygenCmd.MarkFlagRequired("out")

	f := voteSetupCmd.Flags()
	f.String("keys", "", "the public keys file of keygen (required)")
	f.String("circuit", "circuits/ballot", "the circuit's directory")
	f.String("out-dir", "", "where the artifact, vk and setup body go (default <circuit>/target/setup-<slug>)")
	f.StringSlice("relays", nil, "the active relays, wss:// URLs (required)")
	f.String("nostr-pubkey", "", "our server's Nostr public key, hex")
	f.String("app-hash", "", "hash of the reproducible app build")
	f.String("srs-hash", "", "hash of the SRS file the app loads")
	f.Int("k-real", vote.DefaultKReal, "changed ballots per results update")
	f.Int("r-min", vote.DefaultRMin, "later registrants a first ballot's root must share")
	f.Int64("tally-min-gap", 0, "minimum seconds between tallies (86400 for the daily variant)")
	f.String("tally-at", "", "time of day of the daily variant, HH:MM Paris time")
	f.Int64("anchor-age", vote.DefaultAnchorAge, "how old a ballot's root may be, seconds")
	f.Int64("checkpoint-period", vote.DefaultCheckpointPeriod, "seconds between checkpoints")
	f.Int64("validity", vote.DefaultValidity, "ZKPassport proof age and registration freshness, seconds")
	f.Int("version", 1, "the setup's version within the event")
	f.Bool("dry-run", false, "build and print the setup without logging it")
	_ = voteSetupCmd.MarkFlagRequired("keys")
	_ = voteSetupCmd.MarkFlagRequired("relays")
}

func runVoteKeygen(cmd *cobra.Command, args []string) error {
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
	out, _ := cmd.Flags().GetString("out")
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("%s exists: the keys of an event never change, delete it yourself if you mean it", out)
	}
	k, err := vote.GenerateKeys(e.ID)
	if err != nil {
		return err
	}
	secret, _ := json.MarshalIndent(k, "", " ")
	if err := os.WriteFile(out, secret, 0o600); err != nil {
		return err
	}
	public, _ := json.MarshalIndent(k.Public(), "", " ")
	pub := strings.TrimSuffix(out, ".json") + ".pub.json"
	if err := os.WriteFile(pub, public, 0o644); err != nil {
		return err
	}
	fmt.Printf("secret keys: %s (keep on the tallier)\npublic keys: %s (give to the setup)\n", out, pub)
	return nil
}

func runVoteSetup(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	f := cmd.Flags()
	keysFile, _ := f.GetString("keys")
	raw, err := os.ReadFile(keysFile)
	if err != nil {
		return err
	}
	var pub vote.PublicKeys
	if err := json.Unmarshal(raw, &pub); err != nil {
		return fmt.Errorf("%s: %w", keysFile, err)
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
	if pub.Event != e.ID {
		return fmt.Errorf("the keys are for event %d, not %d", pub.Event, e.ID)
	}
	pk, err := pub.ParsedKeys()
	if err != nil {
		return err
	}
	if e.ClosesAt == nil {
		return errors.New("the event has no close: set one (dm poll event set --closes), at the end of its final blackout")
	}
	circuitDir, _ := f.GetString("circuit")
	outDir, _ := f.GetString("out-dir")
	if outDir == "" {
		outDir = filepath.Join(circuitDir, "target", "setup-"+e.Slug)
	}
	repo, _ := filepath.Abs(".")
	tools, err := vote.FindTools(repo)
	if err != nil {
		return err
	}
	fmt.Printf("compiling %s for the event's keys (%s, %s)…\n", circuitDir, tools.Nargo, tools.BB)
	built, err := vote.BuildCircuit(circuitDir, tools, pk)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for name, b := range map[string][]byte{"ballot.json": built.ArtifactJS, "vk": built.VK} {
		if err := os.WriteFile(filepath.Join(outDir, name), b, 0o644); err != nil {
			return err
		}
	}

	ids, err := pollStore(storer).CandidateIDs(ctx, e.ID)
	if err != nil {
		return err
	}
	if len(ids) > ballot.Blank {
		return fmt.Errorf("%d candidates, the circuit holds %d plus the blank vote", len(ids), ballot.Blank)
	}
	var options []vote.Option
	for slot := 0; slot < ballot.N; slot++ {
		o := vote.Option{Slot: slot, Kind: "reserved"}
		switch {
		case slot < len(ids):
			p, err := storer.GetPersonByID(ctx, ids[slot])
			if err != nil {
				return err
			}
			o = vote.Option{Slot: slot, Kind: "candidate", PersonID: ids[slot], Name: p.Name}
		case slot == ballot.Blank:
			o = vote.Option{Slot: slot, Kind: "blank"}
		}
		options = append(options, o)
	}
	var mask uint32
	for _, o := range options {
		if o.Kind == "candidate" || o.Kind == "blank" {
			mask |= 1 << uint(o.Slot)
		}
	}
	relayURLs, _ := f.GetStringSlice("relays")
	var relays []vote.Relay
	for _, u := range relayURLs {
		relays = append(relays, vote.Relay{URL: strings.TrimSpace(u), State: "active"})
	}
	var blackouts []vote.Blackout
	for _, b := range e.Blackouts {
		blackouts = append(blackouts, vote.Blackout{From: b.StartsAt, To: b.EndsAt})
	}
	s := vote.Setup{V: vote.Version, Event: e.ID, Options: options, Blank: ballot.Blank, Mask: mask, NCap: ballot.N,
		PK: pub.PK, PKPoK: pub.PoK, TallyPubkey: pub.TallyPubkey, VKHash: built.VKHash, Circuit: built.Circuit, Relays: relays, Closes: e.ClosesAt.UTC(), Blackouts: blackouts}
	s.Version, _ = f.GetInt("version")
	s.NostrPubkey, _ = f.GetString("nostr-pubkey")
	s.AppHash, _ = f.GetString("app-hash")
	s.SRSHash, _ = f.GetString("srs-hash")
	s.KReal, _ = f.GetInt("k-real")
	s.RMin, _ = f.GetInt("r-min")
	s.TallyMinGap, _ = f.GetInt64("tally-min-gap")
	s.TallyAt, _ = f.GetString("tally-at")
	s.AnchorAge, _ = f.GetInt64("anchor-age")
	s.CheckpointPeriod, _ = f.GetInt64("checkpoint-period")
	s.Validity, _ = f.GetInt64("validity")
	if err := s.Validate(); err != nil {
		return err
	}
	body, err := vote.Canonical(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "setup.json"), body, 0o644); err != nil {
		return err
	}
	if dry, _ := f.GetBool("dry-run"); dry {
		fmt.Printf("%s\n(not logged: --dry-run)\n", body)
		return nil
	}
	seq, err := pollStore(storer).Append(ctx, vote.KindSetup, body)
	if err != nil {
		return err
	}
	fmt.Printf("setup logged at seq %d; artifact, vk and setup.json in %s\n", seq, outDir)
	return nil
}
