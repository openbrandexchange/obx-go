#!/usr/bin/env bash
# Runs obx push for the push action and writes a job summary.
# Env: OBX_URL, OBX_TOKEN and the IN_* inputs of action.yml; GITHUB_OUTPUT,
# GITHUB_STEP_SUMMARY and RUNNER_TEMP as on GitHub Actions.
set -uo pipefail
args=(push)
[ -n "${IN_FOLDER:-}" ] && args+=("$IN_FOLDER")
[ -n "${IN_CSV:-}" ] && args+=(--csv "$IN_CSV")
[ -n "${IN_ASSETS:-}" ] && args+=(--assets-csv "$IN_ASSETS")
[ -n "${IN_IMAGES:-}" ] && args+=(--images "$IN_IMAGES")
[ -n "${IN_PACKAGE:-}" ] && args+=(--package "$IN_PACKAGE")
[ "${IN_MIRROR:-false}" = "true" ] && args+=(--mirror)
[ "${IN_MASS:-false}" = "true" ] && args+=(--allow-mass-unpublish)
[ "${IN_DRY:-false}" = "true" ] && args+=(--dry-run)
[ -n "${IN_LANG:-}" ] && args+=(--language "$IN_LANG")
if [ -z "${OBX_URL:-}" ] || [ -z "${OBX_TOKEN:-}" ]; then
  echo "::error::obx: the url and token inputs are required (store them as repository secrets)" >&2
  echo "exit-code=1" >> "$GITHUB_OUTPUT"
  exit 1
fi
summary="${RUNNER_TEMP}/obx-summary.txt"
obx "${args[@]}" 2>&1 | tee "$summary"
code=${PIPESTATUS[0]}
echo "exit-code=$code" >> "$GITHUB_OUTPUT"
{
  case "$code" in
    0) echo "### Open Brand Exchange: published" ;;
    2) echo "### Open Brand Exchange: published, some rows need attention" ;;
    *) echo "### Open Brand Exchange: failed" ;;
  esac
  echo '```'
  # The progress lines are for terminals; the summary keeps the result.
  grep -v '^  images  *[0-9]*%' "$summary" || true
  echo '```'
} >> "$GITHUB_STEP_SUMMARY"
if [ "$code" = "2" ] && [ "${IN_FAIL:-true}" != "true" ]; then
  exit 0
fi
exit "$code"
