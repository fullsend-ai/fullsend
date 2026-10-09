#!/usr/bin/env bash
# Prints an issue's title and its unchecked Markdown checklist items as JSON.
# Usage: checklist.sh <issue-number>. Reads REPO_FULL_NAME and GH_TOKEN.
set -euo pipefail
gh api "repos/${REPO_FULL_NAME}/issues/$1" \
  --jq '{title: .title, items: [(.body // "") | scan("(?m)^\\s*[-*] \\[ \\] (.+)$") | .[0]]}'
