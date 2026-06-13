-- polar_film schema — M8 (speaker-attributed subtitles). Idempotent; apply
-- after film-schema.sql. Adds per-segment speaker attribution as produced by
-- the `filmscan` analyzer: an anonymous/visible cluster label (speaker_key,
-- e.g. "spk0" or a named "[Darcy]" tag), an optional resolved person, and a
-- 0..1 confidence. Ordinary subtitles leave these NULL.
--
--   PGPASSWORD=… psql -U polar -h 127.0.0.1 -d polar_film -f film-schema-m8.sql

ALTER TABLE subtitle_segments
    ADD COLUMN IF NOT EXISTS speaker_key  TEXT,
    ADD COLUMN IF NOT EXISTS person_id    TEXT REFERENCES people(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS speaker_conf REAL;

CREATE INDEX IF NOT EXISTS idx_seg_person  ON subtitle_segments(person_id);
CREATE INDEX IF NOT EXISTS idx_seg_speaker ON subtitle_segments(media_id, speaker_key);
