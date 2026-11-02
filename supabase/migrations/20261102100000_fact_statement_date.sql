-- +goose Up
-- The statement's own date, when the text gives it (a 2019 tweet quoted in
-- a 2025 article): the judges read it before the source's date
-- (fact_sources.published_at). The text often gives only a year or a month:
-- statement_date is then the first day of it, and the precision says so.
ALTER TABLE facts ADD COLUMN IF NOT EXISTS statement_date DATE;
ALTER TABLE facts ADD COLUMN IF NOT EXISTS statement_date_precision TEXT
    CHECK (statement_date_precision IN ('year', 'month', 'day'));
ALTER TABLE facts ADD COLUMN IF NOT EXISTS statement_date_checked_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE facts DROP COLUMN IF EXISTS statement_date_checked_at;
ALTER TABLE facts DROP COLUMN IF EXISTS statement_date_precision;
ALTER TABLE facts DROP COLUMN IF EXISTS statement_date;
