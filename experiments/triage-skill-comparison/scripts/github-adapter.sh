#!/usr/bin/env bash
# =============================================================================
# github-adapter.sh — Runs the triage experiment against live GitHub issues
# =============================================================================
#
# This script replaces the file-based simulation with actual GitHub API calls.
# It uses the `gh` CLI to create issues, post comments, and read responses.
#
# The core dialogue loop is the same as run-single-trial.sh, but instead of
# reading/writing JSON files, it reads/writes GitHub issue comments.
#
# Usage:
#   github-adapter.sh REPO SCENARIO STRATEGY MAX_TURNS AGENT_CLI [--auto-reply]
#
# Arguments:
#   REPO          GitHub repo (owner/repo format)
#   SCENARIO      Scenario name
#   STRATEGY      Strategy name
#   MAX_TURNS     Max dialogue turns
#   AGENT_CLI     "claude" or "opencode"
#   --auto-reply  Use the reporter agent to auto-respond (instead of waiting
#                 for a real human). This makes the experiment self-contained.
#
# Prerequisites:
#   - `gh` CLI installed and authenticated
#   - Agent CLI installed and authenticated
#   - Repository must allow issue creation
#
# Environment variables:
#   POLL_INTERVAL   Seconds between polls for new comments (default: 30)
#   POLL_TIMEOUT    Max seconds to wait for a response (default: 3600)
#   LABEL_TRIAGE    Label to apply for triage (default: "triage-experiment")
# =============================================================================

set -euo pipefail

REPO="${1:?Usage: $0 REPO SCENARIO STRATEGY MAX_TURNS AGENT_CLI [--auto-reply]}"
SCENARIO_NAME="${2:?}"
STRATEGY_NAME="${3:?}"
MAX_TURNS="${4:?}"
AGENT_CLI="${5:?}"
AUTO_REPLY=false
[[ "${6:-}" == "--auto-reply" ]] && AUTO_REPLY=true

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPERIMENT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

POLL_INTERVAL="${POLL_INTERVAL:-30}"
POLL_TIMEOUT="${POLL_TIMEOUT:-3600}"
LABEL_TRIAGE="${LABEL_TRIAGE:-triage-experiment}"

# Load inputs
SCENARIO_FILE="$EXPERIMENT_DIR/scenarios/${SCENARIO_NAME}.json"
ADAPTER_FILE="$EXPERIMENT_DIR/adapters/${STRATEGY_NAME}.md"
TRIAGE_SYSTEM="$(cat "$EXPERIMENT_DIR/prompts/triage-system.md")"
REPORTER_SYSTEM="$(cat "$EXPERIMENT_DIR/prompts/reporter-system.md")"

SCENARIO_JSON="$(cat "$SCENARIO_FILE")"
ADAPTER_TEXT="$(cat "$ADAPTER_FILE")"
ISSUE_TITLE="$(echo "$SCENARIO_JSON" | jq -r '.title')"
ISSUE_BODY="$(echo "$SCENARIO_JSON" | jq -r '.body')"
GROUND_TRUTH="$(echo "$SCENARIO_JSON" | jq -r '.ground_truth | tojson')"

# ---------------------------------------------------------------------------
# Phase 1: Create the GitHub issue
# ---------------------------------------------------------------------------

echo "Creating issue on $REPO..."
ISSUE_URL="$(gh issue create \
  --repo "$REPO" \
  --title "[Triage Experiment] $ISSUE_TITLE" \
  --body "$ISSUE_BODY

---
_This issue was created by the triage-skill-comparison experiment._
_Strategy: $STRATEGY_NAME | Scenario: $SCENARIO_NAME_" \
  --label "$LABEL_TRIAGE" \
  2>/dev/null)"

ISSUE_NUMBER="$(echo "$ISSUE_URL" | grep -oP '\d+$')"
echo "Created issue #$ISSUE_NUMBER: $ISSUE_URL"

# ---------------------------------------------------------------------------
# Helper: read current issue state from GitHub
# ---------------------------------------------------------------------------

read_issue_state() {
  local issue_data
  issue_data="$(gh api "repos/$REPO/issues/$ISSUE_NUMBER" 2>/dev/null)"

  local comments_data
  comments_data="$(gh api "repos/$REPO/issues/$ISSUE_NUMBER/comments" --paginate 2>/dev/null)"

  jq -n \
    --argjson issue "$issue_data" \
    --argjson comments "$comments_data" \
    '{
      title: $issue.title,
      body: $issue.body,
      comments: [$comments[] | {author: .user.login, body: .body}]
    }'
}

# ---------------------------------------------------------------------------
# Helper: post a comment
# ---------------------------------------------------------------------------

post_comment() {
  local body="$1"
  gh api "repos/$REPO/issues/$ISSUE_NUMBER/comments" \
    -f body="$body" \
    --silent 2>/dev/null
}

# ---------------------------------------------------------------------------
# Helper: wait for a new comment from someone other than the agent
# ---------------------------------------------------------------------------

wait_for_reply() {
  local known_count="$1"
  local elapsed=0

  while [[ $elapsed -lt $POLL_TIMEOUT ]]; do
    local current_count
    current_count="$(gh api "repos/$REPO/issues/$ISSUE_NUMBER/comments" --jq 'length' 2>/dev/null)"

    if [[ "$current_count" -gt "$known_count" ]]; then
      echo "New comment detected."
      return 0
    fi

    sleep "$POLL_INTERVAL"
    elapsed=$((elapsed + POLL_INTERVAL))
    echo -n "."
  done

  echo "Timeout waiting for reply."
  return 1
}

# ---------------------------------------------------------------------------
# Agent invocation (same as run-single-trial.sh)
# ---------------------------------------------------------------------------

invoke_agent() {
  local prompt="$1"
  case "$AGENT_CLI" in
    claude)  claude -p "$prompt" --output-format text 2>/dev/null || true ;;
    opencode) opencode -p "$prompt" 2>/dev/null || true ;;
  esac
}

extract_json() {
  local raw="$1"
  if echo "$raw" | jq . &>/dev/null; then echo "$raw"; return 0; fi
  local fenced
  fenced="$(echo "$raw" | sed -n '/^```json$/,/^```$/p' | sed '1d;$d')"
  if [[ -n "$fenced" ]] && echo "$fenced" | jq . &>/dev/null; then echo "$fenced"; return 0; fi
  local braced
  braced="$(echo "$raw" | awk '/{/{found=1} found{print} /}/{if(found) exit}')"
  if [[ -n "$braced" ]] && echo "$braced" | jq . &>/dev/null; then echo "$braced"; return 0; fi
  echo "$raw"; return 1
}

# ---------------------------------------------------------------------------
# Dialogue loop
# ---------------------------------------------------------------------------

TURN=0
echo ""
echo "Starting triage dialogue (max $MAX_TURNS turns)..."

while [[ $TURN -lt $MAX_TURNS ]]; do
  TURN=$((TURN + 1))

  # Read current issue state from GitHub
  ISSUE_STATE="$(read_issue_state)"
  COMMENT_COUNT="$(echo "$ISSUE_STATE" | jq '.comments | length')"

  # Build triage prompt
  FORCE=""
  [[ $TURN -eq $MAX_TURNS ]] && FORCE="
IMPORTANT: Maximum turns reached. You MUST resolve NOW. Do NOT ask another question."

  TRIAGE_PROMPT="$TRIAGE_SYSTEM

--- TRIAGE STRATEGY ---

$ADAPTER_TEXT

--- CURRENT ISSUE STATE ---

$(echo "$ISSUE_STATE" | jq .)

--- INSTRUCTIONS ---

Examine the issue and all comments. Apply the strategy. ASK or RESOLVE.$FORCE"

  echo "[Turn $TURN/$MAX_TURNS] Triage agent processing..."
  TRIAGE_RAW="$(invoke_agent "$TRIAGE_PROMPT")"
  TRIAGE_JSON="$(extract_json "$TRIAGE_RAW")" || {
    echo "  Warning: unparseable triage response" >&2
    continue
  }

  ACTION="$(echo "$TRIAGE_JSON" | jq -r '.action // "unknown"')"

  if [[ "$ACTION" == "resolve" ]]; then
    echo "[Turn $TURN] Resolved!"
    SUMMARY="$(echo "$TRIAGE_JSON" | jq -r '.triage_summary | to_entries | map("**" + .key + ":** " + (.value | tostring)) | join("\n\n")')"
    post_comment "## Triage Summary

$SUMMARY

---
_Resolved by triage agent using strategy: $STRATEGY_NAME_"
    break
  fi

  if [[ "$ACTION" == "ask" ]]; then
    QUESTION="$(echo "$TRIAGE_JSON" | jq -r '.question')"
    echo "[Turn $TURN] Asking: ${QUESTION:0:100}..."
    post_comment "$QUESTION"

    if $AUTO_REPLY; then
      # Use reporter agent to auto-respond
      ISSUE_STATE="$(read_issue_state)"
      REPORTER_PROMPT="$REPORTER_SYSTEM

--- YOUR GROUND TRUTH EXPERIENCE ---

$GROUND_TRUTH

--- CURRENT ISSUE STATE ---

$(echo "$ISSUE_STATE" | jq .)

--- INSTRUCTIONS ---

Answer the latest question. Respond with ONLY JSON: {\"answer\": \"...\"}"

      echo "[Turn $TURN] Reporter agent responding..."
      REPORTER_RAW="$(invoke_agent "$REPORTER_PROMPT")"
      REPORTER_JSON="$(extract_json "$REPORTER_RAW")" || {
        REPORTER_JSON='{"answer": "I am not sure about that."}'
      }
      ANSWER="$(echo "$REPORTER_JSON" | jq -r '.answer')"
      post_comment "$ANSWER"
    else
      # Wait for a real human to respond
      echo "Waiting for human response on issue #$ISSUE_NUMBER..."
      wait_for_reply "$((COMMENT_COUNT + 1))" || {
        echo "Timeout. Forcing resolution on next turn."
      }
    fi
  fi
done

echo ""
echo "Triage dialogue complete for issue #$ISSUE_NUMBER."
echo "Issue URL: $ISSUE_URL"
