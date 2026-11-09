#!/usr/bin/env bash
# Post or update the single user-facing comment for an agent launch that
# failed before the agent process started. This intentionally uses gh directly
# because the fullsend CLI may be the dependency that failed to install.
set -euo pipefail

if [[ -z "${GH_TOKEN:-}" ]]; then
  echo "::warning::Skipping startup failure comment: GH_TOKEN is required" >&2
  exit 1
fi
FULLSEND_FAILURE_REPOSITORY="${FULLSEND_FAILURE_REPOSITORY:-}"
FULLSEND_FAILURE_NUMBER="${FULLSEND_FAILURE_NUMBER:-}"
FULLSEND_FAILURE_AGENT="${FULLSEND_FAILURE_AGENT:-}"
FULLSEND_FAILURE_ROLE="${FULLSEND_FAILURE_ROLE:-${FULLSEND_FAILURE_AGENT}}"
FULLSEND_FAILURE_RUN_URL="${FULLSEND_FAILURE_RUN_URL:-}"
FULLSEND_FAILURE_CATEGORY="${FULLSEND_FAILURE_CATEGORY:-}"

if [[ ! "${FULLSEND_FAILURE_REPOSITORY}" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid repository" >&2
  exit 1
fi
if [[ ! "${FULLSEND_FAILURE_NUMBER}" =~ ^[1-9][0-9]*$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid issue number" >&2
  exit 1
fi
if [[ ! "${FULLSEND_FAILURE_AGENT}" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid agent" >&2
  exit 1
fi
if [[ ! "${FULLSEND_FAILURE_ROLE}" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid role" >&2
  exit 1
fi
case "${FULLSEND_FAILURE_CATEGORY}" in
  "workflow setup"|"runtime setup"|"CLI setup"|"token mint") ;;
  *)
    echo "::warning::Skipping startup failure comment: invalid category" >&2
    exit 1
    ;;
esac
if [[ ! "${FULLSEND_FAILURE_RUN_URL}" =~ ^https://[A-Za-z0-9.-]+/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+/actions/runs/[1-9][0-9]*$ ]]; then
  echo "::warning::Skipping startup failure comment: invalid workflow URL" >&2
  exit 1
fi

MARKER="<!-- fullsend:agent-startup-failure:${FULLSEND_FAILURE_AGENT}:${FULLSEND_FAILURE_NUMBER} -->"
BODY_FILE="${RUNNER_TEMP:-/tmp}/fullsend-agent-startup-failure.md"
{
  printf '%s\n\n' "${MARKER}"
  printf '⚠️ **Fullsend %s agent did not run**\n\n' "${FULLSEND_FAILURE_AGENT}"
  printf 'The agent could not start because of a **%s** failure.\n\n' "${FULLSEND_FAILURE_CATEGORY}"
  printf '[View the workflow run](%s)\n\n' "${FULLSEND_FAILURE_RUN_URL}"
} > "${BODY_FILE}"

BOT_LOGIN="$(gh api graphql -f query='{ viewer { login } }' --jq '.data.viewer.login')"
COMMENTS_JSON="$(gh api --paginate --slurp \
  "repos/${FULLSEND_FAILURE_REPOSITORY}/issues/${FULLSEND_FAILURE_NUMBER}/comments")"
TRUSTED_BOT_LOGINS="github-actions,fullsend-ai-${FULLSEND_FAILURE_ROLE}"
# FULLSEND_FAILURE_TRUSTED_BOTS contains app-set prefixes (for example,
# "fullsend-ai" or "acme"), not arbitrary login prefixes. Expand each set to
# the known agent identities so a lookalike app cannot update this comment.
TRUSTED_BOTS_JSON="$(jq -Rn \
  --arg current "${BOT_LOGIN}" \
  --arg logins "${TRUSTED_BOT_LOGINS}" \
  --arg app_sets "${FULLSEND_FAILURE_TRUSTED_BOTS:-}" \
  --arg role "${FULLSEND_FAILURE_ROLE}" \
  '[ $current ]
   + ($logins | split(",") | map(select(length > 0)))
   + ($app_sets | split(",") | map(gsub("^\\s+|\\s+$"; "") | select(length > 0)
      | . as $set
      | [$set + "-" + $role]) | add)
   | map(sub("\\[bot\\]$"; ""))
   | unique')"
COMMENT_ID="$(printf '%s' "${COMMENTS_JSON}" | jq -r \
  --arg marker "${MARKER}" --argjson trusted "${TRUSTED_BOTS_JSON}" \
  'add | map(select((.user.type == "Bot")
    and ((.user.login | sub("\\[bot\\]$"; "")) as $login
      | (($trusted | index($login)) != null))
    and (.body | startswith($marker)))) | last | .id // empty')"

if [[ -n "${COMMENT_ID}" ]]; then
  gh api --method PATCH "repos/${FULLSEND_FAILURE_REPOSITORY}/issues/comments/${COMMENT_ID}" \
    -f body="$(<"${BODY_FILE}")" >/dev/null
  echo "Updated startup failure comment ${COMMENT_ID}"
else
  gh api --method POST "repos/${FULLSEND_FAILURE_REPOSITORY}/issues/${FULLSEND_FAILURE_NUMBER}/comments" \
    -f body="$(<"${BODY_FILE}")" >/dev/null
  echo "Posted startup failure comment"
fi
