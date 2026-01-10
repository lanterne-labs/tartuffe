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

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// parseStats is now defined in types.go as ParseStats

// Run is the main entrypoint for cron execution; it is intentionally thin and orchestrates library calls
func Run(ctx context.Context, storer *storage.PostgresStorer, opts Options) error {
	// 1) Discover scrutins -> produce a list of law URLs to process
	laws, groupStats, eventsByLaw, orgCache, pstats, err := discoverAndParse(ctx, opts)
	if err != nil {
		return err
	}

	log.Info().Int("discovered", len(laws)).Msg("frpolitics: discovery finished")

	// 2) Deduplicate law URLs using fact_sources (minimal metadata law_key)
	filteredLaws := make([]Law, 0, len(laws))
	revisitHours := 24
	if opts.RevisitHours > 0 {
		revisitHours = opts.RevisitHours
	}
	for _, law := range laws {
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
			log.Debug().Str("url", law.URL).Bool("source_exists", sourceExists).Msg("frpolitics: skipping already visited URL")
		}
		// Optimization: stop filtering once we have reached MaxLaws limit
		if opts.MaxLaws > 0 && len(filteredLaws) >= opts.MaxLaws {
			break
		}
	}

	log.Info().Int("filtered", len(filteredLaws)).Msg("frpolitics: filtering finished")

	// 2b) Apply semantic filtering (keyword heuristics + AI confidence)
	keywordFilteredLaws, err := FilterLaws(ctx, filteredLaws, storer)
	if err != nil {
		return fmt.Errorf("keyword filtering failed: %w", err)
	}

	log.Info().Int("keyword_filtered", len(keywordFilteredLaws)).Int("skipped", len(filteredLaws)-len(keywordFilteredLaws)).Msg("frpolitics: keyword filtering finished")

	if len(keywordFilteredLaws) == 0 {
		return nil
	}

	// 3) Summarize laws in batches
	summaries, err := SummarizeLaws(ctx, keywordFilteredLaws, opts.MaxLaws)
	if err != nil {
		return err
	}

	// 3b) Apply AI confidence filtering
	var aiFilteredLaws []Law
	for _, law := range keywordFilteredLaws {
		summary, ok := summaries[law.Key]
		if !ok {
			// No summary available, keep the law (fallback behavior)
			aiFilteredLaws = append(aiFilteredLaws, law)
			continue
		}

		if summary.Confidence < 0.5 {
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

	if len(aiFilteredLaws) == 0 {
		return nil
	}
	summaries, err = SummarizeLaws(ctx, keywordFilteredLaws, opts.MaxLaws)
	if err != nil {
		return err
	}

	// 4) Build party facts based on majority rule per law, and deputy facts
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
	for _, law := range aiFilteredLaws {
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
	}

	// 4b) Create temporal affiliations between deputies and parties
	if len(deputySeed) > 0 && len(partySeed) > 0 {
		tx, err := storer.Pool().Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin affiliation transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		// Create affiliations for all deputy-party relationships found in the events
		for _, law := range aiFilteredLaws {
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
				}
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit affiliations: %w", err)
		}
	}

	// 5) Group by fact text to generate embeddings once per unique text, then persist with all mentions
	factTextToRecords := make(map[string][]FactRecord, len(toStore))
	for _, r := range toStore {
		factTextToRecords[r.FactText] = append(factTextToRecords[r.FactText], r)
	}
	if err := StoreFactsGrouped(ctx, storer, factTextToRecords); err != nil {
		return fmt.Errorf("store facts: %w", err)
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
	return nil
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
		legislatures = []int{16}
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
			body, err = downloadCached(ctx, url, getCacheDir())
			if err == nil && len(body) > 0 {
				break
			}
		}
		if err != nil {
			// If we can't download scrutins for this legislature, skip quietly
			return nil
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
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
