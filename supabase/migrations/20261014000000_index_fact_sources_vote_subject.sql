-- +goose Up

-- frpolitics finds, once per scrutin, the stance of each voter that holds a
-- ballot with the same vote subject (metadata vote_subject).
CREATE INDEX IF NOT EXISTS idx_fact_sources_vote_subject ON fact_sources ((metadata->>'vote_subject'))
WHERE source_type = 'assemblee_law_page';

-- +goose Down
DROP INDEX IF EXISTS idx_fact_sources_vote_subject;
