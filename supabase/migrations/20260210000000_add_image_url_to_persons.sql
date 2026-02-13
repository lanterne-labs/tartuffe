-- +goose Up
ALTER TABLE persons ADD COLUMN IF NOT EXISTS image_url TEXT;

-- +goose Down
ALTER TABLE persons DROP COLUMN IF EXISTS image_url;
