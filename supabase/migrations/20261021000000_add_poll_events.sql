-- +goose Up
-- The pre-polls: an event (the presidential election first) lists its
-- candidates among the tracked persons; each signed-in user ranks up to
-- max_choices of them. The results put the share of first choices next to the
-- honesty score of each candidate.

CREATE TABLE IF NOT EXISTS poll_events (
    id BIGSERIAL PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    title_fr TEXT NOT NULL,
    title_en TEXT NOT NULL,
    description_fr TEXT NOT NULL DEFAULT '',
    description_en TEXT NOT NULL DEFAULT '',
    -- how many candidates a ballot may rank
    max_choices INT NOT NULL DEFAULT 3 CHECK (max_choices BETWEEN 1 AND 10),
    -- below this many ballots the shares are not shown
    min_voters INT NOT NULL DEFAULT 50 CHECK (min_voters >= 0),
    opens_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- ballots are refused from closes_at on; NULL never closes
    closes_at TIMESTAMPTZ,
    -- the featured event is the one the home page promotes
    featured BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A blackout hides the results (the polling law forbids publishing them the
-- day before and the day of a vote). Ballots are still taken.
CREATE TABLE IF NOT EXISTS poll_blackouts (
    event_id BIGINT NOT NULL REFERENCES poll_events(id) ON DELETE CASCADE,
    starts_at TIMESTAMPTZ NOT NULL,
    ends_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (event_id, starts_at),
    CHECK (starts_at < ends_at)
);

CREATE TABLE IF NOT EXISTS poll_candidates (
    event_id BIGINT NOT NULL REFERENCES poll_events(id) ON DELETE CASCADE,
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    added_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, person_id)
);

-- A user is one identity proved by one auth provider: (provider, subject) is
-- what the provider vouches for (a pseudonym for noop, the scoped unique
-- identifier for zkpassport). Nothing else about the person is stored.
CREATE TABLE IF NOT EXISTS app_users (
    id BIGSERIAL PRIMARY KEY,
    provider TEXT NOT NULL,
    subject TEXT NOT NULL,
    display_name TEXT NOT NULL DEFAULT '',
    -- when the user agreed that their ballots (political opinions, GDPR art. 9)
    -- are stored; a ballot is refused before
    consent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (provider, subject)
);

-- Only the SHA-256 of a session token is stored.
CREATE TABLE IF NOT EXISTS user_sessions (
    token_hash BYTEA PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_user_sessions_user ON user_sessions (user_id);

-- One row per ranked candidate of a ballot: rank 1 is the first choice.
CREATE TABLE IF NOT EXISTS poll_choices (
    event_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    rank SMALLINT NOT NULL CHECK (rank >= 1),
    person_id BIGINT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, user_id, rank),
    UNIQUE (event_id, user_id, person_id),
    FOREIGN KEY (event_id, person_id) REFERENCES poll_candidates(event_id, person_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_poll_choices_event_person ON poll_choices (event_id, person_id);

-- The presidential election of 2027: first round on Sunday 18 April, second
-- round on Sunday 2 May (reported by LCP on 2026-06-30; check the convening
-- decree). Ballots close when the polling stations of the first round close
-- (20:00 Paris = 18:00 UTC). The blackouts run from the Friday 00:00 Paris
-- (the overseas territories vote on the Saturday) to the Sunday 20:00 Paris.
INSERT INTO poll_events (slug, title_fr, title_en, description_fr, description_en, max_choices, min_voters, closes_at, featured)
VALUES (
    'presidentielle-2027',
    'Présidentielle 2027',
    '2027 presidential election',
    'Classez jusqu''à 3 candidats, puis comparez la préférence des votants avec l''honnêteté mesurée de chacun.',
    'Rank up to 3 candidates, then compare what voters prefer with how honest each candidate measures.',
    3, 50, '2027-04-18 18:00:00+00', true
)
ON CONFLICT (slug) DO NOTHING;

INSERT INTO poll_blackouts (event_id, starts_at, ends_at)
SELECT id, b.starts_at, b.ends_at
FROM poll_events,
     (VALUES ('2027-04-15 22:00:00+00'::timestamptz, '2027-04-18 18:00:00+00'::timestamptz),
             ('2027-04-29 22:00:00+00'::timestamptz, '2027-05-02 18:00:00+00'::timestamptz)) AS b(starts_at, ends_at)
WHERE slug = 'presidentielle-2027'
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE IF EXISTS poll_choices;
DROP TABLE IF EXISTS user_sessions;
DROP TABLE IF EXISTS app_users;
DROP TABLE IF EXISTS poll_candidates;
DROP TABLE IF EXISTS poll_blackouts;
DROP TABLE IF EXISTS poll_events;
