#!/bin/sh
# Build and push a container image without Docker (when Docker is not available):
# cross-compile obx-conformance for linux/amd64 and append it as one layer to alpine with crane
# (go install github.com/google/go-containerregistry/cmd/crane@latest).
#
#   sh push-image.sh checker   # prints registry.cloudflare.com/<account>/obx-checker:<tag>
#   sh push-image.sh demo
#
# The image is the same as the Dockerfile's. Use the printed reference as "image" in a copy of
# wrangler.jsonc to deploy without Docker.
set -eu
what="$1"
: "${CLOUDFLARE_ACCOUNT_ID:?set CLOUDFLARE_ACCOUNT_ID}"
cd "$(dirname "$0")"
root=$(cd ../.. && pwd)
tag=$(git -C "$root" rev-parse --short=12 HEAD)$(git -C "$root" diff --quiet HEAD -- . ':!deploy' || echo -dirty)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/root/usr/local/bin"
(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o "$work/root/usr/local/bin/obx-conformance" ./cmd/obx-conformance)
tar -C "$work/root" --uid 0 --gid 0 --uname root --gname root -cf "$work/layer.tar" usr
export DOCKER_CONFIG="$work/docker"
npx wrangler containers registries credentials registry.cloudflare.com --push --pull --json 2>/dev/null |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["password"])' |
  crane auth login registry.cloudflare.com -u v1 --password-stdin >/dev/null
case "$what" in
checker)
  name=obx-checker
  set -- --entrypoint obx-conformance,check-server,--addr,:8080,--max-concurrent,1,--max-requests,800,--max-mib,32,--min-interval,100ms,--total-timeout,3m ;;
demo)
  name=obx-demo
  set -- --entrypoint obx-conformance,mock,--read-only,--addr,:8080,--rate-limit,1200 ;;
*) echo "usage: push-image.sh checker|demo" >&2; exit 2 ;;
esac
ref="registry.cloudflare.com/$CLOUDFLARE_ACCOUNT_ID/$name:$tag"
crane append --platform linux/amd64 -b alpine:3.22 -f "$work/layer.tar" -t "$ref-base" >/dev/null
crane mutate "$ref-base" "$@" --user 10001 --exposed-ports 8080 -t "$ref" >/dev/null
echo "$ref"
