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
# Skip if GH_TOKEN is not available (best-effort check).
if [[ -z "${GH_TOKEN:-}" ]]; then
  echo "GH_TOKEN not set — skipping existing-PR check"
  echo "skipped=false" >> "${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

# Allow override when --force is in the trigger comment or CODE_FORCE is set.
echo "Evaluating force override: CODE_FORCE='${CODE_FORCE:-}' COMMENT_BODY='${COMMENT_BODY:-}'"
if [[ "${CODE_FORCE:-}" == "true" ]] || [[ "${COMMENT_BODY:-}" == *--force* ]]; then
  echo "Force override — skipping existing-PR check"
  echo "skipped=false" >> "${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

BOT_LOGIN="fullsend-ai[bot]"
CODER_BOT_LOGIN="fullsend-ai-coder[bot]"

echo "Checking for existing open PRs linked to issue #${ISSUE_NUMBER}..."

# Search for open PRs in the repo that mention the issue number.
# This catches PRs with "Closes #N", "Fixes #N", or "#N" in the body/title.
# Use gh's built-in --jq to filter out bot-authored PRs in one call.
HUMAN_PR_LINES="$(gh pr list --repo "${REPO_FULL_NAME}" --state open \
  --search "${ISSUE_NUMBER} in:body,title" \
  --json number,url,author \
  --jq "[.[] | select(.author.login != \"${BOT_LOGIN}\" and .author.login != \"${CODER_BOT_LOGIN}\")] | .[] | \"\(.number)\t\(.author.login)\t\(.url)\"" \
  2>/dev/null || true)"

if [[ -n "${HUMAN_PR_LINES}" ]]; then
  # Parse the first PR for the notice.
  FIRST_PR_NUM="$(echo "${HUMAN_PR_LINES}" | head -1 | cut -f1)"
  FIRST_PR_AUTHOR="$(echo "${HUMAN_PR_LINES}" | head -1 | cut -f2)"

  echo "::notice::Found existing human PR #${FIRST_PR_NUM} by @${FIRST_PR_AUTHOR}"

  # Apply pr-open label to signal work is already underway.
  gh label create "pr-open" --repo "${REPO_FULL_NAME}" \
    --description "An open PR already addresses this issue" --color "D4C5F9" \
    --force 2>/dev/null || true
  gh api "repos/${REPO_FULL_NAME}/issues/${ISSUE_NUMBER}/labels" \
    -f "labels[]=pr-open" --silent 2>/dev/null || true

  # Build a markdown list of existing PRs.
  PR_LIST_MD=""
  while IFS=$'\t' read -r pr_num pr_author _pr_url; do
    PR_LIST_MD="${PR_LIST_MD}
- #${pr_num} by @${pr_author}"
  done <<< "${HUMAN_PR_LINES}"

  SKIP_COMMENT="An open PR already addresses this issue — skipping automated implementation.
${PR_LIST_MD}

To override, comment \`/fs-code --force\` on this issue.

<sub>Posted by <a href=\"https://github.com/fullsend-ai/fullsend\">fullsend</a> pre-code check</sub>"

  printf '%s' "${SKIP_COMMENT}" | gh issue comment "${ISSUE_NUMBER}" \
    --repo "${REPO_FULL_NAME}" --body-file - 2>/dev/null || true

  echo "Skipping code agent — existing PR(s) found for issue #${ISSUE_NUMBER}"
  echo "skipped=true" >> "${GITHUB_OUTPUT:-/dev/null}"
  exit 0
fi

echo "No existing human PRs found — proceeding with code agent"
echo "skipped=false" >> "${GITHUB_OUTPUT:-/dev/null}"

# ---------------------------------------------------------------------------
# Auto-detect and install pre-commit tool dependencies
# ---------------------------------------------------------------------------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET_REPO="${REPO_DIR:-${GITHUB_WORKSPACE:-}/target-repo}"
RESOLVE_SCRIPT="${SCRIPT_DIR}/resolve-precommit-tools.py"
INSTALL_SCRIPT="${SCRIPT_DIR}/install-precommit-tools.sh"

if [ -f "${TARGET_REPO}/.pre-commit-config.yaml" ] \
   && [ -f "${RESOLVE_SCRIPT}" ] \
   && [ -f "${INSTALL_SCRIPT}" ]; then
  echo "Resolving pre-commit tool dependencies..."
  MANIFEST="$(mktemp)"
  LOCAL_REG="$(mktemp)"
  RESOLVE_ARGS=("${TARGET_REPO}")
  DEFAULT_BR="$(git -C "${TARGET_REPO}" symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||')" || DEFAULT_BR=""
  if [ -n "${DEFAULT_BR}" ] \
     && git -C "${TARGET_REPO}" show "origin/${DEFAULT_BR}:.pre-commit-tools.yaml" > "${LOCAL_REG}" 2>/dev/null; then
    RESOLVE_ARGS+=("--local-registry" "${LOCAL_REG}")
  fi
  if python3 "${RESOLVE_SCRIPT}" "${RESOLVE_ARGS[@]}" > "${MANIFEST}"; then
    if [ -s "${MANIFEST}" ] && jq -e '.tools | length > 0' "${MANIFEST}" >/dev/null 2>&1; then
      bash "${INSTALL_SCRIPT}" "${MANIFEST}"
    else
      echo "No additional pre-commit tools needed"
    fi
  else
    echo "::warning::Pre-commit tool resolution failed — continuing without auto-install"
  fi
  rm -f "${MANIFEST}" "${LOCAL_REG}"
fi
export PATH="${HOME}/.local/bin:${PATH}"
echo "${HOME}/.local/bin" >> "${GITHUB_PATH:-/dev/null}"
