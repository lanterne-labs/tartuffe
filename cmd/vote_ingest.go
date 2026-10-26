package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/api"
	"digital-mirror/backend/vote"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var voteIngestCmd = &cobra.Command{
	Use:   "ingest [slug]",
	Short: "Run the ingest server of an event: relays, log, acknowledgements, checkpoints",
	Long: `Reads the event's relays, checks every registration and ballot, logs them
at each checkpoint (ordered by hash), acknowledges them on the relay they
came from, signs and posts the checkpoints (SPEC §7–8). It never serves a
voter: the browser reads the app and the data from the static hosts.

Environment: DATABASE_URL, VOTE_LOG_KEY (the log's signing key),
VOTE_NOSTR_KEY (the server's Nostr key, 32 bytes of hex), the ZKPASSPORT_*
settings of the sign-in (domain, RPC), and the verification key of the
event's circuit: --vk, from dm vote setup.`,
	Args: cobra.ExactArgs(1),
	RunE: runVoteIngest,
}

func init() {
	voteCmd.AddCommand(voteIngestCmd)
	voteIngestCmd.Flags().String("vk", "", "the circuit's verification key file of dm vote setup (required)")
	voteIngestCmd.Flags().String("mirror-dir", "", "directory of the public data mirror (SPEC §8.3), served by a static host")
	voteIngestCmd.Flags().String("artifact", "", "the compiled circuit (ballot.json of dm vote setup), published on the mirror")
	_ = voteIngestCmd.MarkFlagRequired("vk")
}

func runVoteIngest(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	storer, err := openPollStorer(ctx)
	if err != nil {
		return err
	}
	defer storer.Close()
	e, err := mustPollEvent(ctx, storer, args[0])
	if err != nil {
		return err
	}
	vkPath, _ := cmd.Flags().GetString("vk")
	vk, err := os.ReadFile(vkPath)
	if err != nil {
		return err
	}
	var artifact []byte
	if p, _ := cmd.Flags().GetString("artifact"); p != "" {
		if artifact, err = os.ReadFile(p); err != nil {
			return err
		}
	}
	key, err := vote.NostrKeyFromEnv()
	if err != nil {
		return err
	}
	pollsCfg, err := api.PollsFromEnv()
	if err != nil {
		return err
	}
	if os.Getenv("VOTE_LOG_KEY") == "" {
		return fmt.Errorf("VOTE_LOG_KEY is required: the checkpoints of a restart would not verify")
	}
	zk, err := providers.NewZKPassportFromEnv()
	if err != nil {
		return err
	}
	z, ok := zk.(*providers.ZKPassport)
	if !ok {
		return fmt.Errorf("the ZKPassport provider is not configured")
	}
	repo, _ := filepath.Abs(".")
	tools, err := vote.FindTools(repo)
	if err != nil {
		return err
	}
	store := vote.NewStore(storer.Pool())
	setups, err := store.Setups(ctx, e.ID)
	if err != nil {
		return err
	}
	if len(setups) == 0 {
		return fmt.Errorf("no setup logged for %s (dm vote setup)", e.Slug)
	}
	mail := vote.NewNostrMailbox(e.Slug, key, vote.ActiveBoxes(setups))
	// What the page shows of the event and its candidates, from the mirror.
	extra := map[string][]byte{}
	if b, err := json.Marshal(e); err == nil {
		extra["event.json"] = b
	}
	ids, err := pollStore(storer).CandidateIDs(ctx, e.ID)
	if err != nil {
		return err
	}
	type card struct {
		PersonID     int64   `json:"person_id"`
		Name         string  `json:"name"`
		ImageURL     *string `json:"image_url,omitempty"`
		HonestyScore float64 `json:"honesty_score"`
	}
	var cards []card
	for _, id := range ids {
		p, err := storer.GetPersonByID(ctx, id)
		if err != nil {
			return err
		}
		cards = append(cards, card{PersonID: id, Name: p.Name, ImageURL: p.ImageURL, HonestyScore: p.HonestyScore})
	}
	if b, err := json.Marshal(cards); err == nil {
		extra["candidates.json"] = b
	}
	var mirror vote.Mirror
	if dir, _ := cmd.Flags().GetString("mirror-dir"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		mirror = &vote.DirMirror{Dir: dir}
	}
	in, err := vote.NewIngest(ctx, store, vote.Config{
		Event:    e,
		Mail:     mail,
		Signer:   pollsCfg.Signer,
		Passport: vote.Passport{Z: z, Scope: "tartuffe-vote/2:" + e.Slug},
		BB:       tools.BB,
		VK:       vk,
		Stamp: func(ctx context.Context, size int64, note []byte) error {
			_, err := store.Log.StampNote(ctx, size, note)
			return err
		},
		Mirror:   mirror,
		Artifact: artifact,
		Extra:    extra,
	})
	if err != nil {
		return err
	}
	log.Info().Str("event", e.Slug).Str("nostr", key.Public).Msg("vote: ingest starting")
	return in.Run(ctx)
}
