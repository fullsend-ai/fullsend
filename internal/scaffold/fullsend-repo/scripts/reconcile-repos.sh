#!/usr/bin/env bash
# reconcile-repos.sh — Reconciles repo enrollment state with config.yaml.
# Enrolls repos with enabled: true, unenrolls repos with enabled: false.
# Called by repo-maintenance.yml when config.yaml changes or on manual dispatch.
#
# Requires:
#   GH_TOKEN  — GitHub token with contents:write and pull-requests:write on target repos
#   yq        — for YAML parsing (pre-installed on GitHub Actions ubuntu runners)
#
# Usage: ./scripts/reconcile-repos.sh [config-dir]
#   config-dir: directory containing config.yaml and templates/ (default: current directory)
set -euo pipefail

CONFIG_DIR="${1:-.}"
CONFIG_FILE="$CONFIG_DIR/config.yaml"
SHIM_TEMPLATE="$CONFIG_DIR/templates/shim-workflow.yaml"
SHIM_PATH=".github/workflows/fullsend.yaml"
ENROLL_BRANCH="fullsend/onboard"
UNENROLL_BRANCH="fullsend/offboard"
ENROLL_PR_TITLE="Connect to fullsend agent pipeline"
UNENROLL_PR_TITLE="Disconnect from fullsend agent pipeline"
ENROLL_PR_BODY="This PR adds a shim workflow that routes repository events to the fullsend agent dispatch workflow in the \`.fullsend\` config repo.

Once merged, issues, PRs, and comments in this repo will be handled by the fullsend agent pipeline."
UNENROLL_PR_BODY="This PR removes the fullsend shim workflow. The repo has been set to \`enabled: false\` in the fullsend config.

Once merged, this repo will no longer dispatch events to the fullsend agent pipeline."
REPO_NAME_PATTERN='^[a-zA-Z0-9._-]+$'

if [ ! -f "$CONFIG_FILE" ]; then
  echo "::error::config.yaml not found at $CONFIG_FILE"
  exit 1
fi

if [ ! -f "$SHIM_TEMPLATE" ]; then
  echo "::error::shim template not found at $SHIM_TEMPLATE"
  exit 1
fi

ORG="${GITHUB_REPOSITORY_OWNER:?GITHUB_REPOSITORY_OWNER must be set}"
COMMIT_SHA="${GITHUB_SHA:-unknown}"

ENROLLED=0
UNENROLLED=0
SKIPPED=0
FAILED=0

# validate_repo_name checks that a repo name is safe for use in API calls.
validate_repo_name() {
  local name="$1"
  if printf '%s' "$name" | grep -qP '[\x00-\x1f]'; then
    echo "::error::Repo name contains control characters, skipping"
    return 1
  fi
  if ! [[ "$name" =~ $REPO_NAME_PATTERN ]]; then
    echo "::error::Repo name contains disallowed characters, skipping"
    return 1
  fi
}

# close_pr_on_branch closes an open PR on the given branch and deletes the branch.
close_pr_on_branch() {
  local repo="$1"
  local branch="$2"
  local reason="$3"

  local pr_url
  pr_url=$(gh pr list --repo "$ORG/$repo" --head "$branch" --json url --jq '.[0].url' 2>/dev/null || true)
  if [ -n "$pr_url" ]; then
    gh pr close "$pr_url" --comment "$reason (triggered by commit $COMMIT_SHA)" --delete-branch 2>/dev/null || true
    echo "  Closed PR on $branch: $pr_url"
  else
    # Delete branch even if no PR exists.
    gh api "repos/$ORG/$repo/git/refs/heads/$branch" --method DELETE --silent 2>/dev/null || true
  fi
}

# ===========================
# Phase 1: Enroll enabled repos
# ===========================

ENABLED_REPOS=$(yq '.repos | to_entries[] | select(.value.enabled == true) | .key' "$CONFIG_FILE")

if [ -n "$ENABLED_REPOS" ]; then
  echo "=== Phase 1: Enrolling enabled repos ==="
  while IFS= read -r REPO; do
    echo "--- Checking $ORG/$REPO ---"

    if ! validate_repo_name "$REPO"; then
      FAILED=$((FAILED + 1))
      continue
    fi

    # Clean up any stale removal PR from a previous disable cycle.
    close_pr_on_branch "$REPO" "$UNENROLL_BRANCH" "Repo re-enabled in config.yaml"

    # Check if already enrolled (shim exists on default branch).
    if gh api "repos/$ORG/$REPO/contents/$SHIM_PATH" --silent 2>/dev/null; then
      echo "✓ $REPO already enrolled"
      SKIPPED=$((SKIPPED + 1))
      continue
    fi

    # Check if enrollment PR already exists.
    EXISTING_PR=$(gh pr list --repo "$ORG/$REPO" --head "$ENROLL_BRANCH" --json url --jq '.[0].url' 2>/dev/null || true)
    if [ -n "$EXISTING_PR" ]; then
      echo "✓ $REPO has existing enrollment PR: $EXISTING_PR"
      # Update the shim on the existing branch to reflect the latest content.
      EXISTING_SHA=$(gh api "repos/$ORG/$REPO/contents/$SHIM_PATH?ref=$ENROLL_BRANCH" --jq .sha 2>/dev/null || true)
      UPDATE_ARGS=(--method PUT --field "message=chore: update fullsend shim workflow" --field "branch=$ENROLL_BRANCH" --field "content=$(base64 -w0 < "$SHIM_TEMPLATE")")
      if [ -n "$EXISTING_SHA" ]; then
        UPDATE_ARGS+=(--field "sha=$EXISTING_SHA")
      fi
      if ! gh api "repos/$ORG/$REPO/contents/$SHIM_PATH" "${UPDATE_ARGS[@]}" --silent; then
        echo "::error::Failed to update shim on $REPO"
        FAILED=$((FAILED + 1))
      else
        ENROLLED=$((ENROLLED + 1))
      fi
      continue
    fi

    echo "Enrolling $REPO..."

    # Get default branch.
    DEFAULT_BRANCH=$(gh api "repos/$ORG/$REPO" --jq .default_branch)
    if [ -z "$DEFAULT_BRANCH" ]; then
      echo "::error::Could not determine default branch for $REPO"
      FAILED=$((FAILED + 1))
      continue
    fi

    # Create enrollment branch from default branch tip.
    DEFAULT_SHA=$(gh api "repos/$ORG/$REPO/git/ref/heads/$DEFAULT_BRANCH" --jq .object.sha)
    if [ -z "$DEFAULT_SHA" ]; then
      echo "::error::Could not get default branch SHA for $REPO"
      FAILED=$((FAILED + 1))
      continue
    fi

    # Create branch, or update it to the default branch tip if it already exists.
    if ! gh api "repos/$ORG/$REPO/git/refs" \
      --method POST \
      --field "ref=refs/heads/$ENROLL_BRANCH" \
      --field "sha=$DEFAULT_SHA" \
      --silent 2>/dev/null; then
      # Branch exists — force it to the current default branch tip to avoid
      # operating on a stale or attacker-controlled branch.
      if ! gh api "repos/$ORG/$REPO/git/refs/heads/$ENROLL_BRANCH" \
        --method PATCH \
        --field "sha=$DEFAULT_SHA" \
        --field "force=true" \
        --silent; then
        echo "::error::Failed to create or update branch $ENROLL_BRANCH on $REPO"
        FAILED=$((FAILED + 1))
        continue
      fi
    fi

    # Encode shim template content.
    SHIM_CONTENT=$(base64 -w0 < "$SHIM_TEMPLATE")
    if [ -z "$SHIM_CONTENT" ]; then
      echo "::error::Failed to base64-encode shim template at $SHIM_TEMPLATE"
      FAILED=$((FAILED + 1))
      continue
    fi

    # Write shim workflow to branch.
    if ! gh api "repos/$ORG/$REPO/contents/$SHIM_PATH" \
      --method PUT \
      --field "message=chore: add fullsend shim workflow" \
      --field "branch=$ENROLL_BRANCH" \
      --field "content=$SHIM_CONTENT" \
      --silent; then
      echo "::error::Failed to write shim to $REPO (path=$SHIM_PATH, branch=$ENROLL_BRANCH)"
      FAILED=$((FAILED + 1))
      continue
    fi

    # Create PR.
    if ! PR_URL=$(gh pr create \
      --repo "$ORG/$REPO" \
      --head "$ENROLL_BRANCH" \
      --base "$DEFAULT_BRANCH" \
      --title "$ENROLL_PR_TITLE" \
      --body "$ENROLL_PR_BODY"); then
      echo "::error::Failed to create PR for $REPO"
      FAILED=$((FAILED + 1))
      continue
    fi

    echo "✓ Created enrollment PR for $REPO: $PR_URL"
    echo "::notice::Enrollment PR: $PR_URL"
    ENROLLED=$((ENROLLED + 1))
  done <<< "$ENABLED_REPOS"
else
  echo "No enabled repos in config.yaml."
fi

# ===========================
# Phase 2: Unenroll disabled repos
# ===========================

DISABLED_REPOS=$(yq '.repos | to_entries[] | select(.value.enabled != true) | .key' "$CONFIG_FILE")

if [ -n "$DISABLED_REPOS" ]; then
  echo ""
  echo "=== Phase 2: Unenrolling disabled repos ==="
  while IFS= read -r REPO; do
    echo "--- Checking $ORG/$REPO ---"

    if ! validate_repo_name "$REPO"; then
      FAILED=$((FAILED + 1))
      continue
    fi

    # Close any stale enrollment PR.
    close_pr_on_branch "$REPO" "$ENROLL_BRANCH" "Repo disabled in config.yaml"

    # Check if a removal PR already exists.
    EXISTING_PR=$(gh pr list --repo "$ORG/$REPO" --head "$UNENROLL_BRANCH" --json url --jq '.[0].url' 2>/dev/null || true)
    if [ -n "$EXISTING_PR" ]; then
      echo "✓ $REPO already has removal PR: $EXISTING_PR"
      SKIPPED=$((SKIPPED + 1))
      continue
    fi

    # Check if shim exists on default branch.
    if ! gh api "repos/$ORG/$REPO/contents/$SHIM_PATH" --silent 2>/dev/null; then
      echo "✓ $REPO already unenrolled (no shim on default branch)"
      SKIPPED=$((SKIPPED + 1))
      continue
    fi

    echo "Unenrolling $REPO..."

    # Get default branch.
    DEFAULT_BRANCH=$(gh api "repos/$ORG/$REPO" --jq .default_branch)
    if [ -z "$DEFAULT_BRANCH" ]; then
      echo "::error::Could not determine default branch for $REPO"
      FAILED=$((FAILED + 1))
      continue
    fi

    # Create removal branch from default branch tip.
    DEFAULT_SHA=$(gh api "repos/$ORG/$REPO/git/ref/heads/$DEFAULT_BRANCH" --jq .object.sha)
    if [ -z "$DEFAULT_SHA" ]; then
      echo "::error::Could not get default branch SHA for $REPO"
      FAILED=$((FAILED + 1))
      continue
    fi

    # Create branch, or force-update to default branch tip.
    if ! gh api "repos/$ORG/$REPO/git/refs" \
      --method POST \
      --field "ref=refs/heads/$UNENROLL_BRANCH" \
      --field "sha=$DEFAULT_SHA" \
      --silent 2>/dev/null; then
      if ! gh api "repos/$ORG/$REPO/git/refs/heads/$UNENROLL_BRANCH" \
        --method PATCH \
        --field "sha=$DEFAULT_SHA" \
        --field "force=true" \
        --silent; then
        echo "::error::Failed to create or update branch $UNENROLL_BRANCH on $REPO"
        FAILED=$((FAILED + 1))
        continue
      fi
    fi

    # Fetch file SHA on the removal branch (required for DELETE).
    FILE_SHA=$(gh api "repos/$ORG/$REPO/contents/$SHIM_PATH?ref=$UNENROLL_BRANCH" --jq .sha 2>/dev/null || true)
    if [ -z "$FILE_SHA" ]; then
      echo "✓ $REPO shim already removed from branch"
      SKIPPED=$((SKIPPED + 1))
      continue
    fi

    # Delete the shim workflow on the removal branch.
    if ! gh api "repos/$ORG/$REPO/contents/$SHIM_PATH" \
      --method DELETE \
      --field "message=chore: remove fullsend shim workflow" \
      --field "branch=$UNENROLL_BRANCH" \
      --field "sha=$FILE_SHA" \
      --silent; then
      echo "::error::Failed to delete shim from $REPO (path=$SHIM_PATH, branch=$UNENROLL_BRANCH)"
      FAILED=$((FAILED + 1))
      continue
    fi

    # Create removal PR.
    if ! PR_URL=$(gh pr create \
      --repo "$ORG/$REPO" \
      --head "$UNENROLL_BRANCH" \
      --base "$DEFAULT_BRANCH" \
      --title "$UNENROLL_PR_TITLE" \
      --body "$UNENROLL_PR_BODY"); then
      echo "::error::Failed to create removal PR for $REPO"
      FAILED=$((FAILED + 1))
      continue
    fi

    echo "✓ Created removal PR for $REPO: $PR_URL"
    echo "::notice::Removal PR: $PR_URL"
    UNENROLLED=$((UNENROLLED + 1))
  done <<< "$DISABLED_REPOS"
else
  echo "No disabled repos in config.yaml."
fi

echo ""
echo "=== Reconciliation summary ==="
echo "Enrolled: $ENROLLED"
echo "Unenrolled: $UNENROLLED"
echo "Skipped (already reconciled): $SKIPPED"
echo "Failed: $FAILED"

if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
