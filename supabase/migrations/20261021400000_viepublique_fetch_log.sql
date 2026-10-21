-- +goose Up
-- The windows whose vie-publique speeches were all fetched for a person: a
-- collection task over a covered window is not fetched again.
CREATE TABLE IF NOT EXISTS viepublique_fetch_log (
    id BIGSERIAL PRIMARY KEY,
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    date_from DATE NOT NULL,
    date_to DATE NOT NULL,
    speeches INT NOT NULL DEFAULT 0,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_viepublique_fetch_log_person ON viepublique_fetch_log(person_id, date_from);

-- +goose Down
DROP TABLE IF EXISTS viepublique_fetch_log;
