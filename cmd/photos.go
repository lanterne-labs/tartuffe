package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/plugins/photos"
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

// photosSetCmd makes a given image the photo of a person.
var photosSetCmd = &cobra.Command{
	Use:   "set [name] [image-url]",
	Short: "Make the image at a URL the photo of a person",
	Long: `Download the image at the URL, upload it to the object store under a new key and store its address on the person.
For a photo the search chose badly: pick one by hand (dm plugin photos search --targets "Name" --force --dry-run lists the candidates with their URLs).`,
	Args:        cobra.ExactArgs(2),
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runPhotosSet,
}

func init() {
	pluginCmd.AddCommand(photosCmd)
	photosCmd.AddCommand(photosSearchCmd, photosSetCmd)

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

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

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

	store := photos.NewStore(storer)
	var persons []storage.PersonSummary
	switch {
	case all && force:
		persons, err = storer.GetPersonsRankedByScore(ctx, 10000, 0, "", "", "", 0, "")
		if err != nil {
			return fmt.Errorf("failed to get persons: %w", err)
		}
	case all:
		persons, err = store.GetPersonsWithoutImage(ctx)
		if err != nil {
			return fmt.Errorf("failed to get persons without image: %w", err)
		}
	default:
		records, err := core.ResolvePersons(ctx, storer, targets, fuzzyFloor)
		if err != nil {
			return err
		}
		for _, r := range records {
			person, err := storer.GetPersonByID(ctx, r.ID)
			if err != nil || person == nil {
				log.Error().Err(err).Str("target", r.Name).Int64("id", r.ID).Msg("Person not found by ID")
				continue
			}
			persons = append(persons, *person)
		}
	}

	if len(persons) == 0 {
		fmt.Println("No persons to process.")
		return nil
	}
	log.Info().Int("persons", len(persons)).Bool("force", force).Bool("dry_run", dryRun).Msg("Starting photo search")

	d := deps.New(storer)
	var provider *photos.Provider
	if dryRun {
		// No object store needed: nothing is uploaded.
		searcher, err := d.ImageSearcher()
		if err != nil {
			return fmt.Errorf("failed to initialize image search: %w", err)
		}
		provider = &photos.Provider{ImageSearch: searcher, Storer: store}
	} else {
		provider, err = photos.NewProviderFromDeps(d)
		if err != nil {
			return err
		}
	}

	st, err := photos.RunForPersons(ctx, provider, persons, photos.Options{
		Force: force, DryRun: dryRun, MaxResults: maxResults, MinWidth: minWidth, MinHeight: minHeight,
	})
	if err != nil {
		return err
	}
	if !dryRun {
		if err := photos.RecordJobMetrics(ctx, storer, "photos-search", st); err != nil {
			log.Error().Err(err).Msg("Failed to record job metrics")
		}
	}
	return nil
}

func runPhotosSet(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	record, ok, err := resolvePersonInteractive(ctx, storer, args[0], false)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("person %q not found", args[0])
	}
	person, err := storer.GetPersonByID(ctx, record.ID)
	if err != nil {
		return fmt.Errorf("person %d: %w", record.ID, err)
	}
	if person == nil {
		return fmt.Errorf("person %d not found", record.ID)
	}

	d := deps.New(storer)
	store, err := d.ObjectStore()
	if err != nil {
		return fmt.Errorf("object store: %w", err)
	}
	provider := &photos.Provider{R2: store, Storer: photos.NewStore(storer)}
	result := provider.SetFromURL(ctx, *person, args[1])
	if result.Error != nil {
		return result.Error
	}
	fmt.Printf("%s (ID: %d): photo set, %s\n", person.Name, person.ID, result.ImageURL)
	return nil
}
