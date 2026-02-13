-- +goose Up
-- Migration: Ensure person_links has a unique constraint for upserts
-- This supports the FindOrCreateAffiliation logic which uses ON CONFLICT

DROP INDEX IF EXISTS uq_person_links_src_dst_link;

ALTER TABLE person_links 
ADD CONSTRAINT uq_person_links_src_dst_link 
UNIQUE (src_person_id, dst_person_id, link_type);
