#!/usr/bin/env bash
set -euo pipefail

# Contract: repo extraction (run.go) places the modified repo here
# before the sandbox is deleted.
REPO_DIR="repo"

if [ ! -d "${REPO_DIR}" ]; then
  echo "error: extracted repo not found at ${REPO_DIR}" >&2
  exit 1
fi

cd "${REPO_DIR}"

BRANCH="$(git branch --show-current)"

if [ -z "${BRANCH}" ] || [ "${BRANCH}" = "main" ] || [ "${BRANCH}" = "master" ]; then
  echo "error: agent did not create a feature branch" >&2
  exit 1
fi

# Rewrite remote to use the GitHub App token for push auth.
git remote set-url origin \
  "https://x-access-token:${GH_TOKEN}@github.com/${REPO_FULL_NAME}.git"

git push -u origin "${BRANCH}"

# Create PR linking to the originating issue.
gh pr create \
  --repo "${REPO_FULL_NAME}" \
  --head "${BRANCH}" \
  --title "fix: resolve #${ISSUE_NUMBER}" \
  --body "Closes #${ISSUE_NUMBER}"
