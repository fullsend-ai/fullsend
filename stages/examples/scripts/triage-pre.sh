#!/usr/bin/env bash
set -euo pipefail

git clone "https://x-access-token:${GITHUB_TOKEN}@github.com/${REPO_OWNER}/${REPO_NAME}.git" workspace
cd workspace
