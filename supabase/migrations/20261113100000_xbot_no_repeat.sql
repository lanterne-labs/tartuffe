-- +goose Up
-- The X bot never proposes the same text twice: every text it sent to
-- Telegram is kept here (the first versions, each rewrite, the text typed by
-- the person who approves), whatever became of the draft. x_posts.variants
-- only holds the last versions of a draft.
CREATE TABLE IF NOT EXISTS x_post_texts (
    id BIGSERIAL PRIMARY KEY,
    post_id BIGINT NOT NULL REFERENCES x_posts(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (post_id, text)
);
INSERT INTO x_post_texts (post_id, text, created_at)
SELECT p.id, v.text, p.created_at FROM x_posts p, jsonb_array_elements_text(p.variants) AS v(text)
ON CONFLICT DO NOTHING;
INSERT INTO x_post_texts (post_id, text, created_at)
SELECT id, chosen, created_at FROM x_posts WHERE chosen IS NOT NULL
ON CONFLICT DO NOTHING;

-- The angle of a tweet on the vote (xbot.VoteAngles): the angles come in
-- turn, the least recently used first.
ALTER TABLE x_posts ADD COLUMN IF NOT EXISTS angle TEXT NOT NULL DEFAULT '';
-- The fact of a candidate a vote tweet starts from (their own words on the
-- vote, democracy, the constitution): a fact is used once.
ALTER TABLE x_posts ADD COLUMN IF NOT EXISTS fact_id BIGINT;

-- +goose Down
ALTER TABLE x_posts DROP COLUMN IF EXISTS fact_id;
ALTER TABLE x_posts DROP COLUMN IF EXISTS angle;
DROP TABLE IF EXISTS x_post_texts;
