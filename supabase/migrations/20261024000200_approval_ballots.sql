-- +goose Up
-- The ballot becomes an approval ballot: the voter approves every candidate
-- they find acceptable (choices, in increasing id order) and may mark one of
-- them as their favourite. max_choices caps the approvals (0: no cap).
ALTER TABLE anon_ballots ADD COLUMN IF NOT EXISTS favourite BIGINT;
ALTER TABLE poll_events DROP CONSTRAINT IF EXISTS poll_events_max_choices_check;
ALTER TABLE poll_events ADD CONSTRAINT poll_events_max_choices_check CHECK (max_choices BETWEEN 0 AND 100);
ALTER TABLE poll_events ALTER COLUMN max_choices SET DEFAULT 0;
UPDATE poll_events SET max_choices = 0,
    description_fr = 'Approuvez tous les candidats qui vous conviennent, choisissez votre favori, puis comparez avec l''honnêteté mesurée de chacun.',
    description_en = 'Approve every candidate you find acceptable, pick your favourite, then compare with each one''s measured honesty.'
WHERE slug = 'presidentielle-2027';

-- +goose Down
UPDATE poll_events SET max_choices = 3,
    description_fr = 'Classez jusqu''à 3 candidats, puis comparez la préférence des votants avec l''honnêteté mesurée de chacun.',
    description_en = 'Rank up to 3 candidates, then compare what voters prefer with how honest each candidate measures.'
WHERE slug = 'presidentielle-2027';
UPDATE poll_events SET max_choices = 3 WHERE max_choices = 0 OR max_choices > 10;
ALTER TABLE poll_events ALTER COLUMN max_choices SET DEFAULT 3;
ALTER TABLE poll_events DROP CONSTRAINT IF EXISTS poll_events_max_choices_check;
ALTER TABLE poll_events ADD CONSTRAINT poll_events_max_choices_check CHECK (max_choices BETWEEN 1 AND 10);
ALTER TABLE anon_ballots DROP COLUMN IF EXISTS favourite;
