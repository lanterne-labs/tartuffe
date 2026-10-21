package viepublique

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"digital-mirror/backend/util"

	"github.com/rs/zerolog/log"
)

// The index of the "discours publics" collection, published by the DILA
// under the Etalab open licence v2 and refreshed daily: one entry per speech,
// interview, press conference or communiqué of the executive since 1959, with
// its speakers and dates but not its text (the text is on the page).
const (
	indexURL    = "https://echanges.dila.gouv.fr/OPENDATA/DISCOURS_PUBLICS/vp_discours.json"
	indexMaxAge = 12 * time.Hour
)

// Speech is one entry of the index.
type Speech struct {
	ID    string
	Title string
	// URL is the page on vie-publique.fr, without the tracking query.
	URL string
	// Type is the document type: Déclaration, Interview, Communiqué,
	// Conférence de presse, Tribune, Lettre.
	Type     string
	Media    string
	Spoken   time.Time
	Online   time.Time
	Speakers []Speaker
}

// Speaker is one intervenant of a speech: a member of the executive, or the
// journalist of an interview.
type Speaker struct {
	Name string
	Role string
}

// IsJournalist reports whether the speaker asks the questions.
func (s Speaker) IsJournalist() bool {
	return strings.Contains(strings.ToLower(s.Role), "journaliste")
}

type rawSpeech struct {
	ID           string `json:"id"`
	Titre        string `json:"titre"`
	URL          string `json:"url"`
	TypeDocument string `json:"type_document"`
	Media        string `json:"media"`
	Prononcia    string `json:"prononciation"`
	MiseEnLigne  string `json:"mise_en_ligne"`
	Intervenants []struct {
		Nom     string `json:"nom"`
		Qualite string `json:"qualite"`
	} `json:"intervenants"`
}

func (r rawSpeech) speech() Speech {
	s := Speech{ID: r.ID, Title: strings.TrimSpace(r.Titre), URL: canonicalURL(r.URL), Type: r.TypeDocument, Media: r.Media}
	s.Spoken, _ = time.Parse("2006-01-02", r.Prononcia)
	s.Online, _ = time.Parse("2006-01-02", r.MiseEnLigne)
	for _, i := range r.Intervenants {
		s.Speakers = append(s.Speakers, Speaker{Name: strings.TrimSpace(i.Nom), Role: strings.TrimSpace(i.Qualite)})
	}
	return s
}

// canonicalURL drops the query (the publisher's tracking parameters).
func canonicalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// inWindow reports whether the speech was given, or put online, in
// [since, until): most speeches go online days after they are given, so a
// task over yesterday also takes what went online yesterday.
func (s Speech) inWindow(since, until time.Time) bool {
	in := func(t time.Time) bool { return !t.IsZero() && !t.Before(since) && t.Before(until) }
	return in(s.Spoken) || in(s.Online)
}

// SpeechesOf reads the index and returns, per person name key, the speeches
// of the window in which that person speaks, other than as a journalist.
func SpeechesOf(r io.Reader, keys map[string]bool, since, until time.Time) (map[string][]Speech, error) {
	dec := json.NewDecoder(r)
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("reading the index: %w", err)
	}
	out := map[string][]Speech{}
	for dec.More() {
		var raw rawSpeech
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("reading the index: %w", err)
		}
		var hit []string
		for _, i := range raw.Intervenants {
			// A journalist asks the questions: none of the text is their own.
			if (Speaker{Role: i.Qualite}).IsJournalist() {
				continue
			}
			if k := util.PersonNameKey(i.Nom); keys[k] {
				hit = append(hit, k)
			}
		}
		if len(hit) == 0 {
			continue
		}
		s := raw.speech()
		if !s.inWindow(since, until) {
			continue
		}
		for _, k := range hit {
			out[k] = append(out[k], s)
		}
	}
	return out, nil
}

// indexMu serializes the download: the worker runs several tasks at once.
var indexMu sync.Mutex

// openIndex returns the index file, downloaded again when the copy on disk
// is older than indexMaxAge. A failed download falls back on the old copy.
func openIndex(ctx context.Context, client *http.Client) (*os.File, error) {
	indexMu.Lock()
	defer indexMu.Unlock()
	dir := cacheDir()
	path := filepath.Join(dir, "vp_discours.json")
	info, statErr := os.Stat(path)
	if statErr != nil || time.Since(info.ModTime()) > indexMaxAge {
		if err := download(ctx, client, indexURL, dir, path); err != nil {
			if statErr != nil {
				return nil, err
			}
			log.Warn().Err(err).Dur("age", time.Since(info.ModTime())).Msg("viepublique: index download failed, using the old copy")
		}
	}
	return os.Open(path)
}

func download(ctx context.Context, client *http.Client, src, dir, path string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("index cache dir: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading the index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading the index: HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(dir, "vp_discours-*.json")
	if err != nil {
		return fmt.Errorf("index cache file: %w", err)
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("downloading the index: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("index cache file: %w", err)
	}
	log.Info().Int64("bytes", n).Msg("viepublique: index downloaded")
	return nil
}

// cacheDir is VIEPUBLIQUE_CACHE_DIR, else a directory of the temp dir.
func cacheDir() string {
	if dir := os.Getenv("VIEPUBLIQUE_CACHE_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(os.TempDir(), "viepublique-cache")
}
