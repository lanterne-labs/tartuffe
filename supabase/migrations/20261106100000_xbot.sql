-- +goose Up
-- The X bot (backend/xbot): the accounts a tweet may tag, and the tweets it
-- drafts, waiting for a person's approval, posted, or set aside.

-- An account to tag. A person (a politician or a party, persons.id) is tagged
-- through it; an outlet is matched by the site of a fact's source (domain).
-- Only the approved ones are ever tagged; the suggested ones wait for a yes.
CREATE TABLE IF NOT EXISTS x_accounts (
    id BIGSERIAL PRIMARY KEY,
    handle TEXT NOT NULL UNIQUE CHECK (handle = lower(handle) AND handle !~ '^@'),
    name TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT 'other'
        CHECK (category IN ('party', 'outlet', 'journalist', 'factcheck', 'association', 'independent', 'other')),
    person_id BIGINT REFERENCES persons(id) ON DELETE SET NULL,
    domain TEXT,
    status TEXT NOT NULL DEFAULT 'suggested' CHECK (status IN ('suggested', 'approved', 'refused')),
    note TEXT NOT NULL DEFAULT '',
    last_tagged_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_x_accounts_person ON x_accounts (person_id) WHERE person_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_x_accounts_domain ON x_accounts (domain) WHERE domain IS NOT NULL;

-- A tweet of the bot: the AI's versions, the text chosen, its tags. A
-- contradiction is drafted once: a later draft skips the ones listed here
-- (except the skipped ones, which may come back).
CREATE TABLE IF NOT EXISTS x_posts (
    id BIGSERIAL PRIMARY KEY,
    contradiction_id BIGINT REFERENCES contradictions(id) ON DELETE SET NULL,
    person_id BIGINT NOT NULL,
    slot TIMESTAMPTZ NOT NULL,
    variants JSONB NOT NULL DEFAULT '[]',
    tags TEXT[] NOT NULL DEFAULT '{}',
    chosen TEXT,
    status TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'approved', 'posted', 'skipped', 'false', 'failed', 'expired')),
    telegram_message_id BIGINT,
    tweet_id TEXT,
    error TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    decided_at TIMESTAMPTZ,
    posted_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_x_posts_contradiction ON x_posts (contradiction_id);
CREATE INDEX IF NOT EXISTS idx_x_posts_person ON x_posts (person_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS x_posts;
DROP TABLE IF EXISTS x_accounts;
