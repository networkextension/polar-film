-- film-schema-m9.sql — TMDB metadata enrichment (M9).
-- Adds external-metadata columns to media_items. Images (poster/backdrop) are
-- stored as full TMDB CDN URLs for the MVP; importing them into central assets
-- is a follow-up. tmdb_id/imdb_id/douban_id already exist (M1).
-- Apply: psql "$POLAR_FILM_DB_DSN" -f film-schema-m9.sql

ALTER TABLE media_items
    ADD COLUMN IF NOT EXISTS release_date DATE,
    ADD COLUMN IF NOT EXISTS rating       NUMERIC(3,1),   -- TMDB vote_average (0–10)
    ADD COLUMN IF NOT EXISTS backdrop_url TEXT,           -- full TMDB CDN URL
    ADD COLUMN IF NOT EXISTS poster_url   TEXT,           -- full TMDB CDN URL (fallback when no poster_asset_id)
    ADD COLUMN IF NOT EXISTS tagline      TEXT,
    ADD COLUMN IF NOT EXISTS overview     TEXT,           -- TMDB plot; kept separate from user `summary`
    ADD COLUMN IF NOT EXISTS enriched_at  TIMESTAMPTZ;    -- last successful TMDB enrich

-- Find un-enriched movies fast (batch backfill).
CREATE INDEX IF NOT EXISTS idx_media_items_enriched_at ON media_items (workspace_id, enriched_at);
