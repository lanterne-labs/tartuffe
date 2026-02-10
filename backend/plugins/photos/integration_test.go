package photos

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"digital-mirror/backend/objectstore"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockImageSearcher returns canned image search results.
type mockImageSearcher struct {
	results []search.ImageResult
	err     error
}

func (m *mockImageSearcher) SearchImages(query string, opts search.ImageSearchOptions) ([]search.ImageResult, error) {
	return m.results, m.err
}

// mockStorer records SetPersonImageURL calls.
type mockStorer struct {
	imageURLs map[int64]string
}

func newMockStorer() *mockStorer {
	return &mockStorer{imageURLs: make(map[int64]string)}
}

func (m *mockStorer) SetPersonImageURL(_ context.Context, personID int64, url string) error {
	m.imageURLs[personID] = url
	return nil
}

func (m *mockStorer) GetPersonsWithoutImage(_ context.Context) ([]storage.PersonSummary, error) {
	return nil, nil
}

// newFakeS3 creates an in-memory S3 server and returns an R2Client pointed at it,
// plus the httptest server (caller must close).
func newFakeS3(t *testing.T, bucket string) (*objectstore.R2Client, *httptest.Server) {
	t.Helper()

	backend := s3mem.New()
	faker := gofakes3.New(backend)
	ts := httptest.NewServer(faker.Server())

	// Create S3 client pointed at fake server
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(ts.URL),
		Region:       "us-east-1",
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("key", "secret", "")),
		UsePathStyle: true,
	})

	// Create the bucket
	_, err := client.CreateBucket(context.Background(), &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	require.NoError(t, err)

	r2 := objectstore.NewR2ClientWithS3(client, bucket, ts.URL)
	return r2, ts
}

// newFakeImageServer creates an HTTP server that serves a fake JPEG image at /photo.jpg.
func newFakeImageServer(t *testing.T) *httptest.Server {
	t.Helper()

	// Minimal JPEG: just enough bytes with the right content-type
	fakeJPEG := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 0x4A, 0x46, 0x49, 0x46}

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/photo.jpg" {
			w.Header().Set("Content-Type", "image/jpeg")
			w.Write(fakeJPEG)
		} else if r.URL.Path == "/notimage.txt" {
			w.Header().Set("Content-Type", "text/plain")
			w.Write([]byte("not an image"))
		} else {
			http.NotFound(w, r)
		}
	}))
}

func TestSearchAndUpload_FullFlow(t *testing.T) {
	bucket := "test-photos"
	r2, fakeS3 := newFakeS3(t, bucket)
	defer fakeS3.Close()

	imageServer := newFakeImageServer(t)
	defer imageServer.Close()

	human := "human"
	person := storage.PersonSummary{
		ID:         42,
		Name:       "Marine Le Pen",
		EntityType: &human,
	}

	searcher := &mockImageSearcher{
		results: []search.ImageResult{
			{
				Original:       imageServer.URL + "/photo.jpg",
				OriginalWidth:  800,
				OriginalHeight: 800,
				Source:         "wikipedia.org",
				Position:       1,
			},
		},
	}

	storer := newMockStorer()

	provider := &Provider{
		ImageSearch: searcher,
		R2:          r2,
		Storer:      storer,
	}

	result := provider.SearchAndUpload(context.Background(), person, Options{})

	require.NoError(t, result.Error)
	assert.True(t, result.Downloaded)
	assert.True(t, result.Uploaded)
	assert.Equal(t, 1, result.SearchResults)
	assert.Contains(t, result.ImageURL, "photos/42_marine_le_pen.jpg")

	// Verify the URL was stored in the mock storer
	storedURL, ok := storer.imageURLs[42]
	assert.True(t, ok)
	assert.Equal(t, result.ImageURL, storedURL)

	// Verify the object actually exists in fake S3
	getResp, err := s3.New(s3.Options{
		BaseEndpoint: aws.String(fakeS3.URL),
		Region:       "us-east-1",
		Credentials:  aws.NewCredentialsCache(credentials.NewStaticCredentialsProvider("key", "secret", "")),
		UsePathStyle: true,
	}).GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("photos/42_marine_le_pen.jpg"),
	})
	require.NoError(t, err)
	defer getResp.Body.Close()

	data, _ := io.ReadAll(getResp.Body)
	assert.Greater(t, len(data), 0)
	assert.Equal(t, "image/jpeg", *getResp.ContentType)
}

func TestSearchAndUpload_SkipsExistingPhoto(t *testing.T) {
	existingURL := "https://example.com/existing.jpg"
	person := storage.PersonSummary{
		ID:       1,
		Name:     "Test Person",
		ImageURL: &existingURL,
	}

	provider := &Provider{
		ImageSearch: &mockImageSearcher{},
		Storer:      newMockStorer(),
	}

	result := provider.SearchAndUpload(context.Background(), person, Options{})

	assert.True(t, result.Skipped)
	assert.Equal(t, "already has photo", result.SkipReason)
	assert.Equal(t, existingURL, result.ImageURL)
}

func TestSearchAndUpload_ForceOverridesExisting(t *testing.T) {
	bucket := "test-photos"
	r2, fakeS3 := newFakeS3(t, bucket)
	defer fakeS3.Close()

	imageServer := newFakeImageServer(t)
	defer imageServer.Close()

	existingURL := "https://example.com/old.jpg"
	person := storage.PersonSummary{
		ID:       7,
		Name:     "Test Person",
		ImageURL: &existingURL,
	}

	searcher := &mockImageSearcher{
		results: []search.ImageResult{
			{
				Original:       imageServer.URL + "/photo.jpg",
				OriginalWidth:  500,
				OriginalHeight: 500,
				Source:         "reuters.com",
				Position:       1,
			},
		},
	}

	storer := newMockStorer()
	provider := &Provider{
		ImageSearch: searcher,
		R2:          r2,
		Storer:      storer,
	}

	result := provider.SearchAndUpload(context.Background(), person, Options{Force: true})

	require.NoError(t, result.Error)
	assert.False(t, result.Skipped)
	assert.True(t, result.Uploaded)
	assert.Contains(t, result.ImageURL, "photos/7_test_person.jpg")
}

func TestSearchAndUpload_FallbackOnDownloadFailure(t *testing.T) {
	bucket := "test-photos"
	r2, fakeS3 := newFakeS3(t, bucket)
	defer fakeS3.Close()

	imageServer := newFakeImageServer(t)
	defer imageServer.Close()

	person := storage.PersonSummary{
		ID:   3,
		Name: "Fallback Test",
	}

	searcher := &mockImageSearcher{
		results: []search.ImageResult{
			// First candidate: broken URL (404)
			{
				Original:       imageServer.URL + "/nonexistent.jpg",
				OriginalWidth:  900,
				OriginalHeight: 900,
				Source:         "wikipedia.org",
				Position:       1,
			},
			// Second candidate: not an image
			{
				Original:       imageServer.URL + "/notimage.txt",
				OriginalWidth:  800,
				OriginalHeight: 800,
				Source:         "wikipedia.org",
				Position:       2,
			},
			// Third candidate: works
			{
				Original:       imageServer.URL + "/photo.jpg",
				OriginalWidth:  700,
				OriginalHeight: 700,
				Source:         "reuters.com",
				Position:       3,
			},
		},
	}

	storer := newMockStorer()
	provider := &Provider{
		ImageSearch: searcher,
		R2:          r2,
		Storer:      storer,
	}

	result := provider.SearchAndUpload(context.Background(), person, Options{})

	require.NoError(t, result.Error)
	assert.True(t, result.Downloaded)
	assert.True(t, result.Uploaded)
	assert.Contains(t, result.ImageURL, "photos/3_fallback_test.jpg")
}

func TestSearchAndUpload_NoResults(t *testing.T) {
	person := storage.PersonSummary{
		ID:   1,
		Name: "Unknown Person",
	}

	searcher := &mockImageSearcher{results: nil}
	provider := &Provider{
		ImageSearch: searcher,
		Storer:      newMockStorer(),
	}

	result := provider.SearchAndUpload(context.Background(), person, Options{})

	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "no image results found")
}

func TestSearchAndUpload_SearchError(t *testing.T) {
	person := storage.PersonSummary{
		ID:   1,
		Name: "Error Person",
	}

	searcher := &mockImageSearcher{err: fmt.Errorf("API limit exceeded")}
	provider := &Provider{
		ImageSearch: searcher,
		Storer:      newMockStorer(),
	}

	result := provider.SearchAndUpload(context.Background(), person, Options{})

	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "image search failed")
}

func TestSearchAndUpload_DryRun(t *testing.T) {
	person := storage.PersonSummary{
		ID:   1,
		Name: "Dry Run Person",
	}

	searcher := &mockImageSearcher{
		results: []search.ImageResult{
			{
				Original:       "http://example.com/photo.jpg",
				OriginalWidth:  800,
				OriginalHeight: 800,
				Source:         "wikipedia.org",
				Position:       1,
			},
		},
	}

	storer := newMockStorer()
	provider := &Provider{
		ImageSearch: searcher,
		Storer:      storer,
	}

	result := provider.SearchAndUpload(context.Background(), person, Options{DryRun: true})

	assert.True(t, result.Skipped)
	assert.Equal(t, "dry run", result.SkipReason)
	assert.False(t, result.Downloaded)
	assert.False(t, result.Uploaded)
	// Storer should NOT have been called
	assert.Empty(t, storer.imageURLs)
}

func TestSearchAndUpload_AllCandidatesFail(t *testing.T) {
	imageServer := newFakeImageServer(t)
	defer imageServer.Close()

	person := storage.PersonSummary{
		ID:   1,
		Name: "All Fail",
	}

	searcher := &mockImageSearcher{
		results: []search.ImageResult{
			{Original: imageServer.URL + "/nonexistent1.jpg", OriginalWidth: 800, OriginalHeight: 800, Source: "a.com", Position: 1},
			{Original: imageServer.URL + "/nonexistent2.jpg", OriginalWidth: 700, OriginalHeight: 700, Source: "b.com", Position: 2},
		},
	}

	provider := &Provider{
		ImageSearch: searcher,
		Storer:      newMockStorer(),
		R2:          nil, // Won't reach upload
	}

	result := provider.SearchAndUpload(context.Background(), person, Options{})

	assert.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "download attempts failed")
	assert.False(t, result.Downloaded)
}
