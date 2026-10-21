package cmd

import (
	"context"
	"digital-mirror/backend/deps"
	"fmt"

	"github.com/spf13/cobra"
)

var qualityCmd = &cobra.Command{
	Use:   "quality",
	Short: "Quality gauges of the stances, claim groups and contradictions.",
}

var qualityRecordCmd = &cobra.Command{
	Use:   "record",
	Short: "Measure the quality gauges now and store them (job_metrics, job quality).",
	Long: `Measures the stances (count, one-fact stances, persons), the claim groups
(parents, members) and the contradictions (count, score bundles, persons), and
stores them in job_metrics under the job "quality". The worker records them at
the end of each maintenance pass; Grafana (Quality) draws their history.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runQualityRecord,
}

func init() {
	rootCmd.AddCommand(qualityCmd)
	qualityCmd.AddCommand(qualityRecordCmd)
}

func runQualityRecord(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	rows, err := storer.RecordQualityMetrics(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		fmt.Printf("%-22s %.0f\n", r.Metric, r.Value)
	}
	return nil
}
