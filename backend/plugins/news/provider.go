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
	"sort"
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
	// WindowDays breaks the DateFrom→DateTo range into windows of N days.
	// When > 0, requires DateFrom to be set. DateTo defaults to time.Now() if nil.
	WindowDays int
	// MaxPerStory limits how many articles with the same story_token are processed
	// in a single search. 0 = no cap (default). Articles with empty story_token are
	// never capped (each treated as unique).
	MaxPerStory int
	// KeepSourceDir, when set, dumps article content + extracted facts to JSON files
	// in this directory for debugging LLM fact extraction.
	KeepSourceDir string
}

// Result contains the outcome of a news search for a single target.
type Result struct {
	// Target is the entity that was searched.
	Target SearchTarget
	// ArticlesFound is the number of news articles found by search.
	ArticlesFound int
	// ArticlesParsed is the number of articles actually parsed for facts.
	ArticlesParsed int
	// ArticlesParsedNoFacts is the number of articles parsed that yielded zero facts.
	ArticlesParsedNoFacts int
	// FactsExtracted is the number of facts successfully inserted (after dedup).
	FactsExtracted int
	// FactsSkippedDedup is the number of facts skipped as duplicates at insertion time.
	FactsSkippedDedup int
	// SkippedAlreadyProcessed is the number of URLs skipped because already in database.
	SkippedAlreadyProcessed int
	// SkippedEmptyURL is the number of articles skipped due to empty URLs.
	SkippedEmptyURL int
	// SkippedStoryCap is the number of articles skipped due to story cap.
	SkippedStoryCap int
	// StoryCappedDates holds the dates of articles that were skipped due to story cap (for re-query window calculation).
	StoryCappedDates []time.Time
	// StoryCappedTokens holds the unique story_tokens that were capped.
	StoryCappedTokens []string
	// Errors contains any errors encountered during processing.
	Errors []error
}

// Provider orchestrates news search and fact extraction for any entity.
type Provider struct {
	// NewsSearch is the news search provider (SerpAPI, Brave, etc.).
	NewsSearch search.NewsSearcher
	// ContentExtractor extracts content from news article URLs (Firecrawl/Apify).
	ContentExtractor extractor.ContentExtractor
	// FactExtractor extracts facts from article content using LLM.
	FactExtractor *categorizer.OpenAIExtractor
	// Embedder generates embeddings for fact text.
	Embedder *embedding.OpenAIEmbeddingGenerator
	// Storer persists facts to the database.
	Storer *storage.PostgresStorer
	// StanceManager routes every newly inserted fact through stance processing.
	// It is required (every fact must have a stance) — callers must set it
	// before invoking the provider.
	StanceManager *core.StanceManager
}

// NewProvider creates a new news Provider with all dependencies.
func NewProvider(
	newsSearch search.NewsSearcher,
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
	storyTokenCounts := make(map[string]int)
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
		// Story cap: skip articles that exceed MaxPerStory for their story_token
		if opts.MaxPerStory > 0 && article.StoryToken != "" {
			if storyTokenCounts[article.StoryToken] >= opts.MaxPerStory {
				log.Debug().
					Str("url", article.Link).
					Str("story_token", article.StoryToken).
					Int("cap", opts.MaxPerStory).
					Msg("news: skipping article due to story cap")
				result.SkippedStoryCap++
				if article.Date != nil {
					result.StoryCappedDates = append(result.StoryCappedDates, *article.Date)
				}
				if !containsString(result.StoryCappedTokens, article.StoryToken) {
					result.StoryCappedTokens = append(result.StoryCappedTokens, article.StoryToken)
				}
				metrics.IncrementNewsArticles("skipped_story_cap")
				continue
			}
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
		// Track story token count after adding
		if article.StoryToken != "" {
			storyTokenCounts[article.StoryToken]++
		}
	}

	// Log story cap stats
	if result.SkippedStoryCap > 0 {
		log.Info().
			Str("target", target.Name).
			Int("skipped_story_cap", result.SkippedStoryCap).
			Int("capped_stories", len(result.StoryCappedTokens)).
			Msg("news: articles skipped due to story cap")
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

		// Dump source content + extracted facts for debugging if requested
		if opts.KeepSourceDir != "" {
			if dumpErr := core.DumpSourceAndFacts(opts.KeepSourceDir, target.Name, article.Link, content.Title, content.Content, publishedAt, facts); dumpErr != nil {
				log.Warn().Err(dumpErr).Str("url", article.Link).Msg("Failed to dump source and facts")
			}
		}

		if len(facts) == 0 {
			log.Debug().Str("url", article.Link).Msg("news: no facts extracted")
			result.ArticlesParsed++
			result.ArticlesParsedNoFacts++
			metrics.IncrementNewsArticles("parsed_no_facts")
			continue
		}

		result.ArticlesParsed++
		metrics.IncrementNewsArticles("parsed")

		// Generate embeddings and store each fact
		for _, fact := range facts {
			// Generate embedding from EmbeddingText (name-free version) to cluster by topic, not person
			embeddings, err := p.Embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, []string{fact.EmbeddingText})
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
				CanonicalVerb:      fact.CanonicalVerb,
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

			if p.StanceManager == nil {
				return result, fmt.Errorf("news provider: StanceManager is required (set p.StanceManager before calling)")
			}
			inserter := core.NewFactInserter(p.Storer, p.StanceManager)
			insertResult, err := inserter.InsertFacts(ctx, []core.StoreFactInput{input})
			if err != nil {
				log.Warn().Err(err).
					Str("fact", fact.NormalizedFactText).
					Str("subject_entity", fact.SubjectEntity).
					Msg("Failed to store fact")
				result.Errors = append(result.Errors, err)
				continue
			}

			result.FactsExtracted += insertResult.Inserted
			result.FactsSkippedDedup += insertResult.Skipped
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

	totalRaw := result.FactsExtracted + result.FactsSkippedDedup
	dedupRate := 0.0
	if totalRaw > 0 {
		dedupRate = float64(result.FactsSkippedDedup) / float64(totalRaw) * 100
	}
	log.Info().
		Str("target", target.Name).
		Int("articles_parsed", result.ArticlesParsed).
		Int("articles_no_facts", result.ArticlesParsedNoFacts).
		Int("facts_stored", result.FactsExtracted).
		Int("facts_skipped_dedup", result.FactsSkippedDedup).
		Float64("dedup_rate_pct", dedupRate).
		Int("skipped_already_processed", result.SkippedAlreadyProcessed).
		Int("skipped_story_cap", result.SkippedStoryCap).
		Int("errors", len(result.Errors)).
		Msg("news: collection completed")

	return result, nil
}

// ContentItem represents extracted article content ready for batch LLM processing.
type ContentItem struct {
	SourceURL   string
	Content     string
	TargetName  string
	TargetType  string
	PublishedAt *time.Time
}

// CollectContent searches news and extracts article content, but does NOT run LLM fact extraction.
// Returns content items ready for submission to a batch LLM provider.
// This is the batch-mode counterpart of CollectFacts.
func (p *Provider) CollectContent(ctx context.Context, target SearchTarget, opts Options) ([]ContentItem, *Result, error) {
	result := &Result{Target: target}

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

	newsResults, err := p.NewsSearch.SearchNews(target.Name, searchOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("news search failed for %s: %w", target.Name, err)
	}

	result.ArticlesFound = len(newsResults)
	log.Info().Str("target", target.Name).Int("articles_found", len(newsResults)).Msg("news: search completed")

	if len(newsResults) == 0 {
		if target.PersonID != nil {
			if err := p.Storer.LogNewsSearch(ctx, *target.PersonID, target.Name, 0, 0); err != nil {
				log.Warn().Err(err).Msg("Failed to log empty news search")
			}
		}
		return nil, result, nil
	}

	revisitHours := opts.RevisitHours
	if revisitHours == 0 {
		revisitHours = 24
	}

	// Phase 1: Collect visitable URLs
	storyTokenCounts := make(map[string]int)
	var urlsToExtract []string
	var articlesToProcess []search.NewsResult
	for _, article := range newsResults {
		if len(urlsToExtract) >= opts.MaxParse {
			break
		}
		if strings.TrimSpace(article.Link) == "" {
			log.Warn().Str("title", article.Title).Msg("news: skipping article with empty URL")
			result.SkippedEmptyURL++
			continue
		}
		if opts.MaxPerStory > 0 && article.StoryToken != "" {
			if storyTokenCounts[article.StoryToken] >= opts.MaxPerStory {
				result.SkippedStoryCap++
				continue
			}
		}
		visitable, _, err := p.Storer.IsURLVisitable(ctx, article.Link, revisitHours)
		if err != nil {
			log.Warn().Err(err).Str("url", article.Link).Msg("Failed to check URL visitability")
			continue
		}
		if !visitable {
			result.SkippedAlreadyProcessed++
			continue
		}
		urlsToExtract = append(urlsToExtract, article.Link)
		articlesToProcess = append(articlesToProcess, article)
		if article.StoryToken != "" {
			storyTokenCounts[article.StoryToken]++
		}
	}

	if len(urlsToExtract) == 0 {
		log.Info().Str("target", target.Name).Int("skipped", result.SkippedAlreadyProcessed).Msg("news: no new URLs to process")
		return nil, result, nil
	}

	// Phase 2: Extract content
	var extractedContents []*extractor.ExtractedContent
	var extractionErrors []error

	if batchExtractor, ok := p.ContentExtractor.(*core.BatchExtractor); ok {
		batchResults := batchExtractor.ExtractBatch(ctx, urlsToExtract)
		extractedContents = make([]*extractor.ExtractedContent, len(batchResults))
		extractionErrors = make([]error, len(batchResults))
		for i, br := range batchResults {
			extractedContents[i] = br.Content
			extractionErrors[i] = br.Error
		}
	} else {
		extractedContents = make([]*extractor.ExtractedContent, len(urlsToExtract))
		extractionErrors = make([]error, len(urlsToExtract))
		for i, url := range urlsToExtract {
			content, err := p.ContentExtractor.ExtractContent(url)
			extractedContents[i] = content
			extractionErrors[i] = err
		}
	}

	// Phase 3: Collect content items (no LLM extraction)
	var items []ContentItem
	for i, article := range articlesToProcess {
		content := extractedContents[i]
		extractErr := extractionErrors[i]

		if extractErr != nil {
			log.Error().Err(extractErr).Str("url", article.Link).Msg("Failed to extract content")
			result.Errors = append(result.Errors, extractErr)
			continue
		}
		if content == nil || strings.TrimSpace(content.Content) == "" {
			continue
		}

		publishedAt := content.DatePublished
		if article.Date != nil {
			publishedAt = article.Date
		}

		result.ArticlesParsed++
		items = append(items, ContentItem{
			SourceURL:   article.Link,
			Content:     content.Content,
			TargetName:  target.Name,
			TargetType:  string(target.Type),
			PublishedAt: publishedAt,
		})
	}

	// Log the search
	if target.PersonID != nil {
		if err := p.Storer.LogNewsSearch(ctx, *target.PersonID, target.Name, result.ArticlesFound, 0); err != nil {
			log.Warn().Err(err).Msg("Failed to log news search")
		}
	}

	log.Info().
		Str("target", target.Name).
		Int("articles_parsed", result.ArticlesParsed).
		Int("content_items", len(items)).
		Int("errors", len(result.Errors)).
		Msg("news: content collection completed")

	return items, result, nil
}

// CollectFactsWithDateWindows breaks the date range into windows of opts.WindowDays days
// and calls CollectFacts for each window. This works around Google News result caps by
// making multiple narrower queries. Errors in individual windows are logged and collected
// but do not stop processing of subsequent windows.
func (p *Provider) CollectFactsWithDateWindows(ctx context.Context, target SearchTarget, opts Options) (*Result, error) {
	if opts.WindowDays <= 0 {
		return nil, fmt.Errorf("WindowDays must be > 0, got %d", opts.WindowDays)
	}
	if opts.DateFrom == nil {
		return nil, fmt.Errorf("DateFrom is required when using date windows")
	}

	dateTo := time.Now()
	if opts.DateTo != nil {
		dateTo = *opts.DateTo
	}
	dateFrom := *opts.DateFrom

	if !dateFrom.Before(dateTo) {
		return nil, fmt.Errorf("DateFrom (%s) must be before DateTo (%s)", dateFrom.Format("2006-01-02"), dateTo.Format("2006-01-02"))
	}

	// Generate windows
	var windows []dateWindow
	cursor := dateFrom
	for cursor.Before(dateTo) {
		end := cursor.AddDate(0, 0, opts.WindowDays)
		if end.After(dateTo) {
			end = dateTo
		}
		windows = append(windows, dateWindow{from: cursor, to: end})
		cursor = end
	}

	totalWindows := len(windows)
	log.Info().
		Str("target", target.Name).
		Int("windows", totalWindows).
		Int("window_days", opts.WindowDays).
		Str("date_from", dateFrom.Format("2006-01-02")).
		Str("date_to", dateTo.Format("2006-01-02")).
		Msg("Starting date-windowed collection")

	// Aggregate result
	aggregate := &Result{
		Target: target,
	}

	for i, w := range windows {
		select {
		case <-ctx.Done():
			log.Warn().
				Str("target", target.Name).
				Int("window", i+1).
				Int("total_windows", totalWindows).
				Msg("Context cancelled, returning partial results")
			aggregate.Errors = append(aggregate.Errors, ctx.Err())
			return aggregate, ctx.Err()
		default:
		}

		log.Info().
			Str("target", target.Name).
			Int("window", i+1).
			Int("total_windows", totalWindows).
			Str("from", w.from.Format("2006-01-02")).
			Str("to", w.to.Format("2006-01-02")).
			Msg("Processing date window")

		// Copy opts for this window, override dates, clear WindowDays to prevent recursion
		windowOpts := opts
		windowOpts.DateFrom = &w.from
		windowTo := w.to
		windowOpts.DateTo = &windowTo
		windowOpts.WindowDays = 0

		result, err := p.CollectFacts(ctx, target, windowOpts)
		if err != nil {
			log.Error().Err(err).
				Str("target", target.Name).
				Int("window", i+1).
				Int("total_windows", totalWindows).
				Msg("Window failed, continuing to next")
			aggregate.Errors = append(aggregate.Errors, fmt.Errorf("window %d/%d (%s to %s): %w",
				i+1, totalWindows, w.from.Format("2006-01-02"), w.to.Format("2006-01-02"), err))
			continue
		}

		// Aggregate counters
		aggregateResult(aggregate, result)

		// Re-query with shifted window if story cap was hit and budget remains
		if opts.MaxPerStory > 0 && result.SkippedStoryCap > 0 && result.ArticlesParsed < opts.MaxParse {
			reQueryWindow := computeReQueryWindow(w.from, w.to, result.StoryCappedDates)
			if reQueryWindow != nil {
				remainingBudget := opts.MaxParse - result.ArticlesParsed
				log.Info().
					Str("target", target.Name).
					Int("window", i+1).
					Str("requery_from", reQueryWindow.from.Format("2006-01-02")).
					Str("requery_to", reQueryWindow.to.Format("2006-01-02")).
					Int("remaining_budget", remainingBudget).
					Int("skipped_story_cap", result.SkippedStoryCap).
					Msg("Re-querying with shifted window to find diverse stories")

				reQueryOpts := windowOpts
				reQueryOpts.DateFrom = &reQueryWindow.from
				reQueryTo := reQueryWindow.to
				reQueryOpts.DateTo = &reQueryTo
				reQueryOpts.MaxParse = remainingBudget

				reResult, reErr := p.CollectFacts(ctx, target, reQueryOpts)
				if reErr != nil {
					log.Warn().Err(reErr).
						Str("target", target.Name).
						Int("window", i+1).
						Msg("Re-query failed, continuing")
					aggregate.Errors = append(aggregate.Errors, fmt.Errorf("re-query window %d/%d: %w", i+1, totalWindows, reErr))
				} else {
					aggregateResult(aggregate, reResult)
					log.Info().
						Str("target", target.Name).
						Int("window", i+1).
						Int("requery_articles_parsed", reResult.ArticlesParsed).
						Int("requery_facts_extracted", reResult.FactsExtracted).
						Msg("Re-query completed")
				}
			}
		}
	}

	log.Info().
		Str("target", target.Name).
		Int("windows_processed", totalWindows).
		Int("articles_found", aggregate.ArticlesFound).
		Int("articles_parsed", aggregate.ArticlesParsed).
		Int("facts_extracted", aggregate.FactsExtracted).
		Int("skipped_already_processed", aggregate.SkippedAlreadyProcessed).
		Int("skipped_story_cap", aggregate.SkippedStoryCap).
		Int("errors", len(aggregate.Errors)).
		Msg("Date-windowed collection completed")

	return aggregate, nil
}

// dateWindow represents a time range for a search window.
type dateWindow struct {
	from time.Time
	to   time.Time
}

// aggregateResult adds the counters from src into dst.
func aggregateResult(dst *Result, src *Result) {
	dst.ArticlesFound += src.ArticlesFound
	dst.ArticlesParsed += src.ArticlesParsed
	dst.ArticlesParsedNoFacts += src.ArticlesParsedNoFacts
	dst.FactsExtracted += src.FactsExtracted
	dst.FactsSkippedDedup += src.FactsSkippedDedup
	dst.SkippedAlreadyProcessed += src.SkippedAlreadyProcessed
	dst.SkippedEmptyURL += src.SkippedEmptyURL
	dst.SkippedStoryCap += src.SkippedStoryCap
	dst.StoryCappedDates = append(dst.StoryCappedDates, src.StoryCappedDates...)
	for _, token := range src.StoryCappedTokens {
		if !containsString(dst.StoryCappedTokens, token) {
			dst.StoryCappedTokens = append(dst.StoryCappedTokens, token)
		}
	}
	dst.Errors = append(dst.Errors, src.Errors...)
}

// computeReQueryWindow calculates a sub-window that avoids the date cluster of capped articles.
// It picks the larger gap (before or after the cluster) within the original window.
// Returns nil if no viable sub-window >= 1 day exists.
func computeReQueryWindow(windowFrom, windowTo time.Time, cappedDates []time.Time) *dateWindow {
	if len(cappedDates) == 0 {
		return nil
	}

	// Sort capped dates to find cluster boundaries
	sorted := make([]time.Time, len(cappedDates))
	copy(sorted, cappedDates)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })

	// Cluster boundaries with 1-day buffer, clamped to window
	clusterStart := sorted[0].AddDate(0, 0, -1)
	if clusterStart.Before(windowFrom) {
		clusterStart = windowFrom
	}
	clusterEnd := sorted[len(sorted)-1].AddDate(0, 0, 1)
	if clusterEnd.After(windowTo) {
		clusterEnd = windowTo
	}

	// Calculate gaps
	beforeGap := clusterStart.Sub(windowFrom)
	afterGap := windowTo.Sub(clusterEnd)

	minDuration := 24 * time.Hour

	// Pick the larger gap
	if beforeGap >= afterGap && beforeGap >= minDuration {
		return &dateWindow{from: windowFrom, to: clusterStart}
	}
	if afterGap >= minDuration {
		return &dateWindow{from: clusterEnd, to: windowTo}
	}

	return nil
}

// containsString checks if a slice contains a specific string.
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
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
