#!/usr/bin/env bash
# inflight-adr-numbers.sh — collect ADR numbers from open PRs targeting a branch.
#
# Usage: inflight-adr-numbers.sh [base-branch] [exclude-pr]
#   base-branch  defaults to "main"
#   exclude-pr   PR number to exclude (typically the current PR)
#
# Prints one four-digit ADR number per line, sorted and deduplicated.
# Exit code is always 0 (no output means no in-flight ADR numbers found).
#
# Note: reports ADR numbers from all changed files (added, modified, deleted),
# not just additions. This is intentionally conservative — the caller already
# collects target-branch ADR numbers separately, so duplicates are harmless.
#
# Requires: gh CLI (authenticated).

set -euo pipefail

base="${1:-main}"
exclude="${2:-}"

gh pr list --base "$base" --state open --json number --jq '.[].number' \
  | while read -r pr; do
      [ "$pr" = "$exclude" ] && continue
      gh pr diff "$pr" --name-only 2>/dev/null || true
    done \
  | { grep '^docs/ADRs/[0-9]\{4\}-' || true; } \
  | sed 's|docs/ADRs/\([0-9]\{4\}\)-.*|\1|' \
  | sort -u
