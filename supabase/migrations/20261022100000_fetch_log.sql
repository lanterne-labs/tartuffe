-- +goose Up
-- One row per request to a site we read (webfetch.Client.Do), or per note on
-- a page (empty, too thin, behind a paywall): what the Grafana Sources page
-- reads. source is the plugin ("news", "viepublique", "assemblee", "photos"),
-- domain the registrable domain of the host. Rows older than 90 days are
-- deleted by the recorder.
CREATE TABLE IF NOT EXISTS fetch_log (
    id BIGSERIAL PRIMARY KEY,
    at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    source TEXT NOT NULL,
    domain TEXT NOT NULL,
    host TEXT NOT NULL,
    status INT NOT NULL DEFAULT 0,
    outcome TEXT NOT NULL,
    note BOOLEAN NOT NULL DEFAULT FALSE,
    duration_ms INT NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_fetch_log_at ON fetch_log(at);
CREATE INDEX IF NOT EXISTS idx_fetch_log_source_domain_at ON fetch_log(source, domain, at);

-- +goose Down
DROP TABLE IF EXISTS fetch_log;
