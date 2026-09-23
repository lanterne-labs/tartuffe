-- +goose Up
-- translations is polymorphic (entity_type, entity_id) and has no foreign key,
-- so deleting a fact, stance or contradiction left its translations behind
-- (2026-09-23 on prod: 59 fact, 188 contradiction, 21,920 stance orphans).
-- One statement-level trigger per owning table removes them on every delete
-- path, cascades included.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION delete_owned_translations() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM translations t
    USING deleted_rows d
    WHERE t.entity_type = TG_ARGV[0] AND t.entity_id = d.id;
    RETURN NULL;
END $$;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS facts_delete_translations ON facts;
CREATE TRIGGER facts_delete_translations
    AFTER DELETE ON facts REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT EXECUTE FUNCTION delete_owned_translations('fact');

DROP TRIGGER IF EXISTS stance_documents_delete_translations ON stance_documents;
CREATE TRIGGER stance_documents_delete_translations
    AFTER DELETE ON stance_documents REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT EXECUTE FUNCTION delete_owned_translations('stance');

DROP TRIGGER IF EXISTS contradictions_delete_translations ON contradictions;
CREATE TRIGGER contradictions_delete_translations
    AFTER DELETE ON contradictions REFERENCING OLD TABLE AS deleted_rows
    FOR EACH STATEMENT EXECUTE FUNCTION delete_owned_translations('contradiction');

-- +goose Down
DROP TRIGGER IF EXISTS facts_delete_translations ON facts;
DROP TRIGGER IF EXISTS stance_documents_delete_translations ON stance_documents;
DROP TRIGGER IF EXISTS contradictions_delete_translations ON contradictions;
DROP FUNCTION IF EXISTS delete_owned_translations();
