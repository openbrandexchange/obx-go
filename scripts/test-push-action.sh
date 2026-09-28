#!/usr/bin/env bash
# Tests push-action the way GitHub runs it: install.sh downloads a release
# archive from a local HTTP server and checks it against checksums.txt, then
# push.sh publishes the Kestrel fixtures to the mock portal (dry run and real
# run), writing step outputs and a job summary. A tampered archive must be
# refused. Needs Go, Python 3 and curl.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/obx-action.XXXXXX")
pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; wait "$p" 2>/dev/null || true; done
  rm -rf "$work"
}
trap cleanup EXIT
free_port() { python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
wait_http() { for _ in $(seq 1 100); do curl -fsS -o /dev/null "$1" 2>/dev/null && return 0; sleep 0.2; done; echo "no answer from $1" >&2; exit 1; }

case "$(uname -s)" in Linux) os=linux; runner_os=Linux ;; Darwin) os=darwin; runner_os=macOS ;; *) echo "unsupported OS" >&2; exit 1 ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64; runner_arch=X64 ;; arm64|aarch64) arch=arm64; runner_arch=ARM64 ;; *) echo "unsupported arch" >&2; exit 1 ;; esac

# A release folder like GoReleaser's.
mkdir -p "$work/release/stage" "$work/bin"
(cd "$root" && go build -trimpath -ldflags "-X main.Version=0.0.0-test" -o "$work/release/stage/obx" ./cmd/obx)
(cd "$root" && go build -o "$work/bin/obx-conformance" ./cmd/obx-conformance)
tar -czf "$work/release/obx_${os}_${arch}.tar.gz" -C "$work/release/stage" obx
sum() { if command -v sha256sum >/dev/null; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }
(cd "$work/release" && sum "obx_${os}_${arch}.tar.gz" > checksums.txt)
web=$(free_port)
python3 -m http.server "$web" --bind 127.0.0.1 --directory "$work/release" >/dev/null 2>&1 &
pids+=($!)
wait_http "http://127.0.0.1:$web/checksums.txt"

# The mock portal with the Kestrel fixtures.
mp=$(free_port)
"$work/bin/obx-conformance" mock --addr "127.0.0.1:$mp" > "$work/mock.txt" 2>&1 &
pids+=($!)
wait_http "http://127.0.0.1:$mp/.well-known/obx"
token=$(awk '/upload key:/ { print $3 }' "$work/mock.txt")
[ -n "$token" ] || { echo "no access key from the mock" >&2; cat "$work/mock.txt" >&2; exit 1; }

gh_env() {
  export RUNNER_OS=$runner_os RUNNER_ARCH=$runner_arch RUNNER_TEMP="$work/runner-$1"
  export GITHUB_PATH="$work/path-$1" GITHUB_OUTPUT="$work/output-$1" GITHUB_STEP_SUMMARY="$work/summary-$1"
  mkdir -p "$RUNNER_TEMP"; : > "$GITHUB_PATH"; : > "$GITHUB_OUTPUT"; : > "$GITHUB_STEP_SUMMARY"
}

echo "== install.sh checks and installs the archive"
gh_env ok
OBX_VERSION=v0.0.0-test OBX_RELEASE_BASE="http://127.0.0.1:$web" bash "$root/push-action/install.sh" | grep -q "obx 0.0.0-test"
bin=$(cat "$GITHUB_PATH")
[ -x "$bin/obx" ]

echo "== push.sh: dry run, then a real run"
fixtures="$work/kestrel"
cp -R "$root/specdata/fixtures/kestrel" "$fixtures"
rm -f "$fixtures/images.json"
for dry in true false; do
  : > "$GITHUB_OUTPUT"; : > "$GITHUB_STEP_SUMMARY"
  PATH="$bin:$PATH" OBX_URL="http://127.0.0.1:$mp" OBX_TOKEN="$token" OBX_CONFIG_DIR="$work/config" \
    IN_FOLDER="$fixtures" IN_DRY=$dry IN_FAIL=true bash "$root/push-action/push.sh" > "$work/log-$dry.txt"
  grep -q '^exit-code=0$' "$GITHUB_OUTPUT" || { echo "exit-code output missing (dry=$dry)" >&2; cat "$GITHUB_OUTPUT" >&2; exit 1; }
  grep -q 'Open Brand Exchange: published' "$GITHUB_STEP_SUMMARY" || { echo "no job summary (dry=$dry)" >&2; exit 1; }
done
grep -q 'dry run' "$work/log-true.txt"
grep -q 'rows: 40, images: 27' "$work/log-false.txt"

echo "== push.sh: missing inputs fail with a clear message"
: > "$GITHUB_OUTPUT"
if PATH="$bin:$PATH" OBX_URL="" OBX_TOKEN="" IN_FOLDER="$fixtures" bash "$root/push-action/push.sh" 2> "$work/err.txt"; then
  echo "push without url/token succeeded" >&2; exit 1
fi
grep -q 'url and token inputs are required' "$work/err.txt"

echo "== install.sh refuses an archive that does not match checksums.txt"
gh_env bad
printf 'x' >> "$work/release/obx_${os}_${arch}.tar.gz"
if OBX_RELEASE_BASE="http://127.0.0.1:$web" bash "$root/push-action/install.sh" 2> "$work/err.txt"; then
  echo "a tampered archive was installed" >&2; exit 1
fi
grep -q 'does not match checksums.txt' "$work/err.txt"
echo "push-action: all checks passed"
