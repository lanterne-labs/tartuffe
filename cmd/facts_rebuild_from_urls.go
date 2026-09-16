package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var factsRebuildFromURLsCmd = &cobra.Command{
	Use:   "rebuild-from-urls",
	Short: "Re-run the news extraction pipeline against URLs that previously produced facts for given persons.",
	Long: `For each --person, finds the distinct news source_urls that previously produced
fact_mentions for that person, deletes those fact_sources (cascading mentions),
sweeps orphan facts, then re-scrapes each URL via the cleaning pipeline and
extracts facts as the daily pipeline would. Stance routing and contradiction
detection run inline via the normal FactInserter path.

Resumable via facts_rebuild_progress.json. Append-only audit at facts_rebuild_log.jsonl.

Example (test phase):
  ./dm facts rebuild-from-urls --person "Emmanuel Macron" --person "Marine Le Pen" --person "Donald Trump"`,
	RunE: runRebuildFromURLs,
}

func init() {
	factsCmd.AddCommand(factsRebuildFromURLsCmd)
	factsRebuildFromURLsCmd.Flags().StringSlice("person", nil, "Person name(s) to rebuild facts for (repeatable)")
	factsRebuildFromURLsCmd.Flags().Bool("reset", false, "Ignore checkpoint file and start from scratch")
	factsRebuildFromURLsCmd.Flags().Int("max-urls-per-person", 0, "Optional cap on URLs processed per person (0 = no cap)")
	factsRebuildFromURLsCmd.Flags().String("urls-from-log", "", "Recovery mode: read (person, url) pairs from this rebuild-log JSONL instead of querying the DB. Each line must have 'person' and 'url' fields. Use to re-process URLs whose fact_sources were already deleted by a previous run.")
}

func runRebuildFromURLs(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env not found or error loading")
	}

	personNames, _ := cmd.Flags().GetStringSlice("person")
	reset, _ := cmd.Flags().GetBool("reset")
	maxURLsPerPerson, _ := cmd.Flags().GetInt("max-urls-per-person")
	urlsFromLog, _ := cmd.Flags().GetString("urls-from-log")
	if len(personNames) == 0 && urlsFromLog == "" {
		return fmt.Errorf("either --person or --urls-from-log is required")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("storage init: %w", err)
	}
	defer storer.Close()

	provider, err := news.NewProviderFromDeps(deps.New(storer))
	if err != nil {
		return fmt.Errorf("news provider init: %w", err)
	}

	// Build the work list: either from --urls-from-log (recovery mode) or from the DB per-person.
	var items []news.RebuildItem
	if urlsFromLog != "" {
		pairs, err := news.ReadRebuildLog(urlsFromLog)
		if err != nil {
			return fmt.Errorf("read urls-from-log: %w", err)
		}
		log.Info().Int("pairs", len(pairs)).Str("file", urlsFromLog).Msg("rebuild: loaded (person, url) pairs from log")

		// Resolve each person once; nil marks a name that could not be resolved.
		targets := map[string]*plugins.Target{}
		for _, pair := range pairs {
			target, seen := targets[pair.Person]
			if !seen {
				resolved, err := resolvePersonTarget(ctx, storer, pair.Person)
				if err != nil {
					log.Error().Err(err).Str("person", pair.Person).Msg("could not resolve person, skipping its URLs")
					targets[pair.Person] = nil
					continue
				}
				target = &resolved
				targets[pair.Person] = target
			}
			if target == nil {
				continue
			}
			items = append(items, news.RebuildItem{Target: *target, URL: pair.URL})
		}
	} else {
		for _, name := range personNames {
			target, err := resolvePersonTarget(ctx, storer, name)
			if err != nil {
				log.Error().Err(err).Str("person", name).Msg("could not resolve person, skipping")
				continue
			}
			urls, err := provider.NewsURLsForTarget(ctx, target, maxURLsPerPerson)
			if err != nil {
				log.Error().Err(err).Str("person", name).Msg("NewsURLsForTarget failed, skipping")
				continue
			}
			for _, u := range urls {
				items = append(items, news.RebuildItem{Target: target, URL: u})
			}
		}
	}

	summary, err := provider.RebuildFromURLs(ctx, items, news.RebuildOptions{Reset: reset})
	if err != nil {
		return err
	}

	fmt.Printf("\n=== rebuild-from-urls complete ===\n")
	fmt.Printf("  persons:           %d\n", len(personNames))
	fmt.Printf("  urls processed:    %d\n", summary.URLsProcessed)
	fmt.Printf("  facts extracted:   %d\n", summary.FactsExtracted)
	fmt.Printf("  orphan facts swept: %d\n", summary.OrphansSwept)
	fmt.Printf("  log file:          %s\n", news.RebuildLogFile)
	fmt.Printf("  checkpoint:        %s\n", news.RebuildCheckpointFile)
	return nil
}

// resolvePersonTarget looks up a person by name. The rebuild always treats
// the name as a human target; party affiliations are loaded by the provider.
func resolvePersonTarget(ctx context.Context, storer *storage.PostgresStorer, name string) (plugins.Target, error) {
	person, err := core.ResolvePersonID(ctx, storer, name, core.ExactMatchOnly)
	if err != nil {
		return plugins.Target{}, err
	}
	return plugins.Target{PersonID: person.ID, Name: name, EntityType: "human"}, nil
}
