-- +goose Up
-- What one article of a bill does, as the model stated it from the text of
-- the bill (frpolitics articleResolver). One row per text and article; the
-- ballots of every scrutin on that article read it.
CREATE TABLE IF NOT EXISTS vote_articles (
    legislature INT NOT NULL,
    texte_num INT NOT NULL,
    article TEXT NOT NULL,
    statement TEXT NOT NULL,
    confidence DOUBLE PRECISION NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (legislature, texte_num, article)
);

-- +goose Down
DROP TABLE IF EXISTS vote_articles;
