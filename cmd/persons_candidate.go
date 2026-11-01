package cmd

import (
	"context"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"digital-mirror/backend/deps"
)

// The candidates are the persons the AI work after extraction runs for
// (backend/storage/candidates.go): the stances, the contradiction detection,
// the maintenance pass. The facts of everyone are still extracted.
var personsCandidateCmd = &cobra.Command{
	Use:   "candidate",
	Short: "Manage the candidates: the persons whose facts get stances and contradiction detection",
}

var personsCandidateAddCmd = &cobra.Command{
	Use:         "add [name]...",
	Short:       "Flag persons as candidates",
	Args:        cobra.MinimumNArgs(1),
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        func(cmd *cobra.Command, args []string) error { return setCandidates(args, true) },
}

var personsCandidateRemoveCmd = &cobra.Command{
	Use:         "remove [name]...",
	Short:       "Unflag persons: their new facts get no stance and no detection",
	Args:        cobra.MinimumNArgs(1),
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        func(cmd *cobra.Command, args []string) error { return setCandidates(args, false) },
}

var personsCandidateListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List the candidates",
	Args:        cobra.NoArgs,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runPersonsCandidateList,
}

func init() {
	personsCmd.AddCommand(personsCandidateCmd)
	personsCandidateCmd.AddCommand(personsCandidateAddCmd, personsCandidateRemoveCmd, personsCandidateListCmd)
}

func setCandidates(names []string, candidate bool) error {
	_ = godotenv.Load()
	ctx := context.Background()
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	for _, name := range names {
		person, ok, err := resolvePersonInteractive(ctx, storer, name, false)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := storer.SetCandidate(ctx, person.ID, candidate); err != nil {
			return err
		}
		if candidate {
			fmt.Printf("%s (ID: %d) is a candidate\n", person.Name, person.ID)
		} else {
			fmt.Printf("%s (ID: %d) is no longer a candidate\n", person.Name, person.ID)
		}
	}
	return nil
}

func runPersonsCandidateList(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	persons, err := storer.ListCandidates(ctx)
	if err != nil {
		return err
	}
	for _, p := range persons {
		fmt.Printf("%d\t%s\n", p.ID, p.Name)
	}
	fmt.Printf("%d candidates\n", len(persons))
	return nil
}
