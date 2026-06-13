# filmscan — model download guide

filmscan runs on-device CoreML models. The macOS host can't reliably reach
HuggingFace in some networks, so download the models once (mirror or browser) and
point filmscan at the local folders. China mirror: `https://hf-mirror.com`.

---

## 1. Whisper ASR — NEEDED NOW (unblocks subtitles)
Repo: **`argmaxinc/whisperkit-coreml`**. Grab one model folder:
- `openai_whisper-base.en` — good default (≈150 MB)
- `openai_whisper-small.en` — better accuracy, slower (≈480 MB)  ← recommended for film
- `openai_whisper-tiny.en` — fastest, lowest quality

### Option A — browser (simplest, "click")
Open (mirror):
`https://hf-mirror.com/argmaxinc/whisperkit-coreml/tree/main/openai_whisper-base.en`
Download **every file**, keeping the folder structure:
```
openai_whisper-base.en/
├── AudioEncoder.mlmodelc/      (a folder of files)
├── MelSpectrogram.mlmodelc/    (a folder)
├── TextDecoder.mlmodelc/       (a folder)
├── config.json
└── generation_config.json
```

### Option B — huggingface-cli
```bash
pip3 install -U huggingface_hub
export HF_ENDPOINT=https://hf-mirror.com
huggingface-cli download argmaxinc/whisperkit-coreml \
  --include "openai_whisper-base.en/*" --local-dir ~/wk-models
```

### Option C — git + lfs (mirror)
```bash
brew install git-lfs && git lfs install
GIT_LFS_SKIP_SMUDGE=1 git clone https://hf-mirror.com/argmaxinc/whisperkit-coreml
cd whisperkit-coreml && git lfs pull --include "openai_whisper-base.en/*"
```

### Option D — auto (only where HF is directly reachable)
Just run `filmscan analyze <video>`; WhisperKit downloads + caches to
`~/Documents/huggingface/models/argmaxinc/whisperkit-coreml/openai_whisper-base.en`.

### Use it
```bash
filmscan analyze <video.mp4> --model-folder <dir>/openai_whisper-base.en
```

---

## 2. Face recognition (ArcFace) — LATER (face identity clustering)
Vision's generic feature-print can't tell two faces apart (verified: it merged a
2-person scene into one cluster). The fix is a real face-embedding model. **Don't
download yet** — the exact CoreML repo/format depends on the embedding stage I'll
wire (P3). I'll pin the repo here when that lands (candidate: an ArcFace /
`buffalo_l`-style model converted to CoreML).

## 3. Speaker diarization (voice embedding) — LATER (P2)
A CoreML voice-embedding model (ECAPA-TDNN / wespeaker) for "who spoke when".
Also TBD — pinned when the diarize stage is wired.

> P2 (the headline: per-line character attribution) = Whisper × voice-embedding ×
> face-embedding fused. It needs all three models. Whisper unblocks subtitles now;
> the other two land with their stages.
