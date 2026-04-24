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
  gh pr comment "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" --body "$(cat <<'EOF'
## Review: automated review

**Outcome:** failure
**Reason:** agent-no-output

The review agent did not produce a result. This PR was NOT reviewed.
Do not count this as an approval.

<sub>Posted by <a href="https://github.com/fullsend-ai/fullsend">fullsend</a> review agent</sub>
EOF
)"
  exit 1
fi

echo "Using result: ${RESULT_FILE}"

ACTION=$(jq -r '.action' "${RESULT_FILE}")

# Guard against stale reviews: if the PR head has moved since the agent
# reviewed it (e.g. force-push during the race window after cancel-in-progress),
# refuse to post a review against unreviewed code.
if [ "${ACTION}" != "failure" ]; then
  REVIEWED_SHA=$(jq -r '.head_sha // empty' "${RESULT_FILE}")
  CURRENT_SHA=$(gh pr view "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" --json headRefOid --jq '.headRefOid')
  if [ -n "${REVIEWED_SHA}" ] && [ "${REVIEWED_SHA}" != "${CURRENT_SHA}" ]; then
    echo ":⚠:Review stale: reviewed ${REVIEWED_SHA} but HEAD is now ${CURRENT_SHA}"
    gh pr comment "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" --body "$(cat <<EOF
## Review: automated review

**Outcome:** failure
**Reason:** stale-head

The review agent reviewed commit \`${REVIEWED_SHA}\` but the PR HEAD is now \`${CURRENT_SHA}\`. This review was discarded to avoid approving unreviewed code.

<sub>Posted by <a href="https://github.com/fullsend-ai/fullsend">fullsend</a> review agent</sub>
EOF
)"
    exit 1
  fi
fi

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
    ACTION="comment"

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

    ORIGINAL_BODY=$(jq -r '.body' "${RESULT_FILE}")
    MODIFIED_BODY="${ORIGINAL_BODY}${PROTECTED_NOTICE}"
    export MODIFIED_REVIEW_BODY="${MODIFIED_BODY}"
  fi
fi

BODY_FILE=$(mktemp)
trap 'rm -f "${BODY_FILE}"' EXIT

case "${ACTION}" in
  approve)          FLAG="--approve" ;;
  request-changes)  FLAG="--request-changes" ;;
  comment)          FLAG="--comment" ;;
  failure)
    REASON=$(jq -r '.reason' "${RESULT_FILE}")
    BODY=$(jq -r '.body // empty' "${RESULT_FILE}")
    if [ -n "${BODY}" ]; then
      printf '%s' "${BODY}" > "${BODY_FILE}"
    else
      cat > "${BODY_FILE}" <<EOF
## Review: automated review

**Outcome:** failure
**Reason:** ${REASON}

This PR was NOT reviewed. Do not count this as an approval.

<sub>Posted by <a href="https://github.com/fullsend-ai/fullsend">fullsend</a> review agent</sub>
EOF
    fi
    gh pr comment "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" --body-file "${BODY_FILE}"
    echo "Review posted: failure notice (${REASON}) on ${REPO_FULL_NAME}#${PR_NUMBER}"
    exit 0
    ;;
  *)
    echo "::error::Unknown action '${ACTION}'"
    exit 1
    ;;
esac

if [ -n "${MODIFIED_REVIEW_BODY:-}" ]; then
  printf '%s' "${MODIFIED_REVIEW_BODY}" > "${BODY_FILE}"
else
  jq -r '.body' "${RESULT_FILE}" > "${BODY_FILE}"
fi
gh pr review "${PR_NUMBER}" --repo "${REPO_FULL_NAME}" "${FLAG}" --body-file "${BODY_FILE}"

echo "Review posted: ${ACTION} on ${REPO_FULL_NAME}#${PR_NUMBER}"
