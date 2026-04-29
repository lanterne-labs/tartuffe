package core

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// FactExtractor defines the interface for extracting facts from content.
// This is implemented by categorizer.OpenAIExtractor.
type FactExtractor interface {
	// ExtractFactsWithType extracts facts using plugin instructions appropriate for the target type.
	ExtractFactsWithType(ctx context.Context, targetName string, targetType string, content extractor.ExtractedContent) ([]backend.Fact, error)
}

// EmbeddingGenerator defines the interface for generating vector embeddings.
// This is implemented by embedding.OpenAIEmbeddingGenerator.
type EmbeddingGenerator interface {
	// GenerateEmbeddings generates embeddings for the given texts under the stated purpose.
	GenerateEmbeddings(ctx context.Context, purpose embedding.Purpose, texts []string) ([][]float32, error)
}

// FileIngester handles ingestion of facts from local JSON files.
// It reads cached content files, extracts facts using LLM, generates embeddings,
// and stores the facts with deduplication (handled automatically by FactInserter).
type FileIngester struct {
	factExtractor FactExtractor
	embedder      EmbeddingGenerator
	factInserter  *FactInserter
}

// IngestOptions configures the file ingestion process.
type IngestOptions struct {
	// TargetName is the person or party name to associate facts with.
	TargetName string
	// TargetType is "person" or "party". Affects extraction prompts.
	TargetType string
	// SourceType is the source type for fact_sources table (default: "file").
	SourceType string
	// DryRun extracts facts but does not store them.
	DryRun bool
	// KeepSourceDir, when set, dumps article content + extracted facts to JSON files
	// in this directory for debugging LLM fact extraction.
	KeepSourceDir string
}

// IngestResult contains the outcome of a file ingestion run.
type IngestResult struct {
	// FilesProcessed is the number of JSON files processed.
	FilesProcessed int
	// FactsExtracted is the total number of facts extracted from all files.
	FactsExtracted int
	// FactsStored is the number of facts successfully stored (0 in dry-run).
	FactsStored int
	// DuplicatesSkipped is the number of facts skipped due to deduplication.
	DuplicatesSkipped int
	// Errors contains any errors encountered during processing.
	Errors []FileError
}

// FileError represents an error that occurred while processing a specific file.
type FileError struct {
	Path  string
	Error error
}

// cachedContent matches the JSON structure from extractor/cache.go.
type cachedContent struct {
	URL           string     `json:"url"`
	Title         string     `json:"title"`
	Content       string     `json:"content"`
	DatePublished *time.Time `json:"date_published,omitempty"`
	CachedAt      time.Time  `json:"cached_at"`
}

// NewFileIngester creates a new FileIngester with the given dependencies.
func NewFileIngester(
	factExtractor FactExtractor,
	embedder EmbeddingGenerator,
	factInserter *FactInserter,
) *FileIngester {
	return &FileIngester{
		factExtractor: factExtractor,
		embedder:      embedder,
		factInserter:  factInserter,
	}
}

// IngestDirectory processes all JSON files in a directory.
func (fi *FileIngester) IngestDirectory(ctx context.Context, dir string, opts IngestOptions) (*IngestResult, error) {
	// Find all .json files in the directory
	pattern := filepath.Join(dir, "*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to glob directory %s: %w", dir, err)
	}

	if len(files) == 0 {
		log.Warn().Str("dir", dir).Msg("No JSON files found in directory")
		return &IngestResult{}, nil
	}

	log.Info().Str("dir", dir).Int("files", len(files)).Msg("Found JSON files to process")

	return fi.IngestFiles(ctx, files, opts)
}

// IngestFiles processes a list of JSON file paths.
func (fi *FileIngester) IngestFiles(ctx context.Context, paths []string, opts IngestOptions) (*IngestResult, error) {
	result := &IngestResult{}

	// Validate options
	if opts.TargetName == "" {
		return nil, fmt.Errorf("target name is required")
	}
	if opts.TargetType == "" {
		opts.TargetType = "person"
	}
	if opts.SourceType == "" {
		opts.SourceType = "file"
	}

	for i, path := range paths {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		log.Info().
			Int("progress", i+1).
			Int("total", len(paths)).
			Str("file", filepath.Base(path)).
			Msg("Processing file")

		facts, err := fi.processFile(ctx, path, opts)
		if err != nil {
			log.Error().Err(err).Str("file", path).Msg("Failed to process file")
			result.Errors = append(result.Errors, FileError{Path: path, Error: err})
			continue
		}

		result.FilesProcessed++
		result.FactsExtracted += len(facts)

		if opts.DryRun {
			// In dry-run mode, just print extracted facts
			for _, fact := range facts {
				log.Info().
					Str("fact", truncateString(fact.NormalizedFactText, 100)).
					Str("canonical_verb", fact.CanonicalVerb).
					Strs("people", fact.InvolvedPeople).
					Msg("[DRY-RUN] Extracted fact")
			}
		} else {
			// Store the facts
			storeResult, err := fi.storeFacts(ctx, facts, opts)
			if err != nil {
				log.Error().Err(err).Str("file", path).Msg("Failed to store facts")
				result.Errors = append(result.Errors, FileError{Path: path, Error: err})
				continue
			}
			result.FactsStored += storeResult.stored
			result.DuplicatesSkipped += storeResult.duplicatesSkipped
		}
	}

	return result, nil
}

// processFile reads a JSON file and extracts facts from it.
func (fi *FileIngester) processFile(ctx context.Context, path string, opts IngestOptions) ([]backend.Fact, error) {
	// Read and parse the JSON file
	content, err := fi.parseJSONFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to parse JSON file: %w", err)
	}

	// Validate content
	if strings.TrimSpace(content.Content) == "" {
		log.Warn().Str("file", path).Msg("File has empty content, skipping")
		return []backend.Fact{}, nil
	}

	// Convert to ExtractedContent for the fact extractor
	extracted := extractor.ExtractedContent{
		URL:           content.URL,
		Title:         content.Title,
		Content:       content.Content,
		DatePublished: content.DatePublished,
	}

	// Extract facts using the appropriate method based on target type
	facts, err := fi.factExtractor.ExtractFactsWithType(ctx, opts.TargetName, opts.TargetType, extracted)
	if err != nil {
		return nil, fmt.Errorf("failed to extract facts: %w", err)
	}

	// Dump source content + extracted facts for debugging if requested
	if opts.KeepSourceDir != "" {
		if dumpErr := DumpSourceAndFacts(opts.KeepSourceDir, opts.TargetName, content.URL, content.Title, content.Content, content.DatePublished, facts); dumpErr != nil {
			log.Warn().Err(dumpErr).Str("file", path).Msg("Failed to dump source and facts")
		}
	}

	log.Debug().
		Str("file", filepath.Base(path)).
		Int("facts", len(facts)).
		Msg("Extracted facts from file")

	return facts, nil
}

// parseJSONFile reads and parses a cached content JSON file.
func (fi *FileIngester) parseJSONFile(path string) (*cachedContent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	var content cachedContent
	if err := json.Unmarshal(data, &content); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	return &content, nil
}

// storeFactsResult contains the outcome of storing facts.
type storeFactsResult struct {
	stored            int
	duplicatesSkipped int
}

// storeFacts generates embeddings and stores facts using the fact inserter.
func (fi *FileIngester) storeFacts(ctx context.Context, facts []backend.Fact, opts IngestOptions) (*storeFactsResult, error) {
	result := &storeFactsResult{}

	for _, fact := range facts {
		// Generate embedding from EmbeddingText
		embeddings, err := fi.embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, []string{fact.EmbeddingText})
		if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
			log.Warn().Err(err).Str("fact", truncateString(fact.NormalizedFactText, 80)).Msg("Failed to generate embedding, skipping fact")
			continue
		}

		// Build store input
		input := StoreFactInput{
			SubjectEntity:      fact.SubjectEntity,     // Primary entity this fact is about
			SubjectEntityType:  fact.SubjectEntityType, // Entity type for person creation (human/party/organization)
			NormalizedFactText: fact.NormalizedFactText,
			EmbeddingText:      fact.EmbeddingText,
			Embedding:          embeddings[0],
			OriginalQuote:      fact.OriginalQuote,
			CanonicalVerb:      fact.CanonicalVerb,
			InvolvedPeople:     fact.InvolvedPeople, // Secondary people mentioned
			SourceURL:          fact.SourceURL,
			PublishedAt:        fact.PublishedAt,
			SourceType:         opts.SourceType,
			Metadata: map[string]any{
				"target_name": opts.TargetName,
				"target_type": opts.TargetType,
				"ingested_by": "file_ingester",
			},
		}

		// Insert using the fact inserter (handles dedup + similarity-based grouping)
		insertResult, err := fi.factInserter.InsertFacts(ctx, []StoreFactInput{input})
		if err != nil {
			log.Warn().Err(err).Str("fact", truncateString(fact.NormalizedFactText, 80)).Msg("Failed to store fact")
			continue
		}

		result.stored += insertResult.Inserted
		result.duplicatesSkipped += insertResult.Skipped
	}

	return result, nil
}

// truncateString truncates a string to maxLen characters.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
