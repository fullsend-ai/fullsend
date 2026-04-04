#!/usr/bin/env bash
# =============================================================================
# summarize.sh — Generates the final comparison table from judge assessments
# =============================================================================
#
# Usage:
#   summarize.sh RESULTS_DIR
# =============================================================================

set -euo pipefail

RESULTS_DIR="${1:?Usage: $0 RESULTS_DIR}"
SUMMARY_FILE="$RESULTS_DIR/summary.md"

# Collect all judge assessments
declare -A SCORES
STRATEGIES=()
SCENARIOS=()

for assessment in "$RESULTS_DIR"/*/judge-assessment.json "$RESULTS_DIR"/*/*/judge-assessment.json; do
  [[ -f "$assessment" ]] || continue

  # Extract scenario and strategy from path
  # Path: results/<timestamp>/<scenario>/<strategy>/judge-assessment.json
  rel="$(realpath --relative-to="$RESULTS_DIR" "$assessment")"
  scenario="$(echo "$rel" | cut -d/ -f1)"
  strategy="$(echo "$rel" | cut -d/ -f2)"

  # Track unique scenarios and strategies
  if [[ ! " ${SCENARIOS[*]:-} " =~ " $scenario " ]]; then
    SCENARIOS+=("$scenario")
  fi
  if [[ ! " ${STRATEGIES[*]:-} " =~ " $strategy " ]]; then
    STRATEGIES+=("$strategy")
  fi

  SCORES["${scenario}__${strategy}"]="$(jq -r '.weighted_total // 0' "$assessment")"
done

if [[ ${#SCENARIOS[@]} -eq 0 ]] || [[ ${#STRATEGIES[@]} -eq 0 ]]; then
  echo "No judge assessments found in $RESULTS_DIR" >&2
  echo "# Summary" > "$SUMMARY_FILE"
  echo "" >> "$SUMMARY_FILE"
  echo "No results to summarize." >> "$SUMMARY_FILE"
  exit 0
fi

# ---------------------------------------------------------------------------
# Generate markdown summary
# ---------------------------------------------------------------------------

{
  echo "# Triage Skill Comparison: Results Summary"
  echo ""
  echo "Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo ""

  # ---- Overall scores table ----
  echo "## Scores by Strategy x Scenario"
  echo ""

  # Header
  printf "| Strategy |"
  for scenario in "${SCENARIOS[@]}"; do
    printf " %s |" "$scenario"
  done
  printf " **Average** |\n"

  # Separator
  printf "|---|"
  for _ in "${SCENARIOS[@]}"; do
    printf '%s' "---|"
  done
  printf "---|\n"

  # Rows
  for strategy in "${STRATEGIES[@]}"; do
    printf "| %s |" "$strategy"
    total=0
    count=0
    for scenario in "${SCENARIOS[@]}"; do
      score="${SCORES["${scenario}__${strategy}"]:-N/A}"
      printf " %s |" "$score"
      if [[ "$score" != "N/A" ]]; then
        total="$(echo "$total + $score" | bc)"
        count=$((count + 1))
      fi
    done
    if [[ $count -gt 0 ]]; then
      avg="$(echo "scale=2; $total / $count" | bc)"
    else
      avg="N/A"
    fi
    printf " **%s** |\n" "$avg"
  done

  echo ""

  # ---- Per-trial detail ----
  echo "## Detailed Results"
  echo ""

  for scenario in "${SCENARIOS[@]}"; do
    echo "### Scenario: $scenario"
    echo ""

    for strategy in "${STRATEGIES[@]}"; do
      assessment="$RESULTS_DIR/$scenario/$strategy/judge-assessment.json"
      [[ -f "$assessment" ]] || continue

      echo "#### Strategy: $strategy"
      echo ""
      echo "**Weighted total:** $(jq -r '.weighted_total // "N/A"' "$assessment")/5.00"
      echo ""

      # Score breakdown
      echo "| Criterion | Score | Rationale |"
      echo "|---|---|---|"
      for criterion in completeness accuracy efficiency question_quality actionability; do
        s="$(jq -r ".scores.${criterion}.score // \"N/A\"" "$assessment")"
        r="$(jq -r ".scores.${criterion}.rationale // \"\"" "$assessment" | tr '\n' ' ' | head -c 120)"
        echo "| $criterion | $s | $r |"
      done
      echo ""

      # Qualitative notes
      echo "**Strengths:**"
      jq -r '.notable_strengths // [] | .[] | "- " + .' "$assessment" 2>/dev/null || true
      echo ""
      echo "**Weaknesses:**"
      jq -r '.notable_weaknesses // [] | .[] | "- " + .' "$assessment" 2>/dev/null || true
      echo ""
      echo "**Most insightful question:** $(jq -r '.most_insightful_question // "N/A"' "$assessment")"
      echo ""
      echo "---"
      echo ""
    done
  done

  # ---- Strategy rankings ----
  echo "## Strategy Rankings (by average score)"
  echo ""
  echo "| Rank | Strategy | Average Score |"
  echo "|---|---|---|"

  # Calculate averages and sort
  declare -A AVERAGES
  for strategy in "${STRATEGIES[@]}"; do
    total=0
    count=0
    for scenario in "${SCENARIOS[@]}"; do
      score="${SCORES["${scenario}__${strategy}"]:-}"
      if [[ -n "$score" && "$score" != "N/A" ]]; then
        total="$(echo "$total + $score" | bc)"
        count=$((count + 1))
      fi
    done
    if [[ $count -gt 0 ]]; then
      AVERAGES["$strategy"]="$(echo "scale=2; $total / $count" | bc)"
    else
      AVERAGES["$strategy"]="0"
    fi
  done

  # Sort by average (descending)
  rank=1
  for strategy in $(for s in "${STRATEGIES[@]}"; do echo "$s ${AVERAGES[$s]}"; done | sort -k2 -rn | awk '{print $1}'); do
    echo "| $rank | $strategy | ${AVERAGES[$strategy]} |"
    rank=$((rank + 1))
  done

  echo ""
  echo "## Interpretation Guide"
  echo ""
  echo "- **Scores are 1-5** where 3 = adequate, 4 = good, 5 = excellent"
  echo "- **Weighted total** combines: completeness (25%), accuracy (25%),"
  echo "  efficiency (20%), question quality (15%), actionability (15%)"
  echo "- **Question quality** measures insightfulness — did the triage agent"
  echo "  ask questions that a human triager would be impressed by?"
  echo "- **Efficiency** penalizes wasted turns and rewards early resolution"
  echo "  when the issue is clear enough"

} > "$SUMMARY_FILE"

echo "  Summary written to $SUMMARY_FILE"
