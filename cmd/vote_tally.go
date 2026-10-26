package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"digital-mirror/backend/vote"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var voteTallyCmd = &cobra.Command{
	Use:   "tally [slug]",
	Short: "Run the tallier of an event: decrypt the ballots and publish the results",
	Long: `The tallier holds the election secret keys (--keys, from dm vote keygen).
It decrypts every logged ballot for itself, and once K_REAL pseudonyms have
changed their ballot since the last results, publishes a tally entry with
the sums, the decrypted totals and the proofs of decryption (SPEC §9). It
listens on no port: run it apart from the ingest server, on a machine with
no inbound access, with only the database to talk to.`,
	Args: cobra.ExactArgs(1),
	RunE: runVoteTally,
}

func init() {
	voteCmd.AddCommand(voteTallyCmd)
	voteTallyCmd.Flags().String("keys", "", "the secret keys file of keygen (required)")
	voteTallyCmd.Flags().Bool("once", false, "scan and publish if due, then exit")
	_ = voteTallyCmd.MarkFlagRequired("keys")
}

func runVoteTally(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	keysFile, _ := cmd.Flags().GetString("keys")
	raw, err := os.ReadFile(keysFile)
	if err != nil {
		return err
	}
	var keys vote.Keys
	if err := json.Unmarshal(raw, &keys); err != nil {
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
	if keys.Event != e.ID {
		return fmt.Errorf("the keys are for event %d, not %d", keys.Event, e.ID)
	}
	tl, err := vote.NewTallier(ctx, vote.NewStore(storer.Pool()), e, &keys)
	if err != nil {
		return err
	}
	log.Info().Str("event", e.Slug).Msg("vote: tallier starting")
	if once, _ := cmd.Flags().GetBool("once"); once {
		return tl.Step(ctx)
	}
	return tl.Run(ctx)
}
