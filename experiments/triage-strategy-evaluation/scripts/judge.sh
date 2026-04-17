#!/usr/bin/env bash
# =============================================================================
# judge.sh — Invokes the judge agent on a completed triage conversation
# =============================================================================
#
# Usage:
#   judge.sh SCENARIO_NAME TRIAL_DIR AGENT_CLI [JUDGE_MODEL]
# =============================================================================

set -euo pipefail

SCENARIO_NAME="${1:?Usage: $0 SCENARIO_NAME TRIAL_DIR AGENT_CLI [JUDGE_MODEL]}"
TRIAL_DIR="${2:?}"
AGENT_CLI="${3:?}"
JUDGE_MODEL="${4:-claude-sonnet-4-6}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPERIMENT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

SCENARIO_FILE="$EXPERIMENT_DIR/scenarios/${SCENARIO_NAME}.json"
JUDGE_SYSTEM="$(cat "$EXPERIMENT_DIR/prompts/judge-system.md")"
CONVERSATION="$(cat "$TRIAL_DIR/conversation.json")"
SCENARIO_JSON="$(cat "$SCENARIO_FILE")"

GROUND_TRUTH="$(echo "$SCENARIO_JSON" | jq '.ground_truth')"
EXPECTED_EXTRACTS="$(echo "$SCENARIO_JSON" | jq '.expected_triage_extracts')"
ACCEPTABLE_PATHS="$(echo "$SCENARIO_JSON" | jq '.ground_truth.acceptable_paths // []')"

# Get triage summary if available
TRIAGE_SUMMARY="{}"
if [[ -f "$TRIAL_DIR/triage-summary.json" ]]; then
  TRIAGE_SUMMARY="$(cat "$TRIAL_DIR/triage-summary.json")"
fi

JUDGE_PROMPT="$JUDGE_SYSTEM

--- ORIGINAL ISSUE ---

Title: $(echo "$CONVERSATION" | jq -r '.title')
Body: $(echo "$CONVERSATION" | jq -r '.body')

--- GROUND TRUTH ---

$GROUND_TRUTH

--- ACCEPTABLE DIAGNOSTIC PATHS ---

$ACCEPTABLE_PATHS

--- EXPECTED INFORMATION TO EXTRACT ---

$EXPECTED_EXTRACTS

--- FULL CONVERSATION ---

$(echo "$CONVERSATION" | jq '.')

--- TRIAGE SUMMARY ---

$TRIAGE_SUMMARY

--- INSTRUCTIONS ---

Evaluate the triage quality per the rubric. When scoring accuracy, check
against both the canonical root cause AND the acceptable diagnostic paths.
Respond with ONLY valid JSON."

# Invoke judge — use specified model if claude, otherwise fall back to agent CLI
JUDGE_RAW=""
case "$AGENT_CLI" in
  claude)
    JUDGE_RAW="$(claude -p "$JUDGE_PROMPT" --output-format text --model "$JUDGE_MODEL" 2>/dev/null)" || true
    ;;
  opencode)
    JUDGE_RAW="$(opencode -p "$JUDGE_PROMPT" 2>/dev/null)" || true
    ;;
esac

# Extract JSON (same logic as run-single-trial.sh)
extract_json() {
  local raw="$1"
  if echo "$raw" | jq . &>/dev/null; then echo "$raw"; return 0; fi

  local fenced
  fenced="$(echo "$raw" | sed -n '/^```json$/,/^```$/p' | sed '1d;$d')"
  if [[ -n "$fenced" ]] && echo "$fenced" | jq . &>/dev/null; then
    echo "$fenced"; return 0
  fi

  local braced
  braced="$(echo "$raw" | awk '/{/{found=1} found{print} /}/{if(found) exit}')"
  if [[ -n "$braced" ]] && echo "$braced" | jq . &>/dev/null; then
    echo "$braced"; return 0
  fi

  echo "$raw"; return 1
}

JUDGE_JSON="$(extract_json "$JUDGE_RAW")" || {
  echo "    Warning: could not parse judge response" >&2
  echo "$JUDGE_RAW" > "$TRIAL_DIR/judge-error.txt"
  JUDGE_JSON='{"scores":{"completeness":{"score":0,"rationale":"parse error"},"accuracy":{"score":0,"rationale":"parse error"},"thoroughness":{"score":0,"rationale":"parse error"},"economy":{"score":0,"rationale":"parse error"},"question_quality":{"score":0,"rationale":"parse error"},"actionability":{"score":0,"rationale":"parse error"}},"weighted_total":0,"turn_count":0,"notable_strengths":[],"notable_weaknesses":["Judge could not parse response"],"most_insightful_question":null,"missed_opportunities":[]}'
}

echo "$JUDGE_JSON" | jq '.' > "$TRIAL_DIR/judge-assessment.json"
SCORE="$(echo "$JUDGE_JSON" | jq -r '.weighted_total // 0')"
echo "    Score: $SCORE/5.00"
