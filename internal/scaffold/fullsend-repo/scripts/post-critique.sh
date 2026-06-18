#!/usr/bin/env bash
# post-critique.sh — Process critique agent output.
#
# Reads the critique result and performs one of:
#   - verdict=approved + AUTO_CREATE=true: creates child issues immediately
#   - verdict=approved + AUTO_CREATE=false: posts approval, adds label for human gate
#   - verdict=revise + under iteration limit: posts feedback, chains back to refine
#   - verdict=revise + at iteration limit: posts final plan for human decision
#
# Required env vars:
#   ISSUE_KEY      — Issue identifier (Jira key or GH issue number)
#   ISSUE_SOURCE   — "jira" or "github"
#   GH_TOKEN       — GitHub token
#
# GitHub flow env vars:
#   GITHUB_ISSUE_NUMBER — GitHub issue number
#   REPO_FULL_NAME      — owner/repo
#   PUSH_TOKEN          — Token with write access
#
# Jira flow env vars:
#   JIRA_HOST, JIRA_EMAIL, JIRA_API_TOKEN
#
# Critique flow env vars:
#   REVIEW_ROUND        — Current review round (default: 1)
#   MAX_REVIEW_ROUNDS   — Max rounds (default: 3)
#   AUTO_CREATE         — "true" to auto-create on approval (default: "false")
#   REFINE_RUN_ID       — Run ID of the refine stage

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/pipeline-events.sh"
source "${SCRIPT_DIR}/pipeline-helpers.sh"

pe_start "post-critique" "post-critique"

REVIEW_ROUND="${REVIEW_ROUND:-1}"
MAX_REVIEW_ROUNDS="${MAX_REVIEW_ROUNDS:-3}"
AUTO_CREATE="${AUTO_CREATE:-false}"

RESULT_FILE=$(find_agent_result) || exit 1
echo "Reading critique result from: ${RESULT_FILE}"

VERDICT=$(jq -r '.verdict' "${RESULT_FILE}")
COMMENT=$(jq -r '.comment // ""' "${RESULT_FILE}")
OVERALL_SCORE=$(jq -r '.assessment.overall // 0' "${RESULT_FILE}")
REVISION_COUNT=$(jq '.revisions // [] | length' "${RESULT_FILE}")

echo "Verdict: ${VERDICT}, Overall score: ${OVERALL_SCORE}, Revisions: ${REVISION_COUNT}, Round: ${REVIEW_ROUND}/${MAX_REVIEW_ROUNDS}"

determine_reply_target
RUN_LINK=$(build_run_link)

AGENT_HEADER="🔎 **Critique Agent** · ${RUN_LINK} · Review Round ${REVIEW_ROUND}"

echo "Reply target: $(if $USE_GITHUB; then echo "GitHub #${GITHUB_ISSUE_NUMBER}"; else echo "Jira ${ISSUE_KEY}"; fi)"

# --- Update critique history ---
# Accumulate review rounds for the next iteration's context
CRITIQUE_HISTORY_FILE="/tmp/workspace/critique-history.json"
if [[ -f "$CRITIQUE_HISTORY_FILE" ]]; then
  UPDATED_HISTORY=$(jq --argjson round "$REVIEW_ROUND" \
    --arg verdict "$VERDICT" \
    --argjson score "$OVERALL_SCORE" \
    --argjson revisions "$(jq '.revisions // []' "$RESULT_FILE")" \
    '.rounds += [{"round": $round, "verdict": $verdict, "overall_score": $score, "revisions": $revisions}]' \
    "$CRITIQUE_HISTORY_FILE")
  echo "$UPDATED_HISTORY" > "$CRITIQUE_HISTORY_FILE"
else
  jq -n --argjson round "$REVIEW_ROUND" \
    --arg verdict "$VERDICT" \
    --argjson score "$OVERALL_SCORE" \
    --argjson revisions "$(jq '.revisions // []' "$RESULT_FILE")" \
    '{rounds: [{"round": $round, "verdict": $verdict, "overall_score": $score, "revisions": $revisions}]}' \
    > "$CRITIQUE_HISTORY_FILE"
fi

# --- Process based on verdict ---

if [[ "${VERDICT}" == "approved" ]]; then
  pe_start "post-critique" "handle-approval"
  echo "::notice::Critique approved the refinement plan (round ${REVIEW_ROUND})"

  FULL_COMMENT="${AGENT_HEADER}

**Verdict: ✅ Approved** (score: ${OVERALL_SCORE}/100)

${COMMENT}"

  if [[ "${AUTO_CREATE}" == "true" ]]; then
    echo "Auto-create enabled — creating child issues..."

    # Post the approval comment first
    post_comment "$FULL_COMMENT"

    # Find the refine result to create children from
    REFINE_RESULT_FILE="/tmp/workspace/refine-result.json"
    if [[ ! -f "$REFINE_RESULT_FILE" ]]; then
      echo "::error::Refine result not found at ${REFINE_RESULT_FILE}"
      exit 1
    fi

    # Delegate to create-children.sh
    export RESULT_FILE="$REFINE_RESULT_FILE"
    bash "${SCRIPT_DIR}/create-children.sh"

    CHILD_SUMMARY="Created ${CREATED_CHILD_COUNT:-0} child issue(s): ${CREATED_CHILD_KEYS:-none}"
    echo "::notice::${CHILD_SUMMARY}"

    CREATION_COMMENT="📦 **Issue Creator** · Run #${GITHUB_RUN_ID:-manual}

**Child issues created** after critique approval.

${CHILD_SUMMARY}"
    post_comment "$CREATION_COMMENT"

  else
    echo "Auto-create disabled — posting approval for human review"

    PLAN_CHILD_COUNT=$(jq '.children | length' "/tmp/workspace/refine-result.json" 2>/dev/null || echo "0")

    APPROVAL_COMMENT="${FULL_COMMENT}

---
**Ready for human approval.** The plan proposes ${PLAN_CHILD_COUNT} child issue(s).

To create the child issues, comment \`/fs-create\`.
To request further changes, reply with your feedback."

    post_comment "$APPROVAL_COMMENT"

    if $USE_GITHUB; then
      add_label "${REPO_FULL_NAME}" "$GITHUB_ISSUE_NUMBER" "refine-approved"
    fi
  fi

  pe_end "post-critique" "handle-approval" "$(jq -nc --arg auto_create "$AUTO_CREATE" --argjson score "$OVERALL_SCORE" '{auto_create:$auto_create, overall_score:$score}')"

elif [[ "${VERDICT}" == "revise" ]]; then
  NEXT_ROUND=$((REVIEW_ROUND + 1))

  if [[ $NEXT_ROUND -gt $MAX_REVIEW_ROUNDS ]]; then
    pe_start "post-critique" "handle-max-iterations"
    echo "::warning::Max review rounds (${MAX_REVIEW_ROUNDS}) reached — escalating to human"

    PLAN_CHILD_COUNT=$(jq '.children | length' "/tmp/workspace/refine-result.json" 2>/dev/null || echo "0")

    # Synthesize review history for human handoff
    ROUND_SUMMARY=""
    if [[ -f "$CRITIQUE_HISTORY_FILE" ]]; then
      ROUND_SUMMARY=$(jq -r '
        "| Round | Score | Verdict | Revisions |\n|-------|-------|---------|-----------|",
        (.rounds[] |
          "| \(.round) | \(.overall_score)/100 | \(.verdict) | \(.revisions | length) revision(s): \(.revisions | map(.type + ": " + (.target // .description // "—")[0:40]) | join(", ")) |"
        )
      ' "$CRITIQUE_HISTORY_FILE" 2>/dev/null || echo "")
    fi

    HISTORY_SECTION=""
    if [[ -n "$ROUND_SUMMARY" ]]; then
      HISTORY_SECTION="
### Review History

${ROUND_SUMMARY}
"
    fi

    ESCALATION_COMMENT="${AGENT_HEADER}

**Verdict: ⚠️ Max review rounds reached** (${MAX_REVIEW_ROUNDS} rounds, score: ${OVERALL_SCORE}/100)

${COMMENT}
${HISTORY_SECTION}
---
**Human decision needed.** The critique agent still has concerns after ${MAX_REVIEW_ROUNDS} rounds of review.

The current plan proposes ${PLAN_CHILD_COUNT} child issue(s). Options:
- Reply \`/fs-create\` to create the issues as-is
- Reply \`/fs-refine\` to restart the refinement process
- Reply with specific guidance for the refine agent"

    post_comment "$ESCALATION_COMMENT"

    if $USE_GITHUB; then
      add_label "${REPO_FULL_NAME}" "$GITHUB_ISSUE_NUMBER" "refine-needs-human"
      add_label "${REPO_FULL_NAME}" "$GITHUB_ISSUE_NUMBER" "refine-stalled"
      add_label "${REPO_FULL_NAME}" "$GITHUB_ISSUE_NUMBER" "refine-approved"
    fi

    # Mark critique history as approved so create-children.yml can verify
    if [[ -f "$CRITIQUE_HISTORY_FILE" ]]; then
      UPDATED=$(jq '.rounds[-1].verdict = "approved" | .rounds[-1].escalated = true' "$CRITIQUE_HISTORY_FILE")
      echo "$UPDATED" > "$CRITIQUE_HISTORY_FILE"
    fi

    pe_end "post-critique" "handle-max-iterations" "$(jq -nc --argjson round "$REVIEW_ROUND" --argjson score "$OVERALL_SCORE" '{round:$round, score:$score}')"

  else
    pe_start "post-critique" "handle-revision"
    echo "::notice::Critique requests revisions — chaining back to refine (round ${NEXT_ROUND})"

    REVISION_COMMENT="${AGENT_HEADER}

**Verdict: 🔄 Revisions requested** (score: ${OVERALL_SCORE}/100, ${REVISION_COUNT} revision(s))

${COMMENT}"

    post_comment "$REVISION_COMMENT"

    # Save critique result for refine to read
    cp "$RESULT_FILE" "/tmp/workspace/critique-feedback.json"

    # Chain back to refine with review context
    WORKFLOW_REPO="${GITHUB_REPOSITORY}"
    TARGET_REPO="${REPO_FULL_NAME:-}"
    THIS_RUN_ID="${GITHUB_RUN_ID:-}"

    if [[ -n "$THIS_RUN_ID" ]]; then
      CURRENT_TRACEPARENT=$(get_traceparent)

      CHAIN_ARGS=(
        --repo "$WORKFLOW_REPO"
        -f issue_key="${ISSUE_KEY}"
        -f issue_source="${ISSUE_SOURCE}"
        -f critique_run_id="${THIS_RUN_ID}"
        -f review_round="${NEXT_ROUND}"
        -f max_review_rounds="${MAX_REVIEW_ROUNDS}"
        -f auto_create="${AUTO_CREATE}"
      )

      # Only pass repo_full_name if a target repo was specified
      if [[ -n "$TARGET_REPO" ]]; then
        CHAIN_ARGS+=(-f repo_full_name="${TARGET_REPO}")
      fi

      if [[ -n "$CURRENT_TRACEPARENT" ]]; then
        CHAIN_ARGS+=(-f parent_traceparent="${CURRENT_TRACEPARENT}")
      fi

      if [[ -n "${GITHUB_ISSUE_NUMBER:-}" && "${GITHUB_ISSUE_NUMBER}" != "N/A" ]]; then
        CHAIN_ARGS+=(-f github_issue_number="${GITHUB_ISSUE_NUMBER}")
      fi

      gh workflow run refine.yml "${CHAIN_ARGS[@]}" \
        2>/dev/null || echo "::warning::Failed to chain refine workflow — trigger manually"
    else
      echo "::warning::GITHUB_RUN_ID not available — refine must be triggered manually"
    fi

    pe_end "post-critique" "handle-revision" "$(jq -nc --argjson next_round "$NEXT_ROUND" --argjson revisions "$REVISION_COUNT" '{next_round:$next_round, revision_count:$revisions}')"
  fi

elif [[ "${VERDICT}" == "needs_input" ]]; then
  pe_start "post-critique" "handle-needs-input"
  echo "::notice::Critique needs human input — posting question"

  QUESTION_DIM=$(jq -r '.question.dimension // "unknown"' "${RESULT_FILE}")
  QUESTION_TEXT=$(jq -r '.question.text // ""' "${RESULT_FILE}")
  QUESTION_IMPACT=$(jq -r '.question.impact // ""' "${RESULT_FILE}")

  QUESTION_COMMENT="${AGENT_HEADER}

**Verdict: ❓ Needs Human Input** (score: ${OVERALL_SCORE}/100)

${COMMENT}

---
**Question** (${QUESTION_DIM}): ${QUESTION_TEXT}

**Why this matters**: ${QUESTION_IMPACT}

Reply with your answer, then comment \`/fs-refine\` to restart the pipeline with the new context."

  post_comment "$QUESTION_COMMENT"

  if $USE_GITHUB; then
    add_label "${REPO_FULL_NAME}" "$GITHUB_ISSUE_NUMBER" "refine-needs-input"
  fi

  pe_end "post-critique" "handle-needs-input" "$(jq -nc --arg dim "$QUESTION_DIM" --argjson score "$OVERALL_SCORE" '{dimension:$dim, score:$score}')"

else
  echo "ERROR: Unknown verdict '${VERDICT}'"
  exit 1
fi

pe_end "post-critique" "post-critique" "$(jq -nc --arg verdict "$VERDICT" --argjson score "$OVERALL_SCORE" --argjson round "$REVIEW_ROUND" '{verdict:$verdict, score:$score, round:$round}')"
pe_copy_to_output

echo "Post-critique complete."
