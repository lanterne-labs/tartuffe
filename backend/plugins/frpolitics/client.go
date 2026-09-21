package frpolitics

import (
	"net/http"
	"strings"
	"time"
)

// httpClient is the client every frpolitics download goes through; callers
// call applyBrowserHeaders on the request before .Do() — AN's open-data
// portal rate-limits Go's default UA much more aggressively than a real
// browser fingerprint, even though the data is public. A hung connection is
// bounded by the time to the response headers (30 s); the body is not, because
// the archives are tens of MB (the debates archive is 56 MB) and a total
// 30 s timeout killed the read whenever the portal served under ~2 MB/s
// (pipeline run 103, 2026-09-21). The 10 min cap is the ceiling for a whole
// download.
var httpClient = &http.Client{
	Timeout:   10 * time.Minute,
	Transport: &http.Transport{ResponseHeaderTimeout: 30 * time.Second, Proxy: http.ProxyFromEnvironment},
}

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
