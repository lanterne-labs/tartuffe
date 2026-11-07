package extractor

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubExtractor is a fake ContentExtractor for tests — records calls and
// returns the configured result or error.
type stubExtractor struct {
	name      string
	result    *ExtractedContent
	err       error
	callCount int
	asked     string
}

func (s *stubExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	s.callCount++
	s.asked = url
	if s.err != nil {
		return nil, s.err
	}
	return s.result, nil
}

func TestFailoverExtractor_PrimaryWins(t *testing.T) {
	primary := &stubExtractor{
		name:   "primary",
		result: &ExtractedContent{URL: "u", Content: "primary won"},
	}
	backup := &stubExtractor{
		name:   "backup",
		result: &ExtractedContent{URL: "u", Content: "backup won"},
	}

	failover, err := newFailoverExtractor(
		failoverEntry{Name: "primary", Extractor: primary},
		failoverEntry{Name: "backup", Extractor: backup},
	)
	require.NoError(t, err)

	out, err := failover.ExtractContent("https://example.com/x")
	require.NoError(t, err)
	assert.Equal(t, "primary won", out.Content)
	assert.Equal(t, 1, primary.callCount, "primary should have been called once")
	assert.Equal(t, 0, backup.callCount, "backup should not have been called")
}

func TestFailoverExtractor_FailsOverToBackup(t *testing.T) {
	primary := &stubExtractor{
		name: "primary",
		err:  errors.New("primary blew up"),
	}
	backup := &stubExtractor{
		name:   "backup",
		result: &ExtractedContent{URL: "u", Content: "rescued"},
	}

	failover, err := newFailoverExtractor(
		failoverEntry{Name: "primary", Extractor: primary},
		failoverEntry{Name: "backup", Extractor: backup},
	)
	require.NoError(t, err)

	out, err := failover.ExtractContent("https://example.com/x")
	require.NoError(t, err)
	assert.Equal(t, "rescued", out.Content)
	assert.Equal(t, 1, primary.callCount)
	assert.Equal(t, 1, backup.callCount)
}

// A page that redirected and then refused the primary is asked of the backup
// at the URL it was redirected to, so it is stored under the article's URL
// and not under the Google link.
func TestFailoverExtractor_FollowsTheRedirect(t *testing.T) {
	primary := &stubExtractor{err: &RedirectedError{URL: "https://www.lejdd.fr/a", Err: errors.New("HTTP 403")}}
	backup := &stubExtractor{result: &ExtractedContent{URL: "https://www.lejdd.fr/a", Content: "rescued"}}
	failover, err := newFailoverExtractor(
		failoverEntry{Name: "primary", Extractor: primary},
		failoverEntry{Name: "backup", Extractor: backup},
	)
	require.NoError(t, err)
	out, err := failover.ExtractContent("https://www.google.com/goto?url=CAES")
	require.NoError(t, err)
	assert.Equal(t, "https://www.lejdd.fr/a", backup.asked)
	assert.Equal(t, "https://www.lejdd.fr/a", out.URL)
}

func TestFailoverExtractor_AllFail(t *testing.T) {
	a := &stubExtractor{name: "a", err: errors.New("a-err")}
	b := &stubExtractor{name: "b", err: errors.New("b-err")}

	failover, err := newFailoverExtractor(
		failoverEntry{Name: "a", Extractor: a},
		failoverEntry{Name: "b", Extractor: b},
	)
	require.NoError(t, err)

	_, err = failover.ExtractContent("https://example.com/x")
	require.Error(t, err)
	// Error message includes both extractor names and both error texts so
	// callers can diagnose which fallbacks were tried.
	assert.Contains(t, err.Error(), "a: a-err")
	assert.Contains(t, err.Error(), "b: b-err")
	assert.Equal(t, 1, a.callCount)
	assert.Equal(t, 1, b.callCount)
}

// TestFailoverExtractor_SkipsNilEntries verifies that callers can pass an
// optional extractor (e.g. WebcrawlerAPI when its key isn't set) as nil and
// the failover transparently drops it without erroring.
func TestFailoverExtractor_SkipsNilEntries(t *testing.T) {
	local := &stubExtractor{
		name:   "local",
		result: &ExtractedContent{URL: "u", Content: "ok"},
	}

	failover, err := newFailoverExtractor(
		failoverEntry{Name: "local", Extractor: local},
		failoverEntry{Name: "webcrawler", Extractor: nil}, // unconfigured
	)
	require.NoError(t, err)

	out, err := failover.ExtractContent("https://example.com/x")
	require.NoError(t, err)
	assert.Equal(t, "ok", out.Content)
}

func TestFailoverExtractor_NoExtractorsIsError(t *testing.T) {
	_, err := newFailoverExtractor()
	require.Error(t, err)

	_, err = newFailoverExtractor(failoverEntry{Name: "n", Extractor: nil})
	require.Error(t, err, "all-nil entries should error since there's nothing to try")
}
