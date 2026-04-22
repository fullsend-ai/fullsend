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
