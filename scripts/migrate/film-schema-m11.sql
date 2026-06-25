-- m11: link a movie to its extracted audio asset (central polar-assets id).
-- The film→identity voiceprint pipeline (speech.diarize) references this as the
-- recording asset for each voice biometric_sample. 0 = not yet extracted.
ALTER TABLE media_items
    ADD COLUMN IF NOT EXISTS audio_asset_id BIGINT NOT NULL DEFAULT 0;
