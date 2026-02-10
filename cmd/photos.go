package cmd

import (
	"context"
	"fmt"
	"strings"

	"digital-mirror/backend/core"
	"digital-mirror/backend/objectstore"
	"digital-mirror/backend/plugins/photos"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// photosCmd is the parent command for photo subcommands
var photosCmd = &cobra.Command{
	Use:   "photos",
	Short: "Search and upload profile photos for persons",
	Long:  `Find profile photos via image search, upload to R2, and store the URL on person records.`,
}

// photosSearchCmd searches and uploads photos
var photosSearchCmd = &cobra.Command{
	Use:         "search",
	Short:       "Search and upload photos for persons",
	Long:        `Search for profile photos via SerpAPI image search, download the best candidate, upload to Cloudflare R2, and store the URL.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runPhotosSearch,
}

func init() {
	pluginCmd.AddCommand(photosCmd)
	photosCmd.AddCommand(photosSearchCmd)

	photosSearchCmd.Flags().StringSlice("targets", nil, "specific persons/parties by name (comma-separated)")
	photosSearchCmd.Flags().Bool("all", false, "process all persons without photos")
	photosSearchCmd.Flags().Bool("force", false, "re-search even if photo already exists")
	photosSearchCmd.Flags().Bool("dry-run", false, "show scored candidates without downloading")
	photosSearchCmd.Flags().Int("max-results", 10, "number of image search results to fetch")
	photosSearchCmd.Flags().Int("min-width", 200, "minimum image width in pixels")
	photosSearchCmd.Flags().Int("min-height", 200, "minimum image height in pixels")
}

func runPhotosSearch(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Parse flags
	targets, _ := cmd.Flags().GetStringSlice("targets")
	all, _ := cmd.Flags().GetBool("all")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	maxResults, _ := cmd.Flags().GetInt("max-results")
	minWidth, _ := cmd.Flags().GetInt("min-width")
	minHeight, _ := cmd.Flags().GetInt("min-height")

	if len(targets) == 0 && !all {
		return fmt.Errorf("specify --targets or --all")
	}

	// Build person list
	var persons []storage.PersonSummary

	if all {
		if force {
			// Get ALL persons
			allPersons, err := storer.GetPersonsRankedByScore(ctx, 10000, 0, "", "")
			if err != nil {
				return fmt.Errorf("failed to get persons: %w", err)
			}
			persons = allPersons
		} else {
			// Only persons without photos
			withoutImage, err := storer.GetPersonsWithoutImage(ctx)
			if err != nil {
				return fmt.Errorf("failed to get persons without image: %w", err)
			}
			persons = withoutImage
		}
	} else {
		// Resolve each target name
		for _, target := range targets {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}

			result := core.ResolveTargetName(ctx, storer, target)
			if result.Error != nil {
				log.Error().Err(result.Error).Str("target", target).Msg("Failed to resolve target")
				continue
			}

			if result.IsExactMatch {
				person, err := storer.GetPersonByID(ctx, *result.ExactMatch.PersonID)
				if err != nil || person == nil {
					log.Error().Str("target", target).Msg("Person not found by ID")
					continue
				}
				persons = append(persons, *person)
			} else if len(result.FuzzyMatches) > 0 && result.FuzzyMatches[0].Score >= 0.8 {
				top := result.FuzzyMatches[0]
				person, err := storer.GetPersonByID(ctx, *top.Target.PersonID)
				if err != nil || person == nil {
					log.Error().Str("target", target).Msg("Person not found by ID")
					continue
				}
				log.Info().
					Str("query", target).
					Str("matched", person.Name).
					Float64("score", top.Score).
					Msg("Using fuzzy match")
				persons = append(persons, *person)
			} else {
				log.Error().Str("target", target).Msg("No confident match found")
			}
		}
	}

	if len(persons) == 0 {
		fmt.Println("No persons to process.")
		return nil
	}

	log.Info().
		Int("persons", len(persons)).
		Bool("force", force).
		Bool("dry_run", dryRun).
		Msg("Starting photo search")

	// Initialize SerpAPI
	serpAPI, err := search.NewSerpAPINewsProviderFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize SerpAPI: %w", err)
	}

	// Initialize R2 (not needed for dry-run)
	var r2 *objectstore.R2Client
	if !dryRun {
		r2, err = objectstore.NewR2ClientFromEnv()
		if err != nil {
			return fmt.Errorf("failed to initialize R2: %w", err)
		}
	}

	provider := &photos.Provider{
		ImageSearch: serpAPI,
		R2:          r2,
		Storer:      storer,
	}

	opts := photos.Options{
		Force:      force,
		DryRun:     dryRun,
		MaxResults: maxResults,
		MinWidth:   minWidth,
		MinHeight:  minHeight,
	}

	uploaded := 0
	skipped := 0
	errors := 0

	for i, person := range persons {
		log.Info().
			Int("progress", i+1).
			Int("total", len(persons)).
			Str("person", person.Name).
			Msg("Processing person")

		result := provider.SearchAndUpload(ctx, person, opts)

		if result.Error != nil {
			log.Error().
				Err(result.Error).
				Str("person", person.Name).
				Msg("Photo search failed")
			errors++
			continue
		}

		if result.Skipped {
			log.Info().
				Str("person", person.Name).
				Str("reason", result.SkipReason).
				Msg("Skipped")
			skipped++
			continue
		}

		if result.Uploaded {
			log.Info().
				Str("person", person.Name).
				Str("url", result.ImageURL).
				Msg("Photo uploaded")
			uploaded++
		}
	}

	log.Info().
		Int("total", len(persons)).
		Int("uploaded", uploaded).
		Int("skipped", skipped).
		Int("errors", errors).
		Msg("Photo search completed")

	return nil
}
