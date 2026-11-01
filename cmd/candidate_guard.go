package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
)

// requireCandidate keeps the queue to the candidates: a task or a watch for
// another person is refused unless --not-candidate says the caller knows the
// facts will be stored without a stance (storage/candidates.go).
func requireCandidate(ctx context.Context, storer *storage.PostgresStorer, person core.PersonRecord, cmd *cobra.Command) error {
	candidate, err := storer.IsCandidate(ctx, person.ID)
	if err != nil {
		return err
	}
	if candidate {
		return nil
	}
	if force, _ := cmd.Flags().GetBool("not-candidate"); force {
		fmt.Printf("%s is not a candidate: the facts collected get no stance and no contradiction detection\n", person.Name)
		return nil
	}
	return fmt.Errorf("%s is not a candidate: the queue is for the candidates (dm persons candidate add %q), or pass --not-candidate", person.Name, person.Name)
}
