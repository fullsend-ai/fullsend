#!/usr/bin/env bash
# =============================================================================
# summarize.sh — Generates the final comparison table from judge assessments
# =============================================================================
#
# Aggregates multiple trials per scenario x strategy cell, reporting mean and
# standard deviation of weighted_total scores.
#
# Usage:
#   summarize.sh RESULTS_DIR
# =============================================================================

set -euo pipefail

RESULTS_DIR="${1:?Usage: $0 RESULTS_DIR}"
SUMMARY_FILE="$RESULTS_DIR/summary.md"

# ---------------------------------------------------------------------------
# Collect all judge assessments
# ---------------------------------------------------------------------------
# Path: results/<timestamp>/<scenario>/<strategy>/trial-N/judge-assessment.json
# (also supports legacy flat layout without trial-N/)

declare -A CELL_SCORES  # key: scenario__strategy, value: space-separated scores
STRATEGIES=()
SCENARIOS=()

for assessment in "$RESULTS_DIR"/*/*/trial-*/judge-assessment.json "$RESULTS_DIR"/*/*/judge-assessment.json; do
  [[ -f "$assessment" ]] || continue

  rel="$(realpath --relative-to="$RESULTS_DIR" "$assessment")"
  scenario="$(echo "$rel" | cut -d/ -f1)"
  strategy="$(echo "$rel" | cut -d/ -f2)"

  # Skip the scenario-analysis.json which lives at scenario level
  [[ "$strategy" == "scenario-analysis.json" ]] && continue

  if [[ ! " ${SCENARIOS[*]:-} " =~ " $scenario " ]]; then
    SCENARIOS+=("$scenario")
  fi
  if [[ ! " ${STRATEGIES[*]:-} " =~ " $strategy " ]]; then
    STRATEGIES+=("$strategy")
  fi

  score="$(jq -r '.weighted_total // 0' "$assessment")"
  key="${scenario}__${strategy}"
  CELL_SCORES["$key"]="${CELL_SCORES["$key"]:-} $score"
done

if [[ ${#SCENARIOS[@]} -eq 0 ]] || [[ ${#STRATEGIES[@]} -eq 0 ]]; then
  echo "No judge assessments found in $RESULTS_DIR" >&2
  echo "# Summary" > "$SUMMARY_FILE"
  echo "" >> "$SUMMARY_FILE"
  echo "No results to summarize." >> "$SUMMARY_FILE"
  exit 0
fi

# ---------------------------------------------------------------------------
# Helper: compute mean and stddev from space-separated scores
# ---------------------------------------------------------------------------

calc_stats() {
  local scores="$1"
  # Returns: mean stddev n
  echo "$scores" | awk '{
    n = NF; sum = 0; sumsq = 0
    for (i = 1; i <= NF; i++) { sum += $i; sumsq += $i * $i }
    mean = sum / n
    if (n > 1) { var = (sumsq - n * mean * mean) / (n - 1); std = sqrt(var > 0 ? var : 0) }
    else { std = 0 }
    printf "%.2f %.2f %d", mean, std, n
  }'
}

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
  echo "Each cell shows **mean +/- stddev** over N trials."
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
  printf '%s\n' "---|"

  # Rows
  declare -A STRATEGY_MEANS
  for strategy in "${STRATEGIES[@]}"; do
    printf "| %s |" "$strategy"
    all_means=""
    for scenario in "${SCENARIOS[@]}"; do
      key="${scenario}__${strategy}"
      scores="${CELL_SCORES["$key"]:-}"
      if [[ -n "$scores" ]]; then
        read -r mean std n <<< "$(calc_stats "$scores")"
        printf " %s +/- %s (n=%s) |" "$mean" "$std" "$n"
        all_means="$all_means $mean"
      else
        printf " N/A |"
      fi
    done
    if [[ -n "$all_means" ]]; then
      grand_mean="$(echo "$all_means" | awk '{ sum=0; for(i=1;i<=NF;i++) sum+=$i; printf "%.2f", sum/NF }')"
    else
      grand_mean="N/A"
    fi
    STRATEGY_MEANS["$strategy"]="$grand_mean"
    printf " **%s** |\n" "$grand_mean"
  done

  echo ""

  # ---- Per-trial detail ----
  echo "## Detailed Results"
  echo ""

  for scenario in "${SCENARIOS[@]}"; do
    echo "### Scenario: $scenario"
    echo ""

    # Render cross-strategy analysis if available
    analysis_file="$RESULTS_DIR/$scenario/scenario-analysis.json"
    if [[ -f "$analysis_file" ]]; then
      patterns_count="$(jq -r '.common_patterns | length' "$analysis_file")"
      standouts_count="$(jq -r '.standout_items | length' "$analysis_file")"

      if [[ "$patterns_count" -gt 0 ]] || [[ "$standouts_count" -gt 0 ]]; then
        echo "#### Cross-Strategy Analysis"
        echo ""

        if [[ "$patterns_count" -gt 0 ]]; then
          echo "**Common patterns:**"
          jq -r '.common_patterns[] | "- " + .' "$analysis_file"
          echo ""
        fi

        if [[ "$standouts_count" -gt 0 ]]; then
          echo "**Standout items:**"
          jq -r '.standout_items[] | "- " + .' "$analysis_file"
          echo ""
        fi

        echo "---"
        echo ""
      fi
    fi

    for strategy in "${STRATEGIES[@]}"; do
      key="${scenario}__${strategy}"
      scores="${CELL_SCORES["$key"]:-}"
      [[ -n "$scores" ]] || continue

      echo "#### Strategy: $strategy"
      echo ""

      read -r mean std n <<< "$(calc_stats "$scores")"
      echo "**Weighted total (mean):** $mean +/- $std over $n trials"
      echo ""

      # Show per-trial scores
      echo "| Trial | Score | Turns |"
      echo "|---|---|---|"
      trial_idx=0
      for assessment in "$RESULTS_DIR/$scenario/$strategy"/trial-*/judge-assessment.json "$RESULTS_DIR/$scenario/$strategy/judge-assessment.json"; do
        [[ -f "$assessment" ]] || continue
        trial_idx=$((trial_idx + 1))
        t_score="$(jq -r '.weighted_total // "N/A"' "$assessment")"
        t_turns="$(jq -r '.turn_count // "?"' "$assessment")"
        echo "| $trial_idx | $t_score | $t_turns |"
      done
      echo ""

      # Show rubric breakdown for the first trial as a representative example
      first_assessment=""
      for a in "$RESULTS_DIR/$scenario/$strategy"/trial-*/judge-assessment.json "$RESULTS_DIR/$scenario/$strategy/judge-assessment.json"; do
        if [[ -f "$a" ]]; then first_assessment="$a"; break; fi
      done

      if [[ -n "$first_assessment" ]]; then
        echo "<details>"
        echo "<summary>Rubric breakdown (trial 1)</summary>"
        echo ""
        echo "| Criterion | Score | Rationale |"
        echo "|---|---|---|"
        for criterion in completeness accuracy efficiency question_quality actionability; do
          s="$(jq -r ".scores.${criterion}.score // \"N/A\"" "$first_assessment")"
          r="$(jq -r ".scores.${criterion}.rationale // \"\"" "$first_assessment" | tr '\n' ' ' | head -c 120)"
          echo "| $criterion | $s | $r |"
        done
        echo ""

        echo "**Strengths:**"
        jq -r '.notable_strengths // [] | .[] | "- " + .' "$first_assessment" 2>/dev/null || true
        echo ""
        echo "**Weaknesses:**"
        jq -r '.notable_weaknesses // [] | .[] | "- " + .' "$first_assessment" 2>/dev/null || true
        echo ""
        echo "**Most insightful question:** $(jq -r '.most_insightful_question // "N/A"' "$first_assessment")"
        echo ""
        echo "</details>"
        echo ""
      fi

      echo "---"
      echo ""
    done
  done

  # ---- Strategy rankings ----
  echo "## Strategy Rankings (by mean score across scenarios)"
  echo ""
  echo "| Rank | Strategy | Mean Score |"
  echo "|---|---|---|"

  # Sort by mean (descending)
  rank=1
  for strategy in $(for s in "${STRATEGIES[@]}"; do echo "$s ${STRATEGY_MEANS[$s]}"; done | sort -k2 -rn | awk '{print $1}'); do
    echo "| $rank | $strategy | ${STRATEGY_MEANS[$strategy]} |"
    rank=$((rank + 1))
  done

  echo ""
  echo "## Interpretation Guide"
  echo ""
  echo "- **Scores are 1-5** where 3 = adequate, 4 = good, 5 = excellent"
  echo "- **Weighted total** combines: completeness (25%), accuracy (25%),"
  echo "  efficiency (20%), question quality (15%), actionability (15%)"
  echo "- **Mean +/- stddev** shows the average and variation across repeated trials"
  echo "- High stddev indicates the strategy behaves inconsistently"
  echo "- **Question quality** measures insightfulness — did the triage agent"
  echo "  ask questions that a human triager would be impressed by?"
  echo "- **Efficiency** penalizes wasted turns and rewards early resolution"
  echo "  when the issue is clear enough"

} > "$SUMMARY_FILE"

echo "  Summary written to $SUMMARY_FILE"
