#!/usr/bin/env bash
set -euo pipefail

# Clone the target repo
git clone "https://x-access-token:${GITHUB_TOKEN}@github.com/${REPO_OWNER}/${REPO_NAME}.git" workspace
cd workspace
git config user.name "fullsend-agent[bot]"
git config user.email "fullsend-agent[bot]@users.noreply.github.com"

# Create the agent's working branch
BRANCH="agent/${ISSUE_NUMBER}-$(echo "${ISSUE_TITLE}" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-' | head -c 40)"
git checkout -b "${BRANCH}"
echo "AGENT_BRANCH=${BRANCH}" >> "${STAGE_ENV}"
