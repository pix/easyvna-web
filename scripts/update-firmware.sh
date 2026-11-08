#!/usr/bin/env bash
# Fetch the latest EasyVNA firmware release (github.com/pix/easyvna) into
# web/static/firmware/: the NanoVNA-H4 .bin, checked against the release's
# SHA256SUMS, and manifest.json (read by the FIRMWARE dialog, flasher.js).
# The native program embeds that folder (go:embed) and the single-file HTML
# inlines it (scripts/build-html.sh), so run this before building to bundle
# the newest firmware. Builds work offline from the committed copy.
#
#   scripts/update-firmware.sh [TAG]     # default: the latest release
#
# Uses the public GitHub API (no token needed; GITHUB_TOKEN, when set, only
# raises the API rate limit in CI).
set -euo pipefail
cd "$(dirname "$0")/.."
REPO="${FIRMWARE_REPO:-pix/easyvna}"
DIR=web/static/firmware
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

auth=()
[ -n "${GITHUB_TOKEN:-}" ] && auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}")
if [ -n "${1:-}" ]; then url="https://api.github.com/repos/$REPO/releases/tags/$1"; else url="https://api.github.com/repos/$REPO/releases/latest"; fi
curl -fsSL --retry 3 "${auth[@]}" -H 'Accept: application/vnd.github+json' "$url" -o "$tmp/release.json"

# tag, html url, publication date, then "name<TAB>api url<TAB>download url"
# per asset.
python3 -I - "$tmp/release.json" > "$tmp/release.txt" <<'EOF'
import json, sys
d = json.load(open(sys.argv[1]))
print(d["tag_name"]); print(d["html_url"]); print(d.get("published_at") or "")
for a in d.get("assets", []):
    print(a["name"] + "\t" + a["url"] + "\t" + a["browser_download_url"])
EOF
tag="$(sed -n 1p "$tmp/release.txt")"
page="$(sed -n 2p "$tmp/release.txt")"
published="$(sed -n 3p "$tmp/release.txt")"
bin_line="$(tail -n +4 "$tmp/release.txt" | grep -E '^EasyVNA-H4-[^	]*\.bin	' | head -1 || true)"
sums_line="$(tail -n +4 "$tmp/release.txt" | grep -E '^SHA256SUMS	' | head -1 || true)"
[ -n "$bin_line" ] || { echo "update-firmware: release $tag has no EasyVNA-H4-*.bin" >&2; exit 1; }
[ -n "$sums_line" ] || { echo "update-firmware: release $tag has no SHA256SUMS" >&2; exit 1; }
file="${bin_line%%	*}"
case "$file" in */*|.*) echo "update-firmware: odd asset name $file" >&2; exit 1 ;; esac
# fetch OUT LINE: the asset through the API (application/octet-stream), else
# its github.com download link (which sometimes answers 504 for a while).
fetch() {
  local rest="${2#*	}"
  curl -fsSL --retry 3 "${auth[@]}" -H 'Accept: application/octet-stream' -o "$1" "${rest%%	*}" ||
    curl -fsSL --retry 3 -o "$1" "${rest#*	}"
}
fetch "$tmp/$file" "$bin_line"
fetch "$tmp/SHA256SUMS" "$sums_line"

want="$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")"
got="$(sha256sum "$tmp/$file" | cut -d' ' -f1)"
[ -n "$want" ] || { echo "update-firmware: $file is not listed in SHA256SUMS" >&2; exit 1; }
[ "$want" = "$got" ] || { echo "update-firmware: SHA-256 mismatch for $file: $got, SHA256SUMS says $want" >&2; exit 1; }
size="$(wc -c < "$tmp/$file" | tr -d ' ')"
# The firmware area of the H4 ends at 0x08023800 (145408 bytes): the
# calibration and configuration live after it.
[ "$size" -gt 1024 ] && [ "$size" -le 145408 ] || { echo "update-firmware: $file has an unexpected size ($size bytes)" >&2; exit 1; }

mkdir -p "$DIR"
if [ -f "$DIR/manifest.json" ] && grep -q "\"sha256\": \"$got\"" "$DIR/manifest.json" && [ -f "$DIR/$file" ]; then
  echo "update-firmware: $file ($tag) is already bundled"
  exit 0
fi
find "$DIR" -maxdepth 1 -name '*.bin' -delete
cp "$tmp/$file" "$DIR/$file"
cat > "$DIR/manifest.json" <<EOF
{
  "name": "EasyVNA",
  "version": "$tag",
  "file": "$file",
  "sha256": "$got",
  "size": $size,
  "target": "NanoVNA-H4 / STM32F303",
  "flashSize": 262144,
  "published": "$published",
  "source": "$page",
  "license": "GPL-3.0"
}
EOF
echo "update-firmware: bundled $file ($tag, $size bytes, sha256 $got)"
