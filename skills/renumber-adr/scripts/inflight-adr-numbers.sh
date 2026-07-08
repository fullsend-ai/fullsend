#!/usr/bin/env bash
# inflight-adr-numbers.sh — collect ADR numbers from open PRs targeting a branch.
#
# Usage: inflight-adr-numbers.sh [base-branch]
#   base-branch  defaults to "main"
#
# Prints one four-digit ADR number per line, sorted and deduplicated.
# Exit code is always 0 (no output means no in-flight ADR numbers found).

set -euo pipefail

base="${1:-main}"

gh pr list --base "$base" --state open --json number --jq '.[].number' \
  | while read -r pr; do
      gh pr diff "$pr" --name-only 2>/dev/null
    done \
  | grep '^docs/ADRs/[0-9]\{4\}-' \
  | sed 's|docs/ADRs/\([0-9]\{4\}\)-.*|\1|' \
  | sort -u
