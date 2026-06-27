-- +goose Up
-- Generic, language-extensible translation store. One row per (entity, field, language).
-- Replaces the per-language _en columns over time; adding a language adds rows, not columns.
-- source_hash is a hash of the source text that was translated, so the async translate
-- phase can detect staleness (source changed -> re-translate) without comparing full text.
CREATE TABLE IF NOT EXISTS translations (
    id          BIGSERIAL PRIMARY KEY,
    entity_type TEXT        NOT NULL,   -- 'fact' | 'stance_label' | 'stance_summary' | 'contradiction_reason' | ...
    entity_id   BIGINT      NOT NULL,
    field       TEXT        NOT NULL,   -- logical field name within the entity (e.g. 'text', 'label', 'summary', 'reason')
    lang        TEXT        NOT NULL,   -- BCP-47 target language code ('en', 'fr', 'de', ...)
    text        TEXT        NOT NULL,   -- the translated text
    source_hash TEXT        NOT NULL,   -- hash of the source text this translation was produced from
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (entity_type, entity_id, field, lang)
);

CREATE INDEX IF NOT EXISTS idx_translations_lookup ON translations (entity_type, entity_id, lang);

-- +goose Down
DROP TABLE IF EXISTS translations;
