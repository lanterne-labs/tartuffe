package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/archiver"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/storage"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	port string
)

// webCmd represents the web command group
var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Commands related to the web server.",
	Long:  `Start and manage the web server for the Digital Mirror API.`,
}

// serveCmd represents the serve command
var serveCmd = &cobra.Command{
	Use:         "serve",
	Short:       "Start the web server",
	Long:        `Start the HTTP server that serves the API endpoints and SSE streams.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        executeServe,
}

func init() {
	webCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(webCmd)

	defaultPort := "8080"
	if envPort := os.Getenv("PORT"); envPort != "" {
		defaultPort = envPort
	}
	serveCmd.Flags().StringVarP(&port, "port", "p", defaultPort, "Port to run the server on")
}

// WebServer holds the HTTP server and dependencies
type WebServer struct {
	storer *storage.PostgresStorer
	router *chi.Mux
}

// NewWebServer creates a new web server instance
func NewWebServer(storer *storage.PostgresStorer) *WebServer {
	r := chi.NewRouter()

	// Parse rate limit from env (default: 100 req/min per IP)
	rateLimit := 100
	if v := os.Getenv("RATE_LIMIT_PER_MINUTE"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			rateLimit = parsed
		}
	}

	// Middleware — applied in order:
	// securityHeaders → Logger → Recoverer → Timeout → RateLimit → CORS → metrics
	r.Use(securityHeaders)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(httprate.LimitByIP(rateLimit, 1*time.Minute))
	r.Use(cors.Handler(cors.Options{
		AllowOriginFunc:  corsOriginFunc(),
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	r.Use(metrics.Middleware)

	server := &WebServer{
		storer: storer,
		router: r,
	}

	server.setupRoutes()
	return server
}

// corsOriginFunc returns a CORS origin checker based on the ALLOWED_ORIGINS env var.
// If ALLOWED_ORIGINS is set (comma-separated), only those origins are permitted.
// Otherwise, localhost/127.0.0.1 origins are allowed for development.
func corsOriginFunc() func(r *http.Request, origin string) bool {
	if env := os.Getenv("ALLOWED_ORIGINS"); env != "" {
		allowed := make(map[string]struct{})
		for _, o := range strings.Split(env, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				allowed[o] = struct{}{}
			}
		}
		return func(r *http.Request, origin string) bool {
			_, ok := allowed[origin]
			return ok
		}
	}
	return func(r *http.Request, origin string) bool {
		return strings.HasPrefix(origin, "http://localhost:") ||
			strings.HasPrefix(origin, "http://127.0.0.1:") ||
			strings.HasPrefix(origin, "https://localhost:") ||
			strings.HasPrefix(origin, "https://127.0.0.1:")
	}
}

// httpInternalError logs err server-side and returns a generic 500 to the client,
// preventing internal details (e.g. DB connection strings) from leaking.
func httpInternalError(w http.ResponseWriter, err error, msg string) {
	log.Error().Err(err).Msg(msg)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}

// securityHeaders adds standard security headers to all responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data: https:; connect-src 'self'")

		// HSTS only when behind HTTPS (reverse proxy sets X-Forwarded-Proto)
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

// setupRoutes configures all the API routes
func (ws *WebServer) setupRoutes() {
	// Health check endpoint (kept on main API for external LB probes)
	ws.router.Get("/health", ws.handleHealth)

	// API routes
	ws.router.Route("/api", func(r chi.Router) {
		r.Get("/persons", ws.handleGetPersons)
		r.Get("/person/{id}", ws.handleGetPerson)
		r.Get("/person/{id}/facts", ws.handleGetPersonFacts)
		r.Get("/person/{id}/contradictions", ws.handleGetPersonContradictions)
		r.Get("/person/{id}/members", ws.handleGetPersonMembers)
		r.Get("/facts/{id}/persons", ws.handleGetFactPersons)
		r.Get("/person/{id}/stances", ws.handleGetPersonStances)
		r.Get("/stances/{id}/contradictions", ws.handleGetStanceContradictions)
		r.Get("/stances/{id}/facts", ws.handleGetStanceFacts)
		r.Get("/events", ws.handleEventsSSE)
	})
}

// handleHealth checks database connectivity and returns server health status.
func (ws *WebServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")

	if err := ws.storer.Pool().Ping(ctx); err != nil {
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

// publicMinFacts is the floor on fact_count for entities shown on the public
// site: only persons with MORE THAN 3 facts (>= 4) are surfaced, so one-off
// mentions and incidentally-created junk entities never appear.
const publicMinFacts = 4

// handleGetPersons returns persons ranked by fact count and honesty score
func (ws *WebServer) handleGetPersons(w http.ResponseWriter, r *http.Request) {
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

	persons, err := ws.storer.GetPersonsRankedByScore(ctx, limit, offset, entityType, search, sort, minFacts)
	if err != nil {
		httpInternalError(w, err, "failed to get persons")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	json.NewEncoder(w).Encode(persons)
}

// handleGetPerson returns basic person information
func (ws *WebServer) handleGetPerson(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personIDStr := chi.URLParam(r, "id")

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	person, err := ws.storer.GetPersonByID(ctx, personID)
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
func (ws *WebServer) handleGetPersonFacts(w http.ResponseWriter, r *http.Request) {
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

	result, err := ws.storer.GetPersonFacts(ctx, personID, limit, offset)
	if err != nil {
		httpInternalError(w, err, "failed to get facts")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleGetPersonContradictions returns contradictions for a specific person
func (ws *WebServer) handleGetPersonContradictions(w http.ResponseWriter, r *http.Request) {
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

	result, err := ws.storer.GetPersonContradictions(ctx, personID, limit, offset)
	if err != nil {
		httpInternalError(w, err, "failed to get contradictions")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// MemberInfo represents a person who is a member/affiliate of another entity
type MemberInfo struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	EntityType   string  `json:"entity_type"`
	HonestyScore float64 `json:"honesty_score"`
	ImageURL     *string `json:"image_url,omitempty"`
}

// handleGetPersonMembers returns persons affiliated with an entity (e.g., deputies in a party)
func (ws *WebServer) handleGetPersonMembers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personIDStr := chi.URLParam(r, "id")

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	// Query members (persons linked to this entity via person_links)
	query := `
		SELECT DISTINCT p.id, p.name, p.entity_type, p.honesty_score, p.image_url
		FROM persons p
		JOIN person_links pl ON p.id = pl.src_person_id
		WHERE pl.dst_person_id = $1
		ORDER BY p.name`

	rows, err := ws.storer.Pool().Query(ctx, query, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get members")
		return
	}
	defer rows.Close()

	members := make([]MemberInfo, 0)
	for rows.Next() {
		var m MemberInfo
		if err := rows.Scan(&m.ID, &m.Name, &m.EntityType, &m.HonestyScore, &m.ImageURL); err != nil {
			httpInternalError(w, err, "failed to scan member")
			return
		}
		members = append(members, m)
	}

	if rows.Err() != nil {
		httpInternalError(w, rows.Err(), "error iterating members")
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

// handleGetStanceFacts returns all facts attached to a stance, with both native and
// English text so the frontend language toggle can swap per-fact without a second round trip.
func (ws *WebServer) handleGetStanceFacts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stanceIDStr := chi.URLParam(r, "id")
	stanceID, err := strconv.ParseInt(stanceIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid stance ID", http.StatusBadRequest)
		return
	}
	items, err := ws.storer.GetFactsInStance(ctx, stanceID)
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
func (ws *WebServer) handleGetStanceContradictions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	stanceIDStr := chi.URLParam(r, "id")

	stanceID, err := strconv.ParseInt(stanceIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid stance ID", http.StatusBadRequest)
		return
	}

	items, err := ws.storer.GetStanceContradictionGroups(ctx, stanceID)
	if err != nil {
		httpInternalError(w, err, "failed to get stance contradiction groups")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(items)
}

// handleGetPersonStances returns stance documents for a specific person, with a per-stance
// honesty score and contradiction count attached.
func (ws *WebServer) handleGetPersonStances(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	personIDStr := chi.URLParam(r, "id")

	personID, err := strconv.ParseInt(personIDStr, 10, 64)
	if err != nil {
		http.Error(w, "Invalid person ID", http.StatusBadRequest)
		return
	}

	docs, err := ws.storer.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get stances")
		return
	}

	// Two per-stance numbers: the bundle-based total drives the honesty score (kept
	// unchanged), while the group count (distinct base facts) is what the badge shows
	// so users see one count per distinct inconsistency, not the inflated pairwise total.
	counts, err := ws.storer.GetStanceContradictionCounts(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get stance contradiction counts")
		return
	}
	groupCounts, err := ws.storer.GetStanceGroupCounts(ctx, personID)
	if err != nil {
		httpInternalError(w, err, "failed to get stance group counts")
		return
	}

	result := make([]StanceResponse, len(docs))
	for i, d := range docs {
		total := counts[d.ID]
		bundles := 0
		if total > 0 {
			bundles = 1
		}
		score := storage.ComputeHonestyScore(d.FactCount, storage.ContradictionStats{Bundles: bundles, Total: total})
		result[i] = StanceResponse{
			ID:                 d.ID,
			Label:              d.ClusterLabel,
			LabelEn:            d.LabelEn,
			Summary:            d.Summary,
			SummaryEn:          d.SummaryEn,
			FactCount:          d.FactCount,
			ContradictionCount: groupCounts[d.ID],
			Score:              score,
			UpdatedAt:          d.LastUpdatedAt,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleGetFactPersons returns persons who have a fact related to the given fact
func (ws *WebServer) handleGetFactPersons(w http.ResponseWriter, r *http.Request) {
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

	persons, err := ws.storer.GetFactPersons(ctx, factID, limit, offset)
	if err != nil {
		httpInternalError(w, err, "failed to get persons for fact")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(persons)
}

// handleEventsSSE streams events via Server-Sent Events
func (ws *WebServer) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Track active SSE connections
	metrics.IncrementSSEConnections()
	defer metrics.DecrementSSEConnections()

	// Parse since_id parameter
	sinceIDStr := r.URL.Query().Get("since_id")
	sinceID := int64(0)
	if sinceIDStr != "" {
		// Client reconnecting — replay from their last seen ID
		if parsed, err := strconv.ParseInt(sinceIDStr, 10, 64); err == nil && parsed >= 0 {
			sinceID = parsed
		}
	} else {
		// Fresh connection — start from latest, only show new events
		row := ws.storer.Pool().QueryRow(ctx, "SELECT COALESCE(MAX(id), 0) FROM event_log")
		if err := row.Scan(&sinceID); err != nil {
			log.Error().Err(err).Msg("Failed to query max event ID, starting from 0")
			sinceID = 0
		}
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Ensure we can flush
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Send initial connection message
	fmt.Fprintf(w, "data: %s\n\n", `{"type": "connected", "since_id": `+strconv.FormatInt(sinceID, 10)+`}`)
	flusher.Flush()

	log.Info().Int64("since_id", sinceID).Msg("SSE connection established")

	// Stream events in a loop
	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("SSE connection closed by client")
			return
		default:
			// Query for new events since the last ID
			events, err := ws.getNewEvents(ctx, sinceID)
			if err != nil {
				log.Error().Err(err).Msg("Error querying events")
				time.Sleep(5 * time.Second) // Back off on error
				continue
			}

			// Send each new event
			for _, event := range events {
				eventData := map[string]interface{}{
					"id":          event.ID,
					"topic":       event.Topic,
					"entity_type": event.EntityType,
					"entity_id":   event.EntityID,
					"payload":     event.Payload,
					"created_at":  event.CreatedAt,
				}

				eventJSON, err := json.Marshal(eventData)
				if err != nil {
					log.Error().Err(err).Msg("Error marshaling event")
					continue
				}

				fmt.Fprintf(w, "data: %s\n\n", eventJSON)
				flusher.Flush()

				sinceID = event.ID // Update the last seen ID
			}

			// Wait before polling again (avoid busy loop)
			time.Sleep(250 * time.Millisecond)
		}
	}
}

// Event represents an event from the event_log table
type Event struct {
	ID         int64           `json:"id"`
	Topic      string          `json:"topic"`
	EntityType string          `json:"entity_type"`
	EntityID   int64           `json:"entity_id"`
	Payload    json.RawMessage `json:"payload"`
	CreatedAt  time.Time       `json:"created_at"`
}

// getNewEvents fetches events newer than the given ID
func (ws *WebServer) getNewEvents(ctx context.Context, sinceID int64) ([]Event, error) {
	query := `
		SELECT id, topic, entity_type, entity_id, payload, created_at
		FROM event_log
		WHERE id > $1
		ORDER BY id ASC
		LIMIT 100` // Limit to prevent overwhelming the client

	rows, err := ws.storer.Pool().Query(ctx, query, sinceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		err := rows.Scan(&event.ID, &event.Topic, &event.EntityType, &event.EntityID, &event.Payload, &event.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}
		events = append(events, event)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating events: %w", rows.Err())
	}

	return events, nil
}

// Start starts the HTTP server
func (ws *WebServer) Start(addr string) error {
	log.Info().Str("addr", addr).Msg("Starting web server")
	return http.ListenAndServe(addr, ws.router)
}

// executeServe is the main function for the serve command
func executeServe(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg("No .env file found, using environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	server := NewWebServer(storer)

	// Start internal metrics server on a separate port
	metricsPort := "9090"
	if v := os.Getenv("METRICS_PORT"); v != "" {
		metricsPort = v
	}

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	metricsMux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := storer.Pool().Ping(r.Context()); err != nil {
			log.Error().Err(err).Msg("metrics health check: db ping failed")
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		})
	})

	go func() {
		// Bind the IPv6 wildcard (dual-stack) rather than loopback: the
		// observability service's Prometheus scrapes us at
		// mirror.host.internal:9090, and the host's private network is
		// IPv6-only (the *.host.internal DNS returns AAAA records). A
		// 127.0.0.1 bind is unreachable cross-service. The port has no public
		// domain, so it stays private to the host project network.
		metricsAddr := "[::]:" + metricsPort
		log.Info().Str("port", metricsPort).Msg("Starting internal metrics server")
		if err := http.ListenAndServe(metricsAddr, metricsMux); err != nil {
			log.Error().Err(err).Msg("Metrics server failed")
		}
	}()

	// Start balance alert poller if Telegram is configured
	if alerterInstance := buildAlerter(); alerterInstance != nil {
		interval := getBalanceCheckInterval()
		go alerting.StartBalancePoller(ctx, alerterInstance, interval)
		llm.OnCreditError = func(provider, errMsg string) {
			alerterInstance.AlertCreditError(context.Background(), provider, errMsg)
		}
		log.Info().Msg("Balance alerting enabled (Telegram + poller)")
	}

	// Daily pipeline runs as a separate the host cron service (`dm pipeline daily`),
	// not in-process. See CLAUDE.md → Deployment.

	// Background archiver: politely submits fact source URLs to the Wayback Machine
	// so snapshots survive paywalls/deletions. Decoupled from ingestion.
	go archiver.New(storer).Run(ctx, 1*time.Hour)

	addr := ":" + port
	log.Info().Str("port", port).Msg("Starting Digital Mirror web server")

	return server.Start(addr)
}
