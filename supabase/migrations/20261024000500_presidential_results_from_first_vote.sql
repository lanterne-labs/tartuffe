-- +goose Up
-- The presidential pre-poll shows its results from the first vote: the
-- ballots are public in the log anyway, so hiding the shares below a
-- number of voters protects nothing. The page hides the count itself
-- below 1000 voters.
UPDATE poll_events SET min_voters = 1,
    description_fr = 'Classez jusqu’à 3 candidats. Deux façons de compter, et l’honnêteté de chacun.',
    description_en = 'Rank up to 3 candidates. Two ways to count, and each one’s honesty.'
WHERE slug = 'presidentielle-2027';

-- +goose Down
UPDATE poll_events SET min_voters = 50 WHERE slug = 'presidentielle-2027';
