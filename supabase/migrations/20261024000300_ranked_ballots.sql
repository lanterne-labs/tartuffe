-- +goose Up
-- The ballot ranks up to max_choices candidates again (choices, from the
-- preferred one), without a favourite: the results order the candidates by
-- the Condorcet method of Schulze and by first choices.
ALTER TABLE anon_ballots DROP COLUMN IF EXISTS favourite;
UPDATE poll_events SET max_choices = 3 WHERE max_choices = 0 OR max_choices > 10;
ALTER TABLE poll_events ALTER COLUMN max_choices SET DEFAULT 3;
ALTER TABLE poll_events DROP CONSTRAINT IF EXISTS poll_events_max_choices_check;
ALTER TABLE poll_events ADD CONSTRAINT poll_events_max_choices_check CHECK (max_choices BETWEEN 1 AND 10);
UPDATE poll_events SET
    description_fr = 'Classez jusqu''à 3 candidats, du préféré au moins préféré, puis comparez deux façons de compter les mêmes votes, et l''honnêteté mesurée de chacun.',
    description_en = 'Rank up to 3 candidates, from your favourite down, then compare two ways of counting the same ballots, and each one''s measured honesty.'
WHERE slug = 'presidentielle-2027';

-- +goose Down
ALTER TABLE anon_ballots ADD COLUMN IF NOT EXISTS favourite BIGINT;
ALTER TABLE poll_events DROP CONSTRAINT IF EXISTS poll_events_max_choices_check;
ALTER TABLE poll_events ADD CONSTRAINT poll_events_max_choices_check CHECK (max_choices BETWEEN 0 AND 100);
ALTER TABLE poll_events ALTER COLUMN max_choices SET DEFAULT 0;
UPDATE poll_events SET max_choices = 0,
    description_fr = 'Approuvez tous les candidats qui vous conviennent, choisissez votre favori, puis comparez avec l''honnêteté mesurée de chacun.',
    description_en = 'Approve every candidate you find acceptable, pick your favourite, then compare with each one''s measured honesty.'
WHERE slug = 'presidentielle-2027';
