package cmd

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"digital-mirror/backend/deps"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

// queueCmd represents the queue command group
var queueCmd = &cobra.Command{
	Use:   "queue",
	Short: "Manage the search queue",
	Long:  `Add, remove, list, and process persons in the search queue. The search queue is shared across all plugins.`,
}

// queueAddCmd adds a person to the search queue
var queueAddCmd = &cobra.Command{
	Use:         "add [person-name]",
	Short:       "Add a person to the search queue",
	Long:        `Add a person to the search queue. The person must already exist in the database.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runQueueAdd,
}

// queueRemoveCmd removes a person from the search queue
var queueRemoveCmd = &cobra.Command{
	Use:         "remove [person-name]",
	Short:       "Remove a person from the search queue",
	Long:        `Remove a person from the search queue by name.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runQueueRemove,
}

// queueListCmd lists all persons in the search queue
var queueListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List all persons in the search queue",
	Long:        `List all persons currently in the search queue, ordered by person ID.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runQueueList,
}

func init() {
	rootCmd.AddCommand(queueCmd)

	queueAddCmd.Flags().Bool("not-candidate", false, "watch the person although they are not a candidate (the facts are stored without a stance)")
	queueCmd.AddCommand(queueAddCmd)
	queueCmd.AddCommand(queueRemoveCmd)
	queueCmd.AddCommand(queueListCmd)

}

func runQueueAdd(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	person, ok, err := resolvePersonInteractive(ctx, storer, args[0], false)
	if err != nil || !ok {
		return err
	}
	if err := requireCandidate(ctx, storer, person, cmd); err != nil {
		return err
	}
	if err := storer.AddToSearchQueue(ctx, person.ID); err != nil {
		return fmt.Errorf("failed to add to queue: %w", err)
	}
	if inQueue, _ := storer.IsInSearchQueue(ctx, person.ID); inQueue {
		fmt.Printf("Added %s (ID: %d) to search queue\n", person.Name, person.ID)
	}
	return nil
}

func runQueueRemove(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	person, ok, err := resolvePersonInteractive(ctx, storer, args[0], false)
	if err != nil || !ok {
		return err
	}
	if err := storer.RemoveFromSearchQueue(ctx, person.ID); err != nil {
		return err
	}
	fmt.Printf("Removed %s (ID: %d) from search queue\n", person.Name, person.ID)
	return nil
}

func runQueueList(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	queue, err := storer.GetSearchQueue(ctx)
	if err != nil {
		return fmt.Errorf("failed to get search queue: %w", err)
	}
	if len(queue) == 0 {
		fmt.Println("Search queue is empty")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PERSON ID\tNAME\tTYPE\tADDED AT")
	for _, entry := range queue {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n", entry.PersonID, entry.Name, entry.EntityType, entry.AddedAt.Format("2006-01-02 15:04"))
	}
	w.Flush()
	fmt.Printf("\nTotal: %d persons in queue\n", len(queue))
	return nil
}
