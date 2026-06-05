#!/usr/bin/env bash
# Lint eval case definitions — verifies every case has required fields.
#
# Usage:
#   ./eval/lint-cases.sh <agent-name>
#   ./eval/lint-cases.sh triage
#
# Checks:
#   - Every case directory has annotations.yaml
#   - Every annotations.yaml declares max_turns and max_cost_usd
set -euo pipefail

AGENT="${1:?agent name required}"
EVAL_DIR="$(cd "$(dirname "$0")" && pwd)"
CASES_DIR="${EVAL_DIR}/${AGENT}/cases"

if [[ ! -d "$CASES_DIR" ]]; then
  echo "ERROR: cases directory not found: $CASES_DIR" >&2
  exit 1
fi

ERRORS=0
for case_dir in "$CASES_DIR"/*/; do
  case_name=$(basename "$case_dir")
  annotations="$case_dir/annotations.yaml"
  if [[ ! -f "$annotations" ]]; then
    echo "FAIL: ${case_name}: annotations.yaml not found"
    ERRORS=$((ERRORS + 1))
    continue
  fi
  max_turns=$(yq -r '.max_turns // ""' "$annotations")
  max_cost=$(yq -r '.max_cost_usd // ""' "$annotations")
  if [[ -z "$max_turns" || -z "$max_cost" ]]; then
    echo "FAIL: ${case_name}: annotations.yaml missing max_turns and/or max_cost_usd"
    ERRORS=$((ERRORS + 1))
  fi
done

if [[ $ERRORS -gt 0 ]]; then
  echo "ERROR: $ERRORS case lint failures" >&2
  exit 1
fi

echo "OK: all cases pass lint checks"
