#!/usr/bin/env bash
# enroll-repos.sh — Enrolls repos listed in config.yaml into the fullsend pipeline.
# Called by repo-maintenance.yml when config.yaml changes or on manual dispatch.
#
# Requires:
#   GH_TOKEN  — GitHub token with contents:write and pull-requests:write on target repos
#   yq        — for YAML parsing (pre-installed on GitHub Actions ubuntu runners)
#
# Usage: ./scripts/enroll-repos.sh [config-dir]
#   config-dir: directory containing config.yaml and templates/ (default: current directory)
set -euo pipefail

CONFIG_DIR="${1:-.}"
CONFIG_FILE="$CONFIG_DIR/config.yaml"
SHIM_TEMPLATE="$CONFIG_DIR/templates/shim-workflow.yaml"
SHIM_PATH=".github/workflows/fullsend.yaml"
BRANCH_NAME="fullsend/onboard"
PR_TITLE="Connect to fullsend agent pipeline"
PR_BODY="This PR adds a shim workflow that routes repository events to the fullsend agent dispatch workflow in the \`.fullsend\` config repo.

Once merged, issues, PRs, and comments in this repo will be handled by the fullsend agent pipeline."

if [ ! -f "$CONFIG_FILE" ]; then
  echo "::error::config.yaml not found at $CONFIG_FILE"
  exit 1
fi

if [ ! -f "$SHIM_TEMPLATE" ]; then
  echo "::error::shim template not found at $SHIM_TEMPLATE"
  exit 1
fi

ORG="${GITHUB_REPOSITORY_OWNER:?GITHUB_REPOSITORY_OWNER must be set}"

# Read enabled repos from config.yaml using yq.
ENABLED_REPOS=$(yq '.repos | to_entries[] | select(.value.enabled == true) | .key' "$CONFIG_FILE")
if [ -z "$ENABLED_REPOS" ]; then
  echo "No enabled repos in config.yaml — nothing to enroll."
  exit 0
fi

ENROLLED=0
SKIPPED=0
FAILED=0

while IFS= read -r REPO; do
  echo "--- Checking $ORG/$REPO ---"

  # Check if already enrolled (shim exists on default branch).
  if gh api "repos/$ORG/$REPO/contents/$SHIM_PATH" --silent 2>/dev/null; then
    echo "✓ $REPO already enrolled"
    SKIPPED=$((SKIPPED + 1))
    continue
  fi

  # Check if enrollment PR already exists.
  EXISTING_PR=$(gh pr list --repo "$ORG/$REPO" --head "$BRANCH_NAME" --json url --jq '.[0].url' 2>/dev/null || true)
  if [ -n "$EXISTING_PR" ]; then
    echo "✓ $REPO has existing enrollment PR: $EXISTING_PR"
    # Update the shim on the existing branch to reflect the latest content.
    EXISTING_SHA=$(gh api "repos/$ORG/$REPO/contents/$SHIM_PATH?ref=$BRANCH_NAME" --jq .sha 2>/dev/null || true)
    UPDATE_ARGS=(--method PUT --field "message=chore: update fullsend shim workflow" --field "branch=$BRANCH_NAME" --field "content=$(base64 -w0 < "$SHIM_TEMPLATE")")
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
    --field "ref=refs/heads/$BRANCH_NAME" \
    --field "sha=$DEFAULT_SHA" \
    --silent 2>/dev/null; then
    # Branch exists — force it to the current default branch tip to avoid
    # operating on a stale or attacker-controlled branch.
    if ! gh api "repos/$ORG/$REPO/git/refs/heads/$BRANCH_NAME" \
      --method PATCH \
      --field "sha=$DEFAULT_SHA" \
      --field "force=true" \
      --silent; then
      echo "::error::Failed to create or update branch $BRANCH_NAME on $REPO"
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
    --field "branch=$BRANCH_NAME" \
    --field "content=$SHIM_CONTENT" \
    --silent; then
    echo "::error::Failed to write shim to $REPO (path=$SHIM_PATH, branch=$BRANCH_NAME)"
    FAILED=$((FAILED + 1))
    continue
  fi

  # Create PR.
  if ! PR_URL=$(gh pr create \
    --repo "$ORG/$REPO" \
    --head "$BRANCH_NAME" \
    --base "$DEFAULT_BRANCH" \
    --title "$PR_TITLE" \
    --body "$PR_BODY"); then
    echo "::error::Failed to create PR for $REPO"
    FAILED=$((FAILED + 1))
    continue
  fi

  echo "✓ Created enrollment PR for $REPO: $PR_URL"
  echo "::notice::Enrollment PR: $PR_URL"
  ENROLLED=$((ENROLLED + 1))
done <<< "$ENABLED_REPOS"

echo ""
echo "=== Enrollment summary ==="
echo "Enrolled: $ENROLLED"
echo "Skipped (already enrolled): $SKIPPED"
echo "Failed: $FAILED"

if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
