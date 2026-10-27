package cmd

import (
	"fmt"

	"digital-mirror/backend/vote"

	"github.com/spf13/cobra"
)

var voteArchiveCmd = &cobra.Command{
	Use:   "archive [slug]",
	Short: "Keep a copy of the relays of a vote, as any auditor may (SPEC §12.6)",
	Long: `Reads every event of the vote on the relays and writes each, with the time
it was first seen here, to <dir>/<relay>/<day>.jsonl. It never posts. Run
continuously, apart from our server and on a machine whose clock is right,
its first sightings attribute an event no one acknowledged (dm vote verify
--archive); run once (--once), it keeps what the relays hold today.`,
	Example:      `  dm vote archive presidentielle-2027 --dir ./relays --api https://tartuffe.me`,
	Args:         cobra.ExactArgs(1),
	RunE:         runVoteArchive,
	SilenceUsage: true,
}

func init() {
	voteCmd.AddCommand(voteArchiveCmd)
	f := voteArchiveCmd.Flags()
	f.String("dir", "", "the archive's directory (required)")
	f.String("api", "https://tartuffe.me", "base URL of the site's API, for the setup's relays")
	f.String("relays", "setup", "'setup' for the setup's relays, or wss:// URLs, comma-separated")
	f.Bool("once", false, "read what the relays hold and exit")
	_ = voteArchiveCmd.MarkFlagRequired("dir")
}

func runVoteArchive(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	f := cmd.Flags()
	dir, _ := f.GetString("dir")
	base, _ := f.GetString("api")
	relayList, _ := f.GetString("relays")
	once, _ := f.GetBool("once")
	slug := args[0]
	var urls []string
	if relayList == "setup" {
		eventID, err := eventIDOf(ctx, base, slug)
		if err != nil {
			return err
		}
		if urls, err = relayURLs(ctx, apiSource{base}, eventID, "setup"); err != nil {
			return err
		}
	} else if urls, _ = relayURLs(ctx, nil, 0, relayList); len(urls) == 0 {
		return fmt.Errorf("no relay in %q", relayList)
	}
	a, err := vote.NewArchiver(dir, slug, urls)
	if err != nil {
		return err
	}
	fmt.Printf("archiving %d relays of %s into %s\n", len(urls), slug, dir)
	return a.Run(ctx, once)
}
