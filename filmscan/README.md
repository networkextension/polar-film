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

## Status
- **P0** ✅ scaffold + transcribe → SRT/JSON (this).
- **P1** demux(PCM) + keyframes + faces → face clusters.
- **P2** diarize + **fuse** → per-line character attribution (the headline).
- **P3** `filmscan label` naming + TMDB cast match.
- **P4** wire into polar-film (`analyze_jobs`, assets, `polar_film` upserts).
