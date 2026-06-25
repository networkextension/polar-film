-- film-schema-m10.sql — filmscan processing status (M10).
-- A lightweight "处理中" view: the movie page shows which stage a filmscan is
-- at (extracting / extracted / analyzing / done / failed). Driven by the
-- filmscan orchestration POSTing /api/film/movies/:id/scan-status, and
-- auto-set to "done" when subtitles land.
-- Apply: psql "$POLAR_FILM_DB_DSN" -f film-schema-m10.sql

ALTER TABLE media_items
    ADD COLUMN IF NOT EXISTS scan_status     TEXT,        -- '' | extracting | extracted | analyzing | done | failed
    ADD COLUMN IF NOT EXISTS scan_detail     TEXT,        -- free text, e.g. "转写中" / "2491 帧"
    ADD COLUMN IF NOT EXISTS scan_updated_at TIMESTAMPTZ;
