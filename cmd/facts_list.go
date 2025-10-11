package cmd

import (
	"context"
	"digital-mirror/backend/storage"
	"fmt"
	"sort"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var listPersonFlag string // Variable to hold the person flag for the list command

// factsListCmd represents the facts list command
var factsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List stored facts. Can be filtered by person.",
	Long:  `Retrieves stored facts and associated source information. By default, lists all facts grouped by person. Use the --person flag to list facts only for a specific person.`,
	Args:  cobra.NoArgs,
	Run:   listFacts,
}

func init() {
	factsCmd.AddCommand(factsListCmd)
	factsListCmd.Flags().StringVarP(&listPersonFlag, "person", "p", "", "Specify a single person name to list facts for (optional)")
}

func listFacts(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	if listPersonFlag != "" {
		log.Info().Str("person", listPersonFlag).Msg("Attempting to list facts for a specific person...")
	} else {
		log.Info().Msg("Attempting to list all facts grouped by person...")
	}

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	if listPersonFlag != "" {
		// Logic to list facts for a specific person
		factDetails, err := storer.GetFactsForPerson(ctx, listPersonFlag) // This method needs to be implemented
		if err != nil {
			log.Error().Err(err).Str("person", listPersonFlag).Msg("Failed to retrieve facts for person")
			return
		}
		if len(factDetails) == 0 {
			fmt.Printf("No facts found for person: %s\n", listPersonFlag)
			log.Info().Str("person", listPersonFlag).Msg("No facts found for specified person.")
			return
		}

		fmt.Printf("--- Stored Facts for Person: %s ---\n", listPersonFlag)
		// Group by fact_text for cleaner display, similar to the original logic
		factsMap := make(map[string][]storage.FactDetail)
		for _, detail := range factDetails {
			factsMap[detail.FactText] = append(factsMap[detail.FactText], detail)
		}

		factTexts := make([]string, 0, len(factsMap))
		for text := range factsMap {
			factTexts = append(factTexts, text)
		}
		sort.Strings(factTexts) // Sort fact texts for consistent output

		for _, factText := range factTexts {
			fmt.Printf("\n  Fact: %s\n", factText)
			sources := factsMap[factText]
			// Sort sources by URL for consistency if multiple sources for the same fact
			sort.Slice(sources, func(i, j int) bool {
				return sources[i].SourceURL < sources[j].SourceURL
			})
			for _, source := range sources {
				fmt.Printf("    - Source URL: %s\n", source.SourceURL)
				fmt.Printf("      Original Quote: %s\n", source.OriginalQuote)
			}
		}
		log.Info().Str("person", listPersonFlag).Int("fact_count", len(factDetails)).Msg("Successfully listed facts for person.")

	} else {
		// Original logic to list all facts grouped by person
		factsByPerson, err := storer.GetAllFactsGroupedByPerson(ctx)
		if err != nil {
			log.Error().Err(err).Msg("Failed to retrieve facts from database")
			return
		}

		if len(factsByPerson) == 0 {
			fmt.Println("No facts found in the database.")
			log.Info().Msg("No facts found in the database.")
			return
		}

		personNames := make([]string, 0, len(factsByPerson))
		for name := range factsByPerson {
			personNames = append(personNames, name)
		}
		sort.Strings(personNames)

		fmt.Println("--- Stored Facts Grouped by Person ---")
		for _, personName := range personNames {
			fmt.Printf("\n## Person: %s\n", personName)
			factDetails := factsByPerson[personName]

			factsMap := make(map[string][]storage.FactDetail)
			for _, detail := range factDetails {
				factsMap[detail.FactText] = append(factsMap[detail.FactText], detail)
			}

			factTexts := make([]string, 0, len(factsMap))
			for text := range factsMap {
				factTexts = append(factTexts, text)
			}
			sort.Strings(factTexts)

			for _, factText := range factTexts {
				fmt.Printf("\n  Fact: %s\n", factText)
				sources := factsMap[factText]
				sort.Slice(sources, func(i, j int) bool {
					return sources[i].SourceURL < sources[j].SourceURL
				})
				for _, source := range sources {
					fmt.Printf("    - Source URL: %s\n", source.SourceURL)
					fmt.Printf("      Original Quote: %s\n", source.OriginalQuote)
				}
			}
		}
		log.Info().Int("person_count", len(factsByPerson)).Msg("Successfully listed all facts")
	}
}
