-- +goose Up
-- The presidential consultation closes on the morning of the first round
-- (Sunday 18 April 2027, decided in the Conseil des ministres of 1 July 2026),
-- at 08:00 Paris (06:00 UTC) when the polling stations open: there is one
-- poll, the first round's, and voting ends before the real vote starts.
UPDATE poll_events SET closes_at = '2027-04-18 06:00:00+00' WHERE slug = 'presidentielle-2027';

-- +goose Down
UPDATE poll_events SET closes_at = '2027-04-18 18:00:00+00' WHERE slug = 'presidentielle-2027';
