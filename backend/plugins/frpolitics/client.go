package frpolitics

import (
	"net/http"
	"strings"
	"time"
)

// httpClient is a lightweight HTTP client with sane timeout. All frpolitics
// HTTP requests should go through it, and callers should call
// applyBrowserHeaders on the request before .Do() — AN's open-data portal
// rate-limits Go's default UA much more aggressively than a real browser
// fingerprint, even though the data is public.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// browserHeaders is a Chrome-on-Linux fingerprint applied to every frpolitics
// HTTP request. The data we fetch is public; the UA swap exists only to avoid
// the Go-default-UA 429 path.
var browserHeaders = map[string]string{
	"User-Agent":      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Accept":          "application/json, text/plain, */*",
	"Accept-Language": "fr-FR,fr;q=0.9,en-US;q=0.8,en;q=0.7",
	"Accept-Encoding": "gzip, deflate",
	"Cache-Control":   "no-cache",
}

// applyBrowserHeaders sets the browser-like headers on an outgoing request.
func applyBrowserHeaders(req *http.Request) {
	for k, v := range browserHeaders {
		req.Header.Set(k, v)
	}
}

// scrutinIndex represents a minimal structure of a scrutins listing item
type scrutinIndex struct {
	// ID is a unique identifier for the scrutin (e.g., "SCRUTIN_1234")
	ID string `json:"id"`
	// URL is the direct URL to the scrutin JSON file
	URL string `json:"url"`
	// Legislature number
	Legislature int `json:"legislature"`
}

// OrganeInfo represents minimal mapping from organeRef to party name
type OrganeInfo struct {
	// OrganeRef is the Assemblée group reference id
	OrganeRef string
	// ShortName is a short label for the group/party (e.g., "LR")
	ShortName string
	// LongName is the full group/party name
	LongName string
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
