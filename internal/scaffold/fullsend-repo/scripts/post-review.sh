#!/usr/bin/env bash
# Post-script: post the review agent's result to GitHub.
#
# Runs on the GitHub Actions runner AFTER the sandbox is destroyed.
# CWD is runDir.
#
# This script is the sole enforcement point for protected-path checks:
# if the PR touches sensitive paths, an "approve" action is downgraded
# to "comment" so only a human can grant approval.
#
# Required environment variables:
#   REVIEW_TOKEN    — token with pull-requests:write on the target repo
#   PR_NUMBER       — GitHub PR number
#   REPO_FULL_NAME  — owner/repo (e.g. my-org/my-repo)
#
# Exit codes:
#   0 — review posted
#   1 — error (review not posted or fallback comment posted)
set -euo pipefail

: "${REVIEW_TOKEN:?REVIEW_TOKEN is required}"
: "${PR_NUMBER:?PR_NUMBER is required}"
if ! [[ "${PR_NUMBER}" =~ ^[0-9]+$ ]]; then
  echo "::error::PR_NUMBER must be a positive integer"
  exit 1
fi
: "${REPO_FULL_NAME:?REPO_FULL_NAME is required}"

echo "::add-mask::${REVIEW_TOKEN}"
export GH_TOKEN="${REVIEW_TOKEN}"

# Refuse to post reviews on merged or closed PRs
PR_STATE=$(gh pr view "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" --json state --jq '.state')
if [ "${PR_STATE}" != "OPEN" ]; then
  echo "PR is ${PR_STATE}, skipping review"
  exit 0
fi

# Find the agent result from the last iteration
RESULT_FILE=$(find .  -maxdepth 4 -path '*/iteration-*/output/agent-result.json' | sort -V | tail -1)

if [ -z "${RESULT_FILE}" ] || [ ! -f "${RESULT_FILE}" ]; then
  echo "::error::No agent-result.json found — posting failure notice"
  echo '{"action":"failure","reason":"agent-no-output"}' | \
    fullsend post-review \
      --repo "${REPO_FULL_NAME}" \
      --pr "${PR_NUMBER}" \
      --token "${REVIEW_TOKEN}" \
      --result -
  exit 1
fi

echo "Using result: ${RESULT_FILE}"

ACTION=$(jq -r '.action' "${RESULT_FILE}")

# ---------------------------------------------------------------------------
# Protected-path check: the review agent must not approve PRs that touch
# sensitive paths. If the PR modifies any of these, downgrade "approve" to
# "comment" so only a human can grant approval. This is the sole enforcement
# point — the code agent is free to propose changes to any path.
# ---------------------------------------------------------------------------
REVIEW_PROTECTED_PATHS=(
  ".github/"
  ".claude/"
  "agents/"
  "harness/"
  "policies/"
  "scripts/"
  "api-servers/"
  "CODEOWNERS"
  ".pre-commit-config.yaml"
  ".gitattributes"
)

if [ "${ACTION}" = "approve" ]; then
  PR_FILES=$(gh pr view "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" --json files --jq '.files[].path')
  if [ -z "${PR_FILES}" ]; then
    echo "::error::Failed to fetch PR files or PR has no changed files — refusing to approve"
    exit 1
  fi

  PROTECTED_MATCHES=""
  while IFS= read -r file; do
    [ -z "${file}" ] && continue
    for pattern in "${REVIEW_PROTECTED_PATHS[@]}"; do
      if [[ "${file}" == "${pattern}"* ]]; then
        PROTECTED_MATCHES="${PROTECTED_MATCHES}${file}"$'\n'
        break
      fi
    done
  done <<< "${PR_FILES}"

  if [ -n "${PROTECTED_MATCHES}" ]; then
    echo "PR touches protected paths — downgrading approve to comment"
    echo "${PROTECTED_MATCHES}" | sed '/^$/d' | sed 's/^/  /'

    PROTECTED_NOTICE=$'\n\n---\n\n'
    PROTECTED_NOTICE+=$'> **Protected paths detected** — this PR modifies files under one or more\n'
    PROTECTED_NOTICE+=$'> protected paths. The review agent cannot approve PRs that touch these paths.\n'
    PROTECTED_NOTICE+=$'> A human reviewer must approve this PR.\n'
    PROTECTED_NOTICE+=$'>\n'
    PROTECTED_NOTICE+=$'> Protected files in this PR:\n'
    while IFS= read -r f; do
      [ -z "${f}" ] && continue
      PROTECTED_NOTICE+="> - \`${f}\`"$'\n'
    done <<< "${PROTECTED_MATCHES}"

    # Rewrite the result file with downgraded action and appended notice.
    MODIFIED_RESULT=$(mktemp)
    trap 'rm -f "${MODIFIED_RESULT}"' EXIT
    jq --arg notice "${PROTECTED_NOTICE}" \
      '.action = "comment" | .body = (.body + $notice)' \
      "${RESULT_FILE}" > "${MODIFIED_RESULT}"
    RESULT_FILE="${MODIFIED_RESULT}"
  fi
fi

fullsend post-review \
  --repo "${REPO_FULL_NAME}" \
  --pr "${PR_NUMBER}" \
  --token "${REVIEW_TOKEN}" \
  --result "${RESULT_FILE}"

echo "Review posted on ${REPO_FULL_NAME}#${PR_NUMBER}"
