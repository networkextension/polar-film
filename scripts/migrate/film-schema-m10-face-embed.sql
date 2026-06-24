-- polar_film schema — M10 (face embeddings, PF-14). Idempotent; apply after
-- film-schema-m9-faces.sql. Persists the per-face Vision feature-print that
-- filmscan already computes for clustering (Stages/Faces.swift) so the server
-- can power re-identification: auto merge suggestions (疑似同人), within-movie
-- similar-face search, and cross-film person search. See doc/face-curation.md.
--
-- The column is an unspecified-dimension `vector` (pgvector ≥0.5): all rows
-- share the producer's dim (one filmscan build) so cosine `<=>` works; we use
-- exact NN scan (face counts are modest), no HNSW index. A later ArcFace swap
-- can pin 512-d + add an index without touching downstream queries.
--
-- IMPORTANT: apply as (or chown to) role `ideamesh` — film-svc connects as
-- ideamesh, not the superuser that may run the migration.
--
--   PGPASSWORD=… psql -U ideamesh -h 127.0.0.1 -d polar_film -f film-schema-m10-face-embed.sql

ALTER TABLE faces ADD COLUMN IF NOT EXISTS embedding vector;
