package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"digital-mirror/backend/metrics"

	"github.com/rs/zerolog/log"
)

// sseEventBatch caps how many events one poll sends, to avoid overwhelming the client.
const sseEventBatch = 100

// handleEventsSSE streams events via Server-Sent Events
func (s *Server) handleEventsSSE(w http.ResponseWriter, r *http.Request) {
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
		maxID, err := s.storer.GetMaxEventID(ctx)
		if err != nil {
			log.Error().Err(err).Msg("Failed to query max event ID, starting from 0")
			maxID = 0
		}
		sinceID = maxID
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
			events, err := s.storer.GetEventsAfter(ctx, sinceID, sseEventBatch)
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
