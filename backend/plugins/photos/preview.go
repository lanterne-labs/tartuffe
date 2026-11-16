package photos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	// The formats a photo is stored in.
	_ "image/gif"
	_ "image/png"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// PreviewSize is the side of the small copy of a photo, in pixels. A link
// preview draws the image as a thumbnail beside the text: WhatsApp draws an
// image of 300 px or more across the whole message, and drops one that is
// too heavy (the photos are 1500 px and about 300 kB).
const PreviewSize = 256

// MakePreview returns the small copy of a photo: a square JPEG of
// PreviewSize, cut from the middle of the width and the top of the height
// (a portrait has the face in its upper part), on white where the photo
// is transparent.
func MakePreview(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode the photo: %w", err)
	}
	b := src.Bounds()
	side := min(b.Dx(), b.Dy())
	if side == 0 {
		return nil, fmt.Errorf("the photo is empty")
	}
	crop := image.Rect(0, 0, side, side).Add(image.Pt(b.Min.X+(b.Dx()-side)/2, b.Min.Y+(b.Dy()-side)/5))
	dst := image.NewRGBA(image.Rect(0, 0, PreviewSize, PreviewSize))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, crop, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, fmt.Errorf("encode the preview: %w", err)
	}
	return out.Bytes(), nil
}

// storePreview makes the small copy of the person's photo, uploads it and
// records its address. Its key carries a hash of the copy: a new photo gets
// a new address, so no cache of a messaging app keeps the old one.
func (p *Provider) storePreview(ctx context.Context, person storage.PersonSummary, photo []byte) (string, error) {
	small, err := MakePreview(photo)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(small)
	key := fmt.Sprintf("photos/%d_%s_preview_%x.jpg", person.ID, normalizeName(person.Name), sum[:4])
	url, err := p.R2.Upload(ctx, key, small, "image/jpeg")
	if err != nil {
		return "", fmt.Errorf("R2 upload of the preview failed: %w", err)
	}
	if err := p.Storer.SetPersonPreviewURL(ctx, person.ID, url); err != nil {
		return "", fmt.Errorf("failed to store the preview URL: %w", err)
	}
	return url, nil
}

// PhotoWithoutPreview is a person whose photo has no small copy yet.
type PhotoWithoutPreview struct {
	Person   storage.PersonSummary
	ImageURL string
}

// BackfillPreviews makes the small copy of the photos stored before the
// copies existed (or whose copy failed). It returns how many it made; a
// photo that fails is logged and left for the next run.
func (p *Provider) BackfillPreviews(ctx context.Context, todo []PhotoWithoutPreview) (made int, failed int) {
	for _, t := range todo {
		if ctx.Err() != nil {
			return made, failed
		}
		data, _, err := downloadImage(ctx, p.HTTPClient, t.ImageURL)
		if err == nil {
			_, err = p.storePreview(ctx, t.Person, data)
		}
		if err != nil {
			failed++
			log.Error().Err(err).Str("person", t.Person.Name).Str("photo", t.ImageURL).Msg("no preview made of the photo")
			continue
		}
		made++
	}
	return made, failed
}
