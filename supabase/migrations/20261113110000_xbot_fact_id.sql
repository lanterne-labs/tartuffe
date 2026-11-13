-- +goose Up
-- 20261113100000 reached production in an earlier form, without
-- x_posts.fact_id: its version was recorded, so the final file never ran
-- there. Everything it adds is added again here, only where missing.
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
ALTER TABLE x_posts ADD COLUMN IF NOT EXISTS angle TEXT NOT NULL DEFAULT '';
ALTER TABLE x_posts ADD COLUMN IF NOT EXISTS fact_id BIGINT;

-- +goose Down
SELECT 1;
