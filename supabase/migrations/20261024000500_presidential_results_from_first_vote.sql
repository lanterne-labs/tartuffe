-- +goose Up
-- The presidential pre-poll shows its results from the first vote: the
-- ballots are public in the log anyway, so hiding the shares below a
-- number of voters protects nothing. The page hides the count itself
-- below 1000 voters.
UPDATE poll_events SET min_voters = 1,
    description_fr = 'Classez jusqu’à 3 candidats, puis comparez deux façons de compter les votes et le score d’honnêteté de chacun.',
    description_en = 'Rank up to 3 candidates, then compare two ways of counting the votes and each one’s honesty score.'
WHERE slug = 'presidentielle-2027';

-- +goose Down
UPDATE poll_events SET min_voters = 50 WHERE slug = 'presidentielle-2027';
