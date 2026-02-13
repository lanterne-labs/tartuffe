-- +goose Up
CREATE TABLE IF NOT EXISTS twitter_handles (
    id BIGSERIAL PRIMARY KEY,
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    handle TEXT NOT NULL,
    last_fetched_at TIMESTAMPTZ,
    active BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE(person_id, handle)
);
CREATE INDEX IF NOT EXISTS idx_twitter_handles_person_id ON twitter_handles(person_id);

CREATE TABLE IF NOT EXISTS twitter_fetch_log (
    id BIGSERIAL PRIMARY KEY,
    handle_id BIGINT NOT NULL REFERENCES twitter_handles(id) ON DELETE CASCADE,
    date_from DATE NOT NULL,
    date_to DATE NOT NULL,
    tweets_fetched INT NOT NULL DEFAULT 0,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_twitter_fetch_log_handle_id ON twitter_fetch_log(handle_id);

-- +goose Down
DROP TABLE IF EXISTS twitter_fetch_log;
DROP TABLE IF EXISTS twitter_handles;
