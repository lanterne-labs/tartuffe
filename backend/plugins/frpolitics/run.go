package frpolitics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// parseStats is now defined in types.go as ParseStats

// Run is the main entrypoint for cron execution; it is intentionally thin and orchestrates library calls.
// It returns the number of selected laws it went through (MaxLaws slots used), also along with an error.
func Run(ctx context.Context, storer *storage.PostgresStorer, opts Options) (int, error) {
	store := NewStore(storer)

	// 1) Discover scrutins -> produce a list of law URLs to process
	laws, groupStats, eventsByLaw, orgCache, pstats, err := discoverAndParse(ctx, opts)
	if err != nil {
		return 0, err
	}
	// A download cut by a cancelled ctx is skipped like a failed one: the
	// discovered set would be partial.
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("frpolitics stopped after discovery: %w", err)
	}

	log.Info().Int("discovered", len(laws)).Msg("frpolitics: discovery finished")

	// 2) Select the laws this run processes: unsettled, visitable, relevant
	// (keyword and AI filters), with ballots and a resolvable voted object.
	threshold := opts.PartyThreshold
	if threshold <= 0 {
		threshold = 0.8
	}
	sel, err := selectLaws(ctx, storer, store, laws, groupStats, eventsByLaw, orgCache, threshold, opts)
	if err != nil {
		return 0, err
	}
	aiFilteredLaws, summaries := sel.laws, sel.summaries
	if len(aiFilteredLaws) == 0 {
		log.Info().Msg("frpolitics: no law to process")
		return 0, nil
	}

	// 4) Build party facts based on majority rule per law, and deputy facts
	log.Info().Int("total_laws", len(aiFilteredLaws)).Msg("frpolitics: starting fact building")
	toStore := make([]FactRecord, 0, len(aiFilteredLaws))
	var partyFactsCount, deputyFactsCount int
	partySeed := make(map[string]struct {
		orgRef string
		abbrev string
	})
	deputySeed := make(map[string]string) // assembleeID -> displayName
	// resolvePartyName returns "" for an organe the cache cannot name. A raw
	// ref must never become a person row — prod accumulated 13 "parties" named
	// po840056-style that way.
	unresolvedOrgs := make(map[string]bool)
	resolvePartyName := func(orgRef string) string {
		if info, ok := orgCache[orgRef]; ok {
			if strings.TrimSpace(info.LongName) != "" {
				return info.LongName
			}
			if strings.TrimSpace(info.ShortName) != "" {
				return info.ShortName
			}
		}
		if !unresolvedOrgs[orgRef] {
			unresolvedOrgs[orgRef] = true
			log.Warn().Str("organe_ref", orgRef).Msg("frpolitics: organe has no name in cache; not seeding it as a party")
		}
		return ""
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
		// Describe the voted OBJECT (mirror-f28/mirror-0dj): a ballot on an
		// amendement judges the modification only, so its fact text says what
		// the amendment does. selectLaws resolved it and gated the scrutins
		// whose object could not be resolved.
		objectPhrase := votedObjectPhrase(sel.amendments[law.Key], summary)

		group := groupStats[law.Key]
		// Collect party seeds (ensure parties exist with entity_type='party')
		for orgRef := range group {
			name := resolvePartyName(orgRef)
			if name == "" {
				continue
			}
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
				if name == "" {
					continue
				}
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
		partyFacts := derivePartyMajorityFacts(law, group, threshold, orgCache, summary, objectPhrase)
		// Add law metadata to party facts
		for i := range partyFacts {
			partyFacts[i].LawTitle = law.Title
			partyFacts[i].LawShortTitle = summary.ShortTitle
			partyFacts[i].LawSummary = summary.SimpleStatement
		}
		toStore = append(toStore, partyFacts...)
		partyFactsCount += len(partyFacts)
		if evs, ok := eventsByLaw[law.Key]; ok && len(evs) > 0 {
			depFacts := deriveDeputyFacts(law, evs, summary, objectPhrase)
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
			Int("laws_selected", len(aiFilteredLaws)).
			Int("party_facts_built", partyFactsCount).
			Int("deputy_facts_built", deputyFactsCount).
			Int("files", pstats.FilesProcessed).
			Int("json_errors", pstats.JsonErrors).
			Int("events_parsed", pstats.EventsParsed).
			Int("events_complete", pstats.EventsComplete).
			Int("events_dropped", pstats.EventsDropped).
			Msg("frpolitics: nothing to store")
		return 0, nil
	}

	// Batch insert parties and deputies in one transaction
	if len(partySeed) > 0 || len(deputySeed) > 0 {
		log.Info().Int("parties", len(partySeed)).Int("deputies", len(deputySeed)).Msg("frpolitics: batch inserting actors")
		tx, err := storer.Pool().Begin(ctx)
		if err != nil {
			return 0, fmt.Errorf("begin transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		// Convert partySeed to PartyMeta map
		parties := make(map[string]PartyMeta)
		for name, meta := range partySeed {
			parties[name] = PartyMeta{OrgRef: meta.orgRef, Abbrev: meta.abbrev}
		}

		if err := BatchInsertActors(ctx, tx, deputySeed, parties); err != nil {
			return 0, fmt.Errorf("batch insert actors: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("commit actors: %w", err)
		}
		log.Info().Int("parties", len(partySeed)).Int("deputies", len(deputySeed)).Msg("frpolitics: actors batch inserted successfully")
	}

	// 4b) Create temporal affiliations between deputies and parties
	if len(deputySeed) > 0 && len(partySeed) > 0 {
		log.Info().Int("total_laws", len(aiFilteredLaws)).Msg("frpolitics: creating temporal affiliations (this may take a while)")

		// Resolve every deputy and party the events below name in two queries
		// instead of two per event.
		seenDeputy := make(map[string]bool)
		seenOrg := make(map[string]bool)
		var assembleeIDs, orgRefs []string
		for _, law := range aiFilteredLaws {
			for _, ev := range eventsByLaw[law.Key] {
				orgRef := strings.TrimSpace(ev.OrganeRef)
				assembleeID := strings.TrimSpace(ev.AssembleeID)
				if orgRef == "" || assembleeID == "" {
					continue
				}
				if !seenDeputy[assembleeID] {
					seenDeputy[assembleeID] = true
					assembleeIDs = append(assembleeIDs, assembleeID)
				}
				if !seenOrg[orgRef] {
					seenOrg[orgRef] = true
					orgRefs = append(orgRefs, orgRef)
				}
			}
		}
		deputyIDs, err := storer.GetPersonIDsByAttribute(ctx, "assemblee_id", assembleeIDs)
		if err != nil {
			return 0, fmt.Errorf("resolve deputies for affiliations: %w", err)
		}
		partyIDs, err := storer.GetPersonIDsByAttribute(ctx, "organeRef", orgRefs)
		if err != nil {
			return 0, fmt.Errorf("resolve parties for affiliations: %w", err)
		}

		tx, err := storer.Pool().Begin(ctx)
		if err != nil {
			return 0, fmt.Errorf("begin affiliation transaction: %w", err)
		}
		defer tx.Rollback(ctx)

		// Create affiliations for all deputy-party relationships found in the events
		totalAffLaws := len(aiFilteredLaws)
		affiliationsCreated := 0
		for affIdx, law := range aiFilteredLaws {
			if err := ctx.Err(); err != nil {
				return 0, fmt.Errorf("frpolitics stopped creating affiliations: %w", err)
			}
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

					deputyID, ok := deputyIDs[assembleeID]
					if !ok {
						log.Warn().Str("assembleeID", assembleeID).Msg("Failed to get deputy ID for affiliation")
						continue
					}

					partyID, ok := partyIDs[orgRef]
					if !ok {
						log.Warn().Str("orgRef", orgRef).Msg("Failed to get party ID for affiliation")
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
			return 0, fmt.Errorf("commit affiliations: %w", err)
		}
		log.Info().Int("total_affiliations", affiliationsCreated).Msg("frpolitics: affiliations committed successfully")
	}

	// 5) Store facts per-law with completion marking (enables resumability)
	log.Info().Int("total_laws", len(aiFilteredLaws)).Msg("frpolitics: storing facts per law")

	factsByLawKey := make(map[string][]FactRecord)
	for _, r := range toStore {
		factsByLawKey[r.LawKey] = append(factsByLawKey[r.LawKey], r)
	}

	walked, err := storeLaws(ctx, store, aiFilteredLaws, factsByLawKey, func(ctx context.Context, recs []FactRecord) error {
		return StoreFacts(ctx, storer, recs)
	})
	if err != nil {
		return walked, err
	}
	log.Info().
		Int("laws_discovered", len(laws)).
		Int("laws_selected", len(aiFilteredLaws)).
		Int("party_facts_built", partyFactsCount).
		Int("deputy_facts_built", deputyFactsCount).
		Int("stored", len(toStore)).
		Int("files", pstats.FilesProcessed).
		Int("json_errors", pstats.JsonErrors).
		Int("events_parsed", pstats.EventsParsed).
		Int("events_complete", pstats.EventsComplete).
		Int("events_dropped", pstats.EventsDropped).
		Msg("frpolitics: run summary")

	return walked, nil
}

// storeLaws stores the facts of each selected law with storeFacts and marks
// the law processed once all its ballots are stored. A law that fails is left
// for the next run, which resumes after the voters already stored; an
// unreachable LLM (llm.ErrUnavailable) or a cancelled ctx stops the loop and
// is returned, the interrupted law left unmarked. It returns the number of
// laws it went through before any stop (the interrupted law excluded).
func storeLaws(ctx context.Context, store *Store, laws []Law, factsByLawKey map[string][]FactRecord, storeFacts func(context.Context, []FactRecord) error) (int, error) {
	var processedCount, failedCount, skippedCount int
	for idx, law := range laws {
		if err := ctx.Err(); err != nil {
			return idx, fmt.Errorf("frpolitics stopped before law %s: %w", law.Key, err)
		}
		lawFacts := factsByLawKey[law.Key]
		if len(lawFacts) == 0 {
			skippedCount++
			log.Warn().
				Int("law_num", idx+1).
				Int("total_laws", len(laws)).
				Str("law_key", law.Key).
				Msg("frpolitics: no facts generated for law, skipping")
			continue
		}

		log.Info().
			Int("law_num", idx+1).
			Int("total_laws", len(laws)).
			Str("law_key", law.Key).
			Str("law_title", law.Title[:min(60, len(law.Title))]).
			Int("facts_count", len(lawFacts)).
			Msg("frpolitics: storing facts for law")

		// A previous run may have stored part of this law before failing:
		// resume after the voters whose ballot is already stored.
		subjects := make([]string, len(lawFacts))
		for i, r := range lawFacts {
			subjects[i] = r.PersonName
		}
		stored, err := store.StoredLawSubjects(ctx, law.Key, subjects)
		if err != nil && ctx.Err() != nil {
			return idx, fmt.Errorf("frpolitics stopped at law %s: %w", law.Key, ctx.Err())
		}
		if err != nil {
			log.Error().Err(err).Str("law_key", law.Key).Msg("frpolitics: failed to load the law's stored ballots")
			failedCount++
			continue
		}
		pending := make([]FactRecord, 0, len(lawFacts))
		for _, r := range lawFacts {
			if !stored[r.PersonName] {
				pending = append(pending, r)
			}
		}
		if len(pending) < len(lawFacts) {
			log.Info().Int("already_stored", len(lawFacts)-len(pending)).Int("pending", len(pending)).
				Str("law_key", law.Key).Msg("frpolitics: resuming a partially stored law")
		}

		// Store all facts for this law
		if err := storeFacts(ctx, pending); err != nil {
			if errors.Is(err, llm.ErrUnavailable) {
				// Every further law would fail the same way.
				return idx, fmt.Errorf("storing the facts of law %s: %w", law.Key, err)
			}
			if ctx.Err() != nil {
				// Cut part-way: the next run resumes after the ballots stored.
				return idx, fmt.Errorf("frpolitics stopped storing law %s: %w", law.Key, ctx.Err())
			}
			log.Error().
				Err(err).
				Int("law_num", idx+1).
				Int("total_laws", len(laws)).
				Str("law_key", law.Key).
				Msg("frpolitics: failed to store facts for law (will retry on next run)")
			failedCount++
			continue // Don't mark as processed, will retry next run
		}

		// Mark law as fully processed
		factsCount := len(lawFacts)
		if err := store.MarkLawAsProcessed(ctx, law.Key, law.Legislature, law.Title, factsCount); err != nil {
			if ctx.Err() != nil {
				// Every ballot is stored: the next run marks the law at once.
				return idx, fmt.Errorf("frpolitics stopped marking law %s: %w", law.Key, ctx.Err())
			}
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
			Int("total_laws", len(laws)).
			Str("law_key", law.Key).
			Int("facts_stored", factsCount).
			Msg("frpolitics: law successfully processed and marked complete")
	}

	log.Info().
		Int("processed", processedCount).
		Int("failed", failedCount).
		Int("skipped", skippedCount).
		Int("total", len(laws)).
		Msg("frpolitics: law processing complete")

	if failedCount > 0 {
		return len(laws), fmt.Errorf("failed to fully process %d/%d laws (will resume on next run)", failedCount, len(laws))
	}
	return len(laws), nil
}

// lawSelection is the laws a run processes and what their facts need.
type lawSelection struct {
	laws       []Law
	summaries  map[string]LawSummary
	amendments map[string]string // law key → resolveVotedObject's amendment ("" for a text-level vote)
}

// selectLaws walks the discovered laws in order and keeps the ones the run
// will process, up to opts.MaxLaws:
//   - settled laws (processed, or skipped for a verdict of a filter the run
//     applies) and laws
//     whose page was visited within the revisit window are passed over;
//   - the keyword filter and the AI confidence filter record their verdicts
//     in skipped_laws, so those laws never come back;
//   - laws without a ballot to store, or whose voted amendment cannot be
//     resolved yet, are passed over without an LLM call and without a trace
//     (an amendment may reach the archive later). An amendments archive that
//     is not available fails the selection: no lookup can run.
//
// MaxLaws counts only laws that pass every check, so unusable laws at the
// head of the list cannot starve the feed. The LLM summarizes candidates in
// batches no larger than the slots left, and stops once MaxLaws laws passed.
// A summarizer failure fails the selection: an error is never read as low
// relevance.
func selectLaws(ctx context.Context, storer *storage.PostgresStorer, store *Store, laws []Law, groupStats map[string]LawGroupStats, eventsByLaw LawEventMap, orgCache OrganeInfoMap, threshold float64, opts Options) (*lawSelection, error) {
	sel := &lawSelection{summaries: make(map[string]LawSummary), amendments: make(map[string]string)}

	keys := make([]string, len(laws))
	for i, law := range laws {
		keys[i] = law.Key
	}
	var settlingSkips []string
	if !opts.DisableKeywordFilter {
		settlingSkips = append(settlingSkips, "keyword_match")
	}
	if !opts.DisableAIFilter {
		settlingSkips = append(settlingSkips, "low_ai_confidence")
	}
	settled, err := store.SettledLawKeys(ctx, keys, settlingSkips)
	if err != nil {
		return nil, err
	}

	// Default (0): don't re-scan a law URL more than once a day. An explicitly
	// NEGATIVE RevisitHours means "always visitable" (IsURLVisitable compares
	// against a negative window) — the CLI and the daily ingest use it so a
	// law whose store failed mid-batch can be retried immediately instead of
	// deadlocking behind the partial fact_sources rows its own failure left
	// behind.
	revisitHours := 24
	if opts.RevisitHours != 0 {
		revisitHours = opts.RevisitHours
	}
	minConfidence := opts.MinConfidence
	if minConfidence == 0.0 {
		minConfidence = 0.5 // Default threshold for production use
	}

	full := func() bool { return opts.MaxLaws > 0 && len(sel.laws) >= opts.MaxLaws }
	var skippedSettled, skippedVisited, skippedKeyword, skippedNoBallot, skippedGated, skippedAI int
	var batch []Law
	// judge summarizes the batch and keeps the laws the AI filter accepts.
	judge := func() error {
		if len(batch) == 0 {
			return nil
		}
		summaries, err := SummarizeLaws(ctx, batch)
		if err != nil {
			return err
		}
		for _, law := range batch {
			summary, ok := summaries[law.Key]
			if ok && !opts.DisableAIFilter && summary.Confidence < minConfidence {
				confidence := summary.Confidence
				if err := store.StoreSkippedLaw(ctx, law.Key, law.Title, law.Legislature, law.VoteDate, "low_ai_confidence", "", &confidence); err != nil {
					return fmt.Errorf("failed to store skipped law %s: %w", law.Key, err)
				}
				skippedAI++
				continue
			}
			if ok {
				sel.summaries[law.Key] = summary
			}
			sel.laws = append(sel.laws, law)
		}
		batch = batch[:0]
		return nil
	}

	for _, law := range laws {
		if full() {
			break
		}
		if settled[law.Key] {
			skippedSettled++
			continue
		}
		if _, err := url.ParseRequestURI(law.URL); err != nil {
			log.Debug().Str("url", law.URL).Msg("frpolitics: skipping invalid URL")
			continue
		}
		visitable, sourceExists, err := storer.IsURLVisitable(ctx, law.URL, revisitHours)
		if err != nil {
			return nil, err
		}
		if !visitable {
			skippedVisited++
			log.Debug().Str("url", law.URL).Bool("source_exists", sourceExists).Msg("frpolitics: skipping already visited URL")
			continue
		}
		if !opts.DisableKeywordFilter {
			if skip, reason, pattern := classifyLaw(law.Title); skip {
				if err := store.StoreSkippedLaw(ctx, law.Key, law.Title, law.Legislature, law.VoteDate, reason, pattern, nil); err != nil {
					return nil, err
				}
				skippedKeyword++
				continue
			}
		}
		if len(eventsByLaw[law.Key]) == 0 && len(derivePartyMajorityFacts(law, groupStats[law.Key], threshold, orgCache, LawSummary{}, "")) == 0 {
			skippedNoBallot++
			continue
		}
		amendment, ok, err := resolveVotedObject(ctx, law, orgCache)
		if err != nil {
			return nil, fmt.Errorf("resolve the voted object of %s: %w", law.Key, err)
		}
		if !ok {
			skippedGated++
			log.Warn().Str("law_key", law.Key).Str("titre", law.Title[:min(80, len(law.Title))]).
				Msg("frpolitics: amendement content unavailable; gating this scrutin (no facts)")
			continue
		}
		sel.amendments[law.Key] = amendment
		batch = append(batch, law)
		if len(batch) >= summarizeBatchSize || (opts.MaxLaws > 0 && len(sel.laws)+len(batch) >= opts.MaxLaws) {
			if err := judge(); err != nil {
				return nil, err
			}
		}
	}
	if err := judge(); err != nil {
		return nil, err
	}

	log.Info().
		Int("discovered", len(laws)).
		Int("selected", len(sel.laws)).
		Int("skipped_settled", skippedSettled).
		Int("skipped_visited", skippedVisited).
		Int("skipped_keyword", skippedKeyword).
		Int("skipped_no_ballot", skippedNoBallot).
		Int("skipped_gated_object", skippedGated).
		Int("skipped_low_ai_confidence", skippedAI).
		Msg("frpolitics: law selection finished")
	return sel, nil
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

	// Organe names come from the AMO30 archive (all legislatures). Fail rather
	// than continue with an empty map: every downstream party fact depends on it.
	organes, err := loadOrganeCache(ctx)
	if err != nil {
		return nil, nil, nil, nil, ParseStats{}, fmt.Errorf("load organe cache: %w", err)
	}
	for k, v := range organes {
		orgCache[k] = v
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
			// If the first URL exhausted its retry budget on a 429, the
			// second candidate on the same host will too — short-circuit
			// rather than burn another 7-attempt budget keeping the
			// rate-limit hot. Falling-back across candidates only makes
			// sense for 404 (different archive path layouts), not for
			// rate-limit responses.
			if err != nil && strings.Contains(err.Error(), "retryable status 429") {
				log.Warn().Str("url", url).Int("legislature", leg).Msg("frpolitics: 429 after retry budget; skipping further fallback candidates this run")
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
