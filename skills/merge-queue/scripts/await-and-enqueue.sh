#!/usr/bin/env bash
# Waits for a PR's required checks and approvals, then enqueues it.
# Exits early if any required check fails.
#
# Usage: await-and-enqueue.sh [PR_NUMBER] [-R owner/repo]
#        await-and-enqueue.sh [PR_URL]
#
# If no argument is given, uses the current branch's PR. PR_NUMBER defaults
# to the current repo; pass -R/--repo owner/repo for a PR in another repo.
# Prefer PR_NUMBER (+ -R) over PR_URL: an agent invoking this script through
# the sandboxed Bash tool must never put a raw github.com URL on the
# command line, or the SSRF PreToolUse hook fail-closes before this script
# even runs (see SKILL.md).
# Polls every 30 seconds. Requires: gh CLI, jq.

set -euo pipefail

# Reject anything but a plain two-segment owner/repo nwo: no host-qualified
# ("host/owner/repo") or "."/".." path segments. `gh`'s own -R/--repo flag
# accepts [HOST/]OWNER/REPO, so an unvalidated value here would let a
# caller redirect gh's API requests to an attacker-controlled host —
# defeating the SSRF PreToolUse hook this URL-free interface exists to
# cooperate with.
validate_repo_nwo() {
  local value="$1"
  if [[ ! "$value" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]; then
    echo "Error: -R/--repo must be a plain owner/repo value, not: $value" >&2
    exit 1
  fi
  local owner_part="${value%%/*}" repo_part="${value#*/}"
  if [[ "$owner_part" == "." || "$owner_part" == ".." || "$repo_part" == "." || "$repo_part" == ".." ]]; then
    echo "Error: -R/--repo must be a plain owner/repo value, not: $value" >&2
    exit 1
  fi
}

POLL_INTERVAL="${POLL_INTERVAL:-30}"
pr=""
repo=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    -R|--repo)
      if [[ $# -lt 2 ]]; then
        echo "Error: -R/--repo requires an owner/repo value" >&2
        exit 1
      fi
      repo="$2"
      validate_repo_nwo "$repo"
      shift 2
      ;;
    *)
      if [[ -n "$pr" ]]; then
        echo "Error: unexpected extra argument: $1" >&2
        exit 1
      fi
      pr="$1"
      shift
      ;;
  esac
done

if [[ -n "$repo" && "$pr" =~ ^https://github.com/ ]]; then
  echo "Error: provide either a PR URL or -R/--repo, not both" >&2
  exit 1
fi

# Resolve PR URL, number, and repo without passing a github.com URL to gh.
# The sandbox SSRF PreToolUse hook DNS-resolves https?:// literals on
# non-inert commands, and github.com is not allowlisted.
if [[ -n "$repo" ]]; then
  if [[ -z "$pr" ]]; then
    echo "Error: -R/--repo requires a PR number" >&2
    exit 1
  elif [[ "$pr" =~ ^[0-9]+$ ]]; then
    pr_json_init="$(gh pr view "$pr" -R "$repo" --json url,baseRefName,number)"
  else
    echo "Error: -R/--repo requires a PR number" >&2
    exit 1
  fi
elif [[ -z "$pr" ]]; then
  pr_json_init="$(gh pr view --json url,baseRefName,number)"
elif [[ "$pr" =~ ^https://github.com/([^/]+/[^/]+)/pull/([0-9]+) ]]; then
  repo="${BASH_REMATCH[1]}"
  number="${BASH_REMATCH[2]}"
  validate_repo_nwo "$repo"
  pr_json_init="$(gh pr view "$number" -R "$repo" --json url,baseRefName,number)"
elif [[ "$pr" =~ ^[0-9]+$ ]]; then
  pr_json_init="$(gh pr view "$pr" --json url,baseRefName,number)"
else
  echo "Error: provide a PR number or URL" >&2
  exit 1
fi

pr_url="$(echo "$pr_json_init" | jq -r .url)"
base_branch="$(echo "$pr_json_init" | jq -r .baseRefName)"
number="$(echo "$pr_json_init" | jq -r .number)"

if [[ -z "$repo" ]]; then
  if [[ "$pr_url" =~ ^https://github.com/([^/]+/[^/]+)/pull/ ]]; then
    repo="${BASH_REMATCH[1]}"
  else
    echo "Error: could not determine repository from PR URL" >&2
    exit 1
  fi
fi

# Fetch required status checks from branch rulesets as a JSON array
required_json="$(gh api "repos/$repo/rules/branches/$base_branch" \
  --jq '[.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context] | unique' 2>/dev/null || echo '[]')"

if [[ "$(echo "$required_json" | jq 'length')" -gt 0 ]]; then
  echo "Required checks: $(echo "$required_json" | jq -r 'join(", ")')"
fi

echo "Waiting for checks and approvals on: $pr_url"

while true; do
  # Get check rollup and review decision in one call
  pr_json="$(gh pr view "$number" -R "$repo" --json statusCheckRollup,reviewDecision)"

  review_decision="$(echo "$pr_json" | jq -r '.reviewDecision // "NONE"')"

  # Use jq to analyze all check statuses and required check coverage in one pass
  result="$(echo "$pr_json" | jq -r --argjson required "$required_json" '
    .statusCheckRollup as $checks |
    # Build map of name -> conclusion
    ($checks | map({(.name): (.conclusion // .status // "PENDING")}) | add // {}) as $map |
    # Check for failures
    [$map | to_entries[] | select(.value | test("FAILURE|ERROR|CANCELLED|TIMED_OUT|STARTUP_FAILURE|ACTION_REQUIRED")) | .key + " (" + .value + ")"] as $failures |
    # Check for pending
    [$map | to_entries[] | select(.value | test("SUCCESS|NEUTRAL|SKIPPED|COMPLETED|FAILURE|ERROR|CANCELLED|TIMED_OUT|STARTUP_FAILURE|ACTION_REQUIRED") | not) | .key] as $pending |
    # Check for missing required checks
    [$required[] | select(. as $r | $map | has($r) | not)] as $missing |
    {failures: $failures, pending: $pending, missing: $missing}
  ')"

  failures="$(echo "$result" | jq -r '.failures[]' 2>/dev/null || true)"
  pending="$(echo "$result" | jq -r '.pending[]' 2>/dev/null || true)"
  missing="$(echo "$result" | jq -r '.missing[]' 2>/dev/null || true)"

  if [[ -n "$failures" ]]; then
    echo "$failures" | while IFS= read -r f; do echo "FAILED: $f"; done
    echo "Aborting — one or more required checks failed."
    exit 1
  fi

  has_pending=false
  if [[ -n "$pending" ]]; then
    has_pending=true
  fi
  if [[ -n "$missing" ]]; then
    echo "$missing" | while IFS= read -r m; do echo "Required check not yet reported: $m"; done
    has_pending=true
  fi

  if [[ "$has_pending" == "true" ]]; then
    echo "Waiting ${POLL_INTERVAL}s..."
    sleep "$POLL_INTERVAL"
    continue
  fi

  if [[ "$review_decision" != "APPROVED" ]]; then
    echo "Checks passed but review not yet approved (status: $review_decision)... waiting ${POLL_INTERVAL}s"
    sleep "$POLL_INTERVAL"
    continue
  fi

  echo "All checks passed and PR is approved. Enqueuing..."
  break
done

# Delegate to the enqueue script using the number and repo already resolved
# above, never a URL (needed when the PR is not in the cwd repo).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec bash "$SCRIPT_DIR/enqueue-pr.sh" "$number" -R "$repo"
