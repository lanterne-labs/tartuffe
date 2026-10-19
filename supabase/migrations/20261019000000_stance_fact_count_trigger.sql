-- +goose Up

-- stance_documents.fact_count follows the rows of stance_document_facts.
-- The writers set it themselves, and the paths that add or drop a row
-- without it (AddFactToStance, UngroupFact, the claim-group slot moves) left
-- 2,844 stances off on 2026-10-19, most of them counting fewer facts than
-- they hold. The count weights the centroid update and picks the merge
-- destination, so the trigger owns it now.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION stance_fact_count_sync() RETURNS trigger AS $$
BEGIN
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        UPDATE stance_documents
        SET fact_count = (SELECT count(*) FROM stance_document_facts WHERE stance_document_id = NEW.stance_document_id)
        WHERE id = NEW.stance_document_id;
    END IF;
    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        UPDATE stance_documents
        SET fact_count = (SELECT count(*) FROM stance_document_facts WHERE stance_document_id = OLD.stance_document_id)
        WHERE id = OLD.stance_document_id;
    END IF;
    RETURN NULL;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS stance_document_facts_count ON stance_document_facts;
CREATE TRIGGER stance_document_facts_count
    AFTER INSERT OR DELETE OR UPDATE OF stance_document_id ON stance_document_facts
    FOR EACH ROW EXECUTE FUNCTION stance_fact_count_sync();

UPDATE stance_documents sd
SET fact_count = c.n
FROM (SELECT sd2.id, count(sdf.fact_id) AS n
      FROM stance_documents sd2
      LEFT JOIN stance_document_facts sdf ON sdf.stance_document_id = sd2.id
      GROUP BY sd2.id) c
WHERE c.id = sd.id AND sd.fact_count <> c.n;

-- +goose Down
DROP TRIGGER IF EXISTS stance_document_facts_count ON stance_document_facts;
DROP FUNCTION IF EXISTS stance_fact_count_sync();
