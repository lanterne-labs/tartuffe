-- +goose Up
-- When the exact day of a statement known to the year or the month was
-- searched on the web (backend/statementday): a fact searched once is not
-- searched again, whether a day was found (statement_date_precision is then
-- 'day') or not.
ALTER TABLE facts ADD COLUMN IF NOT EXISTS statement_day_searched_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE facts DROP COLUMN IF EXISTS statement_day_searched_at;
