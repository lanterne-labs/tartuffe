-- +goose Up
-- The time a tweet goes out: its slot moved by a random offset, so the bot's
-- hours are not predictable. NULL (the rows before): the slot itself.
ALTER TABLE x_posts ADD COLUMN IF NOT EXISTS post_at timestamptz;

-- +goose Down
ALTER TABLE x_posts DROP COLUMN IF EXISTS post_at;
