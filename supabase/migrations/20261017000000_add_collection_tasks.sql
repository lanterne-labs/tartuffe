-- +goose Up
-- A collection task asks one plugin for the facts of one person over one
-- period. `dm collect add` creates them; `dm worker run` claims and runs them.
CREATE TABLE IF NOT EXISTS collection_tasks (
    id BIGSERIAL PRIMARY KEY,
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    plugin TEXT NOT NULL,
    -- [since, until): whole days, UTC.
    since DATE NOT NULL,
    until DATE NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'running', 'done', 'failed', 'cancelled')),
    priority INT NOT NULL DEFAULT 0,
    attempts INT NOT NULL DEFAULT 0,
    -- A pending task is not claimed before this time (the retry backoff).
    not_before TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT,
    requested_by TEXT,
    items INT NOT NULL DEFAULT 0,
    facts INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    -- The worker that runs the task renews it; a running task without a
    -- recent heartbeat (a deploy stopped its worker) is claimed again.
    heartbeat_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    CHECK (since < until)
);

-- One open task per person, plugin and period.
CREATE UNIQUE INDEX IF NOT EXISTS idx_collection_tasks_open
    ON collection_tasks (person_id, plugin, since, until)
    WHERE status IN ('pending', 'running');

CREATE INDEX IF NOT EXISTS idx_collection_tasks_claim
    ON collection_tasks (priority DESC, created_at)
    WHERE status IN ('pending', 'running');

-- +goose Down
DROP TABLE IF EXISTS collection_tasks;
