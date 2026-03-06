-- +goose Up
ALTER TABLE persons ADD COLUMN IF NOT EXISTS image_url TEXT;
