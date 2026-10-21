package cmd

import (
	"context"
	"fmt"

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
	Short: "List the signed-in users' reports (the worker judges the pending ones).",
	Long: `Lists the reports users filed on the site (user_reports). The worker hands each
pending report to the complaint judge within a minute: accepted removed the
target, rejected kept it, ambiguous waits for a person, stale found the target
already gone. By default the ambiguous ones are listed.`,
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
				fmt.Printf("  judge: %s\n", r.VerdictReason)
			}
		}
		fmt.Printf("%d report(s)\n", len(reports))
		return nil
	},
}

func init() {
	complaintsCmd.AddCommand(complaintsReportsCmd)
	complaintsReportsCmd.Flags().StringVar(&complaintsReportsStatus, "status", storage.ReportAmbiguous,
		"pending, accepted, rejected, ambiguous, stale or all")
	complaintsReportsCmd.Flags().IntVar(&complaintsReportsLimit, "limit", 50, "the number of reports to list")
}
