-- +goose Up
CREATE TABLE IF NOT EXISTS rss_feeds (
    id BIGSERIAL PRIMARY KEY,
    url TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    person_id BIGINT REFERENCES persons(id),
    last_fetched_at TIMESTAMPTZ,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS rss_feeds;
