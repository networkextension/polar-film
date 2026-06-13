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

## Name the speakers (P3)
```bash
filmscan label <dir>/<media>.filmscan              # roster: spkN, line count, thumbnail
filmscan label <dir>/<media>.filmscan --set spk0=Darcy --set spk1=Elizabeth
```
Rewrites the SRT so lines read `[Darcy] …`; persists `names.json`.

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
  remaining: `analyze_jobs` dispatch + audio diarization / ArcFace identity.
