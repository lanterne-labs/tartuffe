package news

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// TargetType represents the type of entity being searched.
type TargetType string

const (
	// TargetPerson is a human individual.
	TargetPerson TargetType = "person"
	// TargetParty is a political party or organization.
	TargetParty TargetType = "party"
)

// SearchTarget represents an entity to search news for.
type SearchTarget struct {
	// Name is the name of the person or party to search for.
	Name string
	// Type is the type of target (person or party).
	Type TargetType
	// PersonID is the database ID of the person, if already known.
	PersonID *int64
}

// PartyInfo holds basic information about an affiliated party
type PartyInfo struct {
	ID   int64
	Name string
}

// Options configures a news search run.
type Options struct {
	// DateFrom is the start of the date range for news search.
	DateFrom *time.Time
	// DateTo is the end of the date range for news search.
	DateTo *time.Time
	// MaxParse is the maximum number of articles to successfully parse per target.
	// The system will fetch this many articles from SerpAPI and try to parse all of them,
	// stopping when MaxParse articles are successfully parsed or no more articles available.
	MaxParse int
	// Location restricts results to a specific country/region.
	Location string
	// RevisitHours controls how often a URL can be revisited (0 = always revisit).
	RevisitHours int
	// SkipUnknown skips targets with no prior search results (see news_search_log).
	SkipUnknown bool
	// DryRun shows what would be done without actually fetching or storing anything.
	DryRun bool
}

// Result contains the outcome of a news search for a single target.
type Result struct {
	// Target is the entity that was searched.
	Target SearchTarget
	// ArticlesFound is the number of news articles found by search.
	ArticlesFound int
	// ArticlesParsed is the number of articles actually parsed for facts.
	ArticlesParsed int
	// FactsExtracted is the number of facts extracted from the articles.
	FactsExtracted int
	// SkippedAlreadyProcessed is the number of URLs skipped because already in database.
	SkippedAlreadyProcessed int
	// SkippedEmptyURL is the number of articles skipped due to empty URLs.
	SkippedEmptyURL int
	// Errors contains any errors encountered during processing.
	Errors []error
}

// Provider orchestrates news search and fact extraction for any entity.
type Provider struct {
	// NewsSearch is the news search provider (SerpAPI).
	NewsSearch *search.SerpAPINewsProvider
	// ContentExtractor extracts content from news article URLs (Firecrawl/Apify).
	ContentExtractor extractor.ContentExtractor
	// FactExtractor extracts facts from article content using LLM.
	FactExtractor *categorizer.OpenAIExtractor
	// Embedder generates embeddings for fact text.
	Embedder *embedding.OpenAIEmbeddingGenerator
	// Storer persists facts to the database.
	Storer *storage.PostgresStorer
}

// NewProvider creates a new news Provider with all dependencies.
func NewProvider(
	newsSearch *search.SerpAPINewsProvider,
	contentExtractor extractor.ContentExtractor,
	factExtractor *categorizer.OpenAIExtractor,
	embedder *embedding.OpenAIEmbeddingGenerator,
	storer *storage.PostgresStorer,
) *Provider {
	return &Provider{
		NewsSearch:       newsSearch,
		ContentExtractor: contentExtractor,
		FactExtractor:    factExtractor,
		Embedder:         embedder,
		Storer:           storer,
	}
}

// CollectFacts searches news for the given target and extracts facts.
func (p *Provider) CollectFacts(ctx context.Context, target SearchTarget, opts Options) (*Result, error) {
	result := &Result{
		Target: target,
	}

	// Build search options - request MaxParse articles
	searchOpts := search.NewsSearchOptions{
		Limit:    opts.MaxParse,
		Location: opts.Location,
	}
	if opts.DateFrom != nil {
		searchOpts.DateFrom = *opts.DateFrom
	}
	if opts.DateTo != nil {
		searchOpts.DateTo = *opts.DateTo
	}

	// Search for news articles
	newsResults, err := p.NewsSearch.SearchNews(target.Name, searchOpts)
	if err != nil {
		return nil, fmt.Errorf("news search failed for %s: %w", target.Name, err)
	}

	result.ArticlesFound = len(newsResults)
	log.Info().
		Str("target", target.Name).
		Str("type", string(target.Type)).
		Int("articles_found", len(newsResults)).
		Msg("news: search completed")

	if len(newsResults) == 0 {
		// Log that no results were found for prioritization
		if target.PersonID != nil {
			if err := p.Storer.LogNewsSearch(ctx, *target.PersonID, target.Name, 0, 0); err != nil {
				log.Warn().Err(err).Int64("person_id", *target.PersonID).Msg("Failed to log empty news search")
			}
		}
		return result, nil
	}

	revisitHours := opts.RevisitHours
	if revisitHours == 0 {
		revisitHours = 24 // Default to 24 hours
	}

	// For person targets, get affiliated parties upfront for party-aware extraction
	var affiliatedParties []PartyInfo
	if target.Type == TargetPerson && target.PersonID != nil {
		var err error
		affiliatedParties, err = p.getAffiliatedParties(ctx, *target.PersonID, nil) // nil = current affiliations
		if err != nil {
			log.Warn().Err(err).
				Int64("person_id", *target.PersonID).
				Str("person", target.Name).
				Msg("Failed to get affiliated parties, will extract without party awareness")
		} else if len(affiliatedParties) > 0 {
			partyNames := make([]string, len(affiliatedParties))
			for i, p := range affiliatedParties {
				partyNames[i] = p.Name
			}
			log.Info().
				Str("person", target.Name).
				Strs("parties", partyNames).
				Msg("Found affiliated parties for party-aware fact extraction")
		}
	}

	maxParse := opts.MaxParse

	// Phase 1: Collect visitable URLs up to maxParse limit
	var urlsToExtract []string
	var articlesToProcess []search.NewsResult
	for _, article := range newsResults {
		if len(urlsToExtract) >= maxParse {
			break
		}
		// Skip articles with empty URLs
		if strings.TrimSpace(article.Link) == "" {
			log.Warn().Str("title", article.Title).Msg("news: skipping article with empty URL")
			result.SkippedEmptyURL++
			metrics.IncrementNewsArticles("skipped_empty_url")
			continue
		}
		// Check if URL was already processed
		visitable, _, err := p.Storer.IsURLVisitable(ctx, article.Link, revisitHours)
		if err != nil {
			log.Warn().Err(err).Str("url", article.Link).Msg("Failed to check URL visitability")
			result.Errors = append(result.Errors, err)
			continue
		}
		if !visitable {
			log.Debug().Str("url", article.Link).Msg("news: skipping already processed URL")
			result.SkippedAlreadyProcessed++
			metrics.IncrementNewsArticles("skipped_already_processed")
			continue
		}
		urlsToExtract = append(urlsToExtract, article.Link)
		articlesToProcess = append(articlesToProcess, article)
	}

	if len(urlsToExtract) == 0 {
		log.Info().
			Str("target", target.Name).
			Int("skipped_already_processed", result.SkippedAlreadyProcessed).
			Int("skipped_empty_url", result.SkippedEmptyURL).
			Msg("news: no new URLs to process")
		return result, nil
	}

	// Dry-run mode: just show what would be done
	if opts.DryRun {
		log.Info().
			Str("target", target.Name).
			Int("urls_to_extract", len(urlsToExtract)).
			Msg("news: [DRY-RUN] would extract content from URLs")
		for i, url := range urlsToExtract {
			log.Info().
				Int("index", i+1).
				Str("url", url).
				Str("title", articlesToProcess[i].Title).
				Msg("news: [DRY-RUN] would process")
		}
		return result, nil
	}

	log.Info().
		Str("target", target.Name).
		Int("urls_to_extract", len(urlsToExtract)).
		Msg("news: starting content extraction")

	// Phase 2: Extract content - use batch if available, otherwise sequential
	var extractedContents []*extractor.ExtractedContent
	var extractionErrors []error

	if batchExtractor, ok := p.ContentExtractor.(*core.BatchExtractor); ok {
		// Use batch extraction with concurrency control
		batchResults := batchExtractor.ExtractBatch(ctx, urlsToExtract)
		extractedContents = make([]*extractor.ExtractedContent, len(batchResults))
		extractionErrors = make([]error, len(batchResults))
		for i, br := range batchResults {
			extractedContents[i] = br.Content
			extractionErrors[i] = br.Error
		}
	} else {
		// Fallback: sequential extraction
		extractedContents = make([]*extractor.ExtractedContent, len(urlsToExtract))
		extractionErrors = make([]error, len(urlsToExtract))
		for i, url := range urlsToExtract {
			content, err := p.ContentExtractor.ExtractContent(url)
			extractedContents[i] = content
			extractionErrors[i] = err
		}
	}

	// Phase 3: Process extracted content and extract facts
	for i, article := range articlesToProcess {
		content := extractedContents[i]
		extractErr := extractionErrors[i]

		if extractErr != nil {
			log.Error().Err(extractErr).Str("url", article.Link).Msg("Failed to extract content")
			result.Errors = append(result.Errors, extractErr)
			metrics.IncrementNewsArticles("error")
			continue
		}
		if content == nil || strings.TrimSpace(content.Content) == "" {
			log.Debug().Str("url", article.Link).Msg("news: no content extracted")
			metrics.IncrementNewsArticles("skipped_empty_content")
			continue
		}

		// Use article date if available, fall back to content date
		publishedAt := content.DatePublished
		if article.Date != nil {
			publishedAt = article.Date
		}

		// Extract facts from the content
		// For persons with affiliated parties, use party-aware extraction
		var facts []backend.Fact
		var err error
		if target.Type == TargetPerson && len(affiliatedParties) > 0 {
			// Use party-aware extraction - LLM will tag facts that concern parties
			partyNames := make([]string, len(affiliatedParties))
			for j, party := range affiliatedParties {
				partyNames[j] = party.Name
			}
			facts, err = p.FactExtractor.ExtractFactsForPersonWithParties(ctx, target.Name, partyNames, *content)
		} else {
			// Standard extraction for parties or persons without affiliations
			facts, err = p.FactExtractor.ExtractFactsWithType(ctx, target.Name, string(target.Type), *content)
		}
		if err != nil {
			log.Warn().Err(err).Str("url", article.Link).Msg("Failed to extract facts")
			result.Errors = append(result.Errors, err)
			continue
		}

		if len(facts) == 0 {
			log.Debug().Str("url", article.Link).Msg("news: no facts extracted")
			// Still count this as parsed since we successfully extracted (even if empty)
			result.ArticlesParsed++
			metrics.IncrementNewsArticles("parsed")
			continue
		}

		result.ArticlesParsed++
		metrics.IncrementNewsArticles("parsed")

		// Generate embeddings and store each fact
		for _, fact := range facts {
			// Generate embedding from EmbeddingText (name-free version) to cluster by topic, not person
			embeddings, err := p.Embedder.GenerateEmbeddings(ctx, []string{fact.EmbeddingText})
			if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
				log.Warn().Err(err).Str("fact", fact.NormalizedFactText).Msg("Failed to generate embedding")
				result.Errors = append(result.Errors, fmt.Errorf("embedding failed: %w", err))
				continue
			}

			// Store the fact with subject entity from LLM extraction
			// The LLM now extracts facts about ALL entities, each with its own subject_entity
			input := core.StoreFactInput{
				SubjectEntity:      fact.SubjectEntity,     // Primary entity this fact is about
				SubjectEntityType:  fact.SubjectEntityType, // Entity type for person creation (human/party/organization)
				NormalizedFactText: fact.NormalizedFactText,
				EmbeddingText:      fact.EmbeddingText,
				Embedding:          embeddings[0],
				OriginalQuote:      fact.OriginalQuote,
				Topics:             fact.Topics,
				InvolvedPeople:     fact.InvolvedPeople, // Secondary people mentioned
				SourceURL:          article.Link,
				PublishedAt:        publishedAt,
				SourceType:         "news",
				Metadata: map[string]any{
					"source_name":  article.Source,
					"story_token":  article.StoryToken,
					"target_type":  string(target.Type),
					"search_query": target.Name,
				},
			}

			inserter := core.NewFactInserter(p.Storer)
			err = inserter.InsertFacts(ctx, []core.StoreFactInput{input})
			if err != nil {
				log.Warn().Err(err).
					Str("fact", fact.NormalizedFactText).
					Str("subject_entity", fact.SubjectEntity).
					Msg("Failed to store fact")
				result.Errors = append(result.Errors, err)
				continue
			}

			result.FactsExtracted++
		}

		log.Debug().
			Str("url", article.Link).
			Int("facts", len(facts)).
			Msg("news: processed article")
	}

	// Log the search for prioritization
	if target.PersonID != nil {
		if err := p.Storer.LogNewsSearch(ctx, *target.PersonID, target.Name, result.ArticlesFound, result.FactsExtracted); err != nil {
			log.Warn().Err(err).Int64("person_id", *target.PersonID).Msg("Failed to log news search")
		}
	}

	log.Info().
		Str("target", target.Name).
		Int("articles_parsed", result.ArticlesParsed).
		Int("facts_extracted", result.FactsExtracted).
		Int("skipped_already_processed", result.SkippedAlreadyProcessed).
		Int("errors", len(result.Errors)).
		Msg("news: collection completed")

	return result, nil
}

// getAffiliatedParties retrieves parties affiliated with a person at a specific date.
// Returns party information (ID, Name, EntityType) for parties where entity_type='party'.
func (p *Provider) getAffiliatedParties(ctx context.Context, personID int64, atDate *time.Time) ([]PartyInfo, error) {
	// Begin transaction for affiliated query
	tx, err := p.Storer.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Get affiliated party IDs using existing temporal query
	partyIDs, err := p.Storer.GetAffiliatedPersonIDs(ctx, tx, personID, atDate)
	if err != nil {
		return nil, fmt.Errorf("failed to get affiliated party IDs: %w", err)
	}

	if len(partyIDs) == 0 {
		return []PartyInfo{}, nil
	}

	// Fetch party details for each ID
	parties := make([]PartyInfo, 0, len(partyIDs))
	for _, partyID := range partyIDs {
		person, err := p.Storer.GetPersonByID(ctx, partyID)
		if err != nil {
			log.Warn().Err(err).Int64("party_id", partyID).Msg("Failed to get party details")
			continue
		}

		// Only include entities with entity_type='party'
		if person.EntityType != nil && *person.EntityType == "party" {
			parties = append(parties, PartyInfo{
				ID:   person.ID,
				Name: person.Name,
			})
		}
	}

	return parties, nil
}

// PreviewSearch checks if news results exist for a target without full processing.
// This is useful for prioritization - skip targets with no coverage.
func (p *Provider) PreviewSearch(ctx context.Context, target SearchTarget, opts Options) (int, error) {
	searchOpts := search.NewsSearchOptions{
		Location: opts.Location,
	}
	if opts.DateFrom != nil {
		searchOpts.DateFrom = *opts.DateFrom
	}
	if opts.DateTo != nil {
		searchOpts.DateTo = *opts.DateTo
	}

	preview, err := p.NewsSearch.PreviewSearch(target.Name, searchOpts)
	if err != nil {
		return 0, fmt.Errorf("preview search failed for %s: %w", target.Name, err)
	}

	return preview.TotalResults, nil
}

// CollectFactsForTargets processes multiple targets with prioritization.
func (p *Provider) CollectFactsForTargets(ctx context.Context, targets []SearchTarget, opts Options) ([]*Result, error) {
	results := make([]*Result, 0, len(targets))

	for _, target := range targets {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		// If SkipUnknown is set, check if we should skip this target
		if opts.SkipUnknown && target.PersonID != nil {
			shouldSearch, err := p.Storer.ShouldSearchNews(ctx, *target.PersonID, 30) // 30 days lookback
			if err != nil {
				log.Warn().Err(err).Int64("person_id", *target.PersonID).Msg("Failed to check search history")
			} else if !shouldSearch {
				log.Debug().Str("target", target.Name).Msg("news: skipping target with no prior results")
				continue
			}
		}

		result, err := p.CollectFacts(ctx, target, opts)
		if err != nil {
			log.Error().Err(err).Str("target", target.Name).Msg("Failed to collect facts")
			result = &Result{
				Target: target,
				Errors: []error{err},
			}
		}
		results = append(results, result)
	}

	return results, nil
}
