package cmd

import (
	"context"
	"fmt"
	"strconv"

	"digital-mirror/backend/complaints"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var complaintsReportsStatus string
var complaintsReportsLimit int

var complaintsReportsCmd = &cobra.Command{
	Use:   "reports",
	Short: "List the signed-in users' reports; accept or refuse them.",
	Long: `Lists the reports users filed on the site (user_reports). The worker hands each
pending report to the complaint judge within a minute; the judge's verdict is
only advice: the report then waits in review until a person decides with
"dm complaints reports accept <id>" (the target is removed and the correction
published) or "dm complaints reports refuse <id>" (the target stays). Stale:
the target was already gone. By default the reports in review are listed.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		if err := godotenv.Load(); err != nil {
			log.Debug().Msg(".env file not found, relying on environment variables")
		}
		store, err := deps.OpenStorer(ctx)
		if err != nil {
			return fmt.Errorf("init storage: %w", err)
		}
		defer store.Close()
		status := complaintsReportsStatus
		if status == "all" {
			status = ""
		}
		reports, err := store.ListUserReports(ctx, status, complaintsReportsLimit)
		if err != nil {
			return err
		}
		for _, r := range reports {
			fmt.Printf("#%d %s %s %d (%s) %s\n  target: %s\n  report: %s\n",
				r.ID, r.CreatedAt.Format("2006-01-02 15:04"), r.TargetKind, r.TargetID, r.PersonName, r.Status,
				util.TruncateString(r.TargetText, 200), r.Reason)
			if r.VerdictReason != "" {
				advice := "none"
				if r.AIVerdict != nil {
					advice = *r.AIVerdict
				}
				fmt.Printf("  judge (%s): %s\n", advice, r.VerdictReason)
			}
		}
		fmt.Printf("%d report(s)\n", len(reports))
		return nil
	},
}

var complaintsDecideReason string

// decideReportsCmd accepts or refuses reports, one id or more.
func decideReportsCmd(use, short string, accept bool) *cobra.Command {
	return &cobra.Command{
		Use:   use + " <id>...",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			if err := godotenv.Load(); err != nil {
				log.Debug().Msg(".env file not found, relying on environment variables")
			}
			store, err := deps.OpenStorer(ctx)
			if err != nil {
				return fmt.Errorf("init storage: %w", err)
			}
			defer store.Close()
			for _, a := range args {
				id, err := strconv.ParseInt(a, 10, 64)
				if err != nil {
					return fmt.Errorf("report id %q: %w", a, err)
				}
				status, err := complaints.DecideReport(ctx, store, id, accept, complaintsDecideReason)
				if err != nil {
					return err
				}
				fmt.Printf("#%d %s\n", id, status)
			}
			return nil
		},
	}
}

func init() {
	complaintsCmd.AddCommand(complaintsReportsCmd)
	complaintsReportsCmd.Flags().StringVar(&complaintsReportsStatus, "status", storage.ReportReview,
		"pending, review, accepted, rejected, stale or all")
	complaintsReportsCmd.Flags().IntVar(&complaintsReportsLimit, "limit", 50, "the number of reports to list")
	accept := decideReportsCmd("accept", "Accept reports: remove their targets and publish the corrections.", true)
	refuse := decideReportsCmd("refuse", "Refuse reports: their targets stay.", false)
	for _, c := range []*cobra.Command{accept, refuse} {
		c.Flags().StringVar(&complaintsDecideReason, "reason", "", "the reason shown in place of the judge's (public for an accepted report)")
		complaintsReportsCmd.AddCommand(c)
	}
}
