package photos

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tallPhoto is a portrait 600 × 900: red in its upper square, blue below.
func tallPhoto(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 600, 900))
	for y := 0; y < 900; y++ {
		c := color.RGBA{R: 220, A: 255}
		if y >= 660 {
			c = color.RGBA{B: 220, A: 255}
		}
		for x := 0; x < 600; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestMakePreview(t *testing.T) {
	small, err := MakePreview(tallPhoto(t))
	require.NoError(t, err)
	img, err := jpeg.Decode(bytes.NewReader(small))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, PreviewSize, PreviewSize), img.Bounds(), "a square of the preview size")
	assert.Less(t, len(small), 60_000, "light enough for every messaging app")
	// The square is cut from the upper part: 60 px from the top, so the
	// blue band (from 660) is the last line only at most.
	r, _, b, _ := img.At(PreviewSize/2, PreviewSize-20).RGBA()
	assert.Greater(t, r, b, "the cut keeps the upper part of a portrait")

	_, err = MakePreview([]byte("not an image"))
	assert.Error(t, err)
}

type memObjects struct{ keys map[string]string }

func (m *memObjects) Upload(_ context.Context, key string, _ []byte, contentType string) (string, error) {
	m.keys[key] = contentType
	return "https://cdn.test/" + key, nil
}
func (m *memObjects) Delete(context.Context, string) error { return nil }

func TestStoreUploadsThePreviewWithThePhoto(t *testing.T) {
	objects, db := &memObjects{keys: map[string]string{}}, newMockStorer()
	p := &Provider{R2: objects, Storer: db}
	person := storage.PersonSummary{ID: 7, Name: "Jean Test"}
	result := &Result{}
	require.NoError(t, p.store(context.Background(), person, "photos/7_jean_test.png", tallPhoto(t), "image/png", result))
	assert.Equal(t, "https://cdn.test/photos/7_jean_test.png", db.imageURLs[7])
	require.Contains(t, db.previewURLs, int64(7))
	assert.Regexp(t, `^https://cdn\.test/photos/7_jean_test_preview_[0-9a-f]{8}\.jpg$`, db.previewURLs[7])
	assert.Len(t, objects.keys, 2)

	// A photo no preview can be made of is still stored.
	db2 := newMockStorer()
	p2 := &Provider{R2: objects, Storer: db2}
	require.NoError(t, p2.store(context.Background(), person, "photos/7_x.svg", []byte("<svg/>"), "image/svg+xml", &Result{}))
	assert.NotEmpty(t, db2.imageURLs[7])
	assert.Empty(t, db2.previewURLs)
}
