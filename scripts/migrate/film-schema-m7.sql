-- polar_film schema — M7 (generalize media_items). Idempotent; apply after
-- film-schema.sql. Adds a self-referential parent_id so episodes belong to a
-- series and podcast episodes to a show — the same media_items table now
-- models movie / episode / doc / podcast via (kind, parent_id).
--
--   PGPASSWORD=… psql -U polar -h 127.0.0.1 -d polar_film -f film-schema-m7.sql

ALTER TABLE media_items
    ADD COLUMN IF NOT EXISTS parent_id TEXT
    REFERENCES media_items(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_media_items_parent ON media_items(parent_id);
