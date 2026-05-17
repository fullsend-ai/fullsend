#!/usr/bin/env bash
# reconcile-repos.sh — Reconciles repo enrollment state with config.yaml.
# Enrolls repos with enabled: true, unenrolls repos with enabled: false.
# Called by repo-maintenance.yml when config.yaml changes or on manual dispatch.
#
# Requires:
#   GH_TOKEN  — GitHub token with contents:write and pull-requests:write on target repos
#   yq        — for YAML parsing (pre-installed on GitHub Actions ubuntu runners)
#   jq        — for constructing Git API request bodies
#
# Usage: ./scripts/reconcile-repos.sh [config-dir]
#   config-dir: directory containing config.yaml and templates/ (default: current directory)
set -euo pipefail

CONFIG_DIR="${1:-.}"
CONFIG_FILE="$CONFIG_DIR/config.yaml"

if [ ! -f "$CONFIG_FILE" ]; then
  echo "::error::config.yaml not found at $CONFIG_FILE"
  exit 1
fi

if ! command -v yq &>/dev/null; then
  echo "ERROR: yq is required but not found in PATH" >&2
  exit 1
fi
SHIM_TEMPLATE="$CONFIG_DIR/templates/shim-workflow-call.yaml"
SHIM_PATH=".github/workflows/fullsend.yaml"
REPO_NAME_PATTERN='^[a-zA-Z0-9._-]+$'

ENROLL_BRANCH="fullsend/onboard"
UNENROLL_BRANCH="fullsend/offboard"

ENROLL_PR_TITLE="chore: connect to fullsend agent pipeline"
UNENROLL_PR_TITLE="chore: disconnect from fullsend agent pipeline"
UPDATE_PR_TITLE="chore: update fullsend shim workflow"

ENROLL_PR_BODY="This PR adds a shim workflow that routes repository events to the fullsend agent dispatch workflow in the \`.fullsend\` config repo.

Once merged, issues, PRs, and comments in this repo will be handled by the fullsend agent pipeline."
UNENROLL_PR_BODY="This PR removes the fullsend shim workflow. The repo has been set to \`enabled: false\` in the fullsend config.

Once merged, this repo will no longer dispatch events to the fullsend agent pipeline."
UPDATE_PR_BODY="This PR updates the fullsend shim workflow to match the current template in the \`.fullsend\` config repo.

The shim content has drifted from the template — this brings it back in sync."

if [ ! -f "$SHIM_TEMPLATE" ]; then
  echo "::error::shim template not found at $SHIM_TEMPLATE"
  exit 1
fi

ORG="${GITHUB_REPOSITORY_OWNER:?GITHUB_REPOSITORY_OWNER must be set}"
if [[ ! "$ORG" =~ ^[a-zA-Z0-9][a-zA-Z0-9-]*[a-zA-Z0-9]$ && ! "$ORG" =~ ^[a-zA-Z0-9]$ ]]; then
  echo "::error::Invalid org name: $ORG"
  exit 1
fi

# shim_content_b64 returns the shim template as base64, with __ORG__
# substituted for the actual org name (used by workflow_call templates).
shim_content_b64() {
  sed "s|__ORG__|${ORG}|g" "$SHIM_TEMPLATE" | base64 -w0
}
COMMIT_SHA="${GITHUB_SHA:-unknown}"
PER_REPO_GUARD_VAR="FULLSEND_PER_REPO_INSTALL"

# check_per_repo_guard checks whether a repo has an active per-repo installation.
# Returns 0 (should skip) if per-repo guard is active or API error (fail closed).
# Returns 1 (proceed) if guard is not set or not "true".
check_per_repo_guard() {
  local repo="$1"
  local action="$2"
  local resp rc var
  resp=$(gh api "repos/$ORG/$repo/actions/variables/$PER_REPO_GUARD_VAR" 2>/dev/null) && rc=0 || rc=$?
  if [[ "$rc" -eq 0 ]]; then
    var=$(printf '%s' "$resp" | jq -r '.value // empty')
    if [[ "$var" == "true" ]]; then
      echo "::warning::Skipping $action of $repo — per-repo installation active ($PER_REPO_GUARD_VAR=true)"
      return 0
    fi
    if [[ -n "$var" ]]; then
      local safe_var
      safe_var=$(printf '%s' "$var" | tr -d '\n\r')
      echo "::notice::$repo has $PER_REPO_GUARD_VAR=$safe_var (not \"true\") — proceeding with $action"
    fi
    return 1
  fi
  # Check if the error is a 404 (variable not found) via numeric status field.
  if printf '%s' "$resp" | jq -e '.status == "404"' >/dev/null 2>&1; then
    return 1
  fi
  # Non-404 error (permissions, network) — skip to be safe.
  local api_msg
  api_msg=$(printf '%s' "$resp" | jq -r '.message // empty' 2>/dev/null | tr -d '\n\r')
  echo "::warning::Skipping $action of $repo — could not check per-repo guard variable (exit code $rc${api_msg:+: $api_msg})"
  return 0
}

ENROLLED=0
UPDATED=0
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
  pr_url=$(gh pr list --repo "$ORG/$repo" --head "$branch" --json url --jq '.[0].url // empty' 2>/dev/null || true)
  if [ -n "$pr_url" ]; then
    gh pr close "$pr_url" --comment "$reason (triggered by commit $COMMIT_SHA)" --delete-branch 2>/dev/null || true
    echo "  Closed PR on $branch: $pr_url"
  else
    # Delete branch even if no PR exists.
    gh api "repos/$ORG/$repo/git/refs/heads/$branch" --method DELETE --silent 2>/dev/null || true
  fi
}

# load_default_branch fetches the default branch name and current SHA for a
# given repo. Sets DEFAULT_BRANCH and DEFAULT_BRANCH_SHA as side effects.
# Returns 0 on success, 1 on failure (with error logged).
load_default_branch() {
  local repo="$1"

  DEFAULT_BRANCH=$(gh api "repos/$ORG/$repo" --jq .default_branch 2>/dev/null || true)
  if [ -z "$DEFAULT_BRANCH" ]; then
    echo "::error::Could not determine default branch for $repo"
    return 1
  fi

  DEFAULT_BRANCH_SHA=$(gh api "repos/$ORG/$repo/git/ref/heads/$DEFAULT_BRANCH" --jq .object.sha 2>/dev/null || true)
  if [ -z "$DEFAULT_BRANCH_SHA" ]; then
    echo "::error::Could not get default branch SHA for $repo"
    return 1
  fi
}

# ensure_branch creates or resets a branch to the default branch tip.
# Sets DEFAULT_BRANCH and DEFAULT_BRANCH_SHA as side effects.
# Returns 0 on success, 1 on failure (with error logged).
ensure_branch() {
  local repo="$1"
  local branch="$2"

  if ! load_default_branch "$repo"; then
    return 1
  fi

  if ! gh api "repos/$ORG/$repo/git/refs" \
    --method POST \
    --field "ref=refs/heads/$branch" \
    --field "sha=$DEFAULT_BRANCH_SHA" \
    --silent 2>/dev/null; then
    # Branch exists — force it to the current default branch tip to avoid
    # operating on a stale or attacker-controlled branch.
    if ! gh api "repos/$ORG/$repo/git/refs/heads/$branch" \
      --method PATCH \
      --field "sha=$DEFAULT_BRANCH_SHA" \
      --field "force=true" \
      --silent; then
      echo "::error::Failed to create or update branch $branch on $repo"
      return 1
    fi
  fi
}

# write_shim_to_branch_from_default writes the shim template to a branch whose
# tree is based on the current default branch. The branch ref moves directly to
# the desired commit, avoiding a transient zero-diff state that can auto-close
# an existing GitHub PR on the branch.
# Returns 0 on success, 1 on failure (with error logged).
write_shim_to_branch_from_default() {
  local repo="$1"
  local branch="$2"
  local content_b64="$3"
  local commit_msg="$4"

  if ! load_default_branch "$repo"; then
    return 1
  fi

  local default_tree_sha
  default_tree_sha=$(gh api "repos/$ORG/$repo/git/commits/$DEFAULT_BRANCH_SHA" --jq .tree.sha 2>/dev/null || true)
  if [ -z "$default_tree_sha" ]; then
    echo "::error::Could not get default branch tree for $repo"
    return 1
  fi

  local blob_sha
  if ! blob_sha=$(jq -n \
    --arg content "$content_b64" \
    '{content: $content, encoding: "base64"}' |
    gh api "repos/$ORG/$repo/git/blobs" --method POST --input - --jq .sha); then
    echo "::error::Failed to create shim blob for $repo"
    return 1
  fi
  if [ -z "$blob_sha" ]; then
    echo "::error::Created empty shim blob SHA for $repo"
    return 1
  fi

  local tree_sha
  if ! tree_sha=$(jq -n \
    --arg base_tree "$default_tree_sha" \
    --arg path "$SHIM_PATH" \
    --arg sha "$blob_sha" \
    '{base_tree: $base_tree, tree: [{path: $path, mode: "100644", type: "blob", sha: $sha}]}' |
    gh api "repos/$ORG/$repo/git/trees" --method POST --input - --jq .sha); then
    echo "::error::Failed to create shim tree for $repo"
    return 1
  fi
  if [ -z "$tree_sha" ]; then
    echo "::error::Created empty shim tree SHA for $repo"
    return 1
  fi

  local commit_sha
  if ! commit_sha=$(jq -n \
    --arg message "$commit_msg" \
    --arg tree "$tree_sha" \
    --arg parent "$DEFAULT_BRANCH_SHA" \
    '{message: $message, tree: $tree, parents: [$parent]}' |
    gh api "repos/$ORG/$repo/git/commits" --method POST --input - --jq .sha); then
    echo "::error::Failed to create shim commit for $repo"
    return 1
  fi
  if [ -z "$commit_sha" ]; then
    echo "::error::Created empty shim commit SHA for $repo"
    return 1
  fi

  if ! gh api "repos/$ORG/$repo/git/refs" \
    --method POST \
    --field "ref=refs/heads/$branch" \
    --field "sha=$commit_sha" \
    --silent 2>/dev/null; then
    if ! gh api "repos/$ORG/$repo/git/refs/heads/$branch" \
      --method PATCH \
      --field "sha=$commit_sha" \
      --field "force=true" \
      --silent; then
      echo "::error::Failed to create or update branch $branch on $repo"
      return 1
    fi
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

    # Skip private repos — agent workflow logs on public .fullsend would expose content.
    # Fail closed: only proceed if the API explicitly confirms the repo is public.
    REPO_PRIVATE=$(gh api "repos/$ORG/$REPO" --jq '.private' 2>/dev/null || echo "unknown")
    if [[ "$REPO_PRIVATE" != "false" ]]; then
      echo "::warning::Skipping $REPO — private repos cannot be enrolled when .fullsend is public (visibility: $REPO_PRIVATE)"
      SKIPPED=$((SKIPPED + 1))
      continue
    fi

    # Clean up any stale removal PR from a previous disable cycle (even for per-repo repos).
    close_pr_on_branch "$REPO" "$UNENROLL_BRANCH" "Repo re-enabled in config.yaml"

    # Skip repos with per-repo installation — they manage their own WIF and shim.
    if check_per_repo_guard "$REPO" "enrollment"; then
      SKIPPED=$((SKIPPED + 1))
      continue
    fi

    # Check if already enrolled (shim exists on default branch).
    # Fetch content and SHA in one call to avoid race between reads.
    REMOTE_CONTENT=$(gh api "repos/$ORG/$REPO/contents/$SHIM_PATH" --jq .content 2>/dev/null || true)
    if [ -n "$REMOTE_CONTENT" ]; then
      # File exists — compare content against current template.
      EXPECTED_B64=$(shim_content_b64)
      # GitHub returns base64 with newlines; strip them for comparison.
      REMOTE_B64=$(printf '%s' "$REMOTE_CONTENT" | tr -d '\r\n')
      if [ "$REMOTE_B64" = "$EXPECTED_B64" ]; then
        echo "✓ $REPO already enrolled (shim up to date)"
        SKIPPED=$((SKIPPED + 1))
        continue
      fi

      # Shim is stale — update via PR to respect branch protection.
      echo "⟳ $REPO enrolled but shim is stale — creating update PR"

      if ! write_shim_to_branch_from_default "$REPO" "$ENROLL_BRANCH" "$EXPECTED_B64" "chore: update fullsend shim workflow"; then
        FAILED=$((FAILED + 1))
        continue
      fi

      # Create or update the PR.
      EXISTING_PR=$(gh pr list --repo "$ORG/$REPO" --head "$ENROLL_BRANCH" --json url --jq '.[0].url // empty' 2>/dev/null || true)
      if [ -z "$EXISTING_PR" ]; then
        if ! PR_URL=$(gh pr create \
          --repo "$ORG/$REPO" \
          --head "$ENROLL_BRANCH" \
          --base "$DEFAULT_BRANCH" \
          --title "$UPDATE_PR_TITLE" \
          --body "$UPDATE_PR_BODY"); then
          echo "::error::Failed to create update PR for $REPO"
          FAILED=$((FAILED + 1))
          continue
        fi
        echo "✓ Created shim update PR for $REPO: $PR_URL"
        echo "::notice::Shim update PR: $PR_URL"
      else
        echo "✓ Updated shim on existing PR for $REPO: $EXISTING_PR"
      fi
      UPDATED=$((UPDATED + 1))
      continue
    fi

    # Check if enrollment PR already exists.
    EXISTING_PR=$(gh pr list --repo "$ORG/$REPO" --head "$ENROLL_BRANCH" --json url --jq '.[0].url // empty' 2>/dev/null || true)
    if [ -n "$EXISTING_PR" ]; then
      echo "✓ $REPO has existing enrollment PR: $EXISTING_PR"
      # Update the shim on the existing branch to reflect the latest content.
      if ! write_shim_to_branch_from_default "$REPO" "$ENROLL_BRANCH" "$(shim_content_b64)" "chore: update fullsend shim workflow"; then
        FAILED=$((FAILED + 1))
      else
        ENROLLED=$((ENROLLED + 1))
      fi
      continue
    fi

    echo "Enrolling $REPO..."

    # Encode shim template content.
    SHIM_CONTENT=$(shim_content_b64)
    if [ -z "$SHIM_CONTENT" ]; then
      echo "::error::Failed to base64-encode shim template at $SHIM_TEMPLATE"
      FAILED=$((FAILED + 1))
      continue
    fi

    if ! write_shim_to_branch_from_default "$REPO" "$ENROLL_BRANCH" "$SHIM_CONTENT" "chore: add fullsend shim workflow"; then
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

DISABLED_REPOS=$(yq '.repos | to_entries[] | select(.value.enabled == false) | .key' "$CONFIG_FILE")

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

    # Skip repos with per-repo installation — unenrollment would break their shim.
    if check_per_repo_guard "$REPO" "unenrollment"; then
      SKIPPED=$((SKIPPED + 1))
      continue
    fi

    # Check if a removal PR already exists.
    EXISTING_PR=$(gh pr list --repo "$ORG/$REPO" --head "$UNENROLL_BRANCH" --json url --jq '.[0].url // empty' 2>/dev/null || true)
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

    if ! ensure_branch "$REPO" "$UNENROLL_BRANCH"; then
      FAILED=$((FAILED + 1))
      continue
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
echo "Updated (stale shim): $UPDATED"
echo "Unenrolled: $UNENROLLED"
echo "Skipped (already reconciled): $SKIPPED"
echo "Failed: $FAILED"

if [ "$FAILED" -gt 0 ]; then
  exit 1
fi
