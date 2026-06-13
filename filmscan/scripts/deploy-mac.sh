#!/bin/bash
# Deploy + build filmscan on a remote Apple-Silicon Mac, and fetch the Whisper
# model. Reusable across hosts; per-host knobs are flags.
#
# Usage:
#   deploy-mac.sh <ssh-host> [--git-proxy http://HOST:PORT] [--hf-mirror] [--model openai_whisper-base.en]
#
# Examples:
#   # 10.88.0.9 — deps via zen proxy, model via that proxy:
#   deploy-mac.sh local@10.88.0.9 --git-proxy http://192.168.11.57:10082
#   # 192.168.3.1 — deps via its own local proxy, model via hf-mirror (faster, no proxy):
#   deploy-mac.sh local@192.168.3.1 --git-proxy http://127.0.0.1:10082 --hf-mirror
set -euo pipefail

HOST="${1:?ssh host required, e.g. local@192.168.3.1}"; shift || true
GIT_PROXY=""; USE_MIRROR=0; MODEL="openai_whisper-base.en"
while [ $# -gt 0 ]; do
  case "$1" in
    --git-proxy) GIT_PROXY="$2"; shift 2;;
    --hf-mirror) USE_MIRROR=1; shift;;
    --model) MODEL="$2"; shift 2;;
    *) echo "unknown arg: $1" >&2; exit 1;;
  esac
done

SRC="$(cd "$(dirname "$0")/.." && pwd)"   # the filmscan package dir
echo "==> rsync $SRC → $HOST:~/filmscan"
rsync -az --delete --exclude .build --exclude .git -e ssh "$SRC"/ "$HOST":~/filmscan/

# remote build + model fetch
ssh "$HOST" "GIT_PROXY='$GIT_PROXY' USE_MIRROR='$USE_MIRROR' MODEL='$MODEL' bash -s" <<'REMOTE'
set -e
export DEVELOPER_DIR="$(xcode-select -p)"
[ -n "$GIT_PROXY" ] && { git config --global http.proxy "$GIT_PROXY"; git config --global https.proxy "$GIT_PROXY"; echo "git proxy = $GIT_PROXY"; }

echo "==> swift build -c release"
cd ~/filmscan && swift build -c release
echo "built: $(ls -lh .build/release/filmscan | awk '{print $5}')"

# pick a python with huggingface_hub (install if missing)
PY=/usr/local/bin/python3
"$PY" -c 'import huggingface_hub' 2>/dev/null || { [ -n "$GIT_PROXY" ] && export https_proxy="$GIT_PROXY" http_proxy="$GIT_PROXY"; "$PY" -m pip install -q -U huggingface_hub; }

# proxy/mirror for the model fetch
if [ "$USE_MIRROR" = 1 ]; then export HF_ENDPOINT=https://hf-mirror.com; unset https_proxy http_proxy;
elif [ -n "$GIT_PROXY" ]; then export https_proxy="$GIT_PROXY" http_proxy="$GIT_PROXY"; fi

echo "==> fetch model $MODEL + tokenizer"
"$PY" - "$MODEL" <<PYEOF
import os, sys
from huggingface_hub import snapshot_download
m = sys.argv[1]
snapshot_download("argmaxinc/whisperkit-coreml", allow_patterns=[m+"/*"],
                  local_dir=os.path.expanduser("~/wk-models"))
# tokenizer repo: openai_whisper-base.en -> openai/whisper-base.en
tok = "openai/" + m.replace("openai_whisper-","whisper-")
snapshot_download(tok, allow_patterns=["*.json","*.txt"],
                  local_dir=os.path.expanduser("~/Documents/huggingface/models/"+tok))
print("model + tokenizer ready")
PYEOF
echo "==> done. run:"
echo "   ~/filmscan/.build/release/filmscan analyze <video> --model-folder ~/wk-models/$MODEL"
REMOTE
