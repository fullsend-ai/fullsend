#!/usr/bin/env bash
# pre-critique.sh — Prepare context for the critique agent.
#
# Downloads the refine agent's result and assembles the full context
# (issue, exploration, refinement plan, prior critique history) for review.
#
# Required env vars:
#   ISSUE_KEY        — Issue identifier
#   ISSUE_SOURCE     — "jira" or "github"
#   REFINE_RUN_ID    — GitHub Actions run ID of the refine stage
#   GH_TOKEN         — GitHub token
#
# Optional env vars:
#   REVIEW_ROUND         — Current review round (default: 1)
#   MAX_REVIEW_ROUNDS    — Max rounds before escalation (default: 3)
#   GITHUB_ISSUE_NUMBER  — GitHub issue for reply-back
#   JIRA_HOST, JIRA_EMAIL, JIRA_API_TOKEN — for Jira sources
#   REPO_FULL_NAME       — for GitHub sources

set -euo pipefail

WORKSPACE="/tmp/workspace"
mkdir -p "$WORKSPACE"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/pipeline-events.sh"

pe_start "pre-critique" "pre-critique"

REVIEW_ROUND="${REVIEW_ROUND:-1}"
MAX_REVIEW_ROUNDS="${MAX_REVIEW_ROUNDS:-3}"

echo "::notice::Pre-critique: preparing context (source=${ISSUE_SOURCE}, key=${ISSUE_KEY}, round=${REVIEW_ROUND}/${MAX_REVIEW_ROUNDS})"

# --- Step 1: Ensure issue context ---
pe_start "pre-critique" "fetch-issue-context"
if [[ ! -f "$WORKSPACE/issue-context.json" ]]; then
  if [[ -f "${SCRIPT_DIR}/pre-explore.sh" ]]; then
    echo "Fetching issue context via pre-explore.sh..."
    bash "${SCRIPT_DIR}/pre-explore.sh"
  else
    echo "ERROR: No issue context available and pre-explore.sh not found"
    exit 1
  fi
fi
pe_end "pre-critique" "fetch-issue-context" '{}'

# --- Step 2: Download refine result ---
pe_start "pre-critique" "fetch-refine-result"

# Check for refine-result.json already present (from post-refine.sh copy),
# or extract from downloaded artifact iteration layout
if [[ -f "$WORKSPACE/refine-result.json" ]]; then
  echo "Refine result already present."
elif ls "$WORKSPACE"/iteration-*/output/agent-result.json 1>/dev/null 2>&1; then
  # Artifact was downloaded directly to workspace — extract the result
  for dir in "$WORKSPACE"/iteration-*/output; do
    if [[ -f "${dir}/agent-result.json" ]]; then
      cp "${dir}/agent-result.json" "$WORKSPACE/refine-result.json"
    fi
  done
  echo "Refine result extracted from downloaded artifact."
elif [[ -n "${REFINE_RUN_ID:-}" && "${REFINE_RUN_ID}" != "N/A" ]]; then
  REPO="${REPO_FULL_NAME:-$(gh api repos/:owner/:repo --jq .full_name 2>/dev/null || echo "")}"
  if [[ -n "$REPO" ]]; then
    echo "Downloading refine artifact from run ${REFINE_RUN_ID}..."
    ARTIFACT_DIR=$(mktemp -d)
    if gh run download "$REFINE_RUN_ID" --repo "$REPO" --name "fullsend-refine" --dir "$ARTIFACT_DIR" 2>/dev/null; then
      # Find the agent-result.json in the artifact
      REFINE_RESULT_IN_ARTIFACT=""
      for dir in "$ARTIFACT_DIR"/iteration-*/output; do
        if [[ -f "${dir}/agent-result.json" ]]; then
          REFINE_RESULT_IN_ARTIFACT="${dir}/agent-result.json"
        fi
      done

      if [[ -n "$REFINE_RESULT_IN_ARTIFACT" ]]; then
        cp "$REFINE_RESULT_IN_ARTIFACT" "$WORKSPACE/refine-result.json"
        echo "Refine result extracted from artifact."
      else
        echo "::error::Refine artifact downloaded but agent-result.json not found"
        exit 1
      fi

      # Also grab exploration context and issue context if present
      for f in exploration_context.json issue-context.json; do
        if [[ -f "$ARTIFACT_DIR/$f" && ! -f "$WORKSPACE/$f" ]]; then
          cp "$ARTIFACT_DIR/$f" "$WORKSPACE/$f"
        fi
      done
    else
      echo "::error::Could not download refine artifact from run ${REFINE_RUN_ID}"
      exit 1
    fi
    rm -rf "$ARTIFACT_DIR"
  fi
else
  echo "::error::No refine result available (REFINE_RUN_ID not set)"
  exit 1
fi

if ! jq empty "$WORKSPACE/refine-result.json" 2>/dev/null; then
  echo "::error::Refine result is not valid JSON"
  exit 1
fi

pe_end "pre-critique" "fetch-refine-result" '{}'

# --- Step 3: Build critique history for round 2+ ---
pe_start "pre-critique" "build-critique-history"

if [[ "$REVIEW_ROUND" -gt 1 && ! -f "$WORKSPACE/critique-history.json" ]]; then
  echo "Round ${REVIEW_ROUND}: building critique history from prior rounds..."
  # History is accumulated by post-critique.sh and passed as an artifact.
  # If it's not already present from artifact download, create empty placeholder.
  echo '{"rounds": [], "note": "History not available from artifact — critique agent should focus on current plan quality"}' \
    > "$WORKSPACE/critique-history.json"
fi

if [[ ! -f "$WORKSPACE/critique-history.json" ]]; then
  echo '{"rounds": []}' > "$WORKSPACE/critique-history.json"
fi

pe_end "pre-critique" "build-critique-history" "$(jq -nc --argjson round "$REVIEW_ROUND" '{review_round:$round}')"

# --- Step 4: Ensure exploration context ---
if [[ ! -f "$WORKSPACE/exploration_context.json" ]]; then
  echo "::warning::No exploration context available — critique will rely on issue context and refine result"
  echo '{"gaps": [{"dimension": "exploration", "description": "Explore stage context not available to critique"}], "confidence": {"overall": 50}}' \
    > "$WORKSPACE/exploration_context.json"
fi

# --- Export paths ---
{
  echo "ISSUE_CONTEXT=$WORKSPACE/issue-context.json"
  echo "EXPLORE_CONTEXT=$WORKSPACE/exploration_context.json"
  echo "REFINE_RESULT=$WORKSPACE/refine-result.json"
  echo "CRITIQUE_HISTORY=$WORKSPACE/critique-history.json"
  echo "REVIEW_ROUND=$REVIEW_ROUND"
  echo "MAX_REVIEW_ROUNDS=$MAX_REVIEW_ROUNDS"
} >> "${GITHUB_ENV:-/dev/null}"

pe_end "pre-critique" "pre-critique" "$(jq -nc --arg source "$ISSUE_SOURCE" --arg key "$ISSUE_KEY" --argjson round "$REVIEW_ROUND" '{source:$source, key:$key, review_round:$round}')"

echo "Pre-critique complete."
