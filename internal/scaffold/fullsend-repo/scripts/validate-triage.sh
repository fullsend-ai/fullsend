#!/usr/bin/env bash
set -euo pipefail

TRIAGE_REPORT_FILE="output/triage-report.md"

if [ ! -f "$TRIAGE_REPORT_FILE" ]; then
  echo "FAIL: $TRIAGE_REPORT_FILE not found"
  exit 1
fi

# Post the triage report as a comment on the issue.
# GITHUB_ISSUE_URL is an HTML URL (e.g., https://github.com/org/repo/issues/1).
REPO=$(echo "$GITHUB_ISSUE_URL" | sed 's|https://github.com/||; s|/issues/.*||')
ISSUE_NUMBER=$(basename "$GITHUB_ISSUE_URL")

gh issue comment "$ISSUE_NUMBER" --repo "$REPO" --body-file "$TRIAGE_REPORT_FILE"

echo "PASS: output validated"
exit 0
