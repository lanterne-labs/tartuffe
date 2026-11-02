-- +goose Up
-- The +1 of signed-in users on the facts and contradictions of the feed: one
-- per user and target. The feed's "hot" order ranks by them (storage/feed.go).
-- A target is a fact or a contradiction: no foreign key, the feed joins from
-- the targets, so the likes of a removed target are never read.
CREATE TABLE IF NOT EXISTS feed_likes (
    user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('fact', 'contradiction')),
    target_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, target_kind, target_id)
);

CREATE INDEX IF NOT EXISTS feed_likes_target_idx ON feed_likes (target_kind, target_id);

-- +goose Down
DROP TABLE IF EXISTS feed_likes;
