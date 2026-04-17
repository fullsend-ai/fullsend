#!/usr/bin/env bash
# =============================================================================
# analyze-scenario.sh — Cross-strategy analysis for a single scenario
# =============================================================================
#
# Reads all judge assessments for one scenario and asks an LLM to identify
# common patterns and standout items across strategies.
#
# Usage:
#   analyze-scenario.sh SCENARIO_NAME RESULTS_DIR AGENT_CLI
# =============================================================================

set -euo pipefail

SCENARIO_NAME="${1:?Usage: $0 SCENARIO_NAME RESULTS_DIR AGENT_CLI}"
RESULTS_DIR="${2:?}"
AGENT_CLI="${3:?}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPERIMENT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

SCENARIO_FILE="$EXPERIMENT_DIR/scenarios/${SCENARIO_NAME}.json"
ANALYSIS_SYSTEM="$(cat "$EXPERIMENT_DIR/prompts/scenario-analysis-system.md")"
SCENARIO_JSON="$(cat "$SCENARIO_FILE")"

GROUND_TRUTH="$(echo "$SCENARIO_JSON" | jq '.ground_truth')"
EXPECTED_EXTRACTS="$(echo "$SCENARIO_JSON" | jq '.expected_triage_extracts')"

# Collect all judge assessments for this scenario (across all trials)
ASSESSMENTS=""
for strategy_dir in "$RESULTS_DIR/$SCENARIO_NAME"/*/; do
  [[ -d "$strategy_dir" ]] || continue
  strategy="$(basename "$strategy_dir")"

  for trial_dir in "$strategy_dir"trial-*/; do
    [[ -d "$trial_dir" ]] || continue
    assessment="$trial_dir/judge-assessment.json"
    [[ -f "$assessment" ]] || continue

    trial="$(basename "$trial_dir")"
    ASSESSMENTS+="
--- STRATEGY: $strategy ($trial) ---

$(cat "$assessment")
"
  done
done

if [[ -z "$ASSESSMENTS" ]]; then
  echo "  No assessments found for scenario $SCENARIO_NAME" >&2
  exit 0
fi

ANALYSIS_PROMPT="$ANALYSIS_SYSTEM

--- GROUND TRUTH ---

$GROUND_TRUTH

--- EXPECTED INFORMATION TO EXTRACT ---

$EXPECTED_EXTRACTS

--- JUDGE ASSESSMENTS ---
$ASSESSMENTS
--- INSTRUCTIONS ---

Analyze the judge assessments above for common patterns and standout items
across all strategies on this scenario. Respond with ONLY valid JSON."

# Invoke agent
ANALYSIS_RAW=""
case "$AGENT_CLI" in
  claude)
    ANALYSIS_RAW="$(claude -p "$ANALYSIS_PROMPT" --output-format text 2>/dev/null)" || true
    ;;
  opencode)
    ANALYSIS_RAW="$(opencode -p "$ANALYSIS_PROMPT" 2>/dev/null)" || true
    ;;
esac

# Extract JSON
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

OUTPUT_FILE="$RESULTS_DIR/$SCENARIO_NAME/scenario-analysis.json"

ANALYSIS_JSON="$(extract_json "$ANALYSIS_RAW")" || {
  echo "    Warning: could not parse scenario analysis response" >&2
  echo "$ANALYSIS_RAW" > "$RESULTS_DIR/$SCENARIO_NAME/scenario-analysis-error.txt"
  ANALYSIS_JSON='{"common_patterns":["Analysis could not be generated (parse error)"],"standout_items":[]}'
}

echo "$ANALYSIS_JSON" | jq '.' > "$OUTPUT_FILE"
echo "    Scenario analysis written to $OUTPUT_FILE"
