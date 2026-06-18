#!/usr/bin/env bash
# pre-refine.sh — Prepare context for the refine agent.
#
# Fetches issue data (if not already available) and downloads/locates
# the exploration context from one of three sources:
#   1. Explore workflow artifact (EXPLORE_RUN_ID)
#   2. User-provided file from a repo (EXPLORE_CONTEXT_REF = owner/repo:path)
#   3. User-provided file from a Jira attachment (EXPLORE_CONTEXT_REF = attachment name)
#
# Required env vars:
#   ISSUE_KEY        — Issue identifier
#   ISSUE_SOURCE     — "jira" or "github"
#   GH_TOKEN         — GitHub token
#
# Optional env vars:
#   EXPLORE_RUN_ID       — GitHub Actions run ID of the explore stage
#   EXPLORE_CONTEXT_REF  — User-provided exploration context reference
#   CRITIQUE_RUN_ID      — GitHub Actions run ID of the critique stage (revision rounds)
#   REVIEW_ROUND         — Current review round (default: 1)
#   GITHUB_ISSUE_NUMBER  — GitHub issue for reply-back (GitHub flow)
#   JIRA_HOST, JIRA_EMAIL, JIRA_API_TOKEN — for Jira sources
#   REPO_FULL_NAME       — for GitHub sources

set -euo pipefail

WORKSPACE="/tmp/workspace"
mkdir -p "$WORKSPACE"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/pipeline-events.sh"

pe_start "pre-refine" "pre-refine"

echo "::notice::Pre-refine: preparing context (source=${ISSUE_SOURCE}, key=${ISSUE_KEY})"

pe_start "pre-refine" "fetch-issue-context"
if [[ ! -f "$WORKSPACE/issue-context.json" ]]; then
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  if [[ -f "${SCRIPT_DIR}/pre-explore.sh" ]]; then
    echo "Fetching issue context via pre-explore.sh..."
    bash "${SCRIPT_DIR}/pre-explore.sh"
  else
    echo "ERROR: No issue context available and pre-explore.sh not found"
    exit 1
  fi
fi

pe_end "pre-refine" "fetch-issue-context" '{}'

pe_start "pre-refine" "obtain-exploration-context"

if [[ -f "$WORKSPACE/exploration_context.json" ]]; then
  echo "Exploration context already present."

elif [[ -n "${EXPLORE_CONTEXT_REF:-}" && "${EXPLORE_CONTEXT_REF}" != "N/A" ]]; then
  # User-provided exploration context (skip-explore flow)
  echo "Fetching user-provided exploration context: ${EXPLORE_CONTEXT_REF}"

  if [[ "$EXPLORE_CONTEXT_REF" =~ ^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+:.+ ]]; then
    # Format: owner/repo:path/to/file.json — fetch from a GitHub repo
    CONTEXT_REPO="${EXPLORE_CONTEXT_REF%%:*}"
    CONTEXT_PATH="${EXPLORE_CONTEXT_REF#*:}"

    echo "  Fetching from GitHub repo: ${CONTEXT_REPO} path: ${CONTEXT_PATH}"
    gh api "repos/${CONTEXT_REPO}/contents/${CONTEXT_PATH}" \
      --jq '.content' | base64 -d > "$WORKSPACE/exploration_context.json" \
      || { echo "::error::Failed to fetch exploration context from ${CONTEXT_REPO}:${CONTEXT_PATH}"; exit 1; }

  elif [[ "$EXPLORE_CONTEXT_REF" =~ ^https?:// ]]; then
    # Direct URL
    echo "  Fetching from URL: ${EXPLORE_CONTEXT_REF}"
    curl -sSfL "$EXPLORE_CONTEXT_REF" > "$WORKSPACE/exploration_context.json" \
      || { echo "::error::Failed to fetch exploration context from URL"; exit 1; }

  elif [[ "${ISSUE_SOURCE}" == "jira" && -n "${JIRA_HOST:-}" ]]; then
    # Treat as Jira attachment name
    ATTACHMENT_NAME="$EXPLORE_CONTEXT_REF"
    echo "  Fetching Jira attachment: ${ATTACHMENT_NAME}"

    AUTH=$(printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 -w0)
    ATTACHMENT_URL=$(curl -sSf \
      -H "Authorization: Basic $AUTH" \
      -H "Accept: application/json" \
      "https://${JIRA_HOST}/rest/api/3/issue/${ISSUE_KEY}?fields=attachment" \
      | jq -r --arg name "$ATTACHMENT_NAME" \
        '.fields.attachment[] | select(.filename == $name) | .content' \
      | head -1)

    if [[ -z "$ATTACHMENT_URL" ]]; then
      echo "::error::Jira attachment '${ATTACHMENT_NAME}' not found on ${ISSUE_KEY}"
      exit 1
    fi

    curl -sSfL -H "Authorization: Basic $AUTH" \
      "$ATTACHMENT_URL" > "$WORKSPACE/exploration_context.json" \
      || { echo "::error::Failed to download Jira attachment"; exit 1; }

  else
    echo "::error::Cannot resolve EXPLORE_CONTEXT_REF: ${EXPLORE_CONTEXT_REF}"
    exit 1
  fi

  # Validate the fetched context
  if ! jq empty "$WORKSPACE/exploration_context.json" 2>/dev/null; then
    echo "::error::Fetched exploration context is not valid JSON"
    exit 1
  fi

  echo "User-provided exploration context loaded."

elif [[ -n "${EXPLORE_RUN_ID:-}" && "${EXPLORE_RUN_ID}" != "N/A" ]]; then
  # Download from explore workflow artifact
  REPO="${REPO_FULL_NAME:-$(gh api repos/:owner/:repo --jq .full_name 2>/dev/null || echo "")}"
  if [[ -n "$REPO" ]]; then
    echo "Downloading exploration artifact from run ${EXPLORE_RUN_ID}..."
    ARTIFACT_DIR=$(mktemp -d)
    if gh run download "$EXPLORE_RUN_ID" --repo "$REPO" --name "fullsend-explore" --dir "$ARTIFACT_DIR" 2>/dev/null; then
      if [[ -f "$ARTIFACT_DIR/exploration_context.json" ]]; then
        cp "$ARTIFACT_DIR/exploration_context.json" "$WORKSPACE/exploration_context.json"
        echo "Exploration context downloaded from explore run."
      fi
    else
      echo "::warning::Could not download exploration artifact — refine will proceed without it"
    fi
    rm -rf "$ARTIFACT_DIR"
  fi
fi

pe_end "pre-refine" "obtain-exploration-context" "$(jq -nc --argjson has_context "$(if [[ -f "$WORKSPACE/exploration_context.json" ]]; then echo true; else echo false; fi)" '{has_exploration_context:$has_context}')"

# Step 3: If no exploration context, create a minimal placeholder
if [[ ! -f "$WORKSPACE/exploration_context.json" ]]; then
  echo "::warning::No exploration context available — refine agent will rely on issue context and codebase only"
  echo '{"gaps": [{"dimension": "exploration", "description": "Explore stage did not run", "impact": "Refine agent has limited context"}], "confidence": {"overall": 50}}' \
    > "$WORKSPACE/exploration_context.json"
fi

# --- Step 4: Load critique feedback for revision rounds ---
REVIEW_ROUND="${REVIEW_ROUND:-1}"

if [[ "$REVIEW_ROUND" -gt 1 && -n "${CRITIQUE_RUN_ID:-}" && "${CRITIQUE_RUN_ID}" != "N/A" ]]; then
  pe_start "pre-refine" "fetch-critique-feedback"
  echo "Revision round ${REVIEW_ROUND}: downloading critique feedback from run ${CRITIQUE_RUN_ID}..."

  REPO="${REPO_FULL_NAME:-$(gh api repos/:owner/:repo --jq .full_name 2>/dev/null || echo "")}"
  if [[ -n "$REPO" ]]; then
    ARTIFACT_DIR=$(mktemp -d)
    if gh run download "$CRITIQUE_RUN_ID" --repo "$REPO" --name "fullsend-critique" --dir "$ARTIFACT_DIR" 2>/dev/null; then
      # Find the critique result
      CRITIQUE_RESULT_IN_ARTIFACT=""
      for dir in "$ARTIFACT_DIR"/iteration-*/output; do
        if [[ -f "${dir}/agent-result.json" ]]; then
          CRITIQUE_RESULT_IN_ARTIFACT="${dir}/agent-result.json"
        fi
      done

      if [[ -n "$CRITIQUE_RESULT_IN_ARTIFACT" ]]; then
        cp "$CRITIQUE_RESULT_IN_ARTIFACT" "$WORKSPACE/critique-feedback.json"
        echo "Critique feedback loaded."
      fi

      # Also grab critique history and exploration context if present
      for f in critique-history.json exploration_context.json issue-context.json; do
        if [[ -f "$ARTIFACT_DIR/$f" && ! -f "$WORKSPACE/$f" ]]; then
          cp "$ARTIFACT_DIR/$f" "$WORKSPACE/$f"
        fi
      done
    else
      echo "::warning::Could not download critique artifact — refine will proceed without feedback"
    fi
    rm -rf "$ARTIFACT_DIR"
  fi
  pe_end "pre-refine" "fetch-critique-feedback" "$(jq -nc --argjson round "$REVIEW_ROUND" '{review_round:$round}')"
elif [[ -f "$WORKSPACE/critique-feedback.json" ]]; then
  echo "Critique feedback already present from artifact download."
fi

{
  echo "ISSUE_CONTEXT=$WORKSPACE/issue-context.json"
  echo "EXPLORE_CONTEXT=$WORKSPACE/exploration_context.json"
  echo "REVIEW_ROUND=$REVIEW_ROUND"
} >> "${GITHUB_ENV:-/dev/null}"

if [[ -f "$WORKSPACE/critique-feedback.json" ]]; then
  echo "CRITIQUE_FEEDBACK=$WORKSPACE/critique-feedback.json" >> "${GITHUB_ENV:-/dev/null}"
fi

pe_end "pre-refine" "pre-refine" "$(jq -nc --arg source "$ISSUE_SOURCE" --arg key "$ISSUE_KEY" --argjson round "$REVIEW_ROUND" '{source:$source, key:$key, review_round:$round}')"

echo "Pre-refine complete."
