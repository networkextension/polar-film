-- polar_film schema — M4 (vector search). Idempotent; apply AFTER
-- film-schema.sql. Separate file so the M0–M3 base stays vector-free
-- (deployable on a Postgres without pgvector); this one requires the
-- `vector` extension to be available (dev: `brew install pgvector`).
--
--   PGPASSWORD=… psql -U polar -h 127.0.0.1 -d polar_film -f film-schema-m4.sql
--
-- Dimension is 1024 (bge-m3 / DashScope text-embedding-v3 / OpenAI
-- text-embedding-3-* with dimensions=1024). Changing models that emit a
-- different dim requires re-creating these columns + re-embedding.

CREATE EXTENSION IF NOT EXISTS vector;

-- line-level segment embeddings: the semantic "搜台词" workhorse.
ALTER TABLE subtitle_segments ADD COLUMN IF NOT EXISTS embedding vector(1024);

-- screenshot embeddings (OCR/caption text) — column reserved here; the
-- ingest that fills it lands in M5. Keeping it now avoids a later ALTER.
ALTER TABLE screenshots ADD COLUMN IF NOT EXISTS embedding vector(1024);

-- movie-level embedding (title + summary + cast/tags digest) for
-- "相似片" recommendations. One row per media item.
CREATE TABLE IF NOT EXISTS media_embeddings (
    media_id     TEXT PRIMARY KEY REFERENCES media_items(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL,
    embedding    vector(1024),
    source_text  TEXT NOT NULL DEFAULT '',  -- what was embedded (debug/reindex)
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_media_emb_ws ON media_embeddings(workspace_id);

-- HNSW cosine indexes (pgvector ≥0.5). Safe on empty/sparse tables;
-- queries use `<=>` (cosine distance). Build now so growth needs no DDL.
CREATE INDEX IF NOT EXISTS idx_seg_emb_hnsw
    ON subtitle_segments USING hnsw (embedding vector_cosine_ops);
CREATE INDEX IF NOT EXISTS idx_media_emb_hnsw
    ON media_embeddings USING hnsw (embedding vector_cosine_ops);
