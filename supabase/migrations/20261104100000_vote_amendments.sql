-- +goose Up
-- What one amendment does, as the model stated it from its dispositif and
-- exposé (frpolitics amendmentStatements). One row per text and amendment
-- number; the ballots of the amendment's scrutin read it.
CREATE TABLE IF NOT EXISTS vote_amendments (
    legislature INT NOT NULL,
    texte_ref TEXT NOT NULL,
    numero INT NOT NULL,
    statement TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (legislature, texte_ref, numero)
);

-- +goose Down
DROP TABLE IF EXISTS vote_amendments;
