#!/usr/bin/env bash
# CLI runner command for the eval harness.
#
# Called by the harness as the runner.command. Setup and teardown are
# handled by before_each/after_each hooks — this script just runs
# fullsend with the right env vars.
#
# Args (from harness placeholders):
#   $1 — agent name (e.g., "triage")
#   $2 — workspace path (case workspace)
#   $3 — output directory
#
# Required env (injected by harness from hook outputs + execution.env):
#   FULLSEND_DIR    — path to the fullsend scaffold directory
#   GH_TOKEN        — GitHub token
#   FIXTURE_URL     — URL of the fixture (issue or PR)
#   FIXTURE_TYPE    — "issue" or "pull_request"
set -euo pipefail

AGENT="${1:?agent name required}"
# $2 is the workspace path (passed by harness, unused here)
OUTPUT_DIR="${3:?output dir required}"

FULLSEND_DIR="$(cd "${FULLSEND_DIR:?FULLSEND_DIR is required}" && pwd)"
FIXTURE_URL="${FIXTURE_URL:?FIXTURE_URL is required (set by before_each hook)}"
FIXTURE_TYPE="${FIXTURE_TYPE:?FIXTURE_TYPE is required (set by before_each hook)}"

# Clone the ephemeral repo as the target for fullsend run.
# The hook already created it and pushed content.
EPHEMERAL_REPO="${EPHEMERAL_REPO:?EPHEMERAL_REPO is required}"
TARGET_DIR=$(mktemp -d)
GH_CRED_HELPER='!f(){ echo "password=${GH_TOKEN}"; };f'
git -c "credential.helper=${GH_CRED_HELPER}" \
  clone "https://x-access-token@github.com/${EPHEMERAL_REPO}.git" "$TARGET_DIR"
git -C "$TARGET_DIR" config credential.helper "${GH_CRED_HELPER}"

cleanup() {
  # shellcheck disable=SC2317 # invoked indirectly via trap
  [[ -n "${ENV_FILE:-}" ]] && rm -f "$ENV_FILE"
  # shellcheck disable=SC2317
  [[ -n "${TARGET_DIR:-}" && -d "${TARGET_DIR:-}" ]] && rm -rf "$TARGET_DIR"
}
trap cleanup EXIT

# Build env file for fullsend run
ENV_FILE="${OUTPUT_DIR}/.eval-env"
install -m 0600 /dev/null "$ENV_FILE"
{
  echo "GH_TOKEN=${GH_TOKEN}"
  echo "PUSH_TOKEN=${GH_TOKEN}"
  echo "REVIEW_TOKEN=${GH_TOKEN}"

  case "$FIXTURE_TYPE" in
    issue)        echo "GITHUB_ISSUE_URL=${FIXTURE_URL}" ;;
    pull_request) echo "GITHUB_PR_URL=${FIXTURE_URL}" ;;
  esac

  [[ -n "${ANTHROPIC_VERTEX_PROJECT_ID:-}" ]] && echo "ANTHROPIC_VERTEX_PROJECT_ID=${ANTHROPIC_VERTEX_PROJECT_ID}"
  [[ -n "${GOOGLE_CLOUD_PROJECT:-}" ]]        && echo "GOOGLE_CLOUD_PROJECT=${GOOGLE_CLOUD_PROJECT}"
  [[ -n "${CLOUD_ML_REGION:-}" ]]             && echo "CLOUD_ML_REGION=${CLOUD_ML_REGION}"
  [[ -n "${GOOGLE_APPLICATION_CREDENTIALS:-}" ]] && echo "GOOGLE_APPLICATION_CREDENTIALS=${GOOGLE_APPLICATION_CREDENTIALS}"
} > "$ENV_FILE"

FULLSEND_BIN="$(command -v fullsend)"
EVAL_TIMEOUT="${EVAL_TIMEOUT:-1800}"

mkdir -p "$OUTPUT_DIR"

rc=0
timeout "$EVAL_TIMEOUT" fullsend run "$AGENT" \
  --fullsend-dir "${FULLSEND_DIR}" \
  --target-repo "$TARGET_DIR" \
  --env-file "$ENV_FILE" \
  --output-dir "$OUTPUT_DIR" \
  --fullsend-binary "$FULLSEND_BIN" \
  || rc=$?

if [[ $rc -ne 0 ]]; then
  echo "WARNING: fullsend run exited with status $rc" >&2
fi

# Remove env file to prevent secrets from being uploaded as artifacts
rm -f "$ENV_FILE"

# Copy metrics.json to the output root so score.py can find it.
# OUTPUT_DIR is {output_dir} from the harness, which is workspace/output.
# score.py loads files relative to case_dir/output, so metrics.json needs
# to be at OUTPUT_DIR/metrics.json (not OUTPUT_DIR/output/metrics.json).
METRICS_FILE=$(find "$OUTPUT_DIR" -maxdepth 3 -name metrics.json -not -path "$OUTPUT_DIR/metrics.json" 2>/dev/null | head -1)
if [[ -n "$METRICS_FILE" ]]; then
  cp "$METRICS_FILE" "$OUTPUT_DIR/metrics.json"
  echo "Copied metrics -> $OUTPUT_DIR/metrics.json"
fi

exit "$rc"
