package news

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// minArticleContentLen is the minimum length (after the extractor's cleaning
// pipeline strips sidebars/related-tiles) for which we'll trust the LLM to
// extract facts. Pages below this floor are typically video-only or paywalled
// stubs where the LLM would otherwise hallucinate from page chrome.
const minArticleContentLen = 200

// targetKind is the search-side label of a target, "person" or "party": it
// selects the extraction prompt and is stored in fact metadata.
func targetKind(t plugins.Target) string {
	if t.IsParty() {
		return "party"
	}
	return "person"
}

// partyInfo holds basic information about an affiliated party
type partyInfo struct {
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
	// CacheDir, when set, caches scraped article content on disk under a
	// per-target subdirectory (see targetCacheDir) and serves later runs from it.
	CacheDir string
	// CacheOnly ingests the content already cached under CacheDir for each
	// target instead of searching and scraping. Requires CacheDir.
	CacheOnly bool
}

// Result contains the outcome of a news search for a single target.
type Result struct {
	// Target is the entity that was searched.
	Target plugins.Target
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
	// SkippedThinContent is the number of articles skipped because the scraper returned
	// content too thin to extract from (typically video-only pages whose body is just
	// related-link tiles that the cleaning pipeline strips out).
	SkippedThinContent int
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
	// store is the plugin's own persistence (search log, news provenance).
	store *Store
}

// NewProvider creates a news Provider. StanceManager is required at insert
// time (CollectFacts returns "StanceManager is required" otherwise); making it
// a positional arg here so the compiler catches drift in any new call site.
func NewProvider(
	newsSearch search.NewsSearcher,
	contentExtractor extractor.ContentExtractor,
	factExtractor *categorizer.OpenAIExtractor,
	embedder *embedding.OpenAIEmbeddingGenerator,
	storer *storage.PostgresStorer,
	stanceManager *core.StanceManager,
) *Provider {
	return &Provider{
		NewsSearch:       newsSearch,
		ContentExtractor: contentExtractor,
		FactExtractor:    factExtractor,
		Embedder:         embedder,
		Storer:           storer,
		StanceManager:    stanceManager,
		store:            NewStore(storer),
	}
}

// UseExtractor replaces the scraper with the named extractor type (see
// extractor.NewExtractorFromType), batch-wrapped like the default one.
// NewProviderFromDeps already wires the "auto" chain; call this only for
// another type.
func (p *Provider) UseExtractor(extractorType string) error {
	raw, err := extractor.NewExtractorFromType(extractorType)
	if err != nil {
		return fmt.Errorf("content extractor %q: %w", extractorType, err)
	}
	p.ContentExtractor = extractor.NewBatchExtractorFromEnv(raw)
	return nil
}

// targetCacheDir returns the target-specific cache subdirectory under baseDir.
// Creates the directory if it doesn't exist.
func targetCacheDir(baseDir string, targetName string) string {
	normalizedName := util.NormalizeToDirName(targetName)
	targetDir := filepath.Join(baseDir, normalizedName)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		log.Warn().Err(err).Str("dir", targetDir).Msg("Failed to create target cache directory")
		return baseDir // Fallback to base directory
	}

	return targetDir
}

// contentExtractorFor returns the scraper for one target: the provider's own,
// or, when opts.CacheDir is set, that scraper behind a read-write disk cache
// in the target's cache directory. The cache is wrapped in a fresh batch
// extractor so extraction stays concurrent; the provider's scraper may itself
// be a batch extractor, whose single-URL path is a plain passthrough.
func (p *Provider) contentExtractorFor(target plugins.Target, opts Options) extractor.ContentExtractor {
	if opts.CacheDir == "" {
		return p.ContentExtractor
	}
	dir := targetCacheDir(opts.CacheDir, target.Name)
	log.Debug().Str("target", target.Name).Str("cache_dir", dir).Msg("Caching enabled")
	return extractor.NewBatchExtractorFromEnv(extractor.NewCacheExtractor(p.ContentExtractor, dir, true, true))
}

// collect runs one target in the mode opts selects: cached content only,
// date-windowed search, or a single search.
func (p *Provider) collect(ctx context.Context, target plugins.Target, opts Options) (*Result, error) {
	switch {
	case opts.CacheOnly:
		return p.CollectFactsFromCache(ctx, target, opts)
	case opts.WindowDays > 0:
		return p.CollectFactsWithDateWindows(ctx, target, opts)
	default:
		return p.CollectFacts(ctx, target, opts)
	}
}

// CollectFacts searches news for the given target and extracts facts.
func (p *Provider) CollectFacts(ctx context.Context, target plugins.Target, opts Options) (*Result, error) {
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

	// Search for news articles bilingually: native language of the person + en.
	// Prevents random foreign-language articles (Korean, Thai, etc.) from being
	// dragged in when search engines return globally-relevant matches.
	newsResults, err := p.bilingualSearch(ctx, target, searchOpts)
	if err != nil {
		return nil, fmt.Errorf("news search failed for %s: %w", target.Name, err)
	}

	result.ArticlesFound = len(newsResults)
	log.Info().
		Str("target", target.Name).
		Str("type", targetKind(target)).
		Int("articles_found", len(newsResults)).
		Msg("news: search completed")

	if len(newsResults) == 0 {
		// Log that no results were found for prioritization
		if target.PersonID != 0 {
			if err := p.store.LogNewsSearch(ctx, target.PersonID, target.Name, 0, 0); err != nil {
				log.Warn().Err(err).Int64("person_id", target.PersonID).Msg("Failed to log empty news search")
			}
		}
		return result, nil
	}

	revisitHours := opts.RevisitHours
	if revisitHours == 0 {
		revisitHours = 24 // Default to 24 hours
	}

	// For person targets, get affiliated parties upfront for party-aware extraction
	var affiliatedParties []partyInfo
	if !target.IsParty() && target.PersonID != 0 {
		var err error
		affiliatedParties, err = p.getAffiliatedParties(ctx, target.PersonID, nil) // nil = current affiliations
		if err != nil {
			log.Warn().Err(err).
				Int64("person_id", target.PersonID).
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

	if err := p.extractAndInsertFromArticles(ctx, target, p.contentExtractorFor(target, opts), urlsToExtract, articlesToProcess, affiliatedParties, opts, result); err != nil {
		return result, err
	}

	// Log the search for prioritization
	if target.PersonID != 0 {
		if err := p.store.LogNewsSearch(ctx, target.PersonID, target.Name, result.ArticlesFound, result.FactsExtracted); err != nil {
			log.Warn().Err(err).Int64("person_id", target.PersonID).Msg("Failed to log news search")
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
		Int("skipped_thin_content", result.SkippedThinContent).
		Int("errors", len(result.Errors)).
		Msg("news: collection completed")

	return result, nil
}

// extractAndInsertFromArticles is the shared "scrape → LLM extract → embed →
// dedup-insert" loop, factored out of CollectFacts so it can be reused by
// replayURLs (which skips the search/visitability phase).
//
// urlsToExtract and articlesToProcess are parallel slices: urlsToExtract[i] ==
// articlesToProcess[i].Link. The caller is responsible for de-duplication and
// bilingualSearch calls the underlying news searcher once per target language
// and merges results (dedup by URL, first occurrence wins). The languages are
// derived from the target's person.lang in the DB plus an English pass:
//   - person.lang = "fr" → ["fr", "en"]
//   - person.lang = "de" → ["de", "en"]
//   - person.lang = "en" → ["en"]
//   - target has no PersonID (e.g. parties not yet linked, fresh ad-hoc queries)
//     → ["fr", "en"] (legacy default — French-politics focus of the project)
//
// Each per-language call sets baseOpts.Language so the upstream search API
// (Serper hl+gl, SerpAPI hl+gl, Brave search_lang+country) restricts results
// to that language's media landscape. Without this filter, queries for
// well-known politicians can pull articles in any language — observed before:
// Korean Yonhap coverage of Norwegian elections leaking into French-politics
// rebuild because the entity validator approved e.g. "노동당(노르웨이)" as a
// real entity from Korean Wikipedia.
//
// Limit is preserved per-call (each language is allowed up to Limit results);
// final merged slice may exceed Limit when both languages return results, by
// design — the dedup cap is the user-visible limit, not the per-call one.
func (p *Provider) bilingualSearch(ctx context.Context, target plugins.Target, baseOpts search.NewsSearchOptions) ([]search.NewsResult, error) {
	langs := p.targetLanguages(ctx, target)

	seen := make(map[string]bool)
	var merged []search.NewsResult
	for _, lang := range langs {
		callOpts := baseOpts
		callOpts.Language = lang
		results, err := p.NewsSearch.SearchNews(target.Name, callOpts)
		if err != nil {
			// Don't fail the whole bilingual search if one language errors —
			// surface the error only when ALL language passes failed.
			log.Warn().Err(err).Str("target", target.Name).Str("lang", lang).Msg("news: per-language search failed; continuing with other languages")
			continue
		}
		for _, r := range results {
			if seen[r.Link] {
				continue
			}
			seen[r.Link] = true
			merged = append(merged, r)
		}
	}
	if len(merged) == 0 && len(langs) > 0 {
		// All language passes returned zero results (or errored). Surface as
		// an empty result, not an error — matches the legacy SearchNews
		// "no results" behaviour the callers already handle gracefully.
		return nil, nil
	}
	return merged, nil
}

// targetLanguages returns the BCP-47 language tags to search for this target.
// Always includes "en" exactly once. Falls back to ["fr", "en"] for legacy
// callers that haven't populated PersonID — historical French-politics focus.
func (p *Provider) targetLanguages(ctx context.Context, target plugins.Target) []string {
	native := "fr"
	if target.PersonID != 0 {
		got, err := p.Storer.GetPersonLang(ctx, target.PersonID)
		if err == nil && got != "" {
			native = got
		}
	}
	if native == "en" {
		return []string{"en"}
	}
	return []string{native, "en"}
}

func (p *Provider) extractAndInsertFromArticles(
	ctx context.Context,
	target plugins.Target,
	contentExtractor extractor.ContentExtractor,
	urlsToExtract []string,
	articlesToProcess []search.NewsResult,
	affiliatedParties []partyInfo,
	opts Options,
	result *Result,
) error {
	// Phase 2: Extract content - use batch if available, otherwise sequential.
	var extractedContents []*extractor.ExtractedContent
	var extractionErrors []error
	if batchExtractor, ok := contentExtractor.(*extractor.BatchExtractor); ok {
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
			content, err := contentExtractor.ExtractContent(url)
			extractedContents[i] = content
			extractionErrors[i] = err
		}
	}

	// Phase 3: For each article, extract facts via LLM, embed, dedup-insert.
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
		if len(strings.TrimSpace(content.Content)) < minArticleContentLen {
			log.Warn().
				Str("url", article.Link).
				Int("content_len", len(content.Content)).
				Msg("news: scraped content too thin after cleaning — skipping extraction")
			metrics.IncrementNewsArticles("skipped_thin_content")
			result.SkippedThinContent++
			continue
		}

		var facts []backend.Fact
		var err error
		if !target.IsParty() && len(affiliatedParties) > 0 {
			partyNames := make([]string, len(affiliatedParties))
			for j, party := range affiliatedParties {
				partyNames[j] = party.Name
			}
			facts, err = p.FactExtractor.ExtractFactsForPersonWithParties(ctx, target.Name, partyNames, *content)
		} else {
			facts, err = p.FactExtractor.ExtractFactsWithType(ctx, target.Name, targetKind(target), *content)
		}
		if err != nil {
			log.Warn().Err(err).Str("url", article.Link).Msg("Failed to extract facts")
			result.Errors = append(result.Errors, err)
			continue
		}

		// Publication date — priority: LLM-from-body > webcrawlerapi <meta>
		// > Serper SERP date. All facts in this batch came from the same
		// article so they share the same article_published_at; take the first
		// non-nil one the LLM emitted (if any).
		var llmDate *time.Time
		for _, f := range facts {
			if f.ArticlePublishedAt != nil {
				llmDate = f.ArticlePublishedAt
				break
			}
		}
		publishedAt := core.PickPublishedAt(llmDate, content.DatePublished, article.Date)

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

		for _, fact := range facts {
			embeddings, err := p.Embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, []string{fact.EmbeddingText})
			if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
				log.Warn().Err(err).Str("fact", fact.NormalizedFactText).Msg("Failed to generate embedding")
				result.Errors = append(result.Errors, fmt.Errorf("embedding failed: %w", err))
				continue
			}

			input := core.StoreFactInput{
				SubjectEntity:        fact.SubjectEntity,
				SubjectEntityType:    fact.SubjectEntityType,
				NormalizedFactText:   fact.NormalizedFactText,
				NormalizedFactTextEn: fact.NormalizedFactTextEn,
				Lang:                 fact.Lang,
				EmbeddingText:        fact.EmbeddingText,
				Embedding:            embeddings[0],
				OriginalQuote:        fact.OriginalQuote,
				CanonicalVerb:        fact.CanonicalVerb,
				InvolvedPeople:       fact.InvolvedPeople,
				SourceURL:            article.Link,
				PublishedAt:          publishedAt,
				SourceType:           "news",
				Metadata: map[string]any{
					"source_name":  article.Source,
					"story_token":  article.StoryToken,
					"target_type":  targetKind(target),
					"search_query": target.Name,
				},
			}

			if p.StanceManager == nil {
				return fmt.Errorf("news provider: StanceManager is required (set p.StanceManager before calling)")
			}
			inserter := core.NewFactInserter(p.Storer, p.StanceManager)
			inserter.EnableLLMDedupe()
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

		log.Debug().Str("url", article.Link).Int("facts", len(facts)).Msg("news: processed article")
	}
	return nil
}

// replayURLs runs the news extraction pipeline against a hand-picked list of
// URLs for a single target. It bypasses the search-engine query and the
// already-processed visit check — the caller is asserting "I want these URLs
// re-processed". Used by RebuildFromURLs to re-extract facts after the
// cleaning-pipeline changes.
//
// Stance routing and contradiction detection run inline via the normal
// FactInserter path, so the rebuild is a deterministic replay of the daily
// pipeline for known URLs.
func (p *Provider) replayURLs(ctx context.Context, target plugins.Target, urls []string, opts Options) (*Result, error) {
	result := &Result{}
	if len(urls) == 0 {
		return result, nil
	}

	var affiliatedParties []partyInfo
	if !target.IsParty() && target.PersonID != 0 {
		var err error
		affiliatedParties, err = p.getAffiliatedParties(ctx, target.PersonID, nil)
		if err != nil {
			log.Warn().Err(err).
				Int64("person_id", target.PersonID).
				Str("person", target.Name).
				Msg("replayURLs: failed to load affiliated parties, continuing without")
		}
	}

	articles := make([]search.NewsResult, len(urls))
	for i, u := range urls {
		articles[i] = search.NewsResult{Link: u}
	}

	if err := p.extractAndInsertFromArticles(ctx, target, p.ContentExtractor, urls, articles, affiliatedParties, opts, result); err != nil {
		return result, err
	}
	return result, nil
}

// CollectFactsFromCache ingests the article content already cached under
// opts.CacheDir for the target: no search, no scraping. Every cached file is
// run through LLM extraction, embedding and dedup-insert.
func (p *Provider) CollectFactsFromCache(ctx context.Context, target plugins.Target, opts Options) (*Result, error) {
	if opts.CacheDir == "" {
		return nil, fmt.Errorf("CacheDir is required for cache-only collection")
	}
	if p.StanceManager == nil {
		return nil, fmt.Errorf("news provider: StanceManager is required (set p.StanceManager before calling)")
	}
	result := &Result{Target: target}

	dir := targetCacheDir(opts.CacheDir, target.Name)
	cachedContents, err := extractor.NewCacheOnlyExtractor(dir).LoadAllCached()
	if err != nil {
		return nil, fmt.Errorf("load cached content for %s from %s: %w", target.Name, dir, err)
	}
	if len(cachedContents) == 0 {
		log.Warn().Str("target", target.Name).Str("cache_dir", dir).Msg("No cached files found")
		return result, nil
	}
	result.ArticlesFound = len(cachedContents)
	log.Info().
		Str("target", target.Name).
		Int("cached_files", len(cachedContents)).
		Msg("Loaded cached content")

	inserter := core.NewFactInserter(p.Storer, p.StanceManager)
	inserter.EnableLLMDedupe()
	kind := targetKind(target)
	for _, content := range cachedContents {
		if strings.TrimSpace(content.Content) == "" {
			log.Debug().Str("url", content.URL).Msg("Skipping empty cached content")
			continue
		}

		facts, err := p.FactExtractor.ExtractFactsWithType(ctx, target.Name, kind, *content)
		if err != nil {
			log.Warn().Err(err).Str("url", content.URL).Msg("Failed to extract facts from cached content")
			result.Errors = append(result.Errors, err)
			continue
		}

		if opts.KeepSourceDir != "" {
			if dumpErr := core.DumpSourceAndFacts(opts.KeepSourceDir, target.Name, content.URL, content.Title, content.Content, content.DatePublished, facts); dumpErr != nil {
				log.Warn().Err(dumpErr).Str("url", content.URL).Msg("Failed to dump source and facts")
			}
		}

		if len(facts) == 0 {
			log.Debug().Str("url", content.URL).Msg("No facts extracted from cached content")
			result.ArticlesParsed++
			result.ArticlesParsedNoFacts++
			continue
		}
		result.ArticlesParsed++

		for _, fact := range facts {
			embeddings, err := p.Embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, []string{fact.EmbeddingText})
			if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
				log.Warn().Err(err).Str("fact", util.TruncateString(fact.NormalizedFactText, 80)).Msg("Failed to generate embedding, skipping fact")
				result.Errors = append(result.Errors, fmt.Errorf("embedding failed: %w", err))
				continue
			}

			input := core.StoreFactInput{
				SubjectEntity:      fact.SubjectEntity,
				SubjectEntityType:  fact.SubjectEntityType,
				NormalizedFactText: fact.NormalizedFactText,
				EmbeddingText:      fact.EmbeddingText,
				Embedding:          embeddings[0],
				OriginalQuote:      fact.OriginalQuote,
				CanonicalVerb:      fact.CanonicalVerb,
				InvolvedPeople:     fact.InvolvedPeople,
				SourceURL:          content.URL,
				PublishedAt:        content.DatePublished,
				SourceType:         "news",
				Metadata: map[string]any{
					"target_type":  kind,
					"search_query": target.Name,
					"cache_only":   true,
				},
			}

			insertResult, err := inserter.InsertFacts(ctx, []core.StoreFactInput{input})
			if err != nil {
				log.Warn().Err(err).Str("fact", util.TruncateString(fact.NormalizedFactText, 80)).Msg("Failed to store fact")
				result.Errors = append(result.Errors, err)
				continue
			}
			result.FactsExtracted += insertResult.Inserted
			result.FactsSkippedDedup += insertResult.Skipped
		}
	}

	dedupRate := 0.0
	if result.FactsExtracted+result.FactsSkippedDedup > 0 {
		dedupRate = float64(result.FactsSkippedDedup) / float64(result.FactsExtracted+result.FactsSkippedDedup) * 100
	}
	log.Info().
		Str("target", target.Name).
		Int("files_processed", len(cachedContents)).
		Int("facts_stored", result.FactsExtracted).
		Int("facts_skipped_dedup", result.FactsSkippedDedup).
		Float64("dedup_rate_pct", dedupRate).
		Int("errors", len(result.Errors)).
		Msg("Completed cache-only extraction for target")

	return result, nil
}

// CollectFactsWithDateWindows breaks the date range into windows of opts.WindowDays days
// and calls CollectFacts for each window. This works around Google News result caps by
// making multiple narrower queries. Errors in individual windows are logged and collected
// but do not stop processing of subsequent windows.
func (p *Provider) CollectFactsWithDateWindows(ctx context.Context, target plugins.Target, opts Options) (*Result, error) {
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
func (p *Provider) getAffiliatedParties(ctx context.Context, personID int64, atDate *time.Time) ([]partyInfo, error) {
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
		return []partyInfo{}, nil
	}

	// Fetch party details for each ID
	parties := make([]partyInfo, 0, len(partyIDs))
	for _, partyID := range partyIDs {
		person, err := p.Storer.GetPersonByID(ctx, partyID)
		if err != nil {
			log.Warn().Err(err).Int64("party_id", partyID).Msg("Failed to get party details")
			continue
		}

		// Only include entities with entity_type='party'
		if person.EntityType != nil && *person.EntityType == "party" {
			parties = append(parties, partyInfo{
				ID:   person.ID,
				Name: person.Name,
			})
		}
	}

	return parties, nil
}

// CollectFactsForTargets runs every target in order through the mode opts
// selects (see collect). A target whose run fails yields a Result carrying
// only that error. History-based skipping is PrioritizeTargets' job and is
// not repeated here.
func (p *Provider) CollectFactsForTargets(ctx context.Context, targets []plugins.Target, opts Options) ([]*Result, error) {
	results := make([]*Result, 0, len(targets))

	for i, target := range targets {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		log.Info().
			Int("progress", i+1).
			Int("total", len(targets)).
			Str("target", target.Name).
			Str("type", targetKind(target)).
			Msg("Searching news for target")

		result, err := p.collect(ctx, target, opts)
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
