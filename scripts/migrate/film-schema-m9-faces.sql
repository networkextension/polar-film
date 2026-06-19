-- polar_film schema — M9 (face curation). Idempotent; apply after
-- film-schema.sql. Stores the face clusters + per-face boxes the `filmscan`
-- analyzer produces (Stages/Faces.swift) so editors can curate them on the
-- server (merge/remove/split/name — see doc/face-curation.md). Each face
-- references an already-uploaded keyframe (screenshots) + a normalized box;
-- the cropped thumbnail is rendered client-side from the keyframe, so no
-- per-face image asset is stored. P0 = storage + upload only (no curation
-- ops, no suggestions yet).
--
--   PGPASSWORD=… psql -U polar -h 127.0.0.1 -d polar_film -f film-schema-m9-faces.sql

CREATE TABLE IF NOT EXISTS face_clusters (
    id                TEXT PRIMARY KEY,             -- fc_<rand>
    workspace_id      TEXT NOT NULL,
    media_id          TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    label             TEXT NOT NULL DEFAULT '',     -- filmscan face-cluster tag, e.g. "fc0"
    person_id         TEXT REFERENCES people(id) ON DELETE SET NULL,  -- set by P1 assign
    rep_screenshot_id TEXT NOT NULL DEFAULT '',     -- representative keyframe
    rep_box_x         REAL NOT NULL DEFAULT 0,      -- representative face box (normalized, top-left)
    rep_box_y         REAL NOT NULL DEFAULT 0,
    rep_box_w         REAL NOT NULL DEFAULT 0,
    rep_box_h         REAL NOT NULL DEFAULT 0,
    face_count        INT  NOT NULL DEFAULT 0,
    source            TEXT NOT NULL DEFAULT 'filmscan',  -- filmscan | manual
    conf              REAL NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS faces (
    id            TEXT PRIMARY KEY,                 -- fa_<rand>
    workspace_id  TEXT NOT NULL,
    media_id      TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    cluster_id    TEXT NOT NULL REFERENCES face_clusters(id) ON DELETE CASCADE,
    screenshot_id TEXT NOT NULL DEFAULT '',         -- keyframe this face is on (by ts_ms)
    ts_ms         INT,
    box_x         REAL NOT NULL DEFAULT 0,          -- normalized, top-left origin
    box_y         REAL NOT NULL DEFAULT 0,
    box_w         REAL NOT NULL DEFAULT 0,
    box_h         REAL NOT NULL DEFAULT 0,
    quality       REAL NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Client-precomputed "likely same person" pairs (filled by a later filmscan
-- change that emits the inter-cluster distance matrix). Empty in P0.
CREATE TABLE IF NOT EXISTS face_merge_suggestions (
    media_id   TEXT NOT NULL REFERENCES media_items(id) ON DELETE CASCADE,
    cluster_a  TEXT NOT NULL,
    cluster_b  TEXT NOT NULL,
    distance   REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (media_id, cluster_a, cluster_b)
);

CREATE INDEX IF NOT EXISTS idx_faces_media_cluster ON faces(media_id, cluster_id);
CREATE INDEX IF NOT EXISTS idx_face_clusters_media ON face_clusters(media_id);
CREATE INDEX IF NOT EXISTS idx_face_clusters_person ON face_clusters(person_id) WHERE person_id IS NOT NULL;
