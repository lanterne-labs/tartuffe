-- +goose Up
-- The podium of each week (the three candidates with the lowest reliable
-- honesty score), as the site showed it: the maintenance pass writes the
-- current week's rows again at its end, so a week keeps the podium of its
-- last pass. The score and the facts are those of then; the name, the slug
-- and the photo are read from persons.
CREATE TABLE IF NOT EXISTS podium_snapshots (
    week DATE NOT NULL,
    rank SMALLINT NOT NULL CHECK (rank >= 1),
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    honesty_score DOUBLE PRECISION NOT NULL,
    fact_count INTEGER NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (week, rank)
);

-- +goose Down
DROP TABLE IF EXISTS podium_snapshots;
