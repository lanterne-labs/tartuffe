// Package archiver submits fact source URLs to archive.org in the background,
// keeping snapshots available if the original page disappears.
package archiver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// Storer is the subset of storage.PostgresStorer the archiver needs.
type Storer interface {
	GetSourcesWithoutArchiveURL(ctx context.Context, limit int) ([]storage.SourceToArchive, error)
	UpdateSourceArchiveURL(ctx context.Context, sourceID int64, archiveURL *string) error
}

const (
	batchSize       = 10
	requestDelay    = 2 * time.Second
	httpTimeout     = 30 * time.Second
	userAgent       = "digital-mirror/1.0 (fact-checking; https://github.com/lanterne-labs/mirror)"
	availabilityAPI = "https://archive.org/wayback/available?url="
	saveAPI         = "https://web.archive.org/save/"
)

// Archiver submits source URLs to the Wayback Machine and records the snapshot URLs.
type Archiver struct {
	storer Storer
	client *http.Client
}

// New creates an Archiver backed by storer.
func New(storer Storer) *Archiver {
	return &Archiver{
		storer: storer,
		client: &http.Client{Timeout: httpTimeout},
	}
}

// Run starts a background loop that archives a small batch of sources on each tick.
// It blocks until ctx is cancelled.
func (a *Archiver) Run(ctx context.Context, interval time.Duration) {
	log.Info().Dur("interval", interval).Msg("archiver: started")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.processBatch(ctx); err != nil && ctx.Err() == nil {
				log.Error().Err(err).Msg("archiver: batch failed")
			}
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

		// Always update archive_attempted_at, even on failure, to avoid retry storms.
		if updateErr := a.storer.UpdateSourceArchiveURL(ctx, src.ID, archiveURL); updateErr != nil {
			log.Error().Err(updateErr).Int64("source_id", src.ID).Msg("archiver: failed to persist archive URL")
		} else if archiveURL != nil {
			log.Info().Int64("source_id", src.ID).Str("archive_url", *archiveURL).Msg("archiver: archived")
		}

		// Polite delay between requests.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(requestDelay):
		}
	}

	return nil
}

// resolveArchiveURL checks whether the URL is already in the Wayback Machine and
// returns its snapshot URL. If no snapshot exists, it submits a save request and
// re-checks once. Returns nil (no error) when the save was submitted but the snapshot
// is not yet available — the caller will retry after 7 days.
func (a *Archiver) resolveArchiveURL(ctx context.Context, sourceURL string) (*string, error) {
	if u, ok := a.checkAvailable(ctx, sourceURL); ok {
		return &u, nil
	}

	// No existing snapshot — request one.
	if err := a.requestSave(ctx, sourceURL); err != nil {
		return nil, err
	}

	// Briefly re-check; save is often immediately available.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(3 * time.Second):
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

func (a *Archiver) checkAvailable(ctx context.Context, sourceURL string) (string, bool) {
	apiURL := availabilityAPI + url.QueryEscape(sourceURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", userAgent)

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

func (a *Archiver) requestSave(ctx context.Context, sourceURL string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, saveAPI+sourceURL, nil)
	if err != nil {
		return fmt.Errorf("build save request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("save request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return fmt.Errorf("save request: archive.org returned %d", resp.StatusCode)
	}
	return nil
}
