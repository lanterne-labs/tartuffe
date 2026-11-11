-- +goose Up
-- The X bot also tweets about the vote (kind 'vote': no person, no
-- contradiction), and a tweet on a contradiction may quote the person's own
-- tweet (quote_tweet_id) when one of the two facts is that tweet.
ALTER TABLE x_posts ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'contradiction'
    CHECK (kind IN ('contradiction', 'vote'));
ALTER TABLE x_posts ADD COLUMN IF NOT EXISTS quote_tweet_id TEXT;
ALTER TABLE x_posts ALTER COLUMN person_id DROP NOT NULL;

-- +goose Down
DELETE FROM x_posts WHERE person_id IS NULL;
ALTER TABLE x_posts ALTER COLUMN person_id SET NOT NULL;
ALTER TABLE x_posts DROP COLUMN IF EXISTS quote_tweet_id;
ALTER TABLE x_posts DROP COLUMN IF EXISTS kind;
