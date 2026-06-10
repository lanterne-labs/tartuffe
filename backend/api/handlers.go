package api

import (
	"digital-mirror/backend/core"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// publicMinFacts is the floor on fact_count for entities shown on the public
// site: only persons with MORE THAN 3 facts (>= 4) are surfaced, so one-off
// mentions and incidentally-created junk entities never appear.
const publicMinFacts = 4

// handleHealth checks database connectivity and returns server health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")

	if err := s.storer.Ping(ctx); err != nil {
		log.Error().Err(err).Msg("health check: db ping failed")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "error",
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

// handleGetPersons returns persons ranked by fact count and honesty score
func (s *Server) handleGetPersons(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse pagination parameters
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	entityType := r.URL.Query().Get("entity_type") // "party", "human", or empty for all
	search := r.URL.Query().Get("search")          // case-insensitive partial match on name
	sort := r.URL.Query().Get("sort")              // "honesty_desc", "honesty_asc", or empty for default (fact_count)
	minFactsStr := r.URL.Query().Get("min_facts")  // minimum fact_count to include

	limit := 50 // default
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	offset := 0 // default
	if offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	// The public site only surfaces entities with a real track record: enforce a
	// floor of >3 facts so one-off mentions and junk entities (institutions,
	// bookstores, mis-extractions the pipeline incidentally created) never show.
	// A caller may request a higher minimum, never a lower one.
	minFacts := publicMinFacts
	if minFactsStr != "" {
		if parsed, err := strconv.Atoi(minFactsStr); err == nil && parsed > publicMinFacts {
			minFacts = parsed
		}
	}

	persons, err := s.storer.GetPersonsRankedByScore(ctx, limit, offset, entityType, search, sort, minFacts)
	if err != nil {
		httpInternalError(w, err, "failed to get persons")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	json.NewEncoder(w).Encode(persons)
}

// handleGetPerson returns basic person information
func (s *Server) handleGetPerson(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personIDStr := chi.URLParam(r, "id")

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	person, err := s.storer.GetPersonByID(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get person")
		return
	}

	if person == nil {
		http.Error(w, "Person not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(person)
}

// handleGetPersonFacts returns facts for a specific person
func (s *Server) handleGetPersonFacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personIDStr := chi.URLParam(r, "id")

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	// Parse pagination parameters
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 25 // default
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	offset := 0 // default
	if offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	result, err := s.storer.GetPersonFacts(ctx, personID, limit, offset)
	if err != nil {
		httpInternalError(w, err, "failed to get facts")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleGetPersonContradictions returns contradictions for a specific person
func (s *Server) handleGetPersonContradictions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personIDStr := chi.URLParam(r, "id")

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	// Parse pagination parameters
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 25 // default
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 1000 {
			limit = parsed
		}
	}

	offset := 0 // default
	if offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	result, err := s.storer.GetPersonContradictions(ctx, personID, limit, offset)
	if err != nil {
		httpInternalError(w, err, "failed to get contradictions")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleGetPersonHistory returns the weekly honesty timeline of a person (statement-time axis).
func (s *Server) handleGetPersonHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	points, err := s.storer.GetPersonHistory(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get person history")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(points)
}

// handleGetPersonMembers returns persons affiliated with an entity (e.g., deputies in a party)
func (s *Server) handleGetPersonMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personIDStr := chi.URLParam(r, "id")

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	members, err := s.storer.GetPersonMembers(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get members")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(members)
}

// StanceResponse is the API response for a stance document (no embedding or key_fact_ids).
// ContradictionCount is the raw number of contradictions whose bundle_stance_id is this stance;
// Score reuses the global ComputeHonestyScore formula scoped to this stance, so it lives on the
// same 0..1 scale as the headline person score.
type StanceResponse struct {
	ID                 int64     `json:"id"`
	Label              string    `json:"label"`
	LabelEn            string    `json:"label_en,omitempty"`
	Summary            string    `json:"summary"`
	SummaryEn          string    `json:"summary_en,omitempty"`
	FactCount          int       `json:"fact_count"`
	ContradictionCount int       `json:"contradiction_count"`
	Score              float64   `json:"score"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// stanceScore is the per-stance honesty score shown on a stance card. It feeds
// core.ComputeHonestyScore with the stance's fact count and its raw
// contradiction total, but collapses every contradiction of the stance into a
// single bundle (Bundles = 1 whenever Total > 0). The person-level score
// instead counts distinct stance-grouped bundles (ContradictionStats from the
// contradictions table), so a stance with N contradictions scores here as one
// inconsistency plus N-1 repetition extras rather than as N bundles. This
// divergence is deliberate for now; changing it must go with a review of the
// person formula.
func stanceScore(factCount, contradictionTotal int) float64 {
	bundles := 0
	if contradictionTotal > 0 {
		bundles = 1
	}
	return core.ComputeHonestyScore(factCount, core.ContradictionStats{Bundles: bundles, Total: contradictionTotal})
}

// handleGetStanceFacts returns all facts attached to a stance, with both native and
// English text so the frontend language toggle can swap per-fact without a second round trip.
func (s *Server) handleGetStanceFacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stanceIDStr := chi.URLParam(r, "id")
	stanceID, err := strconv.ParseInt(stanceIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid stance ID", http.StatusBadRequest)
		return
	}
	items, err := s.storer.GetFactsInStance(ctx, stanceID)
	if err != nil {
		httpInternalError(w, err, "failed to get stance facts")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(items)
}

// handleGetStanceContradictions returns the distinct-inconsistency GROUPS whose base
// fact belongs to this stance — one base fact + its N contradicting facts each, so the
// UI shows one card per inconsistency instead of a wall of pairwise rows. Used by the
// profile UI when a stance card is expanded.
func (s *Server) handleGetStanceContradictions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stanceIDStr := chi.URLParam(r, "id")

	stanceID, err := strconv.ParseInt(stanceIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid stance ID", http.StatusBadRequest)
		return
	}

	items, err := s.storer.GetStanceContradictionGroups(ctx, stanceID)
	if err != nil {
		httpInternalError(w, err, "failed to get stance contradiction groups")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// handleGetPersonStances returns stance documents for a specific person, with a per-stance
// honesty score and contradiction count attached.
func (s *Server) handleGetPersonStances(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personIDStr := chi.URLParam(r, "id")

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	docs, err := s.storer.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get stances")
		return
	}

	// Two per-stance numbers: the bundle-based total drives the honesty score (kept
	// unchanged), while the group count (distinct base facts) is what the badge shows
	// so users see one count per distinct inconsistency, not the inflated pairwise total.
	counts, err := s.storer.GetStanceContradictionCounts(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get stance contradiction counts")
		return
	}
	groupCounts, err := s.storer.GetStanceGroupCounts(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get stance group counts")
		return
	}

	result := make([]StanceResponse, len(docs))
	for i, d := range docs {
		result[i] = StanceResponse{
			ID:                 d.ID,
			Label:              d.ClusterLabel,
			LabelEn:            d.LabelEn,
			Summary:            d.Summary,
			SummaryEn:          d.SummaryEn,
			FactCount:          d.FactCount,
			ContradictionCount: groupCounts[d.ID],
			Score:              stanceScore(d.FactCount, counts[d.ID]),
			UpdatedAt:          d.LastUpdatedAt,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleGetFactPersons returns persons who have a fact related to the given fact
func (s *Server) handleGetFactPersons(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	factIDStr := chi.URLParam(r, "id")

	factID, err := strconv.ParseInt(factIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid fact ID", http.StatusBadRequest)
		return
	}

	// Parse pagination parameters
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 50
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			if parsed > 1000 {
				limit = 1000
			} else {
				limit = parsed
			}
		}
	}

	offset := 0
	if offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	persons, err := s.storer.GetFactPersons(ctx, factID, limit, offset)
	if err != nil {
		httpInternalError(w, err, "failed to get persons for fact")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(persons)
}

// handleGetActivity returns a paginated list of recent contradiction_added and
// person_updated events, enriched with the associated person's display info.
func (s *Server) handleGetActivity(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")

	limit := 20
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}

	offset := 0
	if offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	result, err := s.storer.GetActivityFeed(ctx, limit, offset)
	if err != nil {
		httpInternalError(w, err, "failed to get activity feed")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=30, stale-while-revalidate=60")
	json.NewEncoder(w).Encode(result)
}
