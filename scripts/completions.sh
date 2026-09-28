#!/bin/sh
# Writes the shell completion scripts of obx into completions/ (packed into the
# release archives by GoReleaser; also handy for distribution packages).
set -eu
cd "$(dirname "$0")/.."
mkdir -p completions
for shell in bash zsh fish powershell; do
  OBX_LANG=en go run ./cmd/obx completion "$shell" > "completions/obx.$shell"
done
echo "completions written to completions/"
