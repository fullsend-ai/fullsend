#!/usr/bin/env bash
# =============================================================================
# run-single-trial.sh — Runs one scenario x strategy through the dialogue loop
# =============================================================================
#
# Simulates the asynchronous GitHub issue triage cycle:
#
#   1. Triage agent reads issue + comments, decides to ASK or RESOLVE
#   2. If ASK: question appended as comment, triage agent dies
#   3. Reporter agent reads issue + comments, answers the question
#   4. Answer appended as comment, reporter agent dies
#   5. Repeat until RESOLVE or max turns
#
# The "issue" is a JSON file with { title, body, comments: [...] }.
# Agents are invoked in single-shot mode: `claude -p` or `opencode -p`.
#
# Usage:
#   run-single-trial.sh SCENARIO STRATEGY TRIAL_DIR MAX_TURNS AGENT_CLI [--dry-run]
# =============================================================================

set -euo pipefail

SCENARIO_NAME="${1:?Usage: $0 SCENARIO STRATEGY TRIAL_DIR MAX_TURNS AGENT_CLI [--dry-run]}"
STRATEGY_NAME="${2:?}"
TRIAL_DIR="${3:?}"
MAX_TURNS="${4:?}"
AGENT_CLI="${5:?}"
DRY_RUN=false
[[ "${6:-}" == "--dry-run" ]] && DRY_RUN=true

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPERIMENT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# ---------------------------------------------------------------------------
# Load inputs
# ---------------------------------------------------------------------------

SCENARIO_FILE="$EXPERIMENT_DIR/scenarios/${SCENARIO_NAME}.json"
ADAPTER_FILE="$EXPERIMENT_DIR/adapters/${STRATEGY_NAME}.md"
TRIAGE_PROMPT_FILE="$EXPERIMENT_DIR/prompts/triage-system.md"
REPORTER_PROMPT_FILE="$EXPERIMENT_DIR/prompts/reporter-system.md"

for f in "$SCENARIO_FILE" "$ADAPTER_FILE" "$TRIAGE_PROMPT_FILE" "$REPORTER_PROMPT_FILE"; do
  [[ -f "$f" ]] || { echo "Error: missing $f" >&2; exit 1; }
done

SCENARIO_JSON="$(cat "$SCENARIO_FILE")"
ADAPTER_TEXT="$(cat "$ADAPTER_FILE")"
TRIAGE_SYSTEM="$(cat "$TRIAGE_PROMPT_FILE")"
REPORTER_SYSTEM="$(cat "$REPORTER_PROMPT_FILE")"

ISSUE_TITLE="$(echo "$SCENARIO_JSON" | jq -r '.title')"
ISSUE_BODY="$(echo "$SCENARIO_JSON" | jq -r '.body')"
GROUND_TRUTH="$(echo "$SCENARIO_JSON" | jq -r '.ground_truth | tojson')"

# ---------------------------------------------------------------------------
# Initialize issue state
# ---------------------------------------------------------------------------

ISSUE_JSON="$(jq -n \
  --arg title "$ISSUE_TITLE" \
  --arg body "$ISSUE_BODY" \
  '{title: $title, body: $body, comments: []}'
)"

mkdir -p "$TRIAL_DIR"

# ---------------------------------------------------------------------------
# Agent invocation
# ---------------------------------------------------------------------------

invoke_agent() {
  local prompt="$1"

  if $DRY_RUN; then
    echo "[dry-run] Prompt (${#prompt} chars): ${prompt:0:200}..."
    echo '{"action":"resolve","reasoning":"dry-run","triage_summary":{"title":"dry-run","problem":"dry-run","root_cause_hypothesis":"dry-run","reproduction_steps":[],"environment":"dry-run","severity":"low","impact":"dry-run","recommended_fix":"dry-run","proposed_test_case":"dry-run","information_gaps":[]}}'
    return 0
  fi

  local result=""
  case "$AGENT_CLI" in
    claude)
      result="$(claude -p "$prompt" --output-format text 2>/dev/null)" || true
      ;;
    opencode)
      result="$(opencode -p "$prompt" 2>/dev/null)" || true
      ;;
    *)
      echo "Error: unsupported AGENT_CLI=$AGENT_CLI" >&2; exit 1
      ;;
  esac
  echo "$result"
}

# ---------------------------------------------------------------------------
# JSON extraction from agent output (agents sometimes wrap JSON in markdown)
# ---------------------------------------------------------------------------

extract_json() {
  local raw="$1"

  # Try raw string
  if echo "$raw" | jq . &>/dev/null; then echo "$raw"; return 0; fi

  # Try markdown json fence
  local fenced
  fenced="$(echo "$raw" | sed -n '/^```json$/,/^```$/p' | sed '1d;$d')"
  if [[ -n "$fenced" ]] && echo "$fenced" | jq . &>/dev/null; then
    echo "$fenced"; return 0
  fi

  # Try generic fence
  fenced="$(echo "$raw" | sed -n '/^```$/,/^```$/p' | sed '1d;$d')"
  if [[ -n "$fenced" ]] && echo "$fenced" | jq . &>/dev/null; then
    echo "$fenced"; return 0
  fi

  # Try first { ... } block
  local braced
  braced="$(echo "$raw" | awk '/{/{found=1} found{print} /}/{if(found) exit}')"
  if [[ -n "$braced" ]] && echo "$braced" | jq . &>/dev/null; then
    echo "$braced"; return 0
  fi

  echo "$raw"
  return 1
}

# ---------------------------------------------------------------------------
# Dialogue loop
# ---------------------------------------------------------------------------

TURN=0
echo "  Trial: $SCENARIO_NAME x $STRATEGY_NAME (max $MAX_TURNS turns)"

while [[ $TURN -lt $MAX_TURNS ]]; do
  TURN=$((TURN + 1))

  # Build triage prompt
  FORCE_RESOLVE=""
  if [[ $TURN -eq $MAX_TURNS ]]; then
    FORCE_RESOLVE="

IMPORTANT: You have reached the maximum number of dialogue turns. You MUST
produce a triage summary NOW with whatever information you have. Do NOT ask
another question. Respond with action 'resolve'."
  fi

  TRIAGE_PROMPT="$TRIAGE_SYSTEM

--- TRIAGE STRATEGY ---

$ADAPTER_TEXT

--- CURRENT ISSUE STATE ---

$(echo "$ISSUE_JSON" | jq .)

--- INSTRUCTIONS ---

Examine the issue (title, body, all comments). Apply the strategy above.
Decide: ASK a clarifying question or RESOLVE the issue.
Respond with ONLY valid JSON as described in the system prompt.$FORCE_RESOLVE"

  echo "  [Turn $TURN/$MAX_TURNS] Triage agent..."
  TRIAGE_RAW="$(invoke_agent "$TRIAGE_PROMPT")"

  TRIAGE_JSON="$(extract_json "$TRIAGE_RAW")" || {
    echo "  Warning: unparseable triage response, turn $TURN" >&2
    echo "$TRIAGE_RAW" > "$TRIAL_DIR/error-turn-${TURN}-triage.txt"
    continue
  }

  ACTION="$(echo "$TRIAGE_JSON" | jq -r '.action // "unknown"')"

  # --- RESOLVE ---
  if [[ "$ACTION" == "resolve" ]]; then
    echo "  [Turn $TURN/$MAX_TURNS] Resolved."

    ISSUE_JSON="$(echo "$ISSUE_JSON" | jq \
      --arg author "triage-agent" \
      --arg body "[RESOLVED] $(echo "$TRIAGE_JSON" | jq -c '.triage_summary')" \
      '.comments += [{author: $author, body: $body}]'
    )"

    # Save triage summary
    echo "$TRIAGE_JSON" | jq '.triage_summary' > "$TRIAL_DIR/triage-summary.json"
    echo "$TRIAGE_JSON" | jq -r '.triage_summary |
      "# Triage Summary\n\n" +
      "**Title:** " + (.title // "N/A") + "\n\n" +
      "## Problem\n" + (.problem // "N/A") + "\n\n" +
      "## Root Cause Hypothesis\n" + (.root_cause_hypothesis // "N/A") + "\n\n" +
      "## Reproduction Steps\n" + ((.reproduction_steps // []) | to_entries | map("  " + ((.key+1)|tostring) + ". " + .value) | join("\n")) + "\n\n" +
      "## Environment\n" + (.environment // "N/A") + "\n\n" +
      "## Severity: " + (.severity // "N/A") + "\n\n" +
      "## Impact\n" + (.impact // "N/A") + "\n\n" +
      "## Recommended Fix\n" + (.recommended_fix // "N/A") + "\n\n" +
      "## Proposed Test Case\n" + (.proposed_test_case // "N/A") + "\n\n" +
      "## Information Gaps\n" + ((.information_gaps // []) | map("- " + .) | join("\n"))
    ' > "$TRIAL_DIR/triage-summary.md" 2>/dev/null || {
      echo "# Triage Summary (raw)" > "$TRIAL_DIR/triage-summary.md"
      echo '```json' >> "$TRIAL_DIR/triage-summary.md"
      echo "$TRIAGE_JSON" | jq '.triage_summary' >> "$TRIAL_DIR/triage-summary.md"
      echo '```' >> "$TRIAL_DIR/triage-summary.md"
    }

    break
  fi

  # --- ASK ---
  if [[ "$ACTION" == "ask" ]]; then
    QUESTION="$(echo "$TRIAGE_JSON" | jq -r '.question')"
    echo "  [Turn $TURN/$MAX_TURNS] Asks: ${QUESTION:0:100}..."

    ISSUE_JSON="$(echo "$ISSUE_JSON" | jq \
      --arg author "triage-agent" \
      --arg body "$QUESTION" \
      '.comments += [{author: $author, body: $body}]'
    )"

    # Reporter responds
    REPORTER_PROMPT="$REPORTER_SYSTEM

--- YOUR GROUND TRUTH EXPERIENCE ---

$GROUND_TRUTH

--- CURRENT ISSUE STATE ---

$(echo "$ISSUE_JSON" | jq .)

--- INSTRUCTIONS ---

The latest comment is a question from the triage agent. Read it and answer
based on your ground truth experience. You are a user, not a developer.
Respond with ONLY valid JSON: {\"answer\": \"your response\"}"

    echo "  [Turn $TURN/$MAX_TURNS] Reporter..."
    REPORTER_RAW="$(invoke_agent "$REPORTER_PROMPT")"

    REPORTER_JSON="$(extract_json "$REPORTER_RAW")" || {
      echo "  Warning: unparseable reporter response, turn $TURN" >&2
      echo "$REPORTER_RAW" > "$TRIAL_DIR/error-turn-${TURN}-reporter.txt"
      REPORTER_JSON='{"answer": "Sorry, I am not sure about that."}'
    }

    ANSWER="$(echo "$REPORTER_JSON" | jq -r '.answer // "I am not sure."')"
    echo "  [Turn $TURN/$MAX_TURNS] Answers: ${ANSWER:0:100}..."

    ISSUE_JSON="$(echo "$ISSUE_JSON" | jq \
      --arg author "reporter" \
      --arg body "$ANSWER" \
      '.comments += [{author: $author, body: $body}]'
    )"
  else
    echo "  Warning: unexpected action '$ACTION'" >&2
    echo "$TRIAGE_RAW" > "$TRIAL_DIR/error-turn-${TURN}-bad-action.txt"
  fi
done

# ---------------------------------------------------------------------------
# Force resolution if loop exhausted without resolving
# ---------------------------------------------------------------------------

LAST_RESOLVED="$(echo "$ISSUE_JSON" | jq -r '.comments[-1].body // ""' | grep -c '^\[RESOLVED\]' || true)"
if [[ "$LAST_RESOLVED" -eq 0 ]] && ! $DRY_RUN; then
  echo "  [Forced] Max turns reached, forcing resolution..."

  FORCE_PROMPT="$TRIAGE_SYSTEM

--- TRIAGE STRATEGY ---

$ADAPTER_TEXT

--- CURRENT ISSUE STATE ---

$(echo "$ISSUE_JSON" | jq .)

--- INSTRUCTIONS ---

Maximum turns reached. You MUST produce a triage summary NOW. Do NOT ask
another question. Respond with action 'resolve'."

  FORCE_RAW="$(invoke_agent "$FORCE_PROMPT")"
  FORCE_JSON="$(extract_json "$FORCE_RAW")" || {
    FORCE_JSON='{"action":"resolve","triage_summary":{"title":"(forced - parse error)","problem":"Agent could not produce JSON","root_cause_hypothesis":"unknown","reproduction_steps":[],"environment":"unknown","severity":"medium","impact":"unknown","recommended_fix":"Manual triage required","proposed_test_case":"N/A","information_gaps":["All"]}}'
  }

  ISSUE_JSON="$(echo "$ISSUE_JSON" | jq \
    --arg author "triage-agent" \
    --arg body "[RESOLVED] $(echo "$FORCE_JSON" | jq -c '.triage_summary // {}')" \
    '.comments += [{author: $author, body: $body}]'
  )"
  echo "$FORCE_JSON" | jq '.triage_summary' > "$TRIAL_DIR/triage-summary.json" 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# Save conversation
# ---------------------------------------------------------------------------

if ! $DRY_RUN; then
  echo "$ISSUE_JSON" | jq . > "$TRIAL_DIR/conversation.json"
  COMMENT_COUNT="$(echo "$ISSUE_JSON" | jq '.comments | length')"
  echo "  Done: $TURN turns, $COMMENT_COUNT comments -> $TRIAL_DIR/"
fi
