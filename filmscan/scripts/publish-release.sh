#!/usr/bin/env bash
# publish-release.sh — build the universal filmscan binary and publish it to
# polar-release so fleet agents can pull it via /release/resolve.
#
# filmscan is ONE universal Mach-O (x86_64 + arm64), so we publish the SAME bytes
# under both platform keys (darwin-arm64, darwin-x86_64). polar-assets is
# content-addressed, so the second platform's PUT is skipped (exists=true) — only
# the manifest rows differ.
#
# publish/finalize land on channel=dev only (beta/stable are gated behind the
# stage-test→stamp→promote pipeline — see modules/polar-release). Agents resolve
# whatever channel they're told (default dev for now).
#
# Usage:
#   SERVER=https://zen.4950.store:2443 TOKEN=polar_xxx ./scripts/publish-release.sh
# Env:
#   SERVER   dock/release base URL (required)
#   TOKEN    admin/ops bearer token (required; publish is admin-gated)
#   CHANNEL  publish channel (default: dev — the only un-gated target)
#   VERSION  override version (default: `filmscan version`)
set -euo pipefail

SERVER="${SERVER:?set SERVER, e.g. https://zen.4950.store:2443}"
TOKEN="${TOKEN:?set TOKEN (admin/ops bearer)}"
CHANNEL="${CHANNEL:-dev}"
SERVER="${SERVER%/}"

cd "$(dirname "$0")/.."

echo "== building universal release binary =="
swift build -c release --arch arm64 --arch x86_64

BIN=""
for p in .build/release/filmscan .build/apple/Products/Release/filmscan; do
  [ -f "$p" ] && BIN="$p" && break
done
[ -n "$BIN" ] || { echo "filmscan binary not found after build" >&2; exit 1; }
echo "binary: $BIN"
file "$BIN"

VERSION="${VERSION:-$("$BIN" --version 2>/dev/null | tr -d '[:space:]')}"
[ -n "$VERSION" ] || { echo "could not determine version" >&2; exit 1; }

SHA=$(shasum -a 256 "$BIN" | awk '{print $1}')
SIZE=$(stat -f%z "$BIN" 2>/dev/null || stat -c%s "$BIN")
echo "version=$VERSION sha256=$SHA size=$SIZE channel=$CHANNEL"

# jq isn't guaranteed on the build box; parse JSON with python3.
jget() { python3 -c 'import sys,json;print(json.load(sys.stdin).get(sys.argv[1],""))' "$1"; }

api() { # api METHOD PATH [JSON_BODY]
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" "$SERVER$path" \
      -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d "$body"
  else
    curl -fsS -X "$method" "$SERVER$path" -H "Authorization: Bearer $TOKEN"
  fi
}

for PLATFORM in darwin-arm64 darwin-x86_64; do
  echo "== publish $PLATFORM =="
  PUB=$(api POST /release/publish "$(python3 - "$VERSION" "$CHANNEL" "$PLATFORM" "$SHA" "$SIZE" <<'PY'
import sys,json
_,v,ch,plat,sha,size=sys.argv
print(json.dumps({"module":"filmscan","version":v,"channel":"dev","platform":plat,
                  "sha256":sha,"size_bytes":int(size),"mime":"application/octet-stream"}))
PY
)")
  ASSET=$(printf '%s' "$PUB" | jget asset_id)
  PROVIDER=$(printf '%s' "$PUB" | jget provider_id)
  PUTURL=$(printf '%s' "$PUB" | jget put_url)
  EXISTS=$(printf '%s' "$PUB" | jget exists)
  echo "  asset_id=$ASSET provider_id=$PROVIDER exists=$EXISTS"

  if [ "$EXISTS" != "True" ] && [ -n "$PUTURL" ]; then
    echo "  PUT bytes → provider"
    curl -fsS -X PUT "$PUTURL" -H "Content-Type: application/octet-stream" --upload-file "$BIN" >/dev/null
  else
    echo "  bytes already present — skip PUT"
  fi

  echo "  finalize"
  api POST /release/finalize "$(python3 - "$VERSION" "$PLATFORM" "$SHA" "$SIZE" "$ASSET" "$PROVIDER" <<'PY'
import sys,json
_,v,plat,sha,size,asset,provider=sys.argv
print(json.dumps({"module":"filmscan","version":v,"channel":"dev","platform":plat,
                  "asset_id":int(asset),"provider_id":int(provider),"sha256":sha,
                  "size_bytes":int(size),"format":"binary"}))
PY
)" >/dev/null
  echo "  ✓ published filmscan $VERSION $PLATFORM (channel=dev)"
done

echo "== done. resolve check: =="
echo "curl '$SERVER/release/resolve?module=filmscan&channel=dev&platform=darwin-arm64'"
if [ "$CHANNEL" != "dev" ]; then
  echo "NOTE: to reach channel=$CHANNEL, run the stage-test→stamp→promote pipeline (beta/stable are gated)."
fi
