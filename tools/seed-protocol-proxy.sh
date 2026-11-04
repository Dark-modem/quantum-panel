#!/usr/bin/env bash
# seed-protocol-proxy.sh — seed a local file-based Go module proxy with the
# quantum-protocol commit the panel pins, so a worktree can build offline
# (the GitHub-side module is private and WSL here has no network credential).
# Run inside WSL: bash tools/seed-protocol-proxy.sh <path-to-quantum-protocol>
set -euo pipefail
set +H
SRC="${1:?usage: seed-protocol-proxy.sh <quantum-protocol-repo>}"
SHA="${2:-f34074e8f830d664dc6f3e836e29b6cadf73868b}"
# Pseudo-version: UTC timestamp of the commit + 12-hex sha.
UTC="$(git -C "$SRC" show -s --format='%cI' "$SHA" | tail -1)"
# 2026-11-04T23:54:30+03:30 -> 20261104202430
TS="$(python3 - "$UTC" <<'PY'
import sys, datetime
t = datetime.datetime.fromisoformat(sys.argv[1].strip())
print(t.astimezone(datetime.timezone.utc).strftime('%Y%m%d%H%M%S'))
PY
)"
PSEUDO="v0.0.0-${TS}-${SHA:0:12}"
PROXY="/tmp/goproxy/github.com/$(printf '!dark-!modem')/$(printf '!quantum-!protocol')/@v"
mkdir -p "$PROXY"
TMP="$(mktemp -d)"
git -C "$SRC" archive --format=zip --prefix="quantum-protocol@${PSEUDO}/" "$SHA" -o "$TMP/m.zip"
mkdir -p "$TMP/x" && unzip -q "$TMP/m.zip" -d "$TMP/x"
( cd "$TMP/x" && zip -q -r "$PROXY/${PSEUDO}.zip" . )
cp "$SRC/go.mod" "$PROXY/${PSEUDO}.mod"
printf '{"Version":"%s","Time":"%s"}\n' "$PSEUDO" "${TS:0:4}-${TS:4:2}-${TS:6:2}T${TS:8:2}:${TS:10:2}:${TS:12:2}Z" > "$PROXY/${PSEUDO}.info"
echo "$PSEUDO" > "$PROXY/list"
rm -rf "$TMP"
echo "seeded $PSEUDO at $PROXY"
