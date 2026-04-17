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
#   --trials N         Repetitions per scenario x strategy (default: 5)
#   --max-turns N      Max dialogue turns per trial (default: 6)
#   --agent COMMAND    Agent CLI for triage/reporter: "claude" or "opencode" (default: claude)
#   --judge-model ID   Model ID for the judge agent (default: claude-sonnet-4-6)
#   --resume DIR       Resume a previous run, skipping completed trials
#   --dry-run          Print prompts without invoking agents
#   --help             Show this help
# =============================================================================

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EXPERIMENT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Defaults
SCENARIOS=(crash-on-save slow-search auth-redirect-loop silent-data-corruption flaky-ci memory-leak intermittent-403 email-delay file-upload-corruption wrong-search-results)
STRATEGIES=(superpowers-brainstorming structured-triage socratic-refinement omc-deep-interview omo-prometheus)
NUM_TRIALS=5
MAX_TURNS=6
AGENT_CLI=claude
JUDGE_MODEL=claude-sonnet-4-6
DRY_RUN=""
FILTER_SCENARIO=""
FILTER_STRATEGY=""
RESUME_DIR=""

# Parse args
while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario)     FILTER_SCENARIO="$2"; shift 2 ;;
    --strategy)     FILTER_STRATEGY="$2"; shift 2 ;;
    --trials)       NUM_TRIALS="$2"; shift 2 ;;
    --max-turns)    MAX_TURNS="$2"; shift 2 ;;
    --agent)        AGENT_CLI="$2"; shift 2 ;;
    --judge-model)  JUDGE_MODEL="$2"; shift 2 ;;
    --resume)       RESUME_DIR="$2"; shift 2 ;;
    --dry-run)      DRY_RUN="--dry-run"; shift ;;
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

# Create or reuse results directory
if [[ -n "$RESUME_DIR" ]]; then
  if [[ ! -d "$RESUME_DIR" ]]; then
    echo "Error: resume directory does not exist: $RESUME_DIR" >&2
    exit 1
  fi
  RESULTS_DIR="$(cd "$RESUME_DIR" && pwd)"
else
  TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
  RESULTS_DIR="$EXPERIMENT_DIR/results/$TIMESTAMP"
fi
mkdir -p "$RESULTS_DIR"

CELLS=$((${#SCENARIOS[@]} * ${#STRATEGIES[@]}))
TOTAL=$((CELLS * NUM_TRIALS))
CURRENT=0
SKIPPED=0

echo "=============================================="
echo "Triage Strategy Evaluation (v2)"
echo "=============================================="
echo "Agent:       $AGENT_CLI"
echo "Judge model: $JUDGE_MODEL"
echo "Scenarios:   ${SCENARIOS[*]}"
echo "Strategies:  ${STRATEGIES[*]}"
echo "Trials/cell: $NUM_TRIALS"
echo "Max turns:   $MAX_TURNS"
echo "Results:     $RESULTS_DIR"
echo "Resume:      ${RESUME_DIR:-no}"
echo "Total runs:  $TOTAL ($CELLS cells x $NUM_TRIALS trials)"
echo "=============================================="
echo ""

# ---------------------------------------------------------------------------
# Phase 1: Seed the app (optional — creates the fictional TaskFlow app)
# ---------------------------------------------------------------------------

SEED_DIR="$RESULTS_DIR/seed-app"
if [[ -n "$RESUME_DIR" ]] && [[ -d "$SEED_DIR" ]]; then
  echo "Phase 1: Skipping seed app (already exists from previous run)"
  echo ""
elif [[ -f "$EXPERIMENT_DIR/prompts/seed-app.md" ]] && [[ -z "$DRY_RUN" ]]; then
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
if [[ -n "$RESUME_DIR" ]]; then
  echo "(Resuming — completed trials will be skipped)"
fi
echo ""

for scenario in "${SCENARIOS[@]}"; do
  for strategy in "${STRATEGIES[@]}"; do
    for trial_num in $(seq 1 "$NUM_TRIALS"); do
      CURRENT=$((CURRENT + 1))
      TRIAL_DIR="$RESULTS_DIR/$scenario/$strategy/trial-$trial_num"

      # Skip completed trials when resuming
      if [[ -n "$RESUME_DIR" ]] && [[ -f "$TRIAL_DIR/trial-metadata.json" ]]; then
        SKIPPED=$((SKIPPED + 1))
        echo "[$CURRENT/$TOTAL] $scenario x $strategy (trial $trial_num/$NUM_TRIALS) — skipped (complete)"
        continue
      fi

      mkdir -p "$TRIAL_DIR"

      echo "[$CURRENT/$TOTAL] $scenario x $strategy (trial $trial_num/$NUM_TRIALS)"
      "$SCRIPT_DIR/run-single-trial.sh" \
        "$scenario" "$strategy" "$TRIAL_DIR" "$MAX_TURNS" "$AGENT_CLI" $DRY_RUN
      echo ""
    done
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
      for trial_num in $(seq 1 "$NUM_TRIALS"); do
        TRIAL_DIR="$RESULTS_DIR/$scenario/$strategy/trial-$trial_num"
        if [[ -f "$TRIAL_DIR/conversation.json" ]]; then
          if [[ -n "$RESUME_DIR" ]] && [[ -f "$TRIAL_DIR/judge-assessment.json" ]]; then
            echo "  Skipping (already judged): $scenario x $strategy (trial $trial_num)"
          else
            echo "  Judging: $scenario x $strategy (trial $trial_num)"
            "$SCRIPT_DIR/judge.sh" "$scenario" "$TRIAL_DIR" "$AGENT_CLI" "$JUDGE_MODEL"
          fi
        fi
      done
    done
  done
  echo ""

  # ---- Phase 3.5: Cross-strategy scenario analysis ----
  echo "Phase 3.5: Analyzing scenarios across strategies..."
  echo ""

  for scenario in "${SCENARIOS[@]}"; do
    if ls "$RESULTS_DIR/$scenario"/*/trial-*/judge-assessment.json &>/dev/null; then
      if [[ -n "$RESUME_DIR" ]] && [[ -f "$RESULTS_DIR/$scenario/cross-strategy-analysis.md" ]]; then
        echo "  Skipping (already analyzed): $scenario"
      else
        echo "  Analyzing: $scenario"
        "$SCRIPT_DIR/analyze-scenario.sh" "$scenario" "$RESULTS_DIR" "$AGENT_CLI"
      fi
    fi
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
if [[ $SKIPPED -gt 0 ]]; then
  echo "Skipped:  $SKIPPED/$TOTAL trials (already complete)"
  echo "Ran:      $((TOTAL - SKIPPED))/$TOTAL trials"
fi
echo "Results: $RESULTS_DIR"
echo "Summary: $RESULTS_DIR/summary.md"
echo "=============================================="
