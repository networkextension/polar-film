-- ============================================================
-- polar_film schema — M0 (metadata only; NO vectors yet).
--
-- Apply:
--   CREATE DATABASE polar_film OWNER polar;
--   psql -d polar_film -f scripts/migrate/film-schema.sql   (run as the polar role)
--
-- The film plugin is a video/film KNOWLEDGE BASE: it stores metadata,
-- segmented subtitles, screenshot pointers, people, timeline, and tags —
-- NOT the video files. Binaries (posters, screenshots, avatars) live in
-- polar-assets; tables hold only an asset_id.
--
-- Cross-DB references (TEXT, resolved via dock SDK — NO foreign keys):
--   - created_by      → /internal/v1/users/:id
--   - workspace_id    → /internal/v1/teams/:id
-- Every user-visible table carries workspace_id (multi-tenant partition).
-- IDs are TEXT with a domain prefix (mv_/pe_/sub_/seg_/sc_/tg_/tl_/job_).
--
-- NOTE: embeddings + `CREATE EXTENSION vector` are deferred to M4 (dev
-- Postgres has no pgvector yet). This file is vector-free and idempotent.
-- ============================================================

-- media item (M0: movies; kind generalizes to episode/doc/podcast/... later)
CREATE TABLE IF NOT EXISTS media_items (
    id             TEXT PRIMARY KEY,                  -- mv_<rand>
    workspace_id   TEXT NOT NULL,
    kind           TEXT NOT NULL DEFAULT 'movie',
    title          TEXT NOT NULL,
    original_title TEXT NOT NULL DEFAULT '',
    year           INT,
    country        TEXT NOT NULL DEFAULT '',
    language       TEXT NOT NULL DEFAULT '',
    runtime_min    INT,
    summary        TEXT NOT NULL DEFAULT '',
    poster_asset_id TEXT NOT NULL DEFAULT '',         -- → polar-assets
    imdb_id        TEXT NOT NULL DEFAULT '',
    douban_id      TEXT NOT NULL DEFAULT '',
    tmdb_id        TEXT NOT NULL DEFAULT '',
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, kind, title, year)
);
CREATE INDEX IF NOT EXISTS idx_media_items_ws ON media_items(workspace_id, kind);

CREATE TABLE IF NOT EXISTS people (
    id              TEXT PRIMARY KEY,                 -- pe_<rand>
    workspace_id    TEXT NOT NULL,
    name            TEXT NOT NULL,
    avatar_asset_id TEXT NOT NULL DEFAULT '',
    bio             TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, name)
);

CREATE TABLE IF NOT EXISTS media_people (
    media_id  TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    person_id TEXT NOT NULL REFERENCES people(id) ON DELETE CASCADE,
    role      TEXT NOT NULL,                          -- actor|director|writer|...
    character TEXT NOT NULL DEFAULT '',
    ord       INT NOT NULL DEFAULT 0,
    PRIMARY KEY (media_id, person_id, role)
);

CREATE TABLE IF NOT EXISTS subtitles (
    id           TEXT PRIMARY KEY,                    -- sub_<rand>
    workspace_id TEXT NOT NULL,
    media_id     TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    lang         TEXT NOT NULL,
    format       TEXT NOT NULL DEFAULT 'srt',
    source       TEXT NOT NULL DEFAULT 'uploaded',    -- uploaded|asr
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- line-level segments — the retrieval workhorse (embedding column added in M4)
CREATE TABLE IF NOT EXISTS subtitle_segments (
    id           TEXT PRIMARY KEY,                    -- seg_<rand>
    workspace_id TEXT NOT NULL,
    subtitle_id  TEXT NOT NULL REFERENCES subtitles(id) ON DELETE CASCADE,
    media_id     TEXT NOT NULL,
    idx          INT NOT NULL DEFAULT 0,
    start_ms     INT NOT NULL,
    end_ms       INT NOT NULL,
    text         TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_seg_subtitle ON subtitle_segments(subtitle_id);
CREATE INDEX IF NOT EXISTS idx_seg_media ON subtitle_segments(media_id);

CREATE TABLE IF NOT EXISTS screenshots (
    id           TEXT PRIMARY KEY,                    -- sc_<rand>
    workspace_id TEXT NOT NULL,
    media_id     TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    ts_ms        INT,
    asset_id     TEXT NOT NULL,                       -- image in polar-assets
    phash        TEXT NOT NULL DEFAULT '',
    ocr_text     TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_screenshot_media ON screenshots(media_id);

CREATE TABLE IF NOT EXISTS media_timeline (
    id          TEXT PRIMARY KEY,                     -- tl_<rand>
    workspace_id TEXT NOT NULL,
    media_id    TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    start_ms    INT,
    end_ms      INT,
    event_type  TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_timeline_media ON media_timeline(media_id);

CREATE TABLE IF NOT EXISTS tags (
    id           TEXT PRIMARY KEY,                    -- tg_<rand>
    workspace_id TEXT NOT NULL,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'genre',       -- genre|theme|ai
    UNIQUE (workspace_id, name)
);

CREATE TABLE IF NOT EXISTS media_tags (
    media_id TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    tag_id   TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    source   TEXT NOT NULL DEFAULT 'manual',          -- manual|ai
    PRIMARY KEY (media_id, tag_id)
);

-- AI analyze job tracking (M5 fills the pipeline; table exists from M0 so
-- the schema is stable).
CREATE TABLE IF NOT EXISTS analyze_jobs (
    id           TEXT PRIMARY KEY,                    -- job_<rand>
    workspace_id TEXT NOT NULL,
    media_id     TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    status       TEXT NOT NULL DEFAULT 'queued',      -- queued|running|done|failed
    steps_json   JSONB NOT NULL DEFAULT '{}'::jsonb,
    error        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_jobs_media ON analyze_jobs(media_id);
