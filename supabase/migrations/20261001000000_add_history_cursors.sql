-- +goose Up

-- history_cursors records, per plugin, how far back in time the nightly
-- history step has read its source: everything published from reached_at to
-- the present has been ingested for the main persons. Each run reads the next
-- month before reached_at and moves the cursor back once that month is done.
CREATE TABLE IF NOT EXISTS history_cursors (
    plugin     TEXT PRIMARY KEY,
    reached_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS history_cursors;
