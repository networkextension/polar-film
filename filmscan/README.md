# filmscan

Apple-native CLI that turns a video into **speaker-attributed subtitles**
(every line tagged with the on-screen character/actor — the thing ordinary
subtitles lack) + keyframes, feeding the `polar-film` knowledge base.

Design: [`../doc/speaker-subtitles.md`](../doc/speaker-subtitles.md). One
self-contained macOS binary — AVFoundation / WhisperKit / Vision / CoreML, no
Python runtime.

## Build
```bash
# Xcode 27 toolchain (CommandLineTools' swift-package can't build SwiftPM here):
DEVELOPER_DIR=/Applications/Xcode-beta.app/Contents/Developer swift build -c release
```

## Run (P0: transcribe → SRT)
```bash
filmscan analyze <video.mp4> --lang en --out <dir>
```
Emits `<media>.srt` + `transcript.json` (segments + word timestamps) under
`<dir>/<media>.filmscan/`. mp4/mov only for now (mkv needs demux — P1).

### Whisper model
First run downloads the `base.en` CoreML model from HuggingFace. If the HF
download times out (flaky network), pre-fetch it once and point at it:
```bash
pip3 install -U huggingface_hub
HF_ENDPOINT=https://hf-mirror.com huggingface-cli download \
  argmaxinc/whisperkit-coreml --include "openai_whisper-base.en/*" \
  --local-dir ~/wk-models
filmscan analyze <video.mp4> --model-folder ~/wk-models/openai_whisper-base.en
```

## Diarization (P5) — who spoke when, incl. off-screen
By default `analyze` also runs audio speaker diarization (FluidAudio CoreML
pyannote + wespeaker). Audio is the identity **backbone** (survives scene cuts,
covers off-screen / voiceover lines); the visual active-face names each speaker
and supplies its thumbnail. With no audio signal it falls back to visual-only.
```bash
# offline hosts: pre-fetch the two CoreML models, then point at them
HF_ENDPOINT=https://hf-mirror.com python3 -c \
  'from huggingface_hub import snapshot_download; snapshot_download("FluidInference/speaker-diarization-coreml", local_dir="~/diar-models")'
filmscan analyze <video.mp4> --diar-models ~/diar-models --compute cpu   # cpu: required on macOS 14.0
filmscan analyze <video.mp4> --no-diarize                                # visual-only
```
**macOS 14.0:** the diarization models SIGSEGV on the Neural Engine — pass
`--compute cpu` (forces CoreML CPU units, same as for WhisperKit). Needs
**Swift 6 / Xcode 16+** to build (FluidAudio); build on a Swift-6 box and ship
the prebuilt binary to older hosts (it still targets macOS 14).

## Name the speakers (P3)
```bash
filmscan label <dir>/<media>.filmscan              # roster: spkN, line count, thumbnail
filmscan label <dir>/<media>.filmscan --set spk0=Darcy --set spk1=Elizabeth
```
Rewrites the SRT so lines read `[Darcy] …`; persists `names.json`.

**TMDB auto-name** — match each `spkN.jpg` to the movie's cast profile photos
(Vision feature-print) and auto-assign the nearest character within a confidence
threshold; low-confidence clusters are reported for manual `--set` (and a manual
`--set` always wins). Best-effort (cross-domain frame↔headshot); ArcFace later
raises accuracy.
```bash
filmscan label <dir>/<media>.filmscan --tmdb-id 4348 --tmdb-key $TMDB_API_KEY
filmscan label <dir>/<media>.filmscan --tmdb-cast credits.json   # offline / CN-blocked
```
CN note: TMDB + image.tmdb.org are often blocked and macOS URLSession ignores
`http_proxy` — use `--tmdb-cast` with a pre-saved `/3/movie/{id}/credits` JSON
(its `profile_path` may point at local image files).

## Push to polar-film (P4c)
filmscan is macOS-only, the film service is Linux — so the Mac uploads results
over the film HTTP API (it doesn't shell out). The server parses the SRT's
`[Speaker]` tags into segments + people (P4a/P4b).
```bash
export FILMSCAN_SERVER=https://film.4950.store FILMSCAN_TOKEN=<bearer>
filmscan push <dir>/<media>.filmscan --media-id <mv_id> [--workspace-id <ws>]
```
Uploads the SRT (→ `/subtitles`) and every keyframe JPEG (→ `/screenshots`,
multipart, batched). `--no-subtitles` / `--no-screenshots` to send just one.

## Status
- **P0** ✅ scaffold + transcribe → SRT/JSON.
- **P1** ✅ demux(PCM) + keyframes + faces → face clusters.
- **P2** ✅ **fuse** → visual active-speaker per-line attribution + thumbnails.
- **P3** ✅ `filmscan label` naming (TMDB cast match later).
- **P4** wire into polar-film: ✅ `push` client + server `[Speaker]` ingest;
  remaining: `analyze_jobs` dispatch.
- **P5** ✅ audio diarization (FluidAudio) fused with vision → off-screen/voiceover
  lines + cross-cut identity. Remaining: ArcFace face identity, expression analysis.
