package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"

	"github.com/spf13/cobra"
)

var factsMigrateMetasApply bool

var factsMigrateMetasCmd = &cobra.Command{
	Use:   "migrate-metas",
	Short: "Replace the synthesized meta-facts with claim groups headed by a real statement",
	Long: `One-off migration from synthesized meta-facts to claim groups. For every
synthetic meta: a meta nested in another group hands its members to that group;
every other meta's most concrete real member becomes the group's parent and
takes its members, contradictions, stance slot and stance key-fact references;
then the synthetic rows are deleted (with their meta:// source and their
translations). No real fact is deleted.

READ-ONLY by default: reports what it would do. Pass --apply to write.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runFactsMigrateMetas,
}

func init() {
	factsCmd.AddCommand(factsMigrateMetasCmd)
	factsMigrateMetasCmd.Flags().BoolVar(&factsMigrateMetasApply, "apply", false, "Apply the migration (deletes the synthetic meta rows)")
}

func runFactsMigrateMetas(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	st, err := core.MigrateSyntheticMetas(ctx, storer, factsMigrateMetasApply)
	if err != nil {
		return err
	}
	rows := []resultRow{
		{Label: "Synthetic metas", Value: int64(st.Metas)},
		{Label: "  nested in another group", Value: int64(st.Nested)},
		{Label: "  heading a group", Value: int64(st.Active)},
	}
	if factsMigrateMetasApply {
		rows = append(rows,
			resultRow{Label: "Nested metas flattened", Value: int64(st.Flattened)},
			resultRow{Label: "Real parents promoted", Value: int64(st.Promoted)},
			resultRow{Label: "Synthetic rows deleted", Value: int64(st.Deleted)},
			resultRow{Label: "Failed (left untouched)", Value: int64(st.Failed)},
		)
	}
	printResults("META MIGRATION RESULTS", rows)
	if !factsMigrateMetasApply {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}
	if st.Failed > 0 {
		return fmt.Errorf("%d metas failed to migrate (see the log); a re-run retries them", st.Failed)
	}
	return nil
}
