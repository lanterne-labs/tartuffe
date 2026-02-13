-- +goose Up
-- Notify channel for facts inserted/updated so external services can react

CREATE OR REPLACE FUNCTION notify_facts_changed() RETURNS trigger AS $$
BEGIN
    PERFORM pg_notify('facts_changed', NEW.id::text);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS facts_changed_insert ON facts;
CREATE TRIGGER facts_changed_insert
AFTER INSERT ON facts
FOR EACH ROW
EXECUTE FUNCTION notify_facts_changed();

DROP TRIGGER IF EXISTS facts_changed_update ON facts;
CREATE TRIGGER facts_changed_update
AFTER UPDATE ON facts
FOR EACH ROW
EXECUTE FUNCTION notify_facts_changed();


