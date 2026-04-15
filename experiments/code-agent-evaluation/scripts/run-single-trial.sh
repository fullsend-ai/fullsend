#!/bin/bash
set -euo pipefail

# Single trial runner for code agent evaluation
# Usage: run-single-trial.sh --scenario S01 --variant V1 --trial 3 --output-dir <path> [--judge-model MODEL]

# Default options
SCENARIO=""
VARIANT=""
TRIAL=""
OUTPUT_DIR=""
JUDGE_MODEL="claude-sonnet-4-6"

# Script directory (relative to this script)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "${SCRIPT_DIR}")"

usage() {
    cat <<EOF
Usage: $0 --scenario SCENARIO --variant VARIANT --trial TRIAL --output-dir OUTPUT_DIR [--judge-model MODEL]

Arguments:
  --scenario SCENARIO    Scenario ID (e.g., S01)
  --variant VARIANT      Variant ID (e.g., V1)
  --trial TRIAL          Trial number (e.g., 3)
  --output-dir OUTPUT_DIR Output directory for results
  --judge-model MODEL    Model for LLM judge (default: claude-sonnet-4-6)
  --help                 Show this help

Returns:
  Exit code 0 on success (regardless of agent success/failure)
  Exit code 1 only on safety violations or infrastructure errors
EOF
}

log() {
    echo "[$(date -Iseconds)] $*" >&2
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --scenario)
            SCENARIO="$2"
            shift 2
            ;;
        --variant)
            VARIANT="$2"
            shift 2
            ;;
        --trial)
            TRIAL="$2"
            shift 2
            ;;
        --output-dir)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        --judge-model)
            JUDGE_MODEL="$2"
            shift 2
            ;;
        --help)
            usage
            exit 0
            ;;
        *)
            echo "Unknown option: $1" >&2
            usage >&2
            exit 1
            ;;
    esac
done

# Validate required arguments
if [[ -z "${SCENARIO}" || -z "${VARIANT}" || -z "${TRIAL}" || -z "${OUTPUT_DIR}" ]]; then
    echo "Error: Missing required arguments" >&2
    usage >&2
    exit 1
fi

# Validate scenario format (S01-S20 for main/ablation, p01-p10 for security payloads)
if ! [[ "${SCENARIO}" =~ ^(S[0-9][0-9]|R[0-9][0-9]|p[0-9][0-9].*)$ ]]; then
    echo "Error: Scenario must be S01-S20, R01-R99, or a payload ID (p01-p10)" >&2
    exit 1
fi

# Validate variant format
if ! [[ "${VARIANT}" =~ ^(V[1-7]|A[1-7])$ ]]; then
    echo "Error: Variant must be V1-V7 or A1-A7" >&2
    exit 1
fi

# Validate trial number
if ! [[ "${TRIAL}" =~ ^[0-9]+$ ]] || [[ "${TRIAL}" -lt 1 ]]; then
    echo "Error: Trial must be a positive integer" >&2
    exit 1
fi

log "Starting trial: ${SCENARIO}/${VARIANT}/trial-${TRIAL}"

# Read scenario metadata
IS_PAYLOAD=false
if [[ "${SCENARIO}" =~ ^p[0-9][0-9] ]]; then
    IS_PAYLOAD=true
    PAYLOAD_FILE="${PROJECT_DIR}/payloads/${SCENARIO}.md"
    if [[ ! -f "${PAYLOAD_FILE}" ]]; then
        echo "Error: Payload file not found: ${PAYLOAD_FILE}" >&2
        exit 1
    fi
    REPO="ascerra/eval-hostile-target"
    SCENARIO_FILE="${PROJECT_DIR}/scenarios/S11.json"
    ISSUE_NUMBER=$(jq -r '.issue_number' "${SCENARIO_FILE}")
    log "Payload ${SCENARIO}: repo=${REPO}, using S11 issue=${ISSUE_NUMBER} as base"
else
    SCENARIO_FILE="${PROJECT_DIR}/scenarios/${SCENARIO}.json"
    if [[ ! -f "${SCENARIO_FILE}" ]]; then
        echo "Error: Scenario file not found: ${SCENARIO_FILE}" >&2
        exit 1
    fi

    REPO=$(jq -r '.repo' "${SCENARIO_FILE}")
    ISSUE_NUMBER=$(jq -r '.issue_number' "${SCENARIO_FILE}")

    if [[ "${REPO}" == "null" || "${ISSUE_NUMBER}" == "null" ]]; then
        echo "Error: Invalid scenario file: ${SCENARIO_FILE}" >&2
        exit 1
    fi

    log "Scenario ${SCENARIO}: repo=${REPO}, issue=${ISSUE_NUMBER}"
fi

SCENARIO_FILE="$(cd "$(dirname "${SCENARIO_FILE}")" && pwd)/$(basename "${SCENARIO_FILE}")"

# Create output directory (resolve to absolute path before cd-ing elsewhere)
mkdir -p "${OUTPUT_DIR}"
OUTPUT_DIR="$(cd "${OUTPUT_DIR}" && pwd)"

# Record start timestamp
START_TIME=$(date -Iseconds)
echo "{\"start_time\": \"${START_TIME}\"}" > "${OUTPUT_DIR}/metadata.json"

# Create fresh clone in /tmp
CLONE_DIR="/tmp/eval-${SCENARIO}-${VARIANT}-${TRIAL}-$$"
log "Cloning ${REPO} to ${CLONE_DIR}"

CLONE_OK=false
for attempt in 1 2 3 4 5; do
    if gh repo clone "${REPO}" "${CLONE_DIR}" 2>&1; then
        CLONE_OK=true
        break
    fi
    backoff=$((attempt * 15))
    log "Clone attempt ${attempt} failed, retrying in ${backoff}s..."
    rm -rf "${CLONE_DIR}" 2>/dev/null
    sleep "${backoff}"
done

if [[ "${CLONE_OK}" != "true" ]]; then
    echo "Error: Failed to clone repository ${REPO} after 5 attempts" >&2
    exit 1
fi

# Ensure we clean up on exit
cleanup() {
    local exit_code=$?
    if [[ -d "${CLONE_DIR}" ]]; then
        log "Cleaning up clone directory: ${CLONE_DIR}"
        rm -rf "${CLONE_DIR}"
    fi
    exit ${exit_code}
}
trap cleanup EXIT

# Detect the default branch (master vs main)
cd "${CLONE_DIR}"
DEFAULT_BRANCH="$(git symbolic-ref refs/remotes/origin/HEAD 2>/dev/null | sed 's|refs/remotes/origin/||')"
if [[ -z "$DEFAULT_BRANCH" ]]; then
    if git rev-parse --verify origin/main &>/dev/null; then
        DEFAULT_BRANCH="main"
    elif git rev-parse --verify origin/master &>/dev/null; then
        DEFAULT_BRANCH="master"
    else
        DEFAULT_BRANCH="main"
    fi
fi

# Set up environment variables for agent and gate checker
export ISSUE_NUMBER="${ISSUE_NUMBER}"
export BRANCH_NAME="agent/${ISSUE_NUMBER}-fix"
export TARGET_BRANCH="${DEFAULT_BRANCH}"
export SCAN_SECRETS="true"
export VARIANT="${VARIANT}"
export TRIAL="${TRIAL}"
export ISSUE_URL="https://github.com/${REPO}/issues/${ISSUE_NUMBER}"

log "Invoking variant ${VARIANT} in ${CLONE_DIR}"

# Snapshot remote refs before agent runs (for push detection)
REFS_BEFORE="$(cd "${CLONE_DIR}" && git ls-remote origin 2>/dev/null | sort)"

# Run the variant-specific agent invocation
set +e
"${SCRIPT_DIR}/invoke-variant.sh" --variant "${VARIANT}" --clone-dir "${CLONE_DIR}" --issue-url "${ISSUE_URL}" --output-file "${OUTPUT_DIR}/transcript.txt"
AGENT_EXIT_CODE=$?
set -e

if [[ "${AGENT_EXIT_CODE}" -ne 0 ]]; then
    log "Agent exited with code ${AGENT_EXIT_CODE}"
fi

# Record end timestamp
END_TIME=$(date -Iseconds)
jq --argjson agent_exit_code "${AGENT_EXIT_CODE}" \
   --arg end_time "${END_TIME}" \
   '. + {"agent_exit_code": $agent_exit_code, "end_time": $end_time}' \
   "${OUTPUT_DIR}/metadata.json" > "${OUTPUT_DIR}/metadata.json.tmp"
mv "${OUTPUT_DIR}/metadata.json.tmp" "${OUTPUT_DIR}/metadata.json"

# Safety checks: verify no push occurred by comparing remote refs
cd "${CLONE_DIR}"
REFS_AFTER="$(git ls-remote origin 2>/dev/null | sort)"
if [[ "${REFS_BEFORE}" != "${REFS_AFTER}" ]]; then
    echo "SAFETY VIOLATION: Remote refs changed — agent may have pushed!" >&2
    echo "{\"safety_violation\": \"push_detected\"}" > "${OUTPUT_DIR}/safety-violation.json"
    exit 1
fi

# Check for any suspicious network activity or file operations
if [[ -f "${OUTPUT_DIR}/transcript.txt" ]]; then
    if grep -qE "(git push|gh pr create|curl.*api|wget|scp)" "${OUTPUT_DIR}/transcript.txt" 2>/dev/null; then
        log "Warning: Potentially unsafe network operations detected in transcript"
    fi
fi

log "Running deterministic gates checker"
# Run deterministic gates
if ! "${SCRIPT_DIR}/check-deterministic.sh" --clone-dir "${CLONE_DIR}" --ground-truth "${SCENARIO_FILE}" --output "${OUTPUT_DIR}/gates.json" --transcript "${OUTPUT_DIR}/transcript.txt"; then
    log "Warning: Deterministic gates checker failed"
    # Don't exit - this is not a safety violation, just a gate failure
fi

log "Running LLM judge with model ${JUDGE_MODEL}"
# Generate diff for judge
cd "${CLONE_DIR}"
# Show all changes vs the base branch (committed + uncommitted)
{
    git diff origin/${DEFAULT_BRANCH}..HEAD 2>/dev/null || true
    git diff HEAD 2>/dev/null || true
} > "${OUTPUT_DIR}/git-diff.txt"

# Run LLM judge
if ! "${SCRIPT_DIR}/judge.sh" --scenario-file "${SCENARIO_FILE}" --diff-file "${OUTPUT_DIR}/git-diff.txt" --transcript-file "${OUTPUT_DIR}/transcript.txt" --gate-results "${OUTPUT_DIR}/gates.json" --issue-url "${ISSUE_URL}" --output "${OUTPUT_DIR}/judge-assessment.json"; then
    log "Warning: LLM judge failed"
    # Don't exit - this is not a safety violation, just a judge failure
fi

# Compute composite score from gates + judge
if [[ -f "${OUTPUT_DIR}/gates.json" ]] && [[ -f "${OUTPUT_DIR}/judge-assessment.json" ]]; then
    if ! "${SCRIPT_DIR}/score.sh" --gate-results "${OUTPUT_DIR}/gates.json" --judge-results "${OUTPUT_DIR}/judge-assessment.json" --output "${OUTPUT_DIR}/composite-score.json"; then
        log "Warning: Composite score computation failed"
    fi
fi

# Copy agent artifacts to output directory for analysis
if [[ -f "${CLONE_DIR}/.claude/transcript.txt" ]]; then
    cp "${CLONE_DIR}/.claude/transcript.txt" "${OUTPUT_DIR}/transcript.txt" 2>/dev/null || true
fi

if [[ -d "${CLONE_DIR}/.claude/memory" ]]; then
    cp -r "${CLONE_DIR}/.claude/memory" "${OUTPUT_DIR}/memory" 2>/dev/null || true
fi

# Save git status and log for analysis (diff already generated for judge)
cd "${CLONE_DIR}"
git status --porcelain > "${OUTPUT_DIR}/git-status.txt" 2>/dev/null || true
git log --oneline -10 > "${OUTPUT_DIR}/git-log.txt" 2>/dev/null || true

# Calculate metrics from transcript
TRANSCRIPT_LENGTH=0
TOOL_CALL_COUNT=0

if [[ -f "${OUTPUT_DIR}/transcript.txt" ]]; then
    TRANSCRIPT_LENGTH=$(wc -c < "${OUTPUT_DIR}/transcript.txt" 2>/dev/null | tr -d '[:space:]')
    TOOL_CALL_COUNT=$(grep -c '<function_calls>\|<invoke name=' "${OUTPUT_DIR}/transcript.txt" 2>/dev/null | tr -d '[:space:]' || true)
fi
TRANSCRIPT_LENGTH="${TRANSCRIPT_LENGTH:-0}"
TOOL_CALL_COUNT="${TOOL_CALL_COUNT:-0}"

# Create final results summary
jq -n \
  --arg scenario "${SCENARIO}" \
  --arg variant "${VARIANT}" \
  --argjson trial "${TRIAL}" \
  --arg repo "${REPO}" \
  --argjson issue_number "${ISSUE_NUMBER}" \
  --arg start_time "${START_TIME}" \
  --arg end_time "${END_TIME}" \
  --argjson agent_exit_code "${AGENT_EXIT_CODE}" \
  --argjson transcript_length "${TRANSCRIPT_LENGTH}" \
  --argjson tool_call_count "${TOOL_CALL_COUNT}" \
  '{
    "scenario": $scenario,
    "variant": $variant,
    "trial": $trial,
    "repo": $repo,
    "issue_number": $issue_number,
    "start_time": $start_time,
    "end_time": $end_time,
    "agent_exit_code": $agent_exit_code,
    "transcript_length": $transcript_length,
    "tool_call_count": $tool_call_count,
    "completed": true
  }' > "${OUTPUT_DIR}/summary.json"

log "Trial completed successfully: ${SCENARIO}/${VARIANT}/trial-${TRIAL}"
exit 0
