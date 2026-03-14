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

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
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
		r.Get("/events", ws.handleEventsSSE)
	})
}

// handleHealth checks database connectivity and returns server health status.
func (ws *WebServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	w.Header().Set("Content-Type", "application/json")

	if err := ws.storer.Pool().Ping(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{
			"status": "error",
			"error":  err.Error(),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

// handleGetPersons returns persons ranked by fact count and honesty score
func (ws *WebServer) handleGetPersons(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse pagination parameters
	limitStr := r.URL.Query().Get("limit")
	offsetStr := r.URL.Query().Get("offset")
	entityType := r.URL.Query().Get("entity_type") // "party", "human", or empty for all
	search := r.URL.Query().Get("search")          // case-insensitive partial match on name

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

	persons, err := ws.storer.GetPersonsRankedByScore(ctx, limit, offset, entityType, search)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get persons: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
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
		http.Error(w, fmt.Sprintf("Failed to get person: %v", err), http.StatusInternalServerError)
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
		http.Error(w, fmt.Sprintf("Failed to get facts: %v", err), http.StatusInternalServerError)
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
		http.Error(w, fmt.Sprintf("Failed to get contradictions: %v", err), http.StatusInternalServerError)
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
		http.Error(w, fmt.Sprintf("Failed to get members: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	members := make([]MemberInfo, 0)
	for rows.Next() {
		var m MemberInfo
		if err := rows.Scan(&m.ID, &m.Name, &m.EntityType, &m.HonestyScore, &m.ImageURL); err != nil {
			http.Error(w, fmt.Sprintf("Failed to scan member: %v", err), http.StatusInternalServerError)
			return
		}
		members = append(members, m)
	}

	if rows.Err() != nil {
		http.Error(w, fmt.Sprintf("Error iterating members: %v", rows.Err()), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(members)
}

// StanceResponse is the API response for a stance document (no embedding or key_fact_ids).
type StanceResponse struct {
	ID        int64     `json:"id"`
	Label     string    `json:"label"`
	Summary   string    `json:"summary"`
	FactCount int       `json:"fact_count"`
	UpdatedAt time.Time `json:"updated_at"`
}

// handleGetPersonStances returns stance documents for a specific person.
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
		http.Error(w, fmt.Sprintf("Failed to get stances: %v", err), http.StatusInternalServerError)
		return
	}

	result := make([]StanceResponse, len(docs))
	for i, d := range docs {
		result[i] = StanceResponse{
			ID:        d.ID,
			Label:     d.ClusterLabel,
			Summary:   d.Summary,
			FactCount: d.FactCount,
			UpdatedAt: d.LastUpdatedAt,
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
		http.Error(w, fmt.Sprintf("Failed to get persons for fact: %v", err), http.StatusInternalServerError)
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
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Cache-Control")

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
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{
				"status": "error",
				"error":  err.Error(),
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		})
	})

	go func() {
		metricsAddr := ":" + metricsPort
		log.Info().Str("port", metricsPort).Msg("Starting internal metrics server")
		if err := http.ListenAndServe(metricsAddr, metricsMux); err != nil {
			log.Error().Err(err).Msg("Metrics server failed")
		}
	}()

	// Start Prometheus remote write if configured (e.g. Grafana Cloud)
	if rwURL := os.Getenv("PROMETHEUS_REMOTE_WRITE_URL"); rwURL != "" {
		rwUser := os.Getenv("PROMETHEUS_REMOTE_WRITE_USER")
		rwPass := os.Getenv("PROMETHEUS_REMOTE_WRITE_PASSWORD")
		if rwUser == "" || rwPass == "" {
			return fmt.Errorf("PROMETHEUS_REMOTE_WRITE_URL is set but PROMETHEUS_REMOTE_WRITE_USER or PROMETHEUS_REMOTE_WRITE_PASSWORD is missing")
		}
		rwCfg := metrics.RemoteWriteConfig{
			URL:      rwURL,
			Username: rwUser,
			Password: rwPass,
			Interval: 15 * time.Second,
		}
		go metrics.StartRemoteWrite(ctx, rwCfg)
	}

	// Start batch poller if BATCH_PROVIDER is configured
	if providerName := os.Getenv("BATCH_PROVIDER"); providerName != "" {
		provider, err := newBatchProvider()
		if err != nil {
			log.Error().Err(err).Msg("Failed to create batch provider, batch poller disabled")
		} else {
			embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
			if err != nil {
				log.Error().Err(err).Msg("Failed to create embedder, batch poller disabled")
			} else {
				inserter := core.NewFactInserter(storer)
				adapter := newBatchStorerAdapter(storer)
				orchestrator := core.NewBatchJobOrchestrator(provider, adapter, embedder, inserter, categorizer.ParseFactJSON)
				go core.StartBatchPoller(ctx, orchestrator, 5*time.Minute)
			}
		}
	}

	addr := ":" + port
	log.Info().Str("port", port).Msg("Starting Digital Mirror web server")

	return server.Start(addr)
}
