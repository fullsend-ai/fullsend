#!/usr/bin/env bash
# =============================================================================
# summarize.sh — Generates the final comparison table from judge assessments
# =============================================================================
#
# Aggregates multiple trials per scenario x strategy cell, reporting mean and
# standard deviation of weighted_total scores. Tracks reliability (parse success
# rate) separately from quality scores.
#
# Usage:
#   summarize.sh RESULTS_DIR
# =============================================================================

set -euo pipefail

RESULTS_DIR="${1:?Usage: $0 RESULTS_DIR}"
SUMMARY_FILE="$RESULTS_DIR/summary.md"

# ---------------------------------------------------------------------------
# Collect all judge assessments and trial metadata
# ---------------------------------------------------------------------------

declare -A CELL_SCORES        # key: scenario__strategy, value: space-separated scores
declare -A CELL_TOTAL_TRIALS  # key: scenario__strategy, value: total trial count
declare -A CELL_PARSE_FAILS   # key: scenario__strategy, value: total parse failures
STRATEGIES=()
SCENARIOS=()

# Collect scores from judge assessments
for assessment in "$RESULTS_DIR"/*/*/trial-*/judge-assessment.json; do
  [[ -f "$assessment" ]] || continue

  rel="$(realpath --relative-to="$RESULTS_DIR" "$assessment")"
  scenario="$(echo "$rel" | cut -d/ -f1)"
  strategy="$(echo "$rel" | cut -d/ -f2)"

  if [[ ! " ${SCENARIOS[*]:-} " =~ " $scenario " ]]; then
    SCENARIOS+=("$scenario")
  fi
  if [[ ! " ${STRATEGIES[*]:-} " =~ " $strategy " ]]; then
    STRATEGIES+=("$strategy")
  fi

  # Only include non-zero scores (zero = parse failure in judge)
  score="$(jq -r '.weighted_total // 0' "$assessment")"
  key="${scenario}__${strategy}"
  if [[ "$(echo "$score > 0" | bc)" -eq 1 ]]; then
    CELL_SCORES["$key"]="${CELL_SCORES["$key"]:-} $score"
  fi
done

# Collect reliability data from trial metadata
for metadata in "$RESULTS_DIR"/*/*/trial-*/trial-metadata.json; do
  [[ -f "$metadata" ]] || continue

  rel="$(realpath --relative-to="$RESULTS_DIR" "$metadata")"
  scenario="$(echo "$rel" | cut -d/ -f1)"
  strategy="$(echo "$rel" | cut -d/ -f2)"
  key="${scenario}__${strategy}"

  CELL_TOTAL_TRIALS["$key"]=$(( ${CELL_TOTAL_TRIALS["$key"]:-0} + 1 ))
  pf="$(jq -r '.parse_failures // 0' "$metadata")"
  if [[ "$pf" -gt 0 ]]; then
    CELL_PARSE_FAILS["$key"]=$(( ${CELL_PARSE_FAILS["$key"]:-0} + pf ))
  fi
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
  echo "# Triage Strategy Evaluation (v2): Results Summary"
  echo ""
  echo "Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo ""

  # ---- Overall scores table ----
  echo "## Scores by Strategy x Scenario"
  echo ""
  echo "Each cell shows **mean +/- stddev** over N valid trials (excluding parse failures)."
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

  # ---- Reliability table ----
  echo "## Reliability by Strategy"
  echo ""
  echo "Parse failures are tracked separately from quality scores."
  echo ""
  echo "| Strategy | Total Trials | Parse Failures | Reliability |"
  echo "|---|---|---|---|"

  for strategy in "${STRATEGIES[@]}"; do
    total_trials=0
    total_fails=0
    for scenario in "${SCENARIOS[@]}"; do
      key="${scenario}__${strategy}"
      total_trials=$((total_trials + ${CELL_TOTAL_TRIALS["$key"]:-0}))
      total_fails=$((total_fails + ${CELL_PARSE_FAILS["$key"]:-0}))
    done
    if [[ $total_trials -gt 0 ]]; then
      reliability="$(echo "scale=0; ($total_trials - $total_fails) * 100 / $total_trials" | bc)%"
    else
      reliability="N/A"
    fi
    echo "| $strategy | $total_trials | $total_fails | $reliability |"
  done

  echo ""

  # ---- Per-scenario detail ----
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
      echo "**Weighted total (mean):** $mean +/- $std over $n valid trials"
      echo ""

      # Show per-trial scores
      echo "| Trial | Score | Turns | Parse Failures |"
      echo "|---|---|---|---|"
      trial_idx=0
      for trial_dir in "$RESULTS_DIR/$scenario/$strategy"/trial-*/; do
        [[ -d "$trial_dir" ]] || continue
        trial_idx=$((trial_idx + 1))
        assessment="$trial_dir/judge-assessment.json"
        metadata="$trial_dir/trial-metadata.json"
        if [[ -f "$assessment" ]]; then
          t_score="$(jq -r '.weighted_total // "N/A"' "$assessment")"
          t_turns="$(jq -r '.turn_count // "?"' "$assessment")"
        else
          t_score="N/A"
          t_turns="?"
        fi
        if [[ -f "$metadata" ]]; then
          t_pf="$(jq -r '.parse_failures // 0' "$metadata")"
        else
          t_pf="?"
        fi
        echo "| $trial_idx | $t_score | $t_turns | $t_pf |"
      done
      echo ""

      # Show rubric breakdown for the first valid trial
      first_assessment=""
      for a in "$RESULTS_DIR/$scenario/$strategy"/trial-*/judge-assessment.json; do
        if [[ -f "$a" ]]; then
          s="$(jq -r '.weighted_total // 0' "$a")"
          if [[ "$(echo "$s > 0" | bc)" -eq 1 ]]; then
            first_assessment="$a"
            break
          fi
        fi
      done

      if [[ -n "$first_assessment" ]]; then
        echo "<details>"
        echo "<summary>Rubric breakdown (first valid trial)</summary>"
        echo ""
        echo "| Criterion | Score | Rationale |"
        echo "|---|---|---|"
        for criterion in completeness accuracy thoroughness economy question_quality actionability; do
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
  echo "  thoroughness (15%), economy (10%), question quality (15%), actionability (10%)"
  echo "- **Mean +/- stddev** shows the average and variation across repeated trials"
  echo "- High stddev indicates the strategy behaves inconsistently"
  echo "- **Reliability** is tracked separately — parse failures are excluded from"
  echo "  quality scores so they don't distort strategy comparisons"
  echo "- **Thoroughness** measures whether the agent asked enough before resolving"
  echo "- **Economy** measures whether turns were well-spent (no redundant questions)"
  echo "- These two criteria replace the old unified 'efficiency' score which was"
  echo "  self-contradictory (penalizing both too few and too many questions)"

} > "$SUMMARY_FILE"

echo "  Summary written to $SUMMARY_FILE"
