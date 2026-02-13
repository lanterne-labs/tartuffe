-- +goose Up
ALTER TABLE fact_sources
ADD COLUMN last_visited_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP;

-- Optional: Uncomment the line below if you want to initialize last_visited_at for existing rows
-- so they become eligible for revisits sooner (e.g., set to a very old date).
-- UPDATE fact_sources SET last_visited_at = '2000-01-01 00:00:00+00' WHERE last_visited_at IS NULL; 