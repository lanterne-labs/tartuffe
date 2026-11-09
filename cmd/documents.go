package cmd

import (
	"context"
	"fmt"
	"sort"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"digital-mirror/backend/deps"
)

// documentsCmd works on the stored pages (source_documents): the texts the
// facts are extracted from, with the state of their extraction.
var documentsCmd = &cobra.Command{
	Use:   "documents",
	Short: "The stored pages and the state of their extraction",
}

// documentsRetryCmd gives their attempts back to the documents that failed
// for a reason that was not theirs: an LLM outage taken for an error of each
// document. On 2026-11-09 the API's prepaid credit ran out; the API answered
// 400 "credit balance is too low", the worker marked 18 000 documents failed
// in three hours. The retry is for once the cause is gone: the next run
// extracts them again.
var documentsRetryCmd = &cobra.Command{
	Use:   "retry --match <text>",
	Short: "Give their attempts back to the failed documents whose error contains the text",
	RunE:  runDocumentsRetry,
}

func init() {
	rootCmd.AddCommand(documentsCmd)
	documentsCmd.AddCommand(documentsRetryCmd)
	documentsRetryCmd.Flags().String("match", "", "the text the last error must contain (case-insensitive), e.g. 'credit balance'")
	documentsRetryCmd.Flags().Bool("apply", false, "reset the attempts (default: count only)")
	_ = documentsRetryCmd.MarkFlagRequired("match")
}

func runDocumentsRetry(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	match, _ := cmd.Flags().GetString("match")
	apply, _ := cmd.Flags().GetBool("apply")

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	counts, err := storer.FailedDocumentsByError(ctx, match)
	if err != nil {
		return err
	}
	types := make([]string, 0, len(counts))
	total := 0
	for st, n := range counts {
		types = append(types, st)
		total += n
	}
	sort.Strings(types)
	for _, st := range types {
		fmt.Printf("%-18s %7d\n", st, counts[st])
	}
	if total == 0 {
		fmt.Printf("no failed document has an error containing %q\n", match)
		return nil
	}
	if !apply {
		fmt.Printf("%d documents would get their attempts back (dry run: add --apply)\n", total)
		return nil
	}
	n, err := storer.RetryFailedDocuments(ctx, match)
	if err != nil {
		return err
	}
	fmt.Printf("%d documents got their attempts back; the next extraction run takes them\n", n)
	return nil
}
