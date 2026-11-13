-- +goose Up
-- The X bot proposes replies under the candidates' most seen tweets. X lets
-- an app reply only where it is mentioned, so the reply is posted by hand
-- from the Telegram draft: an x_posts row of kind 'reply' is never posted
-- through the API.
ALTER TABLE x_posts DROP CONSTRAINT IF EXISTS x_posts_kind_check;
ALTER TABLE x_posts ADD CONSTRAINT x_posts_kind_check CHECK (kind IN ('contradiction', 'vote', 'reply'));
ALTER TABLE x_posts ADD COLUMN IF NOT EXISTS reply_to_tweet_id TEXT;

-- Every tweet the bot considered answering, once: a tweet is never proposed
-- twice, and the LLM is not asked twice about a tweet nothing fits.
CREATE TABLE IF NOT EXISTS x_reply_targets (
    tweet_id TEXT PRIMARY KEY,
    person_id BIGINT NOT NULL,
    author TEXT NOT NULL,
    text TEXT NOT NULL,
    url TEXT NOT NULL DEFAULT '',
    views INT NOT NULL DEFAULT 0,
    tweeted_at TIMESTAMPTZ,
    outcome TEXT NOT NULL CHECK (outcome IN ('drafted', 'no_match')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS x_reply_targets;
DELETE FROM x_posts WHERE kind = 'reply';
ALTER TABLE x_posts DROP COLUMN IF EXISTS reply_to_tweet_id;
ALTER TABLE x_posts DROP CONSTRAINT IF EXISTS x_posts_kind_check;
ALTER TABLE x_posts ADD CONSTRAINT x_posts_kind_check CHECK (kind IN ('contradiction', 'vote'));
