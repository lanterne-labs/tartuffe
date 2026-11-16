-- +goose Up
-- The small copy of a person's photo (256 px, a few kilobytes), for the link
-- previews: the messaging apps drop an image that is too heavy, and the photo
-- itself is 1500 px (backend/plugins/photos/preview.go). NULL until it is
-- made: the preview then shows the photo.
ALTER TABLE persons ADD COLUMN IF NOT EXISTS image_preview_url TEXT;

-- +goose Down
ALTER TABLE persons DROP COLUMN IF EXISTS image_preview_url;
