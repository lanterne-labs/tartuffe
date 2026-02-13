-- +goose Up
-- Migration: Normalize person_links to use 'affiliation' and add unique constraint
-- Update existing membership entries to 'affiliation' and add unique index to support upsert.

-- 1) Update existing values
UPDATE person_links SET link_type = 'affiliation' WHERE link_type = 'membership';

-- 2) Add a unique index to enable ON CONFLICT on src,dst,link_type
-- This requires that there are no duplicates currently; if duplicates exist, this will fail.
-- In that case you may want to deduplicate first.

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_indexes WHERE tablename = 'person_links' AND indexname = 'uq_person_links_src_dst_link') THEN
        CREATE UNIQUE INDEX uq_person_links_src_dst_link ON person_links (src_person_id, dst_person_id, link_type);
    END IF;
END$$;

-- 3) Update link_type constraint to only allow 'affiliation'
ALTER TABLE person_links DROP CONSTRAINT IF EXISTS person_links_link_type_check;
ALTER TABLE person_links ADD CONSTRAINT person_links_link_type_check CHECK (link_type IN ('affiliation'));

-- 4) Add partial index to speed up queries by src_person_id and dates
CREATE INDEX IF NOT EXISTS idx_person_links_aff_src ON person_links (src_person_id) WHERE link_type = 'affiliation';
CREATE INDEX IF NOT EXISTS idx_person_links_aff_valid ON person_links (src_person_id, valid_from, valid_to) WHERE link_type = 'affiliation';

-- End of migration
