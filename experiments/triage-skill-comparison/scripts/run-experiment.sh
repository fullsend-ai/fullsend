#!/usr/bin/env bash
# =============================================================================
# run-experiment.sh — Runs all scenario x strategy combinations
# =============================================================================
#
# Usage:
#   ./scripts/run-experiment.sh [OPTIONS]
#
# Options:
#   --scenario NAME    Run only this scenario (default: all)
#   --strategy NAME    Run only this strategy (default: all)
#   --max-turns N      Max dialogue turns per trial (default: 6)
#   --agent COMMAND    Agent CLI to use: "claude" or "opencode" (default: claude)
#   --dry-run          Print prompts without invoking agents
#   --help             Show this help
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPERIMENT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Defaults
SCENARIOS=(crash-on-save slow-search auth-redirect-loop)
STRATEGIES=(superpowers-brainstorming structured-triage socratic-refinement omc-deep-interview omo-prometheus)
MAX_TURNS=6
AGENT_CLI=claude
DRY_RUN=""
FILTER_SCENARIO=""
FILTER_STRATEGY=""

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario)   FILTER_SCENARIO="$2"; shift 2 ;;
    --strategy)   FILTER_STRATEGY="$2"; shift 2 ;;
    --max-turns)  MAX_TURNS="$2"; shift 2 ;;
    --agent)      AGENT_CLI="$2"; shift 2 ;;
    --dry-run)    DRY_RUN="--dry-run"; shift ;;
    --help|-h)
      sed -n '2,/^$/p' "$0" | sed 's/^# //' | sed 's/^#//'
      exit 0
      ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

# Apply filters
if [[ -n "$FILTER_SCENARIO" ]]; then
  SCENARIOS=("$FILTER_SCENARIO")
fi
if [[ -n "$FILTER_STRATEGY" ]]; then
  STRATEGIES=("$FILTER_STRATEGY")
fi

# Validate CLI
if ! command -v "$AGENT_CLI" &>/dev/null && [[ -z "$DRY_RUN" ]]; then
  echo "Error: '$AGENT_CLI' not found in PATH." >&2
  echo "Install Claude Code (claude) or OpenCode (opencode), or use --dry-run." >&2
  exit 1
fi

# Create results directory
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
RESULTS_DIR="$EXPERIMENT_DIR/results/$TIMESTAMP"
mkdir -p "$RESULTS_DIR"

TOTAL=$((${#SCENARIOS[@]} * ${#STRATEGIES[@]}))
CURRENT=0

echo "=============================================="
echo "Triage Skill Comparison Experiment"
echo "=============================================="
echo "Agent:      $AGENT_CLI"
echo "Scenarios:  ${SCENARIOS[*]}"
echo "Strategies: ${STRATEGIES[*]}"
echo "Max turns:  $MAX_TURNS"
echo "Results:    $RESULTS_DIR"
echo "Trials:     $TOTAL"
echo "=============================================="
echo ""

# ---------------------------------------------------------------------------
# Phase 1: Seed the app (optional — creates the fictional TaskFlow app)
# ---------------------------------------------------------------------------

SEED_DIR="$RESULTS_DIR/seed-app"
if [[ -f "$EXPERIMENT_DIR/prompts/seed-app.md" ]] && [[ -z "$DRY_RUN" ]]; then
  echo "Phase 1: Seeding TaskFlow application..."
  SEED_PROMPT="$(cat "$EXPERIMENT_DIR/prompts/seed-app.md")"
  mkdir -p "$SEED_DIR"

  if [[ "$AGENT_CLI" == "claude" ]]; then
    (cd "$SEED_DIR" && claude -p "$SEED_PROMPT" --output-format text > seed-log.txt 2>&1) || {
      echo "  Warning: seed-app generation failed (non-fatal)" >&2
    }
  else
    (cd "$SEED_DIR" && opencode -p "$SEED_PROMPT" > seed-log.txt 2>&1) || {
      echo "  Warning: seed-app generation failed (non-fatal)" >&2
    }
  fi
  echo "  Seed app created in $SEED_DIR"
  echo ""
else
  echo "Phase 1: Skipping seed app (dry-run or missing prompt)"
  echo ""
fi

# ---------------------------------------------------------------------------
# Phase 2: Run all trials
# ---------------------------------------------------------------------------

echo "Phase 2: Running triage trials..."
echo ""

for scenario in "${SCENARIOS[@]}"; do
  for strategy in "${STRATEGIES[@]}"; do
    CURRENT=$((CURRENT + 1))
    TRIAL_DIR="$RESULTS_DIR/$scenario/$strategy"
    mkdir -p "$TRIAL_DIR"

    echo "[$CURRENT/$TOTAL] $scenario x $strategy"
    "$SCRIPT_DIR/run-single-trial.sh" \
      "$scenario" "$strategy" "$TRIAL_DIR" "$MAX_TURNS" "$AGENT_CLI" $DRY_RUN
    echo ""
  done
done

# ---------------------------------------------------------------------------
# Phase 3: Judge all completed trials
# ---------------------------------------------------------------------------

if [[ -z "$DRY_RUN" ]]; then
  echo "Phase 3: Judging trials..."
  echo ""

  for scenario in "${SCENARIOS[@]}"; do
    for strategy in "${STRATEGIES[@]}"; do
      TRIAL_DIR="$RESULTS_DIR/$scenario/$strategy"
      if [[ -f "$TRIAL_DIR/conversation.json" ]]; then
        echo "  Judging: $scenario x $strategy"
        "$SCRIPT_DIR/judge.sh" "$scenario" "$TRIAL_DIR" "$AGENT_CLI"
      fi
    done
  done
  echo ""
fi

# ---------------------------------------------------------------------------
# Phase 4: Generate summary table
# ---------------------------------------------------------------------------

echo "Phase 4: Generating summary..."
"$SCRIPT_DIR/summarize.sh" "$RESULTS_DIR"

echo ""
echo "=============================================="
echo "Experiment complete."
echo "Results: $RESULTS_DIR"
echo "Summary: $RESULTS_DIR/summary.md"
echo "=============================================="
