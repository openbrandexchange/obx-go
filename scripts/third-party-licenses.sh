#!/bin/sh
# Collects the licence and notice files of every Go module compiled into obx
# and obx-conformance into third-party-licenses/ (packed into the release
# archives and the container image; BSD licences require it). Needs go-licenses:
#   go install github.com/google/go-licenses/v2@v2.0.1
set -eu
cd "$(dirname "$0")/.."
if ! command -v go-licenses >/dev/null 2>&1; then
  echo "go-licenses is not installed: go install github.com/google/go-licenses/v2@v2.0.1" >&2
  exit 1
fi
rm -rf third-party-licenses
# go-licenses must look at the standard library of the toolchain that builds
# obx (go.mod's toolchain line), not the one it was built with.
GOROOT=$(go env GOROOT)
export GOROOT
go-licenses save ./cmd/obx ./cmd/obx-conformance --save_path third-party-licenses \
  --ignore github.com/openbrandexchange/obx-go 2>&1 | grep -v '^W[0-9]\|^/.*\.s$' || true
test -f third-party-licenses/golang.org/x/text/LICENSE
echo "third-party licences written to third-party-licenses/"
