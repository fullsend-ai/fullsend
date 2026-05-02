#!/usr/bin/env bash
# Pre-script: validate workflow_dispatch inputs before the agent runs.
#
# Prevents malformed or malicious event_payload from reaching the sandbox.
# Runs on the GitHub Actions runner BEFORE sandbox creation.
#
# Required environment variables (set by the workflow):
#   ISSUE_NUMBER       — must be a positive integer
#   REPO_FULL_NAME     — must be owner/repo format
#   GITHUB_ISSUE_URL   — must be a valid GitHub issue URL
set -euo pipefail

echo "::notice::🔗 Code target: ${GITHUB_ISSUE_URL:-}"

errors=0

if [[ ! "${ISSUE_NUMBER:-}" =~ ^[1-9][0-9]*$ ]]; then
  echo "::error::ISSUE_NUMBER must be a positive integer, got: '${ISSUE_NUMBER:-}'"
  errors=$((errors + 1))
fi

if [[ ! "${REPO_FULL_NAME:-}" =~ ^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$ ]]; then
  echo "::error::REPO_FULL_NAME must be owner/repo format, got: '${REPO_FULL_NAME:-}'"
  errors=$((errors + 1))
fi

if [[ ! "${GITHUB_ISSUE_URL:-}" =~ ^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/issues/[0-9]+$ ]]; then
  echo "::error::GITHUB_ISSUE_URL format invalid, got: '${GITHUB_ISSUE_URL:-}'"
  errors=$((errors + 1))
fi

URL_REPO="$(echo "${GITHUB_ISSUE_URL:-}" | sed -E 's|https://github.com/([^/]+/[^/]+)/issues/.*|\1|')"
URL_ISSUE="$(echo "${GITHUB_ISSUE_URL:-}" | sed -E 's|.*/issues/([0-9]+)$|\1|')"

if [[ -n "${URL_REPO}" && "${URL_REPO}" != "${REPO_FULL_NAME:-}" ]]; then
  echo "::error::REPO_FULL_NAME does not match issue URL repo ('${REPO_FULL_NAME:-}' vs '${URL_REPO}')"
  errors=$((errors + 1))
fi
if [[ -n "${URL_ISSUE}" && "${URL_ISSUE}" != "${ISSUE_NUMBER:-}" ]]; then
  echo "::error::ISSUE_NUMBER does not match issue URL number ('${ISSUE_NUMBER:-}' vs '${URL_ISSUE}')"
  errors=$((errors + 1))
fi

if [[ "${errors}" -gt 0 ]]; then
  echo "::error::Input validation failed with ${errors} error(s). Aborting."
  exit 1
fi

echo "Input validation passed:"
echo "  ISSUE_NUMBER=${ISSUE_NUMBER}"
echo "  REPO_FULL_NAME=${REPO_FULL_NAME}"
echo "  GITHUB_ISSUE_URL=${GITHUB_ISSUE_URL}"

# ---------------------------------------------------------------------------
# Check for existing human PRs linked to this issue
# ---------------------------------------------------------------------------
SKIP_PR_CHECK=false

if [[ -z "${GH_TOKEN:-}" ]]; then
  echo "GH_TOKEN not set — skipping existing-PR check"
  SKIP_PR_CHECK=true
fi

if [[ "${CODE_FORCE:-}" == "true" ]]; then
  echo "CODE_FORCE=true — skipping existing-PR check"
  SKIP_PR_CHECK=true
fi

if [[ "${SKIP_PR_CHECK}" != "true" ]]; then
  BOT_LOGIN="${FULLSEND_BOT_LOGIN:-fullsend-ai[bot]}"
  BOT_LOGIN_RE='^[][a-zA-Z0-9._-]+$'
  if [[ ! "${BOT_LOGIN}" =~ ${BOT_LOGIN_RE} ]]; then
    echo "::error::FULLSEND_BOT_LOGIN contains invalid characters: '${BOT_LOGIN}'"
    exit 1
  fi

  echo "Checking for existing open PRs linked to issue #${ISSUE_NUMBER}..."

  # Use the timeline API to find PRs that reference this issue via
  # cross-reference events. This avoids substring false positives from
  # gh pr list --search (e.g. issue #42 matching a PR mentioning #421).
  PR_LIST_EXIT=0
  HUMAN_PR_LINES="$(gh api "repos/${REPO_FULL_NAME}/issues/${ISSUE_NUMBER}/timeline" \
    --paginate --jq '
    [.[]
      | select(.event == "cross-referenced")
      | select(.source.issue.pull_request != null)
      | select(.source.issue.state == "open")
      | select(.source.issue.user.login != "'"${BOT_LOGIN}"'")
      | "\(.source.issue.number)\t\(.source.issue.user.login)\t\(.source.issue.html_url)"
    ] | unique | .[]' 2>&1)" || PR_LIST_EXIT=$?

  if [[ ${PR_LIST_EXIT} -ne 0 ]]; then
    echo "::warning::Failed to check for existing PRs (exit ${PR_LIST_EXIT}): ${HUMAN_PR_LINES}"
    HUMAN_PR_LINES=""
  fi

  if [[ -n "${HUMAN_PR_LINES}" ]]; then
    FIRST_PR_NUM="$(echo "${HUMAN_PR_LINES}" | head -1 | cut -f1)"
    FIRST_PR_AUTHOR="$(echo "${HUMAN_PR_LINES}" | head -1 | cut -f2)"

    echo "::notice::Found existing human PR #${FIRST_PR_NUM} by @${FIRST_PR_AUTHOR}"

    gh label create "pr-open" --repo "${REPO_FULL_NAME}" \
      --description "An open PR already addresses this issue" --color "D4C5F9" \
      --force 2>/dev/null || true
    gh api "repos/${REPO_FULL_NAME}/issues/${ISSUE_NUMBER}/labels" \
      -f "labels[]=pr-open" --silent 2>/dev/null || true

    PR_LIST_MD=""
    while IFS=$'\t' read -r pr_num pr_author pr_url; do
      PR_LIST_MD="${PR_LIST_MD}
- #${pr_num} by @${pr_author}"
    done <<< "${HUMAN_PR_LINES}"

    COMMENT_BODY="An open PR already addresses this issue — skipping automated implementation.
${PR_LIST_MD}

To override, comment \`/code --force\` on this issue.

<sub>Posted by <a href=\"https://github.com/fullsend-ai/fullsend\">fullsend</a> pre-code check</sub>"

    # Check for existing bot comment to avoid duplicates.
    EXISTING_COMMENT="$(gh api "repos/${REPO_FULL_NAME}/issues/${ISSUE_NUMBER}/comments" \
      --jq '[.[] | select(.body | startswith("An open PR already addresses"))] | length' \
      2>/dev/null || echo "0")"

    if [[ "${EXISTING_COMMENT}" == "0" ]]; then
      printf '%s' "${COMMENT_BODY}" | gh issue comment "${ISSUE_NUMBER}" \
        --repo "${REPO_FULL_NAME}" --body-file - 2>/dev/null || true
    else
      echo "::notice::Skipping duplicate comment — bot already posted on issue #${ISSUE_NUMBER}"
    fi

    echo "Skipping code agent — existing PR(s) found for issue #${ISSUE_NUMBER}"
    exit 0
  fi

  echo "No existing human PRs found — proceeding with code agent"
fi
