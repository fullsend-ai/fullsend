#!/usr/bin/env bash
# post-explore.sh — Store exploration results and chain the refine stage.
#
# The explore agent writes its result to agent-result.json. This script
# validates the output, stores it for artifact upload, and triggers the
# refine workflow with this run's ID for artifact correlation.
#
# Required env vars:
#   ISSUE_KEY      — Issue identifier
#   ISSUE_SOURCE   — "jira" or "github"
#   REPO_FULL_NAME — owner/repo
#   GH_TOKEN       — GitHub token

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/pipeline-events.sh"
source "${SCRIPT_DIR}/pipeline-helpers.sh"

pe_start "post-explore" "post-explore"

RESULT_FILE=$(find_agent_result) || exit 1
echo "Reading exploration result from: ${RESULT_FILE}"

pe_start "post-explore" "validate-result"

OVERALL_CONFIDENCE=$(jq -r '.confidence.overall // 0' "${RESULT_FILE}")
GAP_COUNT=$(jq '.gaps // [] | length' "${RESULT_FILE}")
RELATED_COUNT=$(jq '.related_work | length' "${RESULT_FILE}")

pe_end "post-explore" "validate-result" "$(jq -nc --argjson conf "$OVERALL_CONFIDENCE" --argjson gaps "$GAP_COUNT" --argjson related "$RELATED_COUNT" '{confidence:$conf, gap_count:$gaps, related_work_count:$related}')"

echo "::notice::Exploration complete: confidence=${OVERALL_CONFIDENCE}, gaps=${GAP_COUNT}, related_work=${RELATED_COUNT}"

# Copy to a well-known location for artifact upload
WORKSPACE="/tmp/workspace"
mkdir -p "$WORKSPACE"
cp "${RESULT_FILE}" "${WORKSPACE}/exploration_context.json"

echo "Exploration context saved to ${WORKSPACE}/exploration_context.json"

# Chain the refine stage with this run's ID for artifact correlation.
# GITHUB_RUN_ID is set by GitHub Actions automatically.
WORKFLOW_REPO="${GITHUB_REPOSITORY}"
TARGET_REPO="${REPO_FULL_NAME:-}"
THIS_RUN_ID="${GITHUB_RUN_ID:-}"

if [[ -n "$THIS_RUN_ID" ]]; then
  pe_start "post-explore" "chain-refine"
  echo "Chaining refine stage with explore run ID: ${THIS_RUN_ID}"

  CURRENT_TRACEPARENT=$(get_traceparent)

  CHAIN_ARGS=(
    --repo "$WORKFLOW_REPO"
    -f issue_key="${ISSUE_KEY}"
    -f issue_source="${ISSUE_SOURCE}"
    -f explore_run_id="${THIS_RUN_ID}"
  )

  # Only pass repo_full_name if a target repo was specified (not the config repo)
  if [[ -n "$TARGET_REPO" ]]; then
    CHAIN_ARGS+=(-f repo_full_name="${TARGET_REPO}")
    echo "Propagating target repo: ${TARGET_REPO}"
  fi

  # Propagate trace context for distributed tracing
  if [[ -n "$CURRENT_TRACEPARENT" ]]; then
    CHAIN_ARGS+=(-f parent_traceparent="${CURRENT_TRACEPARENT}")
    echo "Propagating trace context: ${CURRENT_TRACEPARENT}"
  fi

  # Pass through GitHub issue number for reply-back (GitHub flow)
  if [[ -n "${GITHUB_ISSUE_NUMBER:-}" && "${GITHUB_ISSUE_NUMBER}" != "N/A" ]]; then
    CHAIN_ARGS+=(-f github_issue_number="${GITHUB_ISSUE_NUMBER}")
  fi

  # Pass through auto-create preference to refine → critique chain
  if [[ "${AUTO_CREATE:-false}" == "true" ]]; then
    CHAIN_ARGS+=(-f auto_create="true")
  fi

  gh workflow run refine.yml "${CHAIN_ARGS[@]}" \
    2>/dev/null || echo "::warning::Failed to chain refine workflow — trigger manually"
  pe_end "post-explore" "chain-refine" "$(jq -nc --arg run_id "$THIS_RUN_ID" --arg traceparent "$CURRENT_TRACEPARENT" '{explore_run_id:$run_id, traceparent:$traceparent}')"
else
  echo "::warning::GITHUB_RUN_ID not available — refine must be triggered manually"
fi

# --- Post exploration summary with agent identity ---
EXPLORE_SUMMARY=$(jq -r '.summary // "Exploration complete."' "${RESULT_FILE}")

RUN_LINK=$(build_run_link)

EXPLORE_COMMENT="🔍 **Explore Agent** · ${RUN_LINK}

**Status: ✅ Exploration Complete** (confidence: ${OVERALL_CONFIDENCE}/100, gaps: ${GAP_COUNT}, related work: ${RELATED_COUNT})

${EXPLORE_SUMMARY}

---
*Chaining to the Refine Agent for decomposition.*"

determine_reply_target
post_comment "$EXPLORE_COMMENT" 2>/dev/null || true

if $USE_GITHUB; then
  EVAL_META=$(jq -nc \
    --arg run_id "${GITHUB_RUN_ID:-manual}" \
    --arg agent "explore" \
    --arg issue_key "${ISSUE_KEY}" \
    --arg issue_source "${ISSUE_SOURCE:-unknown}" \
    --arg status "complete" \
    --argjson confidence "$OVERALL_CONFIDENCE" \
    --argjson dimensions "$(jq '.confidence // {}' "${RESULT_FILE}")" \
    --argjson child_count 0 \
    '{run_id:$run_id, agent:$agent, issue_key:$issue_key, issue_source:$issue_source, status:$status, confidence:$confidence, dimensions:$dimensions, child_count:$child_count}')

  EVAL_PROMPT="---
**Eval this run:** React with :+1: or :-1: on this comment, or reply \`/eval yes\` or \`/eval no \"reason\"\`.
<!-- eval-meta: ${EVAL_META} -->"

  github_comment "${REPO_FULL_NAME:-${GITHUB_REPOSITORY}}" "$GITHUB_ISSUE_NUMBER" "$EVAL_PROMPT" 2>/dev/null || true
fi

pe_end "post-explore" "post-explore" "$(jq -nc --argjson conf "$OVERALL_CONFIDENCE" --argjson gaps "$GAP_COUNT" '{confidence:$conf, gaps:$gaps}')"
pe_copy_to_output

echo "Post-explore complete."
