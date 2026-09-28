#!/usr/bin/env bash
# Cross-implementation interop run: the same automation against node-go,
# node-php and node-ts must give the same protocol output.
#
#   scripts/interop.sh [workspace]     workspace = folder with the sibling checkouts
#                                      (default: the parent of this repository)
#
# Per node: fresh start, first-run setup, `obx push` of spec/fixtures/kestrel,
# `obx pull` (all four image versions), then obx-interop trace (records the
# change feed, every product and every file hash, replays
# spec/fixtures/ingest/script.ndjson step by step), then an incremental and a
# fresh `obx pull`. Finally every trace and every mirror is compared.
#
# Needs: Go, Docker (node-php), Bun (node-ts), python3 (reads the creds file).
# NODES=go,php,ts picks a subset. KEEP=1 leaves the nodes running.
# PHP_RENDITIONS=client makes node-php behave like a node without an image library.
set -euo pipefail

here=$(cd "$(dirname "$0")/.." && pwd)
ws=${1:-$(dirname "$here")}
spec="$ws/spec"
nodes=${NODES:-go,php,ts}
work=$(mktemp -d "${TMPDIR:-/tmp}/obx-interop.XXXXXX")
code=INTEROP-SETUP-1
secret=interop-only-secret-key-0123456789abcdef
pids=()

# shellcheck disable=SC2329 # called by the EXIT trap
cleanup() {
  if [ "${KEEP:-0}" != 1 ]; then
    for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
    docker rm -f obx-php-interop >/dev/null 2>&1 || true
  fi
  echo "work folder: $work"
}
trap cleanup EXIT

port_of() { case $1 in go) echo 18081 ;; php) echo 18082 ;; ts) echo 18083 ;; esac; }

echo "building obx, obx-interop and node-go into $work"
(cd "$here" && go build -o "$work/obx" ./cmd/obx && go build -o "$work/obx-interop" ./cmd/obx-interop)

start() {
  local n=$1 port
  port=$(port_of "$n")
  case $n in
  go)
    (cd "$ws/node-go" && go build -o "$work/node-go" .)
    mkdir -p "$work/go-data"
    OBX_DATA_DIR="$work/go-data" PORT=$port OBX_PUBLIC_URL="http://localhost:$port" OBX_SETUP_CODE=$code \
      OBX_SECRET_KEY=$secret "$work/node-go" serve >"$work/go.log" 2>&1 &
    pids+=($!)
    ;;
  ts)
    mkdir -p "$work/ts-data"
    (cd "$ws/node-ts" && OBX_DATA_DIR="$work/ts-data" PORT=$port OBX_PUBLIC_URL="http://localhost:$port" \
      OBX_SETUP_CODE=$code OBX_SECRET_KEY=$secret exec bun src/adapters/bun/main.ts >"$work/ts.log" 2>&1) &
    pids+=($!)
    ;;
  php)
    docker build -q -t obx-node-php:interop "$ws/node-php" >/dev/null
    docker rm -f obx-php-interop >/dev/null 2>&1 || true
    docker run -d --name obx-php-interop -p "$port:8080" -e OBX_PUBLIC_URL="http://localhost:$port" \
      -e OBX_SETUP_CODE=$code -e OBX_RENDITIONS="${PHP_RENDITIONS:-auto}" obx-node-php:interop >/dev/null
    ;;
  esac
  for _ in $(seq 1 90); do
    if curl -s -o /dev/null "http://localhost:$port/.well-known/obx"; then return 0; fi
    sleep 1
  done
  echo "node $n did not start; see $work/$n.log" >&2
  exit 1
}

cred() { python3 -c "import json,sys;print(json.load(open(sys.argv[1]))[sys.argv[2]])" "$1" "$2"; }

pull() { # node folder
  OBX_CLIENT_ID=$(cred "$work/creds-$1.json" client_id) OBX_CLIENT_SECRET=$(cred "$work/creds-$1.json" client_secret) \
    "$work/obx" pull "http://localhost:$(port_of "$1")" --out "$2" --renditions original,web,square,thumb | tail -1
}

IFS=, read -r -a list <<<"$nodes"
for n in "${list[@]}"; do
  echo "== $n: start"
  start "$n"
  url="http://localhost:$(port_of "$n")"
  "$work/obx-interop" setup --url "$url" --setup-code $code --out "$work/creds-$n.json"
  echo "== $n: obx push"
  OBX_URL=$url OBX_TOKEN=$(cred "$work/creds-$n.json" ingest_token) HOME="$work/home-$n" "$work/obx" push "$spec/fixtures/kestrel" | tail -1
  echo "== $n: obx pull"
  pull "$n" "$work/mirror-$n"
  "$work/obx-interop" mirror --dir "$work/mirror-$n" --out "$work/pushed-$n.json"
  echo "== $n: trace + ingest script"
  "$work/obx-interop" trace --url "$url" --creds "$work/creds-$n.json" --fixtures "$spec/fixtures" --out "$work/trace-$n.json"
  echo "== $n: obx pull again (incremental) and into a new folder"
  pull "$n" "$work/mirror-$n"
  pull "$n" "$work/fresh-$n"
  "$work/obx-interop" mirror --dir "$work/mirror-$n" --out "$work/incremental-$n.json"
  "$work/obx-interop" mirror --dir "$work/fresh-$n" --out "$work/fresh-$n.json"
done

status=0
cmp() { # label files…
  local label=$1
  shift
  echo "== compare: $label"
  "$work/obx-interop" compare "$@" || status=1
}
files() { for n in "${list[@]}"; do printf '%s ' "$work/$1-$n.json"; done; }
if [ ${#list[@]} -gt 1 ]; then
  # shellcheck disable=SC2046
  cmp "protocol traces" $(files trace)
  # shellcheck disable=SC2046
  cmp "mirrors after obx push" $(files pushed)
fi
all=()
for n in "${list[@]}"; do all+=("$work/incremental-$n.json" "$work/fresh-$n.json"); done
cmp "mirrors after the script (incremental and fresh, every node)" "${all[@]}"
if [ $status = 0 ]; then echo "INTEROP OK"; else echo "INTEROP DIFFERENCES FOUND"; fi
exit $status
