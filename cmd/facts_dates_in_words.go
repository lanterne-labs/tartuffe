package cmd

import (
	"context"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/deps"
)

// factsDatesInWordsCmd rewrites the ISO dates the extractor copied into fact
// texts ("a déclaré le 2026-10-28") in words ("le 28 octobre 2026"). New
// facts are rewritten at extraction (categorizer.isoDatesInWords); this
// repairs the ones stored before (audit of 2026-10-31: 655 facts).
var factsDatesInWordsCmd = &cobra.Command{
	Use:   "dates-in-words",
	Short: "Rewrite the ISO dates of stored fact texts in words",
	RunE:  runFactsDatesInWords,
}

func init() {
	factsCmd.AddCommand(factsDatesInWordsCmd)
	factsDatesInWordsCmd.Flags().Bool("apply", false, "write the rewritten texts (default: report only)")
}

func runFactsDatesInWords(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	apply, _ := cmd.Flags().GetBool("apply")

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	facts, err := storer.ListFactsWithISODates(ctx)
	if err != nil {
		return err
	}
	changed, written := 0, 0
	for _, f := range facts {
		text := categorizer.ISODatesInWords(f.Text, f.Lang)
		if text == f.Text {
			continue
		}
		changed++
		if changed <= 3 {
			fmt.Printf("  %d: %s\n", f.ID, text)
		}
		if !apply {
			continue
		}
		if err := storer.SetFactText(ctx, f.ID, text); err != nil {
			return err
		}
		written++
	}
	fmt.Printf("facts with an ISO date: %d; rewritten %d, written %d\n", len(facts), changed, written)
	if !apply {
		fmt.Println("dry run: nothing written (use --apply)")
	}
	return nil
}
