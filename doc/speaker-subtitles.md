# filmscan — speaker-attributed subtitles + keyframes (Swift CLI)

## Goal
Given a video file (assume English audio), produce subtitles where **every line is
attributed to the specific on-screen character/actor who said it** — the thing
ordinary subtitles lack — plus keyframes. Output lands in `polar_film` for the Go
service to serve.

## Why Swift (one self-contained macOS CLI)
The whole pipeline is Apple-native, so no Python runtime, one signed binary:
- **AVFoundation** — demux audio, sample frames, scene cuts.
- **WhisperKit** (CoreML Whisper) — on-device ASR with **word-level timestamps**.
  Alt on macOS 26: `Speech`'s `SpeechAnalyzer`/`SpeechTranscriber` (on-device,
  long-form, no model download) — keep behind a `--asr apple|whisper` flag.
- **Vision** — face detection + landmarks + face feature-prints (clustering).
- **CoreML** — a speaker-embedding model (ECAPA-TDNN / wespeaker) for diarization,
  and optionally a stronger face-recognition model (ArcFace) for cast matching.
- **Accelerate** — embedding distances + clustering.
- **swift-argument-parser** — CLI.

The only gap is speaker diarization (Apple has none); we cover it with a CoreML
voice-embedding model **and lean on vision for the hard part** (see Fuse).

## CLI
```
filmscan analyze <video> [--lang en] [--asr whisper|apple] [--out <dir>] [--db <dsn>]
filmscan label <media_id> --cluster <c> --name "Walter White" --actor "Bryan Cranston"
```
Resumable, mirrors `analyze_jobs.steps_json`: each stage writes an intermediate
artifact under `<out>/<media>/`; re-running skips finished stages and only the
edited/new stage + downstream re-run.

## Pipeline (stages = the steps in analyze_jobs.steps_json)
1. **demux** — `AVAssetReader` → 16 kHz mono PCM WAV; probe duration/fps/res.
2. **transcribe** — WhisperKit → segments + **words** with ms timestamps (`en`).
3. **diarize** — VAD (Whisper segments / energy) → 1.5 s sliding windows over
   speech → CoreML voice embeddings → agglomerative clustering → anonymous
   speaker turns `spk0/spk1/…`; align to transcript words so each subtitle
   segment carries one **audio-speaker cluster**.
4. **keyframes** — `AVAssetImageGenerator`: one frame at each segment midpoint +
   scene-cut frames (luma-histogram diff). Saved as JPEGs → `screenshots`.
5. **faces** — Vision `VNDetectFaceRectangles` + landmarks + face feature-print
   per face on those frames → cluster faces into **characters** (face clusters).
6. **fuse** — *the actual IP*. For each segment's time window, pick the **active
   speaker** among on-screen faces via: (a) **lip motion** (mouth-landmark delta
   across the segment's frames), (b) face centrality/size, (c) shot framing.
   Accumulate co-occurrence → match **audio-speaker cluster ↔ face cluster**
   (greedy/Hungarian). Each segment → one unified **character id**.
   - Honest bit: film audio (music, overlap) makes audio-only diarization shaky;
     **vision active-speaker is usually more reliable for on-screen dialogue**.
     Rule: faces agree → high confidence; disagree → trust vision for on-screen
     lines, audio cluster for off-screen/voiceover. Emit a per-line `confidence`.
7. **label** — clusters start as "角色 A / Speaker A" with 3 representative face
   thumbnails. Optional auto-name: match face clusters against a **cast list**
   (TMDB reference photos → face-embedding nearest-neighbor) → real actor +
   character. Otherwise the user names one face → propagates to the whole cluster
   (`filmscan label …`).
8. **emit** — SRT/VTT (speaker-prefixed) + rich JSON; upsert `polar_film`:
   `subtitles` + `subtitle_segments` (+ new speaker column) + `people` /
   `media_people` (character name) + `screenshots` + `media_embeddings`
   (face & voice vectors for later search/dedup). Update `analyze_jobs`.

## Schema delta (migration m8)
`subtitle_segments` has no speaker field yet — add:
```sql
ALTER TABLE subtitle_segments ADD COLUMN person_id   TEXT REFERENCES people(id);
ALTER TABLE subtitle_segments ADD COLUMN speaker_key TEXT NOT NULL DEFAULT '';  -- cluster id pre-naming
ALTER TABLE subtitle_segments ADD COLUMN speaker_conf REAL NOT NULL DEFAULT 0;  -- 0..1
```
`people` rows are the characters; `media_people.character` holds the character
name, `role='actor'`, with the actor as the person. Face/voice prototype vectors
go in `media_embeddings` keyed by cluster so re-runs and cross-episode matching
reuse them.

## Build phases
- **P0** demux + transcribe + emit plain SRT (prove ASR + DB write).
- **P1** keyframes + faces + face clustering (characters, unnamed).
- **P2** diarize + **fuse** → per-line speaker attribution (the headline feature).
- **P3** labeling: manual `label` + optional TMDB cast auto-match.
- **P4** wire into the Go svc: `analyze_jobs` runner shells out to `filmscan`,
  streams `steps_json` progress.

## Models to ship (CoreML, bundled or fetched once)
- Whisper (via WhisperKit, e.g. `base.en`/`small.en`).
- Voice embedding: ECAPA-TDNN or wespeaker → CoreML.
- (optional) Face recognition: ArcFace → CoreML (Vision's feature-print works for
  clustering; ArcFace is better for cast-photo matching).
