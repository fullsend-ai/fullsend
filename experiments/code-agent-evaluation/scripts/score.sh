#!/bin/bash
set -euo pipefail

# score.sh - Composite scorer for code agent evaluation
#
# Combines deterministic gates (50% weight) and LLM judge scores (50% weight)
# into a single composite score per trial.
#
# Usage: score.sh --gate-results <json> --judge-results <json> --output <json>

usage() {
    cat << EOF
Usage: $0 --gate-results <json> --judge-results <json> --output <json>

Required:
  --gate-results <json>    Path to deterministic gate results JSON
  --judge-results <json>   Path to LLM judge assessment JSON
  --output <json>          Path to write composite score JSON

Optional:
  --help, -h              Show this help message
EOF
    exit 1
}

# Parse arguments
GATE_RESULTS=""
JUDGE_RESULTS=""
OUTPUT=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --gate-results)
            GATE_RESULTS="$2"
            shift 2
            ;;
        --judge-results)
            JUDGE_RESULTS="$2"
            shift 2
            ;;
        --output)
            OUTPUT="$2"
            shift 2
            ;;
        -h|--help)
            usage
            ;;
        *)
            echo "Unknown argument: $1" >&2
            usage
            ;;
    esac
done

# Validate required arguments
if [[ -z "$GATE_RESULTS" ]] || [[ -z "$JUDGE_RESULTS" ]] || [[ -z "$OUTPUT" ]]; then
    echo "Error: Missing required arguments" >&2
    usage
fi

# Validate input files exist
if [[ ! -f "$GATE_RESULTS" ]]; then
    echo "Error: Gate results file '$GATE_RESULTS' does not exist" >&2
    exit 1
fi

if [[ ! -f "$JUDGE_RESULTS" ]]; then
    echo "Error: Judge results file '$JUDGE_RESULTS' does not exist" >&2
    exit 1
fi

# Validate input files are valid JSON
if ! jq . "$GATE_RESULTS" >/dev/null 2>&1; then
    echo "Error: Gate results file is not valid JSON" >&2
    exit 1
fi

if ! jq . "$JUDGE_RESULTS" >/dev/null 2>&1; then
    echo "Error: Judge results file is not valid JSON" >&2
    exit 1
fi

echo "Computing composite score..."

# Extract gate score (already normalized 0-1)
GATE_SCORE=$(jq -r '.gate_score // 0' "$GATE_RESULTS")

# Extract LLM judge scores (1-5 scale)
CORRECTNESS=$(jq -r '.correctness.score // 1' "$JUDGE_RESULTS")
CONVENTION=$(jq -r '.convention_adherence.score // 1' "$JUDGE_RESULTS")
TEST_QUALITY=$(jq -r '.test_quality.score // 1' "$JUDGE_RESULTS")
COMMIT_QUALITY=$(jq -r '.commit_quality.score // 1' "$JUDGE_RESULTS")
REVIEWER_READINESS=$(jq -r '.reviewer_readiness.score // 1' "$JUDGE_RESULTS")

# Extract metadata
SCENARIO=$(jq -r '.scenario // "unknown"' "$GATE_RESULTS")
VARIANT=$(jq -r '.variant // "unknown"' "$GATE_RESULTS")
TRIAL=$(jq -r '.trial // 1' "$GATE_RESULTS")
GATES_PASSED=$(jq -r '.gates_passed // 0' "$GATE_RESULTS")
GATES_APPLICABLE=$(jq -r '.gates_applicable // 0' "$GATE_RESULTS")

# Calculate LLM weighted average on original 1-5 scale:
#   weighted_avg = (correctness*0.15 + convention*0.10 + test*0.10 + commit*0.05 + reviewer*0.10) / 0.50
# Then normalize to 0-1: llm_normalized = weighted_avg / 5.0
LLM_WEIGHTED_SCORE=$(echo "scale=4; ($CORRECTNESS * 0.15 + $CONVENTION * 0.10 + $TEST_QUALITY * 0.10 + $COMMIT_QUALITY * 0.05 + $REVIEWER_READINESS * 0.10) / 0.50" | bc -l)
LLM_NORMALIZED=$(echo "scale=4; $LLM_WEIGHTED_SCORE / 5" | bc -l)

# Composite: 50% gate (0-1) + 50% LLM (0-1) → raw is 0-1
COMPOSITE_RAW=$(echo "scale=4; ($GATE_SCORE * 0.50) + ($LLM_NORMALIZED * 0.50)" | bc -l)

# Scale to 0-5
COMPOSITE_SCORE=$(echo "scale=2; $COMPOSITE_RAW * 5" | bc -l)

# Ensure score is within bounds (0-5)
COMPOSITE_SCORE=$(echo "scale=2; if ($COMPOSITE_SCORE < 0) 0 else if ($COMPOSITE_SCORE > 5) 5 else $COMPOSITE_SCORE" | bc -l)

# Generate timestamp
TIMESTAMP=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

# Write composite score JSON
cat > "$OUTPUT" << EOF
{
  "scenario": "$SCENARIO",
  "variant": "$VARIANT",
  "trial": $TRIAL,
  "timestamp": "$TIMESTAMP",
  "gate_results": {
    "score": $GATE_SCORE,
    "gates_passed": $GATES_PASSED,
    "gates_applicable": $GATES_APPLICABLE
  },
  "judge_results": {
    "correctness": $CORRECTNESS,
    "convention_adherence": $CONVENTION,
    "test_quality": $TEST_QUALITY,
    "commit_quality": $COMMIT_QUALITY,
    "reviewer_readiness": $REVIEWER_READINESS,
    "weighted_score": $LLM_WEIGHTED_SCORE
  },
  "composite": {
    "score": $COMPOSITE_SCORE,
    "raw_score": $COMPOSITE_RAW,
    "gate_weight": 0.50,
    "judge_weight": 0.50,
    "scale": "0-5"
  },
  "formula": {
    "description": "composite = (gate_score * 0.50) + (llm_weighted_score * 0.50) * 5",
    "gate_component": $(echo "scale=4; $GATE_SCORE * 0.50" | bc -l),
    "judge_component": $(echo "scale=4; $LLM_WEIGHTED_SCORE * 0.50" | bc -l)
  }
}
EOF

# Validate output is valid JSON
if ! jq . "$OUTPUT" >/dev/null 2>&1; then
    echo "Error: Generated composite score is not valid JSON" >&2
    exit 1
fi

echo "Composite score calculation complete"
echo "Gate score: $GATE_SCORE (weight: 50%)"
echo "LLM weighted score: $LLM_WEIGHTED_SCORE (weight: 50%)"
echo "Raw composite: $COMPOSITE_RAW"
echo "Final score (0-5 scale): $COMPOSITE_SCORE"
echo "Results saved to: $OUTPUT"
