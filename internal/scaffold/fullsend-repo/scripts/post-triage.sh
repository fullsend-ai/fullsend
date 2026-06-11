#!/usr/bin/env bash
# post-triage.sh — Parse triage agent JSON output and perform GitHub mutations.
#
# Runs on the host after sandbox cleanup. Working directory is the fullsend
# run output directory (e.g., /tmp/fullsend/agent-triage-<id>/iteration-1/).
#
# Required env vars:
#   GITHUB_ISSUE_URL  — HTML URL of the issue (e.g., https://github.com/org/repo/issues/42)
#   GH_TOKEN          — GitHub token with issues read/write scope
#
# The agent writes its decision to output/agent-result.json (relative to
# the iteration directory). This script finds the most recent iteration's output.
#
# IMPORTANT: Label mutations use the labels API directly (gh api) instead of
# gh issue edit. gh issue edit uses PATCH /issues/{number} which fires
# issues.edited, re-triggering the triage dispatch in the shim workflow.
# The labels API (POST/DELETE /issues/{number}/labels) only fires
# issues.labeled/issues.unlabeled, avoiding the re-triage loop.

set -euo pipefail

# Find the triage result JSON. The run dir contains iteration-N/ subdirectories;
# we want the last one's output.
RESULT_FILE=""
for dir in iteration-*/output; do
  if [[ -f "${dir}/agent-result.json" ]]; then
    RESULT_FILE="${dir}/agent-result.json"
  fi
done

if [[ -z "${RESULT_FILE}" ]]; then
  echo "ERROR: agent-result.json not found in any iteration output directory"
  exit 1
fi

echo "Reading triage result from: ${RESULT_FILE}"

# Validate JSON is parseable.
if ! jq empty "${RESULT_FILE}" 2>/dev/null; then
  echo "ERROR: ${RESULT_FILE} is not valid JSON"
  exit 1
fi

ACTION=$(jq -r '.action' "${RESULT_FILE}")
COMMENT=$(jq -r '.comment // empty' "${RESULT_FILE}")

# Validate and extract repo and issue number from the HTML URL.
# GITHUB_ISSUE_URL is e.g. https://github.com/org/repo/issues/42
if [[ ! "${GITHUB_ISSUE_URL}" =~ ^https://github\.com/[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+/issues/[0-9]+$ ]]; then
  echo "ERROR: GITHUB_ISSUE_URL does not match expected pattern: ${GITHUB_ISSUE_URL}"
  exit 1
fi
REPO=$(echo "${GITHUB_ISSUE_URL}" | sed 's|https://github.com/||; s|/issues/.*||')
ISSUE_NUMBER=$(basename "${GITHUB_ISSUE_URL}")

echo "Action: ${ACTION}"
echo "Repo: ${REPO}"
echo "Issue: #${ISSUE_NUMBER}"

# add_label uses the labels API to avoid firing issues.edited.
add_label() {
  if ! gh api "repos/${REPO}/issues/${ISSUE_NUMBER}/labels" -f "labels[]=$1" --silent; then
    echo "ERROR: failed to add label '$1' to issue #${ISSUE_NUMBER}" >&2
    exit 1
  fi
}

# remove_label silently removes a label (no error if absent).
remove_label() {
  local encoded
  encoded=$(printf '%s' "$1" | jq -sRr @uri)
  gh api "repos/${REPO}/issues/${ISSUE_NUMBER}/labels/${encoded}" -X DELETE --silent 2>/dev/null || true
}

# Control labels managed by the triage pipeline. The post script refuses to
# add or remove these via label_actions. This list covers labels that the
# pipeline itself applies (pre-triage.sh resets the first five; the action
# handlers apply blocked/triaged/feature).
CONTROL_LABELS=("needs-info" "ready-to-code" "duplicate" "feature" "blocked" "triaged" "question")

is_control_label() {
  local label="$1"
  for cl in "${CONTROL_LABELS[@]}"; do
    if [[ "${cl}" == "${label}" ]]; then
      return 0
    fi
  done
  return 1
}

# --- Action-specific validation and control labels ---

# Deferred label: when set, applied after label_actions so it fires last.
# This prevents the ready-to-code webhook event from being superseded by
# subsequent label events in the dispatch concurrency group (see #1752).
DEFERRED_LABEL=""

case "${ACTION}" in
  insufficient)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'insufficient' but no comment provided"
      exit 1
    fi
    remove_label "blocked"
    add_label "needs-info"
    ;;

  duplicate)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'duplicate' but no comment provided"
      exit 1
    fi
    DUPLICATE_OF=$(jq -r '.duplicate_of' "${RESULT_FILE}")
    if [[ "${DUPLICATE_OF}" -eq "${ISSUE_NUMBER}" ]]; then
      echo "ERROR: issue cannot be a duplicate of itself (#${ISSUE_NUMBER})"
      exit 1
    fi
    remove_label "blocked"
    add_label "duplicate"
    ;;

  blocked)
    # NOTE: There is no automatic mechanism to remove the "blocked" label when
    # the blocking issue is resolved. Currently, editing the issue re-triggers
    # triage, and the agent checks whether existing blockers are still open
    # (Step 2c in triage.md). A scheduled workflow to check blocked issues
    # periodically would be a more complete solution. (See review notes.)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'blocked' but no comment provided"
      exit 1
    fi
    BLOCKED_BY=$(jq -r '.blocked_by // empty' "${RESULT_FILE}")
    if [[ -z "${BLOCKED_BY}" ]]; then
      echo "ERROR: action is 'blocked' but no blocked_by URL provided"
      exit 1
    fi
    echo "Blocked by: ${BLOCKED_BY}"
    remove_label "ready-to-code"
    remove_label "needs-info"
    add_label "blocked"
    ;;

  sufficient)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'sufficient' but no comment provided"
      exit 1
    fi

    # Guard: reject sufficient results that contain information_gaps.
    # If the agent identified open questions, it should have used "insufficient".
    GAP_COUNT=$(jq '.triage_summary.information_gaps // [] | length' "${RESULT_FILE}")
    if [[ "${GAP_COUNT}" -gt 0 ]]; then
      echo "ERROR: action is 'sufficient' but triage_summary contains ${GAP_COUNT} information_gaps — open questions must block triage"
      exit 1
    fi

    remove_label "blocked"
    remove_label "needs-info"

    # Low-risk categories (bug, documentation, performance) auto-promote to
    # ready-to-code, which triggers the code agent. Feature work and anything
    # else receives the triaged label and waits for human prioritization
    # (per #561, only feature issues should require human review before coding).
    CATEGORY=$(jq -r '.triage_summary.category // "unknown"' "${RESULT_FILE}")
    echo "Category: ${CATEGORY}"
    case "${CATEGORY}" in
      bug|documentation|performance)
        echo "Deferring ready-to-code label (${CATEGORY}) until after label_actions..."
        DEFERRED_LABEL="ready-to-code"
        ;;
      feature)
        echo "Applying feature + triaged labels..."
        add_label "feature"
        add_label "triaged"
        ;;
      *)
        echo "Applying triaged label (${CATEGORY})..."
        add_label "triaged"
        ;;
    esac
    ;;

  question)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'question' but no comment provided"
      exit 1
    fi
    remove_label "blocked"
    remove_label "needs-info"
    add_label "question"
    ;;

  *)
    echo "ERROR: unknown action '${ACTION}' — this may be a newer action that post-triage.sh does not handle yet"
    exit 1
    ;;
esac

# --- Process label_actions (applies to all actions) ---

HAS_LABEL_ACTIONS=$(jq 'has("label_actions")' "${RESULT_FILE}")
if [[ "${HAS_LABEL_ACTIONS}" == "true" ]]; then
  LABEL_REASON=$(jq -r '.label_actions.reason' "${RESULT_FILE}")
  LABEL_COUNT=$(jq '.label_actions.actions | length' "${RESULT_FILE}")

  echo "Processing ${LABEL_COUNT} label action(s)..."

  # Fetch existing repo labels once so we can reject labels that don't exist.
  # This prevents the agent from accidentally creating labels the org removed.
  EXISTING_LABELS=$(gh api "repos/${REPO}/labels" --paginate --jq '.[].name' 2>/dev/null || true)

  label_exists() {
    local label="$1"
    # Use grep with fixed-string and line-match to avoid regex issues with
    # label names that contain special characters (e.g., "c++").
    echo "${EXISTING_LABELS}" | grep -qFx "${label}"
  }

  LABELS_APPLIED=0
  for i in $(seq 0 $((LABEL_COUNT - 1))); do
    LA_ACTION=$(jq -r ".label_actions.actions[${i}].action" "${RESULT_FILE}")
    LA_LABEL=$(jq -r ".label_actions.actions[${i}].label" "${RESULT_FILE}")

    # Validate label name to prevent path injection from untrusted agent output.
    if [[ ! "${LA_LABEL}" =~ ^[a-zA-Z0-9._/:\ +\-]+$ ]]; then
      echo "::warning::Refused label '${LA_LABEL}' -- contains invalid characters"
      continue
    fi

    if is_control_label "${LA_LABEL}"; then
      echo "::warning::Refused to ${LA_ACTION} control label '${LA_LABEL}' -- control labels are managed by the triage pipeline"
      continue
    fi

    case "${LA_ACTION}" in
      add)
        if ! label_exists "${LA_LABEL}"; then
          echo "::warning::Skipping label '${LA_LABEL}' -- does not exist in repo (will not auto-create)"
          continue
        fi
        echo "Adding label '${LA_LABEL}'..."
        add_label "${LA_LABEL}"
        LABELS_APPLIED=$((LABELS_APPLIED + 1))
        ;;
      remove)
        echo "Removing label '${LA_LABEL}'..."
        remove_label "${LA_LABEL}"
        LABELS_APPLIED=$((LABELS_APPLIED + 1))
        ;;
      *)
        echo "::warning::Unknown label action '${LA_ACTION}' for label '${LA_LABEL}'"
        ;;
    esac
  done

  # Append the label reason to the comment only if at least one label was applied.
  if [[ "${LABELS_APPLIED}" -gt 0 ]]; then
    COMMENT="${COMMENT}

---
**Labels:** ${LABEL_REASON}"
  fi
fi

# --- Apply deferred label (must be last label mutation) ---

if [[ -n "${DEFERRED_LABEL}" ]]; then
  echo "Applying deferred label '${DEFERRED_LABEL}'..."
  add_label "${DEFERRED_LABEL}"
fi

# --- Post comment ---

echo "Posting comment..."
if [[ "${ACTION}" == "sufficient" ]]; then
  # Summaries use sticky comments — there's one logical summary per issue and
  # updating it in-place avoids flooding. See #602.
  printf '%s' "${COMMENT}" | fullsend post-comment --repo "${REPO}" --number "${ISSUE_NUMBER}" --marker "<!-- fullsend:triage-agent -->" --token "${GH_TOKEN}" --result -
else
  # Interactive comments (needs-info questions, blocked notices, duplicates)
  # post as new comments so the conversation reads chronologically.
  printf '%s' "${COMMENT}" | gh issue comment "${ISSUE_NUMBER}" --repo "${REPO}" --body-file -
fi

# --- Post-action: close duplicate issues ---

if [[ "${ACTION}" == "duplicate" ]]; then
  gh issue close "${ISSUE_NUMBER}" --repo "${REPO}" --reason "duplicate"
fi

echo "Post-triage complete."
