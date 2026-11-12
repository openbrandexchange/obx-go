#!/usr/bin/env bash
# Installs obx for the push action: downloads the release archive for this
# runner, checks it against the release's checksums.txt and puts obx on PATH.
# Env: OBX_VERSION ("latest" or a tag such as v0.2.0), RUNNER_OS, RUNNER_ARCH,
# RUNNER_TEMP, GITHUB_PATH; OBX_RELEASE_BASE overrides the download location
# (a mirror, or a local folder served over HTTP in tests).
set -euo pipefail
case "${RUNNER_OS}" in
  Linux) os=linux ;;
  macOS) os=darwin ;;
  Windows) os=windows ;;
  *) echo "::error::obx: unsupported runner ${RUNNER_OS}" >&2; exit 1 ;;
esac
case "${RUNNER_ARCH}" in
  X64) arch=amd64 ;;
  ARM64) arch=arm64 ;;
  *) echo "::error::obx: unsupported architecture ${RUNNER_ARCH}" >&2; exit 1 ;;
esac
version=${OBX_VERSION:-latest}
if [ -n "${OBX_RELEASE_BASE:-}" ]; then
  base=${OBX_RELEASE_BASE%/}
elif [ "$version" = "latest" ]; then
  base="https://github.com/openbrandexchange/obx-go/releases/latest/download"
else
  base="https://github.com/openbrandexchange/obx-go/releases/download/${version}"
fi
ext=tar.gz
[ "$os" = windows ] && ext=zip
archive="obx_${os}_${arch}.${ext}"
dir="${RUNNER_TEMP}/obx-bin"
mkdir -p "$dir"
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}
if curl -fsSL --retry 3 -o "$dir/$archive" "$base/$archive" && curl -fsSL --retry 3 -o "$dir/checksums.txt" "$base/checksums.txt"; then
  want=$(awk -v f="$archive" '$2 == f { print $1 }' "$dir/checksums.txt")
  got=$(sha256 "$dir/$archive")
  if [ -z "$want" ] || [ "$want" != "$got" ]; then
    echo "::error::obx: the downloaded $archive does not match checksums.txt (want ${want:-nothing}, got $got)" >&2
    exit 1
  fi
  if [ "$ext" = zip ]; then
    unzip -o -q "$dir/$archive" -d "$dir"
  else
    tar -xzf "$dir/$archive" -C "$dir" obx
  fi
elif command -v go >/dev/null 2>&1; then
  echo "obx: no release archive at $base; building obx from source" >&2
  GOBIN="$dir" go install "github.com/openbrandexchange/obx-go/cmd/obx@${version}"
else
  echo "::error::obx: could not download $base/$archive and Go is not installed to build it" >&2
  exit 1
fi
echo "$dir" >> "$GITHUB_PATH"
"$dir/obx" version
