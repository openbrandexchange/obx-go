#!/usr/bin/env bash
# Runs obx-conformance once for the conformance action: the JSON report is the
# result, the Markdown report goes to the job summary and the text report to
# the log. Exit code: 0 conformant, 1 a required check failed, 2 could not run.
set -uo pipefail
bin="$RUNNER_TEMP/obx-conformance"
report="$RUNNER_TEMP/obx-conformance.json"
args=("$IN_PROFILE" --url "$IN_URL")
[ -n "${IN_ADMIN_USER:-}" ] && args+=(--admin-user "$IN_ADMIN_USER" --admin-password "${IN_ADMIN_PASSWORD:-}")
if [ -n "${IN_ARGS:-}" ]; then
  read -r -a extra <<< "$IN_ARGS"
  args+=("${extra[@]}")
fi
"$bin" "${args[@]}" --json > "$report"
code=$?
echo "report=$report" >> "$GITHUB_OUTPUT"
if [ -s "$report" ]; then
  python3 - "$report" "$GITHUB_OUTPUT" <<'PY'
import json, sys
rep = json.load(open(sys.argv[1]))
with open(sys.argv[2], "a") as out:
    out.write(f"passed={rep.get('passed', 0)}\nfailed={rep.get('failed', 0)}\n")
PY
  # The same report for people, from the JSON (no second run).
  "$bin" report --format markdown "$report" >> "$GITHUB_STEP_SUMMARY"
  "$bin" report --format text "$report"
fi
exit "$code"
