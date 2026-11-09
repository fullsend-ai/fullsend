#!/usr/bin/env bash
# Post or update the single user-facing comment for an agent launch that
# failed before the agent process started. This intentionally uses gh directly
# because the fullsend CLI may be the dependency that failed to install.
set -euo pipefail

: "${GH_TOKEN:?GH_TOKEN is required}"
: "${FULLSEND_FAILURE_REPOSITORY:?FULLSEND_FAILURE_REPOSITORY is required}"
FULLSEND_FAILURE_REPOSITORY="${FULLSEND_FAILURE_REPOSITORY:-}"
FULLSEND_FAILURE_NUMBER="${FULLSEND_FAILURE_NUMBER:-}"
FULLSEND_FAILURE_AGENT="${FULLSEND_FAILURE_AGENT:-}"
FULLSEND_FAILURE_RUN_URL="${FULLSEND_FAILURE_RUN_URL:-}"
FULLSEND_FAILURE_CATEGORY="${FULLSEND_FAILURE_CATEGORY:-}"

if [[ ! "${FULLSEND_FAILURE_REPOSITORY}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid repository" >&2
  exit 0
fi
if [[ ! "${FULLSEND_FAILURE_NUMBER}" =~ ^[1-9][0-9]*$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid issue number" >&2
  exit 0
fi
if [[ ! "${FULLSEND_FAILURE_AGENT}" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid agent" >&2
  exit 0
fi
case "${FULLSEND_FAILURE_CATEGORY}" in
  "workflow setup"|"runtime setup"|"CLI setup"|"token mint") ;;
  *)
    echo "::warning::Skipping startup failure comment: invalid category" >&2
    exit 0
    ;;
esac
if [[ ! "${FULLSEND_FAILURE_RUN_URL}" =~ ^https://[^/]+/.*/actions/runs/[1-9][0-9]*$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid workflow URL" >&2
  exit 0
fi

MARKER="<!-- fullsend:agent-startup-failure:${FULLSEND_FAILURE_AGENT}:${FULLSEND_FAILURE_NUMBER} -->"
BODY_FILE="${RUNNER_TEMP:-/tmp}/fullsend-agent-startup-failure.md"
{
  printf '%s\n\n' "${MARKER}"
  printf '⚠️ **Fullsend %s agent did not run**\n\n' "${FULLSEND_FAILURE_AGENT}"
  printf 'The agent could not start because of a **%s** failure during workflow setup.\n\n' "${FULLSEND_FAILURE_CATEGORY}"
  printf '[View the workflow run](%s)\n\n' "${FULLSEND_FAILURE_RUN_URL}"
} > "${BODY_FILE}"

BOT_LOGIN="$(gh api graphql -f query='{ viewer { login } }' --jq '.data.viewer.login')"
COMMENTS_JSON="$(gh api --paginate --slurp \
  "repos/${FULLSEND_FAILURE_REPOSITORY}/issues/${FULLSEND_FAILURE_NUMBER}/comments")"
COMMENT_ID="$(printf '%s' "${COMMENTS_JSON}" | jq -r \
  --arg marker "${MARKER}" --arg bot "${BOT_LOGIN}" \
  'add | map(select(.user.login == $bot and (.body | contains($marker)))) | last | .id // empty')"

if [[ -n "${COMMENT_ID}" ]]; then
  gh api --method PATCH "repos/${FULLSEND_FAILURE_REPOSITORY}/issues/comments/${COMMENT_ID}" \
    -f body="$(<"${BODY_FILE}")" >/dev/null
  echo "Updated startup failure comment ${COMMENT_ID}"
else
  gh api --method POST "repos/${FULLSEND_FAILURE_REPOSITORY}/issues/${FULLSEND_FAILURE_NUMBER}/comments" \
    -f body="$(<"${BODY_FILE}")" >/dev/null
  echo "Posted startup failure comment"
fi
