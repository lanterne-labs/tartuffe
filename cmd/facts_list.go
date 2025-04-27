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

// factsListCmd represents the facts list command
var factsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all stored facts, grouped by person.",
	Long:  `Retrieves all stored facts and associated source information, grouped by the persons mentioned.`,
	Args:  cobra.NoArgs,
	Run:   listFacts,
}

func init() {
	factsCmd.AddCommand(factsListCmd)
}

func listFacts(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	log.Info().Msg("Attempting to list facts grouped by person...")

	// Load environment variables if needed
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	// Initialize storer
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize database storer")
		return
	}
	defer storer.Close()

	// Get facts grouped by person
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

	// Sort person names for consistent output
	personNames := make([]string, 0, len(factsByPerson))
	for name := range factsByPerson {
		personNames = append(personNames, name)
	}
	sort.Strings(personNames)

	fmt.Println("--- Stored Facts Grouped by Person ---")
	for _, personName := range personNames {
		fmt.Printf("\n## Person: %s\n", personName)
		factDetails := factsByPerson[personName]
		
		// Further group by fact_text for cleaner display
		factsMap := make(map[string][]storage.FactDetail) // Key: fact_text
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
			for _, source := range sources {
				fmt.Printf("    - Source URL: %s\n", source.SourceURL)
				fmt.Printf("      Original Quote: %s\n", source.OriginalQuote)
				// Optional: Print FactID and FactSourceID if useful
				// fmt.Printf("      (FactID: %d, SourceID: %d)\n", source.FactID, source.FactSourceID)
			}
		}
	}

	log.Info().Int("person_count", len(factsByPerson)).Msg("Successfully listed facts")
} 