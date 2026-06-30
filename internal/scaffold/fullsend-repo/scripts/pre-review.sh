#!/usr/bin/env bash
# pre-review.sh — Validate review inputs before the agent runs.
#
# Runs on the host via the harness pre_script mechanism.
#
# Required environment variables (set by the workflow):
#   PR_NUMBER      — must be a positive integer
#   REPO_FULL_NAME — must be owner/repo format
#   GITHUB_PR_URL  — must be a valid GitHub pull request URL
set -euo pipefail

echo "::notice::🔗 Review target: ${GITHUB_PR_URL:-}"

errors=0

if [[ ! "${PR_NUMBER:-}" =~ ^[1-9][0-9]*$ ]]; then
  echo "::error::PR_NUMBER must be a positive integer, got: '${PR_NUMBER:-}'"
  errors=$((errors + 1))
fi

if [[ ! "${REPO_FULL_NAME:-}" =~ ^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$ ]]; then
  echo "::error::REPO_FULL_NAME must be owner/repo format, got: '${REPO_FULL_NAME:-}'"
  errors=$((errors + 1))
fi

if [[ ! "${GITHUB_PR_URL:-}" =~ ^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/pull/[0-9]+$ ]]; then
  echo "::error::GITHUB_PR_URL format invalid, got: '${GITHUB_PR_URL:-}'"
  errors=$((errors + 1))
fi

URL_REPO="$(echo "${GITHUB_PR_URL:-}" | sed -E 's|https://github.com/([^/]+/[^/]+)/pull/.*|\1|')"
URL_PR="$(echo "${GITHUB_PR_URL:-}" | sed -E 's|.*/pull/([0-9]+)$|\1|')"

if [[ -n "${URL_REPO}" && "${URL_REPO}" != "${REPO_FULL_NAME:-}" ]]; then
  echo "::error::REPO_FULL_NAME does not match PR URL repo ('${REPO_FULL_NAME:-}' vs '${URL_REPO}')"
  errors=$((errors + 1))
fi
if [[ -n "${URL_PR}" && "${URL_PR}" != "${PR_NUMBER:-}" ]]; then
  echo "::error::PR_NUMBER does not match PR URL number ('${PR_NUMBER:-}' vs '${URL_PR}')"
  errors=$((errors + 1))
fi

if [[ "${errors}" -gt 0 ]]; then
  echo "::error::Input validation failed with ${errors} error(s). Aborting."
  exit 1
fi

echo "Input validation passed:"
echo "  PR_NUMBER=${PR_NUMBER}"
echo "  REPO_FULL_NAME=${REPO_FULL_NAME}"
echo "  GITHUB_PR_URL=${GITHUB_PR_URL}"

# ---------------------------------------------------------------------------
# Check PR state — skip review on merged or closed PRs
# ---------------------------------------------------------------------------
# Use REVIEW_TOKEN if available (set by the harness), fall back to GH_TOKEN.
_TOKEN="${REVIEW_TOKEN:-${GH_TOKEN:-}}"
if [[ -z "${_TOKEN}" ]]; then
  echo "No token available — skipping PR state check"
  exit 0
fi

PR_STATE="$(GH_TOKEN="${_TOKEN}" gh pr view "${PR_NUMBER}" \
  --repo "${REPO_FULL_NAME}" --json state --jq '.state' 2>/dev/null || true)"

if [[ -n "${PR_STATE}" && "${PR_STATE}" != "OPEN" ]]; then
  echo "::notice::PR #${PR_NUMBER} is ${PR_STATE} — skipping review"

  STATE_LOWER="$(echo "${PR_STATE}" | tr '[:upper:]' '[:lower:]')"
  COMMENT_BODY="Review skipped — this PR is already **${STATE_LOWER}**.

The \`/fs-review\` command only reviews open pull requests.

<sub>Posted by <a href=\"https://github.com/fullsend-ai/fullsend\">fullsend</a> pre-review check</sub>"

  printf '%s' "${COMMENT_BODY}" | GH_TOKEN="${_TOKEN}" gh issue comment "${PR_NUMBER}" \
    --repo "${REPO_FULL_NAME}" --body-file - 2>/dev/null || true

  exit 0
fi

echo "PR #${PR_NUMBER} is open — proceeding with review agent"
