package frpolitics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// AmbiguousNamesError is returned when target names cannot be uniquely resolved.
type AmbiguousNamesError struct {
	Resolutions map[string]core.TargetResolutionResult
}

func (e *AmbiguousNamesError) Error() string {
	return fmt.Sprintf("ambiguous target names: %d queries need disambiguation", len(e.Resolutions))
}

// parseStats is now defined in types.go as ParseStats

// Run is the main entrypoint for cron execution; it is intentionally thin and orchestrates library calls
func Run(ctx context.Context, storer *storage.PostgresStorer, opts Options) error {
	// Default mode to "votes" if not specified
	if opts.Mode == "" {
		opts.Mode = "votes"
	}

	// If mode is "news", skip vote processing and go straight to news search
	if opts.Mode == "news" {
		return runNewsSearchStandalone(ctx, storer, opts)
	}

	// 1) Discover scrutins -> produce a list of law URLs to process
	laws, groupStats, eventsByLaw, orgCache, pstats, err := discoverAndParse(ctx, opts)
	if err != nil {
		return err
	}

	log.Info().Int("discovered", len(laws)).Msg("frpolitics: discovery finished")

	// 2) Deduplicate law URLs using fact_sources (minimal metadata law_key)
	filteredLaws := make([]Law, 0, len(laws))
	skippedProcessed := 0
	skippedVisited := 0
	revisitHours := 24
	if opts.RevisitHours > 0 {
		revisitHours = opts.RevisitHours
	}
	for _, law := range laws {
		// Check if law fully processed (prevents duplicate law insertions and enables resumability)
		lawKeyProcessed, err := storer.IsLawFullyProcessed(ctx, law.Key)
		if err != nil {
			return err
		}
		if lawKeyProcessed {
			skippedProcessed++
			continue
		}

		// Only keep laws whose law page URL is valid and not visited recently
		if _, err := url.ParseRequestURI(law.URL); err != nil {
			log.Debug().Str("url", law.URL).Msg("frpolitics: skipping invalid URL")
			continue
		}
		// We rely on IsURLVisitable to apply revisit window logic
		visitable, sourceExists, err := storer.IsURLVisitable(ctx, law.URL, revisitHours)
		if err != nil {
			return err
		}
		if visitable {
			filteredLaws = append(filteredLaws, law)
		} else {
			skippedVisited++
			log.Debug().Str("url", law.URL).Bool("source_exists", sourceExists).Msg("frpolitics: skipping already visited URL")
		}
		// Optimization: stop filtering once we have reached MaxLaws limit
		if opts.MaxLaws > 0 && len(filteredLaws) >= opts.MaxLaws {
			break
		}
	}

	log.Info().
		Int("filtered", len(filteredLaws)).
		Int("skipped_processed", skippedProcessed).
		Int("skipped_visited", skippedVisited).
		Msg("frpolitics: filtering finished")

	// 2b) Apply semantic filtering (keyword heuristics + AI confidence)
	var keywordFilteredLaws []Law
	if opts.DisableKeywordFilter {
		log.Info().Msg("frpolitics: keyword filtering disabled, keeping all laws")
		keywordFilteredLaws = filteredLaws
	} else {
		var err error
		keywordFilteredLaws, err = FilterLaws(ctx, filteredLaws, storer)
		if err != nil {
			return fmt.Errorf("keyword filtering failed: %w", err)
		}
		log.Info().Int("keyword_filtered", len(keywordFilteredLaws)).Int("skipped", len(filteredLaws)-len(keywordFilteredLaws)).Msg("frpolitics: keyword filtering finished")
	}

	if len(keywordFilteredLaws) == 0 {
		log.Info().Msg("frpolitics: all votes already extracted or filtered out")
		if opts.Mode == "both" {
			return runNewsSearchStandalone(ctx, storer, opts)
		}
		return nil
	}

	// 3) Summarize laws in batches
	sampleKeys := make([]string, 0, 3)
	for i := 0; i < len(keywordFilteredLaws) && i < 3; i++ {
		sampleKeys = append(sampleKeys, keywordFilteredLaws[i].Key)
	}
	log.Info().
		Int("total_laws", len(keywordFilteredLaws)).
		Strs("sample_keys", sampleKeys).
		Msg("frpolitics: starting law summarization (this may take a while with AI calls)")
	summaries, err := SummarizeLaws(ctx, keywordFilteredLaws, opts.MaxLaws)
	if err != nil {
		return err
	}
	log.Info().Int("summaries_generated", len(summaries)).Msg("frpolitics: law summarization completed")

	// 3b) Apply AI confidence filtering
	var aiFilteredLaws []Law
	if opts.DisableAIFilter {
		log.Info().Msg("frpolitics: AI confidence filtering disabled, keeping all laws")
		aiFilteredLaws = keywordFilteredLaws
	} else {
		// Use 0.5 as default threshold if MinConfidence is not set (0.0)
		minConfidence := opts.MinConfidence
		if minConfidence == 0.0 {
			minConfidence = 0.5 // Default threshold for production use
		}
		log.Info().Int("total_laws", len(keywordFilteredLaws)).Float64("min_confidence", minConfidence).Msg("frpolitics: starting AI confidence filtering")
		totalLaws := len(keywordFilteredLaws)
		for idx, law := range keywordFilteredLaws {
			// Log progress every 10%
			if totalLaws > 10 && idx > 0 && idx%(totalLaws/10) == 0 {
				log.Info().Int("processed", idx).Int("total", totalLaws).Int("percent", (idx*100)/totalLaws).Msg("frpolitics: AI filtering progress")
			}

			summary, ok := summaries[law.Key]
			if !ok {
				// No summary available, keep the law (fallback behavior)
				aiFilteredLaws = append(aiFilteredLaws, law)
				continue
			}

			if summary.Confidence < minConfidence {
				// Skip low-confidence laws
				confidence := summary.Confidence
				err := storer.StoreSkippedLaw(ctx, law.Key, law.Title, law.Legislature, law.VoteDate, "low_ai_confidence", "", &confidence)
				if err != nil {
					return fmt.Errorf("failed to store skipped law %s: %w", law.Key, err)
				}
			} else {
				// Keep high-confidence laws
				aiFilteredLaws = append(aiFilteredLaws, law)
			}
		}

		log.Info().Int("ai_filtered", len(aiFilteredLaws)).Int("skipped_ai", len(keywordFilteredLaws)-len(aiFilteredLaws)).Msg("frpolitics: AI confidence filtering finished")
	}

	if len(aiFilteredLaws) == 0 {
		log.Info().Msg("frpolitics: no laws passed AI confidence filter")
		if opts.Mode == "both" {
			return runNewsSearchStandalone(ctx, storer, opts)
		}
		return nil
	}

	// 4) Build party facts based on majority rule per law, and deputy facts
	log.Info().Int("total_laws", len(aiFilteredLaws)).Msg("frpolitics: starting fact building")
	toStore := make([]FactRecord, 0, len(aiFilteredLaws))
	threshold := opts.PartyThreshold
	if threshold <= 0 {
		threshold = 0.8
	}
	var partyFactsCount, deputyFactsCount int
	partySeed := make(map[string]struct {
		orgRef string
		abbrev string
	})
	deputySeed := make(map[string]string) // assembleeID -> displayName
	resolvePartyName := func(orgRef string) string {
		name := orgRef
		if info, ok := orgCache[orgRef]; ok {
			if strings.TrimSpace(info.LongName) != "" {
				name = info.LongName
			} else if strings.TrimSpace(info.ShortName) != "" {
				name = info.ShortName
			}
		}
		return name
	}
	totalFactLaws := len(aiFilteredLaws)
	for idx, law := range aiFilteredLaws {
		// Log progress every 10%
		if totalFactLaws > 10 && idx > 0 && idx%(totalFactLaws/10) == 0 {
			log.Info().Int("processed", idx).Int("total", totalFactLaws).Int("percent", (idx*100)/totalFactLaws).Int("facts_so_far", len(toStore)).Msg("frpolitics: fact building progress")
		}
		summary, ok := summaries[law.Key]
		if !ok {
			summary = LawSummary{SimpleStatement: law.Title}
		}
		group := groupStats[law.Key]
		// Collect party seeds (ensure parties exist with entity_type='party')
		for orgRef := range group {
			name := resolvePartyName(orgRef)
			abbr := ""
			if info, ok := orgCache[orgRef]; ok {
				abbr = info.ShortName
			}
			partySeed[name] = struct {
				orgRef string
				abbrev string
			}{orgRef: orgRef, abbrev: abbr}
		}
		// Also collect from nominative events to ensure party records exist even if group stats missing
		if evs, ok := eventsByLaw[law.Key]; ok {
			for _, ev := range evs {
				orgRef := strings.TrimSpace(ev.OrganeRef)
				if orgRef == "" {
					continue
				}
				name := resolvePartyName(orgRef)
				abbr := ""
				if info, ok := orgCache[orgRef]; ok {
					abbr = info.ShortName
				}
				partySeed[name] = struct {
					orgRef string
					abbrev string
				}{orgRef: orgRef, abbrev: abbr}
			}
		}
		partyFacts := derivePartyMajorityFacts(law, group, threshold, orgCache, summary)
		// Add law metadata to party facts
		for i := range partyFacts {
			partyFacts[i].LawTitle = law.Title
			partyFacts[i].LawShortTitle = summary.ShortTitle
			partyFacts[i].LawSummary = summary.SimpleStatement
		}
		toStore = append(toStore, partyFacts...)
		partyFactsCount += len(partyFacts)
		if evs, ok := eventsByLaw[law.Key]; ok && len(evs) > 0 {
			depFacts := deriveDeputyFacts(law, evs, summary)
			// Add law metadata to deputy facts
			for i := range depFacts {
				depFacts[i].LawTitle = law.Title
				depFacts[i].LawShortTitle = summary.ShortTitle
				depFacts[i].LawSummary = summary.SimpleStatement
			}
			toStore = append(toStore, depFacts...)
			deputyFactsCount += len(depFacts)
			// Collect deputy seeds
			for _, ev := range evs {
				id := strings.TrimSpace(ev.AssembleeID)
				name := strings.TrimSpace(ev.DisplayName)
				if id != "" && name != "" {
					deputySeed[id] = name
				}
			}
		}
	}

	if len(toStore) == 0 {
		log.Info().
			Int("laws_discovered", len(laws)).
			Int("laws_filtered", len(filteredLaws)).
			Int("party_facts_built", partyFactsCount).
			Int("deputy_facts_built", deputyFactsCount).
			Int("files", pstats.FilesProcessed).
			Int("json_errors", pstats.JsonErrors).
			Int("events_parsed", pstats.EventsParsed).
			Int("events_complete", pstats.EventsComplete).
			Int("events_dropped", pstats.EventsDropped).
			Msg("frpolitics: nothing to store")
		return nil
	}

	// Batch insert parties and deputies in one transaction
	if len(partySeed) > 0 || len(deputySeed) > 0 {
		log.Info().Int("parties", len(partySeed)).Int("deputies", len(deputySeed)).Msg("frpolitics: batch inserting actors")
		tx, err := storer.Pool().Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		// Convert partySeed to PartyMeta map
		parties := make(map[string]PartyMeta)
		for name, meta := range partySeed {
			parties[name] = PartyMeta{OrgRef: meta.orgRef, Abbrev: meta.abbrev}
		}

		if err := BatchInsertActors(ctx, tx, deputySeed, parties); err != nil {
			return fmt.Errorf("batch insert actors: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit actors: %w", err)
		}
		log.Info().Int("parties", len(partySeed)).Int("deputies", len(deputySeed)).Msg("frpolitics: actors batch inserted successfully")
	}

	// 4b) Create temporal affiliations between deputies and parties
	if len(deputySeed) > 0 && len(partySeed) > 0 {
		log.Info().Int("total_laws", len(aiFilteredLaws)).Msg("frpolitics: creating temporal affiliations (this may take a while)")
		tx, err := storer.Pool().Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin affiliation transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		// Create affiliations for all deputy-party relationships found in the events
		totalAffLaws := len(aiFilteredLaws)
		affiliationsCreated := 0
		for affIdx, law := range aiFilteredLaws {
			// Log progress every 10%
			if totalAffLaws > 10 && affIdx > 0 && affIdx%(totalAffLaws/10) == 0 {
				log.Info().Int("processed", affIdx).Int("total", totalAffLaws).Int("percent", (affIdx*100)/totalAffLaws).Int("affiliations_created", affiliationsCreated).Msg("frpolitics: affiliation creation progress")
			}

			if evs, ok := eventsByLaw[law.Key]; ok {
				for _, ev := range evs {
					orgRef := strings.TrimSpace(ev.OrganeRef)
					assembleeID := strings.TrimSpace(ev.AssembleeID)
					if orgRef == "" || assembleeID == "" {
						continue
					}

					// Get deputy ID
					deputyID, err := storer.GetPersonIDByAssembleeID(ctx, assembleeID)
					if err != nil {
						log.Warn().Err(err).Str("assembleeID", assembleeID).Msg("Failed to get deputy ID for affiliation")
						continue
					}

					// Get party ID
					partyID, err := storer.GetPersonIDByOrgRef(ctx, orgRef)
					if err != nil {
						log.Warn().Err(err).Str("orgRef", orgRef).Msg("Failed to get party ID for affiliation")
						continue
					}

					// Create temporal affiliation
					attributes := map[string]any{
						"organeRef":   orgRef,
						"legislature": law.Legislature,
					}

					err = storer.FindOrCreateAffiliation(ctx, tx, deputyID, partyID, law.VoteDate, nil, attributes)
					if err != nil {
						log.Warn().Err(err).Str("deputyID", fmt.Sprintf("%d", deputyID)).Str("partyID", fmt.Sprintf("%d", partyID)).Msg("Failed to create affiliation")
						continue
					}
					affiliationsCreated++
				}
			}
		}

		log.Info().Int("total_affiliations", affiliationsCreated).Msg("frpolitics: committing affiliation transaction")
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit affiliations: %w", err)
		}
		log.Info().Int("total_affiliations", affiliationsCreated).Msg("frpolitics: affiliations committed successfully")
	}

	// 5) Group by fact text to generate embeddings once per unique text, then persist with all mentions
	log.Info().Int("total_facts", len(toStore)).Msg("frpolitics: grouping facts by text for deduplication")
	factTextToRecords := make(map[string][]FactRecord, len(toStore))
	for _, r := range toStore {
		factTextToRecords[r.FactText] = append(factTextToRecords[r.FactText], r)
	}

	// 6) Store facts per-law with completion marking (enables resumability)
	log.Info().Int("total_laws", len(aiFilteredLaws)).Msg("frpolitics: storing facts per law")

	// Group facts by law_key for per-law processing
	factsByLawKey := make(map[string]map[string][]FactRecord)
	for factText, records := range factTextToRecords {
		if len(records) == 0 {
			continue
		}
		lawKey := records[0].LawKey // All records for same fact have same law_key
		if factsByLawKey[lawKey] == nil {
			factsByLawKey[lawKey] = make(map[string][]FactRecord)
		}
		factsByLawKey[lawKey][factText] = records
	}

	// Store facts per law and mark completion
	var processedCount, failedCount, skippedCount int
	for idx, law := range aiFilteredLaws {
		lawFacts := factsByLawKey[law.Key]
		if len(lawFacts) == 0 {
			skippedCount++
			log.Warn().
				Int("law_num", idx+1).
				Int("total_laws", len(aiFilteredLaws)).
				Str("law_key", law.Key).
				Msg("frpolitics: no facts generated for law, skipping")
			continue
		}

		log.Info().
			Int("law_num", idx+1).
			Int("total_laws", len(aiFilteredLaws)).
			Str("law_key", law.Key).
			Str("law_title", law.Title[:min(60, len(law.Title))]).
			Int("facts_count", len(lawFacts)).
			Msg("frpolitics: storing facts for law")

		// Clean up any partial data from previous failed run
		// This deletes facts, fact_sources, contradictions, and fact_mentions for this law
		deletedCount, err := storer.DeletePartialLawData(ctx, law.Key)
		if err != nil {
			log.Error().
				Err(err).
				Str("law_key", law.Key).
				Msg("frpolitics: failed to clean up partial law data")
			failedCount++
			continue
		}
		if deletedCount > 0 {
			log.Info().
				Int("deleted_facts", deletedCount).
				Str("law_key", law.Key).
				Msg("frpolitics: cleaned up partial data from previous failed run")
		}

		// Store all facts for this law
		if err := StoreFactsGrouped(ctx, storer, lawFacts); err != nil {
			log.Error().
				Err(err).
				Int("law_num", idx+1).
				Int("total_laws", len(aiFilteredLaws)).
				Str("law_key", law.Key).
				Msg("frpolitics: failed to store facts for law (will retry on next run)")
			failedCount++
			continue // Don't mark as processed, will retry next run
		}

		// Mark law as fully processed
		factsCount := len(lawFacts)
		if err := storer.MarkLawAsProcessed(ctx, law.Key, law.Legislature, law.Title, factsCount); err != nil {
			log.Error().
				Err(err).
				Str("law_key", law.Key).
				Msg("frpolitics: failed to mark law as processed")
			failedCount++
			continue
		}

		processedCount++

		log.Info().
			Int("law_num", idx+1).
			Int("total_laws", len(aiFilteredLaws)).
			Str("law_key", law.Key).
			Int("facts_stored", factsCount).
			Msg("frpolitics: law successfully processed and marked complete")
	}

	log.Info().
		Int("processed", processedCount).
		Int("failed", failedCount).
		Int("skipped", skippedCount).
		Int("total", len(aiFilteredLaws)).
		Msg("frpolitics: law processing complete")

	if failedCount > 0 {
		return fmt.Errorf("failed to fully process %d/%d laws (will resume on next run)", failedCount, len(aiFilteredLaws))
	}
	unique := make([]FactRecord, 0, len(factTextToRecords))
	for _, recs := range factTextToRecords {
		if len(recs) > 0 {
			unique = append(unique, recs[0])
		}
	}
	log.Info().
		Int("laws_discovered", len(laws)).
		Int("laws_url_filtered", len(filteredLaws)).
		Int("laws_keyword_filtered", len(keywordFilteredLaws)).
		Int("laws_ai_filtered", len(aiFilteredLaws)).
		Int("party_facts_built", partyFactsCount).
		Int("deputy_facts_built", deputyFactsCount).
		Int("dedup_removed", len(toStore)-len(unique)).
		Int("stored", len(unique)).
		Int("files", pstats.FilesProcessed).
		Int("json_errors", pstats.JsonErrors).
		Int("events_parsed", pstats.EventsParsed).
		Int("events_complete", pstats.EventsComplete).
		Int("events_dropped", pstats.EventsDropped).
		Msg("frpolitics: run summary")

	// 6) Optional: Search news for deputies and parties (only in "both" mode)
	if opts.Mode == "both" {
		if err := runNewsSearch(ctx, storer, deputySeed, partySeed, opts); err != nil {
			log.Error().Err(err).Msg("frpolitics: news search failed")
			// Don't return error - news search is optional
		}
	}

	return nil
}

// LegislaturesForDateRange returns all legislature numbers whose period overlaps with [since, now].
// Legislature periods are derived from legislatureStarts (newest-first); each legislature ends
// when the next one begins, and the current legislature extends to now.
func LegislaturesForDateRange(since time.Time) []int {
	now := time.Now()
	var result []int
	for i, entry := range legislatureStarts {
		// End of this legislature is the next (newer) entry's start, or now for the current one.
		var end time.Time
		if i == 0 {
			end = now
		} else {
			end = legislatureStarts[i-1].Start
		}
		// Overlap with [since, now]: legislature started before now AND ended after since.
		if !entry.Start.After(now) && end.After(since) {
			result = append(result, entry.Number)
		}
	}
	return result
}

// discoverAndParse lists recent scrutins and parses them into:
// - laws: minimal law info per scrutin
// - groupStats: per-law map of party majority stats
// - eventsByLaw: per-law nominative votes (only complete ones kept)
// - orgCache: organe (party) reference -> names
func discoverAndParse(ctx context.Context, opts Options) ([]Law, map[string]LawGroupStats, LawEventMap, OrganeInfoMap, ParseStats, error) {
	var outLaws []Law
	groupStatsByLaw := make(map[string]LawGroupStats)
	eventsByLaw := make(LawEventMap)
	orgCache := make(OrganeInfoMap)
	var stats ParseStats

	// Build options defaults (legislatures and since) by inspecting a conservative set.
	// We read a larger set here; upper layers can limit summarization later.
	legislatures := opts.Legislatures
	if len(legislatures) == 0 {
		if opts.Since != nil {
			// Auto-derive all legislatures that overlap with [since, now].
			legislatures = LegislaturesForDateRange(*opts.Since)
			log.Info().Ints("legislatures", legislatures).Msg("frpolitics: auto-derived legislatures for date range")
		} else {
			legislatures = []int{currentLegislature()}
		}
	}
	var sincePtr *time.Time
	if opts.Since != nil {
		s := *opts.Since
		sincePtr = &s
	}

	// Load organe caches per legislature and merge
	for _, leg := range legislatures {
		cache := loadOrganeCacheForLegislature(ctx, leg)
		for k, v := range cache {
			orgCache[k] = v
		}
	}

	// Load actor name map to resolve AssembleeID -> DisplayName (historical)
	actorNames := loadActorNameMap(ctx)

	// Track total votes processed for MaxVotes limit
	var totalVotesProcessed int

	// Helper to parse Scrutins.zip for a legislature
	parseLeg := func(leg int) error {
		candidates := []string{
			fmt.Sprintf("https://data.assemblee-nationale.fr/static/openData/repository/%d/loi/scrutins/Scrutins.json.zip", leg),
			fmt.Sprintf("https://data.assemblee-nationale.fr/static/openData/repository/%d/scrutins/Scrutins.json.zip", leg),
		}
		var body []byte
		var err error
		for _, url := range candidates {
			body, err = downloadCached(ctx, url, getCacheDir(), 24*time.Hour)
			if err == nil && len(body) > 0 {
				break
			}
		}
		if err != nil {
			log.Warn().Err(err).Int("legislature", leg).Msg("frpolitics: failed to download scrutins, skipping legislature")
			return nil
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			log.Warn().Err(err).Int("legislature", leg).Msg("frpolitics: failed to read scrutins zip, skipping legislature")
			return nil
		}
		for _, f := range zr.File {
			if !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
				continue
			}
			stats.FilesProcessed++
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(rc, 10<<20))
			rc.Close()
			if err != nil {
				continue
			}
			var s scrutinJSON
			if err := json.Unmarshal(data, &s); err != nil {
				stats.JsonErrors++
				continue
			}
			// Filter by date if requested
			if sincePtr != nil && s.Scrutin.DateScrutin != "" {
				// Try parse as date-only or RFC3339
				if t, err := time.Parse(time.RFC3339, s.Scrutin.DateScrutin); err == nil {
					if t.Before(*sincePtr) {
						continue
					}
				} else if t2, err2 := time.Parse("2006-01-02", s.Scrutin.DateScrutin); err2 == nil {
					if t2.Before(*sincePtr) {
						continue
					}
				}
			}
			// Filter by legislature match (defensive)
			if int(s.Scrutin.Legislature) != leg {
				// Keep anyway; some files may lack the field, but they still belong to the set
			}
			// Parse group stats and nominative events
			law, groups, err := ParseScrutin(s)
			if err != nil {
				continue
			}
			complete, dropped := CleanAndNameEvents(s, actorNames)
			stats.EventsParsed += len(complete) + dropped
			stats.EventsDropped += dropped
			stats.EventsComplete += len(complete)
			totalVotesProcessed += len(complete)

			// Keep only if we have some usable signals (either party stats or nominative events)
			if len(groups) == 0 && len(complete) == 0 {
				continue
			}
			// Deduplicate laws by key
			if _, exists := groupStatsByLaw[law.Key]; !exists && eventsByLaw[law.Key] == nil {
				outLaws = append(outLaws, law)
			}
			groupStatsByLaw[law.Key] = groups
			if len(complete) > 0 {
				eventsByLaw[law.Key] = complete
			}

			// Check MaxVotes limit (0 = unlimited)
			if opts.MaxVotes > 0 && totalVotesProcessed >= opts.MaxVotes {
				log.Info().Int("max_votes_reached", opts.MaxVotes).Int("votes_processed", totalVotesProcessed).Msg("Stopping processing due to MaxVotes limit")
				return nil // Stop processing this legislature
			}
		}
		return nil
	}

	for _, leg := range legislatures {
		_ = parseLeg(leg)
	}

	return outLaws, groupStatsByLaw, eventsByLaw, orgCache, stats, nil
}

// runNewsSearch initializes news search dependencies and searches for news about deputies and parties.
func runNewsSearch(ctx context.Context, storer *storage.PostgresStorer, deputySeed map[string]string, partySeed map[string]struct {
	orgRef string
	abbrev string
}, opts Options) error {
	log.Info().
		Int("deputies", len(deputySeed)).
		Int("parties", len(partySeed)).
		Msg("frpolitics: starting news search")

	// Initialize news search provider
	newsSearcher, err := search.NewNewsSearcherFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize news search: %w", err)
	}

	// Initialize content extractor based on option (default to webcrawler)
	var contentExtractor extractor.ContentExtractor
	if opts.CacheOnly {
		// Cache-only mode: use CacheOnlyExtractor (no inner extractor)
		contentExtractor = extractor.NewCacheOnlyExtractor(opts.CacheDir)
		log.Info().Str("cache_dir", opts.CacheDir).Msg("Using cache-only mode (no scraping)")
	} else {
		extractorType := opts.ContentExtractor
		if extractorType == "" {
			extractorType = "webcrawler"
		}
		ext, err := extractor.NewExtractorFromType(extractorType)
		if err != nil {
			return fmt.Errorf("failed to initialize content extractor: %w", err)
		}
		contentExtractor = ext

		// Wrap with cache if cache-dir specified
		if opts.CacheDir != "" {
			contentExtractor = extractor.NewCacheExtractor(ext, opts.CacheDir, true, true)
			log.Info().Str("cache_dir", opts.CacheDir).Msg("Caching enabled for content extraction")
		}
	}

	// Initialize fact extractor
	factExtractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize fact extractor: %w", err)
	}

	// Initialize embedder
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize embedder: %w", err)
	}

	// Stance manager — required by the news provider so newly inserted facts
	// get routed into stance documents. Without it, CollectFacts errors out.
	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(storer))
	if err != nil {
		return fmt.Errorf("failed to initialize stance manager: %w", err)
	}

	// Create news provider
	newsProvider := news.NewProvider(newsSearcher, contentExtractor, factExtractor, embedder, storer, stanceMgr)

	// Build targets from deputies and parties
	targets := make([]news.SearchTarget, 0, len(deputySeed)+len(partySeed))

	// Add parties first (they always have coverage)
	for partyName, meta := range partySeed {
		// Get party ID from database using orgRef
		partyID, err := storer.GetPersonIDByOrgRef(ctx, meta.orgRef)
		if err != nil {
			// Fall back to name lookup
			partyID, err = storer.GetPersonIDByName(ctx, partyName)
			if err != nil {
				log.Warn().Err(err).Str("party", partyName).Msg("Failed to get party ID for news search")
				continue
			}
		}
		targets = append(targets, news.SearchTarget{
			Name:     partyName,
			Type:     news.TargetParty,
			PersonID: &partyID,
		})
	}

	// Add deputies
	for assembleeID, displayName := range deputySeed {
		// Get deputy ID from database
		deputyID, err := storer.GetPersonIDByAssembleeID(ctx, assembleeID)
		if err != nil {
			log.Warn().Err(err).Str("assembleeID", assembleeID).Msg("Failed to get deputy ID for news search")
			continue
		}
		targets = append(targets, news.SearchTarget{
			Name:     displayName,
			Type:     news.TargetPerson,
			PersonID: &deputyID,
		})
	}

	if len(targets) == 0 {
		log.Info().Msg("frpolitics: no targets for news search")
		return nil
	}

	// Prioritize targets
	prioritizedTargets, err := news.PrioritizeTargets(ctx, storer, targets, news.PrioritizationOptions{
		LookbackDays:  30,
		SkipNoResults: opts.NewsSkipUnknown,
		PartiesFirst:  true,
	})
	if err != nil {
		return fmt.Errorf("failed to prioritize targets: %w", err)
	}

	// Build news search options
	newsOpts := news.Options{
		MaxParse:     opts.NewsLimit,
		Location:     opts.NewsLocation,
		RevisitHours: opts.RevisitHours,
		SkipUnknown:  opts.NewsSkipUnknown,
		DateFrom:     opts.NewsSince,
	}
	if newsOpts.MaxParse == 0 {
		newsOpts.MaxParse = 5 // Default to 5 articles per target
	}
	if newsOpts.Location == "" {
		newsOpts.Location = "France" // Default to France for frpolitics
	}

	// Search news for each target
	results, err := newsProvider.CollectFactsForTargets(ctx, prioritizedTargets, newsOpts)
	if err != nil {
		return fmt.Errorf("news collection failed: %w", err)
	}

	// Log summary
	var totalArticles, totalFacts int
	for _, r := range results {
		totalArticles += r.ArticlesParsed
		totalFacts += r.FactsExtracted
	}

	log.Info().
		Int("targets_searched", len(results)).
		Int("articles_processed", totalArticles).
		Int("facts_extracted", totalFacts).
		Msg("frpolitics: news search completed")

	return nil
}

// runNewsSearchStandalone runs news search without processing votes (mode="news").
// Returns a map of query -> resolution results for disambiguation if needed.
func runNewsSearchStandalone(ctx context.Context, storer *storage.PostgresStorer, opts Options) error {
	log.Info().Msg("frpolitics: running in news-only mode")

	// Build targets list
	var targets []news.SearchTarget
	var ambiguousQueries map[string]core.TargetResolutionResult

	if len(opts.NewsTargets) > 0 {
		// Resolve target names with fuzzy matching
		log.Info().Int("count", len(opts.NewsTargets)).Msg("frpolitics: resolving specified news targets")

		resolutions := core.ResolveTargetNames(ctx, storer, opts.NewsTargets)
		ambiguousQueries = make(map[string]core.TargetResolutionResult)

		for query, result := range resolutions {
			if result.Error != nil {
				log.Error().Err(result.Error).Str("query", query).Msg("Failed to resolve target")
				continue
			}

			if result.IsExactMatch {
				// Exact match found
				log.Info().Str("query", query).Str("matched", result.ExactMatch.Name).Msg("Exact match found")
				targetType := news.TargetPerson
				if result.ExactMatch.EntityType == "party" {
					targetType = news.TargetParty
				}
				targets = append(targets, news.SearchTarget{
					Name:     result.ExactMatch.Name,
					Type:     targetType,
					PersonID: result.ExactMatch.PersonID,
				})
			} else if len(result.FuzzyMatches) > 0 {
				// Ambiguous - store for later
				log.Warn().Str("query", query).Int("candidates", len(result.FuzzyMatches)).Msg("Ambiguous name - disambiguation needed")
				ambiguousQueries[query] = result
			} else {
				// No matches found - also needs disambiguation (user can create)
				log.Warn().Str("query", query).Msg("No matches found in database - user can create")
				ambiguousQueries[query] = result
			}
		}

		// If there are ambiguous queries or missing persons, return error with disambiguation info
		if len(ambiguousQueries) > 0 {
			return &AmbiguousNamesError{
				Resolutions: ambiguousQueries,
			}
		}
	} else {
		// Get all persons from database
		log.Info().Msg("frpolitics: fetching all persons from database for news search")
		allPersons, err := storer.GetAllPersons(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch persons: %w", err)
		}

		if len(allPersons) == 0 {
			log.Info().Msg("frpolitics: no persons in database to search news for")
			return nil
		}

		for _, p := range allPersons {
			targetType := news.TargetPerson
			if p.EntityType == "party" {
				targetType = news.TargetParty
			}

			personID := p.ID
			targets = append(targets, news.SearchTarget{
				Name:     p.Name,
				Type:     targetType,
				PersonID: &personID,
			})
		}

		log.Info().Int("count", len(targets)).Msg("frpolitics: loaded persons from database")
	}

	if len(targets) == 0 {
		log.Info().Msg("frpolitics: no targets for news search")
		return nil
	}

	// Initialize news search provider
	newsSearcher, err := search.NewNewsSearcherFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize news search: %w", err)
	}

	// Initialize content extractor based on option (default to webcrawler)
	var contentExtractor extractor.ContentExtractor
	if opts.CacheOnly {
		// Cache-only mode: use CacheOnlyExtractor (no inner extractor)
		contentExtractor = extractor.NewCacheOnlyExtractor(opts.CacheDir)
		log.Info().Str("cache_dir", opts.CacheDir).Msg("Using cache-only mode (no scraping)")
	} else {
		extractorType := opts.ContentExtractor
		if extractorType == "" {
			extractorType = "webcrawler"
		}
		ext, err := extractor.NewExtractorFromType(extractorType)
		if err != nil {
			return fmt.Errorf("failed to initialize content extractor: %w", err)
		}
		contentExtractor = ext

		// Wrap with cache if cache-dir specified
		if opts.CacheDir != "" {
			contentExtractor = extractor.NewCacheExtractor(ext, opts.CacheDir, true, true)
			log.Info().Str("cache_dir", opts.CacheDir).Msg("Caching enabled for content extraction")
		}
	}

	// Initialize fact extractor
	factExtractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize fact extractor: %w", err)
	}

	// Initialize embedder
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize embedder: %w", err)
	}

	// Stance manager — required by the news provider (same as above).
	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(storer))
	if err != nil {
		return fmt.Errorf("failed to initialize stance manager: %w", err)
	}

	// Create news provider
	newsProvider := news.NewProvider(newsSearcher, contentExtractor, factExtractor, embedder, storer, stanceMgr)

	// Prioritize targets
	prioritizedTargets, err := news.PrioritizeTargets(ctx, storer, targets, news.PrioritizationOptions{
		LookbackDays:  30,
		SkipNoResults: opts.NewsSkipUnknown,
		PartiesFirst:  true,
	})
	if err != nil {
		return fmt.Errorf("failed to prioritize targets: %w", err)
	}

	// Build news search options
	newsOpts := news.Options{
		MaxParse:     opts.NewsLimit,
		Location:     opts.NewsLocation,
		RevisitHours: opts.RevisitHours,
		SkipUnknown:  opts.NewsSkipUnknown,
		DateFrom:     opts.NewsSince,
	}
	if newsOpts.MaxParse == 0 {
		newsOpts.MaxParse = 5 // Default to 5 articles per target
	}
	if newsOpts.Location == "" {
		newsOpts.Location = "France" // Default to France for frpolitics
	}

	// Search news for each target
	results, err := newsProvider.CollectFactsForTargets(ctx, prioritizedTargets, newsOpts)
	if err != nil {
		return fmt.Errorf("news collection failed: %w", err)
	}

	// Log summary
	var totalArticles, totalFacts int
	for _, r := range results {
		totalArticles += r.ArticlesParsed
		totalFacts += r.FactsExtracted
	}

	log.Info().
		Int("targets_searched", len(results)).
		Int("articles_processed", totalArticles).
		Int("facts_extracted", totalFacts).
		Msg("frpolitics: news-only mode completed")

	return nil
}

// legislatureStart maps legislature numbers to their start dates (newest first).
// To add a new legislature, insert one entry at the top.
var legislatureStarts = []struct {
	Number int
	Start  time.Time
}{
	{17, time.Date(2024, 7, 18, 0, 0, 0, 0, time.UTC)},
	{16, time.Date(2022, 6, 28, 0, 0, 0, 0, time.UTC)},
	{15, time.Date(2017, 6, 27, 0, 0, 0, 0, time.UTC)},
	{14, time.Date(2012, 6, 20, 0, 0, 0, 0, time.UTC)},
}

// currentLegislatureAt returns the legislature number that is active at time t.
// Falls back to the oldest known legislature if t predates all entries.
func currentLegislatureAt(t time.Time) int {
	for _, entry := range legislatureStarts {
		if !t.Before(entry.Start) {
			return entry.Number
		}
	}
	// Before all known legislatures — return the oldest
	return legislatureStarts[len(legislatureStarts)-1].Number
}

// currentLegislature returns the legislature number active right now.
func currentLegislature() int {
	return currentLegislatureAt(time.Now())
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
