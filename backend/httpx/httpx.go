// Package httpx holds the JSON answers shared by the HTTP parts of the site
// (mirror, accounts, polls), so each part writes its replies the same way.
package httpx

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/rs/zerolog/log"
)

// WriteJSON writes v as the JSON body with the status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("failed to encode JSON response")
	}
}

// WriteError answers a client error with {"error": msg}.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// CodedError is a refusal with a code the page reads.
type CodedError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// InternalError logs err server-side and returns a generic 500 to the client,
// so internal details (a connection string, a query) never leak.
func InternalError(w http.ResponseWriter, err error, msg string) {
	log.Error().Err(err).Msg(msg)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}

// QueryInt reads a positive integer query parameter: def when absent or
// invalid, at most maxV.
func QueryInt(r *http.Request, name string, def, maxV int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil && v > 0 {
		return min(v, maxV)
	}
	return def
}
