#!/usr/bin/env bash
# post-refine.sh — Process refine agent output and chain to critique.
#
# The refine agent ALWAYS produces a plan (status=complete). This script
# posts a summary comment and ALWAYS chains to the critique agent regardless
# of confidence. The confidence score is passed to critique as context —
# critique decides whether to approve, request revisions, or escalate.
#
# Issue creation is handled downstream by the critique agent's approval flow,
# NOT by this script. See post-critique.sh and create-children.sh.
#
# Routing: results go back to the same system that owns the work item.
#   - GitHub flow: GITHUB_ISSUE_NUMBER is set → post to GitHub issue
#   - Jira flow: GITHUB_ISSUE_NUMBER is empty → post to Jira
#
# Required env vars:
#   ISSUE_KEY      — Issue identifier (Jira key or GH issue number)
#   ISSUE_SOURCE   — "jira" or "github"
#   GH_TOKEN       — GitHub token
#
# GitHub flow env vars:
#   GITHUB_ISSUE_NUMBER — GitHub issue number to post results to
#   REPO_FULL_NAME      — owner/repo
#   PUSH_TOKEN          — Token with write access
#
# Jira flow env vars:
#   JIRA_HOST, JIRA_EMAIL, JIRA_API_TOKEN
#
# Critique flow env vars (passed through from critique → refine loop):
#   REVIEW_ROUND        — Current review round (default: 1)
#   MAX_REVIEW_ROUNDS   — Max rounds (default: 3)
#   AUTO_CREATE         — "true" to auto-create on approval (default: "false")

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/pipeline-events.sh"
source "${SCRIPT_DIR}/pipeline-helpers.sh"

pe_start "post-refine" "post-refine"

RESULT_FILE=$(find_agent_result) || exit 1
echo "Reading refine result from: ${RESULT_FILE}"

STATUS=$(jq -r '.status' "${RESULT_FILE}")
COMMENT=$(jq -r '.comment // ""' "${RESULT_FILE}")
CONFIDENCE=$(jq -r '.confidence.overall // 0' "${RESULT_FILE}")

echo "Status: ${STATUS}, Confidence: ${CONFIDENCE}"

determine_reply_target
echo "Reply target: $(if $USE_GITHUB; then echo "GitHub #${GITHUB_ISSUE_NUMBER}"; else echo "Jira ${ISSUE_KEY}"; fi)"

REVIEW_ROUND="${REVIEW_ROUND:-1}"
MAX_REVIEW_ROUNDS="${MAX_REVIEW_ROUNDS:-3}"
AUTO_CREATE="${AUTO_CREATE:-false}"
IS_REVISION=$([[ "$REVIEW_ROUND" -gt 1 ]] && echo "true" || echo "false")

RUN_LINK=$(build_run_link)

AGENT_HEADER="📋 **Refine Agent** · ${RUN_LINK}"
if [[ "$IS_REVISION" == "true" ]]; then
  AGENT_HEADER="${AGENT_HEADER} · Iteration ${REVIEW_ROUND} (revised)"
fi

# --- Post plan and always chain to critique ---

CONFIDENCE_INT=$(printf '%.0f' "$CONFIDENCE" 2>/dev/null || echo "0")

pe_start "post-refine" "post-plan"
echo "::notice::Refine complete (confidence ${CONFIDENCE_INT}/100) — posting proposed plan and chaining critique"

if $USE_GITHUB; then
  remove_label "${REPO_FULL_NAME}" "$GITHUB_ISSUE_NUMBER" "refine-needs-input"
  remove_label "${REPO_FULL_NAME}" "$GITHUB_ISSUE_NUMBER" "human-refinement"
fi

CHILD_COUNT=$(jq '.children | length' "${RESULT_FILE}" 2>/dev/null || echo "0")
OPEN_QUESTION_COUNT=$(jq '.open_questions | length' "${RESULT_FILE}" 2>/dev/null || echo "0")

EPIC_COUNT=$(jq '[.children[]? | select(.type == "epic")] | length' "${RESULT_FILE}" 2>/dev/null || echo "0")
STORY_COUNT=$(jq '[.children[]? | select(.type == "story")] | length' "${RESULT_FILE}" 2>/dev/null || echo "0")
TASK_COUNT=$(jq '[.children[]? | select(.type == "task")] | length' "${RESULT_FILE}" 2>/dev/null || echo "0")

PLAN_SUMMARY="Proposed: ${CHILD_COUNT} work items"
PLAN_PARTS=()
[[ "$EPIC_COUNT" -gt 0 ]] && PLAN_PARTS+=("${EPIC_COUNT} epics")
[[ "$STORY_COUNT" -gt 0 ]] && PLAN_PARTS+=("${STORY_COUNT} stories")
[[ "$TASK_COUNT" -gt 0 ]] && PLAN_PARTS+=("${TASK_COUNT} tasks")
if [[ ${#PLAN_PARTS[@]} -gt 0 ]]; then
  PLAN_SUMMARY="${PLAN_SUMMARY} ($(IFS=', '; echo "${PLAN_PARTS[*]}"))"
fi

if [[ "$OPEN_QUESTION_COUNT" -gt 0 ]]; then
  PLAN_SUMMARY="${PLAN_SUMMARY} · ${OPEN_QUESTION_COUNT} open question(s)"
fi

WORKFLOW_REPO="${GITHUB_REPOSITORY:-${REPO_FULL_NAME}}"
ARTIFACT_URL="https://github.com/${WORKFLOW_REPO}/actions/runs/${GITHUB_RUN_ID:-}"

QUESTIONS_SECTION=""
if [[ "$OPEN_QUESTION_COUNT" -gt 0 ]]; then
  QUESTIONS_LIST=$(jq -r '.open_questions[]? | if type == "object" then "- **\(.dimension // "general")**: \(.question // .text // .description // tostring)\n  *Impact*: \(.impact // "Unknown")" else "- \(tostring)" end' "${RESULT_FILE}" 2>/dev/null || true)
  if [[ -n "$QUESTIONS_LIST" ]]; then
    QUESTIONS_SECTION="
---

## Open Questions

${OPEN_QUESTION_COUNT} question(s) that may affect plan accuracy — reply with answers, then comment \`/fs-refine\` to re-run.

${QUESTIONS_LIST}"
  fi
fi

PLAN_COMMENT="${AGENT_HEADER}

**Refinement Plan** (confidence: ${CONFIDENCE}/100)

${PLAN_SUMMARY}

${COMMENT}

📎 [**Full plan details** (all epics, stories, tasks, acceptance criteria)](${ARTIFACT_URL}) — download the \`fullsend-refine\` artifact for the complete \`refine-result.json\`.
${QUESTIONS_SECTION}

---
*This plan will be reviewed by the Critique Agent before any issues are created.*"

post_comment "$PLAN_COMMENT"

# Post proposed description as a separate comment (standalone document)
PROPOSED_DESC=$(jq -r '.proposed_description // ""' "${RESULT_FILE}")
if [[ -n "$PROPOSED_DESC" && "$PROPOSED_DESC" != "null" ]]; then
  DESC_COMMENT="📝 **Refine Agent** · Proposed Feature Description

The following is a proposed enhanced description for this feature based on exploration research and decomposition analysis. If the plan is approved, this description can replace the current one.

---

${PROPOSED_DESC}"

  post_comment "$DESC_COMMENT"
fi

# Save the refine result for critique to pick up via artifact
cp "${RESULT_FILE}" "/tmp/workspace/refine-result.json"

# Chain to the critique agent
WORKFLOW_REPO="${GITHUB_REPOSITORY}"
TARGET_REPO="${REPO_FULL_NAME:-}"
THIS_RUN_ID="${GITHUB_RUN_ID:-}"

if [[ -n "$THIS_RUN_ID" ]]; then
  pe_start "post-refine" "chain-critique"
  echo "Chaining critique stage with refine run ID: ${THIS_RUN_ID}"

  CURRENT_TRACEPARENT=$(get_traceparent)

  CHAIN_ARGS=(
    --repo "$WORKFLOW_REPO"
    -f issue_key="${ISSUE_KEY}"
    -f issue_source="${ISSUE_SOURCE}"
    -f refine_run_id="${THIS_RUN_ID}"
    -f review_round="${REVIEW_ROUND}"
    -f max_review_rounds="${MAX_REVIEW_ROUNDS}"
    -f auto_create="${AUTO_CREATE}"
  )

  if [[ -n "$TARGET_REPO" ]]; then
    CHAIN_ARGS+=(-f repo_full_name="${TARGET_REPO}")
    echo "Propagating target repo: ${TARGET_REPO}"
  fi

  if [[ -n "$CURRENT_TRACEPARENT" ]]; then
    CHAIN_ARGS+=(-f parent_traceparent="${CURRENT_TRACEPARENT}")
    echo "Propagating trace context: ${CURRENT_TRACEPARENT}"
  fi

  if [[ -n "${GITHUB_ISSUE_NUMBER:-}" && "${GITHUB_ISSUE_NUMBER}" != "N/A" ]]; then
    CHAIN_ARGS+=(-f github_issue_number="${GITHUB_ISSUE_NUMBER}")
  fi

  gh workflow run critique.yml "${CHAIN_ARGS[@]}" \
    2>/dev/null || echo "::warning::Failed to chain critique workflow — trigger manually"
  pe_end "post-refine" "chain-critique" "$(jq -nc --arg run_id "$THIS_RUN_ID" --arg traceparent "$CURRENT_TRACEPARENT" --argjson round "$REVIEW_ROUND" '{refine_run_id:$run_id, traceparent:$traceparent, review_round:$round}')"
else
  echo "::warning::GITHUB_RUN_ID not available — critique must be triggered manually"
fi

pe_end "post-refine" "post-plan" "$(jq -nc --argjson total "$CHILD_COUNT" --argjson epics "$EPIC_COUNT" --argjson stories "$STORY_COUNT" --argjson tasks "$TASK_COUNT" --argjson open_questions "$OPEN_QUESTION_COUNT" '{total:$total, epics:$epics, stories:$stories, tasks:$tasks, open_questions:$open_questions}')"

pe_end "post-refine" "post-refine" "$(jq -nc --arg status "$STATUS" --argjson confidence "$CONFIDENCE_INT" --argjson round "$REVIEW_ROUND" '{status:$status, confidence:$confidence, review_round:$round}')"
pe_copy_to_output

echo "Post-refine complete."
