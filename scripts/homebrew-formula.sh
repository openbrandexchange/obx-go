#!/bin/sh
# Renders packaging/homebrew/obx.rb.tmpl for a release.
#   scripts/homebrew-formula.sh <version without v> <checksums.txt> > obx.rb
set -eu
version=${1:?usage: homebrew-formula.sh <version> <checksums.txt>}
sums=${2:?usage: homebrew-formula.sh <version> <checksums.txt>}
here=$(dirname "$0")/..
sha() {
  s=$(awk -v f="obx_$1.tar.gz" '$2 == f { print $1 }' "$sums")
  if [ -z "$s" ]; then
    echo "no checksum for obx_$1.tar.gz in $sums" >&2
    exit 1
  fi
  echo "$s"
}
sed -e "s/@VERSION@/$version/g" \
    -e "s/@SHA_DARWIN_ARM64@/$(sha darwin_arm64)/" \
    -e "s/@SHA_DARWIN_AMD64@/$(sha darwin_amd64)/" \
    -e "s/@SHA_LINUX_ARM64@/$(sha linux_arm64)/" \
    -e "s/@SHA_LINUX_AMD64@/$(sha linux_amd64)/" \
    "$here/packaging/homebrew/obx.rb.tmpl"
