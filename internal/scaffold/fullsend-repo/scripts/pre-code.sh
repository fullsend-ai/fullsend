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
