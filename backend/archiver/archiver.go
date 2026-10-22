// Package archiver submits fact source URLs to the Wayback Machine in the
// background, keeping snapshots available if the original page later disappears
// (paywalls, deleted articles, site redesigns). It runs decoupled from the main
// ingestion pipeline so archiving latency never blocks fact extraction.
package archiver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"digital-mirror/backend/storage"
	"digital-mirror/backend/webfetch"

	"github.com/rs/zerolog/log"
)

// Storer is the subset of storage.PostgresStorer the archiver needs.
type Storer interface {
	GetSourcesWithoutArchiveURL(ctx context.Context, limit int) ([]storage.SourceToArchive, error)
	UpdateSourceArchiveURL(ctx context.Context, sourceID int64, archiveURL *string) error
}

const (
	batchSize       = 10
	httpTimeout     = 30 * time.Second
	availabilityAPI = "https://archive.org/wayback/available?url="
	saveAPI         = "https://web.archive.org/save/"
)

// Archiver submits source URLs to the Wayback Machine and records the snapshot URLs.
type Archiver struct {
	storer Storer
	// client is the shared webfetch client (tests swap in an *http.Client).
	client interface {
		Do(req *http.Request) (*http.Response, error)
	}
	// requestDelay is the polite gap between archive.org requests; recheckDelay is
	// how long we wait after a save before re-checking availability. Fields (not
	// constants) so tests can shrink them.
	requestDelay time.Duration
	recheckDelay time.Duration
}

// New creates an Archiver backed by storer.
func New(storer Storer) *Archiver {
	return &Archiver{
		storer:       storer,
		client:       archiveClient{},
		requestDelay: 2 * time.Second,
		recheckDelay: 3 * time.Second,
	}
}

// archiveClient sends a request through the shared webfetch client, under
// httpTimeout: the common headers, the record for the Sources page.
type archiveClient struct{}

func (archiveClient) Do(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(webfetch.WithSource(req.Context(), "archive"), httpTimeout)
	resp, err := webfetch.Shared().Do(ctx, req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{resp.Body, cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// Run starts a background loop that archives a small batch of sources on each tick.
// It runs one batch immediately, then every interval, and blocks until ctx is cancelled.
func (a *Archiver) Run(ctx context.Context, interval time.Duration) {
	log.Info().Dur("interval", interval).Msg("archiver: started")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := a.processBatch(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("archiver: batch failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Archiver) processBatch(ctx context.Context) error {
	sources, err := a.storer.GetSourcesWithoutArchiveURL(ctx, batchSize)
	if err != nil {
		return fmt.Errorf("get sources: %w", err)
	}

	for _, src := range sources {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		archiveURL, err := a.resolveArchiveURL(ctx, src.SourceURL)
		if err != nil {
			log.Warn().Err(err).Str("url", src.SourceURL).Msg("archiver: could not archive URL")
		}

		// Always stamp archive_attempted_at, even on failure, to avoid retry storms.
		if updateErr := a.storer.UpdateSourceArchiveURL(ctx, src.ID, archiveURL); updateErr != nil {
			log.Error().Err(updateErr).Int64("source_id", src.ID).Msg("archiver: failed to persist archive URL")
		} else if archiveURL != nil {
			log.Info().Int64("source_id", src.ID).Str("archive_url", *archiveURL).Msg("archiver: archived")
		}

		// Polite delay between requests.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(a.requestDelay):
		}
	}

	return nil
}

// resolveArchiveURL checks whether the URL is already in the Wayback Machine and
// returns its snapshot URL. If no snapshot exists, it submits a save request and
// re-checks once. Returns nil (no error) when the save was submitted but the snapshot
// is not yet available — the caller stamps the attempt and retries on a later tick.
func (a *Archiver) resolveArchiveURL(ctx context.Context, sourceURL string) (*string, error) {
	if u, ok := a.checkAvailable(ctx, sourceURL); ok {
		return &u, nil
	}

	// No existing snapshot — politely request one.
	if err := a.requestSave(ctx, sourceURL); err != nil {
		return nil, err
	}

	// Briefly re-check; a fresh save is often immediately indexed.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(a.recheckDelay):
	}

	if u, ok := a.checkAvailable(ctx, sourceURL); ok {
		return &u, nil
	}

	// Save submitted but snapshot not yet indexed — record attempt, retry later.
	return nil, nil
}

type waybackAvailability struct {
	ArchivedSnapshots struct {
		Closest struct {
			Available bool   `json:"available"`
			URL       string `json:"url"`
		} `json:"closest"`
	} `json:"archived_snapshots"`
}

// checkAvailable queries the Wayback availability API and returns the closest
// existing snapshot URL, if any.
func (a *Archiver) checkAvailable(ctx context.Context, sourceURL string) (string, bool) {
	apiURL := availabilityAPI + url.QueryEscape(sourceURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", false
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	var avail waybackAvailability
	if err := json.NewDecoder(resp.Body).Decode(&avail); err != nil {
		return "", false
	}

	c := avail.ArchivedSnapshots.Closest
	if c.Available && c.URL != "" {
		return c.URL, true
	}
	return "", false
}

// requestSave asks the Wayback Machine to capture sourceURL now. We do not wait
// for the capture to finish; the next availability check picks up the snapshot.
func (a *Archiver) requestSave(ctx context.Context, sourceURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, saveAPI+sourceURL, nil)
	if err != nil {
		return fmt.Errorf("build save request: %w", err)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("save request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("save request: archive.org returned %d", resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		// 4xx (notably 429 rate-limit) is not retried as an error — the recheck still
		// runs and the attempt is stamped — but log it so throttling stays visible.
		log.Warn().Int("status", resp.StatusCode).Str("url", sourceURL).Msg("archiver: save request rejected (non-retryable)")
	}
	return nil
}
