-- +goose Up

-- Counters for the pipeline's judgement calls: for every gate and judge, how
-- often each outcome was chosen (a fact deduplicated or kept, a stance joined
-- or created, an entity rejected as out of scope, …). The counts live in the
-- database because the judgements happen in the cron process, which
-- Prometheus cannot scrape; the web service exports them from here.
-- Day-bucketed so a trend is visible and the table stays small.
CREATE TABLE IF NOT EXISTS decision_counters (
    stage   TEXT NOT NULL,
    outcome TEXT NOT NULL,
    day     DATE NOT NULL DEFAULT CURRENT_DATE,
    count   BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (stage, outcome, day)
);

CREATE INDEX IF NOT EXISTS idx_decision_counters_day ON decision_counters (day);

-- +goose Down
DROP TABLE IF EXISTS decision_counters;
