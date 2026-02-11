package photos

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode"

	"digital-mirror/backend/objectstore"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const (
	maxDownloadSize = 10 * 1024 * 1024 // 10MB
	maxFallbacks    = 5
)

// ImageSearcher is the interface for searching images (satisfied by SerpAPINewsProvider).
type ImageSearcher interface {
	SearchImages(query string, opts search.ImageSearchOptions) ([]search.ImageResult, error)
}

// Storer is the interface for database operations needed by the photos plugin.
type Storer interface {
	SetPersonImageURL(ctx context.Context, personID int64, url string) error
	GetPersonsWithoutImage(ctx context.Context) ([]storage.PersonSummary, error)
}

// Provider orchestrates image search, download, upload, and storage.
type Provider struct {
	ImageSearch ImageSearcher
	R2          objectstore.ObjectStore
	Storer      Storer
	HTTPClient  *http.Client
}

// SearchAndUpload finds a photo for a person, uploads to R2, and stores the URL.
func (p *Provider) SearchAndUpload(ctx context.Context, person storage.PersonSummary, opts Options) *Result {
	result := &Result{
		PersonID:   person.ID,
		PersonName: person.Name,
	}

	// Skip if already has photo and not forcing
	if person.ImageURL != nil && *person.ImageURL != "" && !opts.Force {
		result.Skipped = true
		result.SkipReason = "already has photo"
		result.ImageURL = *person.ImageURL
		return result
	}

	// Build search query
	query := buildSearchQuery(person)

	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}

	// Search for images
	images, err := p.ImageSearch.SearchImages(query, search.ImageSearchOptions{
		Limit: maxResults,
	})
	if err != nil {
		result.Error = fmt.Errorf("image search failed: %w", err)
		return result
	}

	result.SearchResults = len(images)

	if len(images) == 0 {
		result.Error = fmt.Errorf("no image results found for %q", person.Name)
		return result
	}

	// Score and sort candidates
	minWidth := opts.MinWidth
	if minWidth <= 0 {
		minWidth = 200
	}
	minHeight := opts.MinHeight
	if minHeight <= 0 {
		minHeight = 200
	}

	scored := scoreImages(images, minWidth, minHeight)

	if len(scored) == 0 {
		result.Error = fmt.Errorf("no images passed minimum quality filter (%dx%d) for %q", minWidth, minHeight, person.Name)
		return result
	}

	log.Info().
		Str("person", person.Name).
		Int("candidates", len(scored)).
		Float64("top_score", scored[0].Score).
		Str("top_source", scored[0].Source).
		Msg("scored image candidates")

	if opts.DryRun {
		result.Skipped = true
		result.SkipReason = "dry run"
		for i, s := range scored {
			log.Info().
				Int("rank", i+1).
				Float64("score", s.Score).
				Str("url", s.URL).
				Int("width", s.Width).
				Int("height", s.Height).
				Str("source", s.Source).
				Str("breakdown", s.ScoreBreakdown).
				Msg("candidate image")
		}
		return result
	}

	// Try downloading top candidates
	httpClient := p.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	limit := maxFallbacks
	if limit > len(scored) {
		limit = len(scored)
	}

	for i := 0; i < limit; i++ {
		candidate := scored[i]

		data, contentType, err := downloadImage(ctx, httpClient, candidate.URL)
		if err != nil {
			log.Warn().
				Err(err).
				Str("url", candidate.URL).
				Int("rank", i+1).
				Msg("failed to download candidate image, trying next")
			continue
		}

		result.Downloaded = true

		// Build R2 key
		ext := extensionFromContentType(contentType)
		key := fmt.Sprintf("photos/%d_%s%s", person.ID, normalizeName(person.Name), ext)

		publicURL, err := p.R2.Upload(ctx, key, data, contentType)
		if err != nil {
			result.Error = fmt.Errorf("R2 upload failed: %w", err)
			return result
		}

		result.Uploaded = true
		result.ImageURL = publicURL

		// Store in DB
		if err := p.Storer.SetPersonImageURL(ctx, person.ID, publicURL); err != nil {
			result.Error = fmt.Errorf("failed to store image URL: %w", err)
			return result
		}

		log.Info().
			Str("person", person.Name).
			Str("url", publicURL).
			Str("source", candidate.Source).
			Msg("photo uploaded and stored")

		return result
	}

	result.Error = fmt.Errorf("all %d download attempts failed for %q", limit, person.Name)
	return result
}

// buildSearchQuery constructs an appropriate search query for the person.
func buildSearchQuery(person storage.PersonSummary) string {
	entityType := ""
	if person.EntityType != nil {
		entityType = *person.EntityType
	}

	if entityType == "party" {
		return fmt.Sprintf(`"%s" parti politique logo`, person.Name)
	}
	return fmt.Sprintf(`"%s" official`, person.Name)
}

// blockedURLPatterns contains URL substrings from domains that consistently block
// direct image downloads (return HTML instead of images).
var blockedURLPatterns = []string{
	"lookaside.fbsbx.com",
	"lookaside.instagram.com",
	"scontent.fbcdn.net",
	"scontent.cdninstagram.com",
	"platform-lookaside.fbsbx.com",
}

// isBlockedURL returns true if the URL matches a known-bad pattern.
func isBlockedURL(url string) bool {
	lower := strings.ToLower(url)
	for _, pattern := range blockedURLPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// scoreImages scores image candidates and returns them sorted by score (highest first).
// Images below the minimum dimensions or from blocked domains are excluded.
func scoreImages(images []search.ImageResult, minWidth, minHeight int) []ScoredImage {
	var scored []ScoredImage

	for _, img := range images {
		// Filter out images below minimum size
		if img.OriginalWidth < minWidth || img.OriginalHeight < minHeight {
			continue
		}

		// Filter out URLs from domains that never serve images directly
		if isBlockedURL(img.Original) {
			continue
		}

		score, breakdown := computeScore(img)
		scored = append(scored, ScoredImage{
			URL:            img.Original,
			ThumbnailURL:   img.Thumbnail,
			Width:          img.OriginalWidth,
			Height:         img.OriginalHeight,
			Source:         img.Source,
			Title:          img.Title,
			Position:       img.Position,
			Score:          score,
			ScoreBreakdown: breakdown,
		})
	}

	// Sort by score descending
	for i := 0; i < len(scored); i++ {
		for j := i + 1; j < len(scored); j++ {
			if scored[j].Score > scored[i].Score {
				scored[i], scored[j] = scored[j], scored[i]
			}
		}
	}

	return scored
}

// computeScore calculates a quality score for an image.
// Max score: 100 (resolution 40 + aspect ratio 20 + domain 25 + position 15).
func computeScore(img search.ImageResult) (float64, string) {
	var total float64
	var parts []string

	// Resolution score (40 pts): larger is better, caps at 1000px
	resPx := math.Min(float64(img.OriginalWidth), float64(img.OriginalHeight))
	resScore := math.Min(resPx/1000.0, 1.0) * 40
	total += resScore
	parts = append(parts, fmt.Sprintf("res=%.1f", resScore))

	// Aspect ratio score (20 pts): closer to 1:1 (square) is better for portraits
	ratio := float64(img.OriginalWidth) / math.Max(float64(img.OriginalHeight), 1)
	// Ideal ratio range: 0.6 to 1.4 (portrait to slightly landscape)
	aspectScore := 0.0
	if ratio >= 0.6 && ratio <= 1.4 {
		// Perfect square (1.0) gets full points; edges get less
		deviation := math.Abs(ratio - 1.0)
		aspectScore = (1.0 - deviation/0.4) * 20
		if aspectScore < 0 {
			aspectScore = 0
		}
	}
	total += aspectScore
	parts = append(parts, fmt.Sprintf("aspect=%.1f", aspectScore))

	// Domain reputation score (25 pts)
	domainScore := scoreDomain(img.Source)
	total += domainScore
	parts = append(parts, fmt.Sprintf("domain=%.1f", domainScore))

	// Position score (15 pts): earlier results tend to be better
	posScore := math.Max(0, 15.0-float64(img.Position)*1.5)
	total += posScore
	parts = append(parts, fmt.Sprintf("pos=%.1f", posScore))

	return total, strings.Join(parts, " ")
}

// scoreDomain returns a reputation score for known image source domains.
func scoreDomain(source string) float64 {
	source = strings.ToLower(source)

	// Official / government / encyclopedia sources (25 pts)
	highTrust := []string{
		"wikipedia", "wikimedia", "assemblee-nationale", "gouvernement",
		"elysee", "senat.fr", "europa.eu", "nato.int",
	}
	for _, d := range highTrust {
		if strings.Contains(source, d) {
			return 25
		}
	}

	// Major news outlets (20 pts)
	mediumTrust := []string{
		"reuters", "afp", "gettyimages", "ap news", "apnews",
		"lemonde", "lefigaro", "liberation", "bfmtv", "france24",
		"bbc", "nytimes", "theguardian", "politico",
	}
	for _, d := range mediumTrust {
		if strings.Contains(source, d) {
			return 20
		}
	}

	// General media / social (10 pts)
	lowTrust := []string{
		"twitter", "facebook", "instagram", "pinterest",
		"flickr", "youtube",
	}
	for _, d := range lowTrust {
		if strings.Contains(source, d) {
			return 10
		}
	}

	// Unknown source: moderate score
	return 12
}

// downloadImage downloads an image from a URL and validates the response.
func downloadImage(ctx context.Context, client *http.Client, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "DigitalMirror/1.0 (photo-fetcher; contact@digital-mirror.app)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("not an image: content-type %q", contentType)
	}

	// Limit read to maxDownloadSize
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("read body: %w", err)
	}

	if len(data) > maxDownloadSize {
		return nil, "", fmt.Errorf("image too large (>%dMB)", maxDownloadSize/1024/1024)
	}

	return data, contentType, nil
}

// extensionFromContentType maps content types to file extensions.
func extensionFromContentType(contentType string) string {
	switch {
	case strings.Contains(contentType, "png"):
		return ".png"
	case strings.Contains(contentType, "gif"):
		return ".gif"
	case strings.Contains(contentType, "webp"):
		return ".webp"
	case strings.Contains(contentType, "svg"):
		return ".svg"
	default:
		return ".jpg"
	}
}

// normalizeName converts a name to a filesystem-safe slug.
func normalizeName(name string) string {
	// Remove diacritics
	t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	result, _, _ := transform.String(t, name)

	// Lowercase and replace non-alphanumeric with underscore
	var b strings.Builder
	for _, r := range strings.ToLower(result) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}

	// Clean up multiple underscores
	s := b.String()
	for strings.Contains(s, "__") {
		s = strings.ReplaceAll(s, "__", "_")
	}
	s = strings.Trim(s, "_")

	return path.Clean(s)
}
