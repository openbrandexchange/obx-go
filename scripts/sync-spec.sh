#!/bin/sh
# Copy the JSON Schemas, vocabularies and fixtures from a checkout of the OBX
# spec repository into specdata/, where they are embedded into the SDK, the
# mock node and the conformance suite. Only the current spec version is kept.
# Usage: scripts/sync-spec.sh [path-to-spec-checkout]
set -eu
SPEC=${1:-../spec}
DEST=$(dirname "$0")/../specdata
if [ ! -f "$SPEC/schemas/Common.json" ] || [ ! -d "$SPEC/vocabularies" ]; then
  echo "spec checkout (with schemas/ and vocabularies/) not found at $SPEC" >&2
  exit 1
fi
rm -rf "$DEST/schemas" "$DEST/vocabularies" "$DEST/fixtures"
mkdir -p "$DEST/schemas" "$DEST/vocabularies" "$DEST/fixtures"
cp "$SPEC"/schemas/*.json "$DEST/schemas/"
cp "$SPEC"/vocabularies/*.json "$DEST/vocabularies/"
for d in kestrel expected ingest examples imports; do
  cp -R "$SPEC/fixtures/$d" "$DEST/fixtures/$d"
done
cp "$SPEC/fixtures/README.md" "$DEST/fixtures/README.md"
( cd "$SPEC" && git rev-parse HEAD 2>/dev/null || echo unknown ) > "$DEST/SPEC_COMMIT"
echo "synced spec $(cat "$DEST/SPEC_COMMIT") into $DEST"
