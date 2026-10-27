package frpolitics

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"digital-mirror/backend/webfetch"
)

// httpClient is the client every frpolitics download goes through: the
// shared webfetch client (the common browser headers, the record of each
// request for the Sources page), with the per-attempt cap below. Tests swap
// in an *http.Client. A hung connection is bounded by the time to the
// response headers (30 s, in webfetch); the body is not, because the archives
// are tens of MB (the debates archive is 56 MB) and a total 30 s timeout
// killed the read whenever the portal served under ~2 MB/s (pipeline run 103,
// 2026-09-21). The 10 min cap is the ceiling for one attempt, not for a whole
// download: downloadResumable continues a cut transfer from its .part file in
// the next attempt.
var httpClient interface {
	Do(req *http.Request) (*http.Response, error)
} = anClient{}

const attemptTimeout = 10 * time.Minute

type anClient struct{}

func (anClient) Do(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(webfetch.WithSource(req.Context(), "assemblee"), attemptTimeout)
	resp, err := webfetch.Shared().Do(ctx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

// cancelOnClose ends the attempt's context when its body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// browserHeaders are applied to every frpolitics HTTP request on top of the
// common ones of webfetch: AN's open-data portal rate-limits Go's default
// request much more aggressively than a browser's, even though the data is
// public.
var browserHeaders = map[string]string{
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
