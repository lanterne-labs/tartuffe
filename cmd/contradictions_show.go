package cmd

import (
	"context"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	showPersonFlag string
)

var contradictionsShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show contradictions for a person.",
	Long:  `Displays all detected contradictions for a given person, including the two conflicting facts, their dates, and the reason.`,
	RunE:  runContradictionShow,
	Annotations: map[string]string{
		"needs_db": "true",
	},
}

func init() {
	contradictionsCmd.AddCommand(contradictionsShowCmd)
	contradictionsShowCmd.Flags().StringVarP(&showPersonFlag, "person", "p", "", "Person name to show contradictions for (required)")
	contradictionsShowCmd.MarkFlagRequired("person")
}

func runContradictionShow(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	normalizedName := util.NormalizeString(showPersonFlag)
	personID, err := storer.GetPersonIDByName(ctx, normalizedName)
	if err != nil {
		return fmt.Errorf("failed to find person %q: %w", showPersonFlag, err)
	}

	result, err := storer.GetPersonContradictions(ctx, personID, 100, 0)
	if err != nil {
		return fmt.Errorf("failed to get contradictions: %w", err)
	}

	if len(result.Items) == 0 {
		fmt.Printf("No contradictions found for %s (ID: %d)\n", normalizedName, personID)
		return nil
	}

	fmt.Printf("Found %d contradiction(s) for %s (ID: %d)\n\n", result.Total, normalizedName, personID)

	for i, c := range result.Items {
		dateA := "unknown"
		if c.FactADate != nil {
			dateA = c.FactADate.Format("2006-01-02")
		}
		dateB := "unknown"
		if c.FactBDate != nil {
			dateB = c.FactBDate.Format("2006-01-02")
		}

		fmt.Printf("--- Contradiction %d (detected %s) ---\n", i+1, c.DetectedAt.Format("2006-01-02 15:04"))
		fmt.Printf("  Fact A [%s]: %s\n", dateA, c.FactAText)
		for _, url := range c.FactASourceURLs {
			fmt.Printf("    Source: %s\n", url)
		}
		fmt.Printf("  Fact B [%s]: %s\n", dateB, c.FactBText)
		for _, url := range c.FactBSourceURLs {
			fmt.Printf("    Source: %s\n", url)
		}
		fmt.Printf("  Reason: %s\n\n", c.Reason)
	}

	return nil
}
