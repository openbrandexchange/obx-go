#!/bin/sh
# Prints the CHANGELOG.md section of one version (the release notes).
#   scripts/release-notes.sh 0.1.0
set -eu
version=${1:?usage: release-notes.sh <version without v>}
notes=$(awk -v v="$version" '
  $0 ~ "^## \\[" v "\\]" { on = 1; next }
  on && /^## \[/ { exit }
  on && /^\[[^]]+\]: / { exit }
  on { print }
' "$(dirname "$0")/../CHANGELOG.md")
if [ -z "$(printf '%s' "$notes" | tr -d '[:space:]')" ]; then
  echo "CHANGELOG.md has no section for $version" >&2
  exit 1
fi
printf '%s\n' "$notes"
