#!/usr/bin/env bash
# post-triage-jira.sh — Parse triage agent JSON output and apply Jira mutations.
#
# Runs on the host after sandbox cleanup. Working directory is the fullsend
# run output directory (e.g., /tmp/fullsend/agent-triage-<id>/iteration-1/).
#
# Required env vars:
#   ISSUE_KEY        — Jira issue key (e.g., PROJ-123)
#   JIRA_HOST        — Jira hostname (e.g., myorg.atlassian.net)
#   JIRA_EMAIL       — Jira user email
#   JIRA_API_TOKEN   — Jira API token
#   GH_TOKEN         — GitHub token (unused here but available for future use)
#
# The agent writes its decision to output/agent-result.json (relative to
# the iteration directory). This script finds the most recent iteration's output.
#
# Label mutations use Jira's issue update API with the "update" payload format
# ({"update":{"labels":[{"add":"..."}]}}) rather than fetching and replacing the
# full labels array. This is the safest approach: it avoids race conditions and
# does not require reading the current label state.

set -euo pipefail

# --- Find the triage result JSON ---

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

# Validate required env vars.
if [[ -z "${ISSUE_KEY:-}" ]]; then
  echo "ERROR: ISSUE_KEY is not set"
  exit 1
fi
if [[ -z "${JIRA_HOST:-}" ]]; then
  echo "ERROR: JIRA_HOST is not set"
  exit 1
fi
if [[ -z "${JIRA_EMAIL:-}" ]]; then
  echo "ERROR: JIRA_EMAIL is not set"
  exit 1
fi
if [[ -z "${JIRA_API_TOKEN:-}" ]]; then
  echo "ERROR: JIRA_API_TOKEN is not set"
  exit 1
fi

JIRA_BASE="https://${JIRA_HOST}/rest/api/3"
# base64 -w0 is Linux-specific; macOS base64 wraps at 76 chars by default.
# Use -w0 when available, otherwise strip newlines with tr.
AUTH=$(printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 -w0 2>/dev/null \
  || printf '%s:%s' "$JIRA_EMAIL" "$JIRA_API_TOKEN" | base64 | tr -d '\n')

echo "Action: ${ACTION}"
echo "Issue: ${ISSUE_KEY}"

# --- Jira API helpers ---

jira_put() {
  curl -sSf -X PUT \
    -H "Authorization: Basic $AUTH" \
    -H "Content-Type: application/json" \
    -d "$1" \
    "${JIRA_BASE}/issue/${ISSUE_KEY}"
}

jira_post() {
  local path="$1"
  local body="$2"
  curl -sSf -X POST \
    -H "Authorization: Basic $AUTH" \
    -H "Content-Type: application/json" \
    -H "Accept: application/json" \
    -d "$body" \
    "${JIRA_BASE}/${path}"
}

jira_get() {
  curl -sSf \
    -H "Authorization: Basic $AUTH" \
    -H "Accept: application/json" \
    "${JIRA_BASE}/$1"
}

# add_label adds a Jira label to the issue using the update API.
add_label() {
  local label="$1"
  if ! jira_put "{\"update\":{\"labels\":[{\"add\":\"${label}\"}]}}"; then
    echo "ERROR: failed to add label '${label}' to ${ISSUE_KEY}" >&2
    exit 1
  fi
}

# remove_label silently removes a Jira label (no error if absent).
remove_label() {
  local label="$1"
  jira_put "{\"update\":{\"labels\":[{\"remove\":\"${label}\"}]}}" 2>/dev/null || true
}

# Control labels managed by the Jira triage pipeline. The post script refuses
# to add or remove these via label_actions; they are set exclusively by the
# action handlers below.
CONTROL_LABELS=(
  "fullsend:needs-info"
  "fullsend:ready-to-code"
  "fullsend:duplicate"
  "fullsend:blocked"
  "fullsend:triaged"
  "fullsend:feature"
  "fullsend:question"
)

is_control_label() {
  local label="$1"
  # Any label in the fullsend: namespace is a control label.
  if [[ "${label}" == fullsend:* ]]; then
    return 0
  fi
  return 1
}

# label_exists: Jira does not restrict label names to a predefined list, so any
# syntactically valid label name is accepted. We validate format only.
label_exists() {
  # Always returns true for Jira — labels are free-form.
  return 0
}

# --- Deferred label: applied after label_actions so it fires last ---
# This prevents the fullsend:ready-to-code label event from being superseded
# by subsequent label events (mirrors the GitHub post-triage.sh behaviour).
DEFERRED_LABEL=""

# --- Action-specific validation and control labels ---

case "${ACTION}" in
  insufficient)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'insufficient' but no comment provided"
      exit 1
    fi
    remove_label "fullsend:blocked"
    add_label "fullsend:needs-info"
    ;;

  duplicate)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'duplicate' but no comment provided"
      exit 1
    fi
    DUPLICATE_OF=$(jq -r '.duplicate_of // empty' "${RESULT_FILE}")
    if [[ -z "${DUPLICATE_OF}" ]]; then
      echo "ERROR: action is 'duplicate' but no duplicate_of provided"
      exit 1
    fi
    if [[ "${DUPLICATE_OF}" == "${ISSUE_KEY}" ]]; then
      echo "ERROR: issue cannot be a duplicate of itself (${ISSUE_KEY})"
      exit 1
    fi
    remove_label "fullsend:blocked"
    add_label "fullsend:duplicate"
    ;;

  blocked)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'blocked' but no comment provided"
      exit 1
    fi
    BLOCKED_BY=$(jq -r '.blocked_by // empty' "${RESULT_FILE}")
    if [[ -z "${BLOCKED_BY}" ]]; then
      echo "ERROR: action is 'blocked' but no blocked_by URL provided"
      exit 1
    fi
    # Validate that blocked_by looks like a Jira issue URL or key.
    if [[ ! "${BLOCKED_BY}" =~ ^https?:// ]] && [[ ! "${BLOCKED_BY}" =~ ^[A-Z][A-Z0-9_]+-[0-9]+$ ]]; then
      echo "ERROR: blocked_by must be a URL or a Jira issue key (e.g., PROJ-123): ${BLOCKED_BY}"
      exit 1
    fi
    echo "Blocked by: ${BLOCKED_BY}"
    remove_label "fullsend:ready-to-code"
    remove_label "fullsend:needs-info"
    add_label "fullsend:blocked"
    ;;

  sufficient)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'sufficient' but no comment provided"
      exit 1
    fi

    # Guard: reject sufficient results that contain information_gaps.
    GAP_COUNT=$(jq '.triage_summary.information_gaps // [] | length' "${RESULT_FILE}")
    if [[ "${GAP_COUNT}" -gt 0 ]]; then
      echo "ERROR: action is 'sufficient' but triage_summary contains ${GAP_COUNT} information_gaps — open questions must block triage"
      exit 1
    fi

    remove_label "fullsend:blocked"
    remove_label "fullsend:needs-info"

    CATEGORY=$(jq -r '.triage_summary.category // "unknown"' "${RESULT_FILE}")
    echo "Category: ${CATEGORY}"
    case "${CATEGORY}" in
      bug|documentation|performance)
        echo "Deferring fullsend:ready-to-code label (${CATEGORY}) until after label_actions..."
        DEFERRED_LABEL="fullsend:ready-to-code"
        ;;
      feature)
        echo "Applying fullsend:feature + fullsend:triaged labels..."
        add_label "fullsend:feature"
        add_label "fullsend:triaged"
        ;;
      *)
        echo "Applying fullsend:triaged label (${CATEGORY})..."
        add_label "fullsend:triaged"
        ;;
    esac
    ;;

  question)
    if [[ -z "${COMMENT}" ]]; then
      echo "ERROR: action is 'question' but no comment provided"
      exit 1
    fi
    remove_label "fullsend:blocked"
    remove_label "fullsend:needs-info"
    add_label "fullsend:question"
    ;;

  *)
    echo "ERROR: unknown action '${ACTION}' — this may be a newer action that post-jira-triage.sh does not handle yet"
    exit 1
    ;;
esac

# --- Process label_actions (applies to all actions) ---

HAS_LABEL_ACTIONS=$(jq 'has("label_actions")' "${RESULT_FILE}")
if [[ "${HAS_LABEL_ACTIONS}" == "true" ]]; then
  LABEL_REASON=$(jq -r '.label_actions.reason' "${RESULT_FILE}")
  LABEL_COUNT=$(jq '.label_actions.actions | length' "${RESULT_FILE}")

  echo "Processing ${LABEL_COUNT} label action(s)..."

  LABELS_APPLIED=0
  for i in $(seq 0 $((LABEL_COUNT - 1))); do
    LA_ACTION=$(jq -r ".label_actions.actions[${i}].action" "${RESULT_FILE}")
    LA_LABEL=$(jq -r ".label_actions.actions[${i}].label" "${RESULT_FILE}")

    # Validate label name (allow fullsend: namespace colon).
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

# Convert markdown to ADF.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ADF_BODY=$(printf '%s' "${COMMENT}" | python3 "${SCRIPT_DIR}/markdown-to-adf.py")

COMMENT_MARKER="<!-- fullsend:jira-triage-agent -->"

# Embed the HTML marker as a plain text node inside the ADF. We append it as a
# paragraph so it does not affect the visible text but can be found via the
# comments API when searching for an existing sticky comment.
ADF_WITH_MARKER=$(echo "${ADF_BODY}" | jq \
  --arg marker "${COMMENT_MARKER}" \
  '.body.content += [{"type":"paragraph","content":[{"type":"text","text":$marker}]}] | .')

if [[ "${ACTION}" == "sufficient" ]]; then
  # Summaries use sticky comments — find existing and update in-place.
  EXISTING_COMMENTS=$(jira_get "issue/${ISSUE_KEY}/comment?maxResults=100" 2>/dev/null || echo '{"comments":[]}')
  EXISTING_ID=$(echo "$EXISTING_COMMENTS" | jq -r \
    --arg marker "${COMMENT_MARKER}" \
    '[.comments[] | select(.body | if type == "object" then (.content // [] | map(.content // [] | map(.text // "") | join("")) | join("")) else . end | contains($marker))] | first | .id // empty')

  if [[ -n "${EXISTING_ID}" ]]; then
    echo "Updating existing triage summary comment (id=${EXISTING_ID})..."
    curl -sSf -X PUT \
      -H "Authorization: Basic $AUTH" \
      -H "Content-Type: application/json" \
      -H "Accept: application/json" \
      -d "${ADF_WITH_MARKER}" \
      "${JIRA_BASE}/issue/${ISSUE_KEY}/comment/${EXISTING_ID}" > /dev/null
  else
    echo "Posting new triage summary comment..."
    jira_post "issue/${ISSUE_KEY}/comment" "${ADF_WITH_MARKER}" > /dev/null
  fi
else
  # Interactive comments post as new comments so the conversation reads chronologically.
  jira_post "issue/${ISSUE_KEY}/comment" "${ADF_WITH_MARKER}" > /dev/null
fi

# --- Post-action: handle duplicate issues ---

if [[ "${ACTION}" == "duplicate" ]]; then
  echo "Creating Duplicates issue link to ${DUPLICATE_OF}..."
  LINK_BODY=$(jq -n \
    --arg in_key "${ISSUE_KEY}" \
    --arg out_key "${DUPLICATE_OF}" \
    '{"type":{"name":"Duplicates"},"inwardIssue":{"key":$in_key},"outwardIssue":{"key":$out_key}}')
  jira_post "issueLink" "${LINK_BODY}" > /dev/null || true

  # Transition the issue to Done if a Done transition exists.
  echo "Attempting to close ${ISSUE_KEY} as duplicate..."
  TRANSITIONS=$(jira_get "issue/${ISSUE_KEY}/transitions" 2>/dev/null || echo '{"transitions":[]}')
  DONE_TRANSITION_ID=$(echo "$TRANSITIONS" | jq -r \
    '[.transitions[] | select(.name == "Done" or .name == "Closed" or .name == "Resolved" or .name == "Close Issue" or .name == "Resolve Issue")] | first | .id // empty')

  if [[ -n "${DONE_TRANSITION_ID}" ]]; then
    echo "Transitioning ${ISSUE_KEY} to Done (transition id=${DONE_TRANSITION_ID})..."
    jira_post "issue/${ISSUE_KEY}/transitions" \
      "{\"transition\":{\"id\":\"${DONE_TRANSITION_ID}\"}}" > /dev/null || true
  else
    echo "No Done/Closed transition found for ${ISSUE_KEY} — skipping close"
  fi
fi

echo "Post-jira-triage complete."
