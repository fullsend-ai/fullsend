#!/bin/bash
set -euo pipefail

# LLM Judge - Evaluates code agent implementation quality
#
# Usage: judge.sh --scenario-file <path> --diff-file <path> --transcript-file <path>
#                --gate-results <path> --issue-url <url> --output <path>
#
# Arguments:
#   --scenario-file:  Path to scenario ground truth JSON (e.g., scenarios/S01.json)
#   --diff-file:      Path to agent's diff output (git diff)
#   --transcript-file: Path to agent's transcript/log
#   --gate-results:   Path to deterministic gate results JSON
#   --issue-url:      GitHub issue URL
#   --output:         Output path for judge assessment JSON

usage() {
    echo "Usage: $0 --scenario-file <path> --diff-file <path> --transcript-file <path> --gate-results <path> --issue-url <url> --output <path>"
    echo "       $0 --help"
    exit 1
}

# Parse arguments
SCENARIO_FILE=""
DIFF_FILE=""
TRANSCRIPT_FILE=""
GATE_RESULTS=""
ISSUE_URL=""
OUTPUT=""

while [[ $# -gt 0 ]]; do
    case $1 in
        --scenario-file)
            SCENARIO_FILE="$2"
            shift 2
            ;;
        --diff-file)
            DIFF_FILE="$2"
            shift 2
            ;;
        --transcript-file)
            TRANSCRIPT_FILE="$2"
            shift 2
            ;;
        --gate-results)
            GATE_RESULTS="$2"
            shift 2
            ;;
        --issue-url)
            ISSUE_URL="$2"
            shift 2
            ;;
        --output)
            OUTPUT="$2"
            shift 2
            ;;
        --help|-h)
            usage
            ;;
        *)
            echo "Unknown option: $1"
            usage
            ;;
    esac
done

# Validate required arguments
if [[ -z "${SCENARIO_FILE}" || -z "${DIFF_FILE}" || -z "${TRANSCRIPT_FILE}" || -z "${GATE_RESULTS}" || -z "${ISSUE_URL}" || -z "${OUTPUT}" ]]; then
    echo "Error: All arguments are required"
    usage
fi

# Validate input files exist
for file in "${SCENARIO_FILE}" "${DIFF_FILE}" "${TRANSCRIPT_FILE}" "${GATE_RESULTS}"; do
    if [[ ! -f "${file}" ]]; then
        echo "Error: File does not exist: ${file}"
        exit 1
    fi
done

# Determine script directory for relative path resolution
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "${SCRIPT_DIR}")"
JUDGE_SYSTEM_PROMPT="${PROJECT_ROOT}/prompts/judge-system.md"

if [[ ! -f "${JUDGE_SYSTEM_PROMPT}" ]]; then
    echo "Error: Judge system prompt not found: ${JUDGE_SYSTEM_PROMPT}"
    exit 1
fi

# Read input files
SCENARIO_JSON=$(cat "${SCENARIO_FILE}")
DIFF_CONTENT=$(cat "${DIFF_FILE}")
TRANSCRIPT_CONTENT=$(cat "${TRANSCRIPT_FILE}")
GATE_RESULTS_JSON=$(cat "${GATE_RESULTS}")

# Fetch issue description from GitHub
ISSUE_DESCRIPTION=""
if command -v gh >/dev/null 2>&1; then
    # Extract repo and issue number from URL
    if [[ "${ISSUE_URL}" =~ github\.com/([^/]+/[^/]+)/issues/([0-9]+) ]]; then
        REPO="${BASH_REMATCH[1]}"
        ISSUE_NUM="${BASH_REMATCH[2]}"
        ISSUE_DESCRIPTION=$(gh issue view "${ISSUE_NUM}" --repo "${REPO}" --json body -q .body 2>/dev/null || echo "Could not fetch issue description")
    else
        ISSUE_DESCRIPTION="Invalid issue URL format"
    fi
else
    ISSUE_DESCRIPTION="gh CLI not available"
fi

# Create temporary file for composed prompt
TEMP_PROMPT=$(mktemp)
trap 'rm -f "${TEMP_PROMPT}"' EXIT

# Compose the full prompt with context
cat > "${TEMP_PROMPT}" << EOF
$(cat "${JUDGE_SYSTEM_PROMPT}")

---

## Context for this evaluation

**Issue URL:** ${ISSUE_URL}

**Original Issue Description:**
\`\`\`
${ISSUE_DESCRIPTION}
\`\`\`

**Ground Truth (Expected Fix):**
\`\`\`json
${SCENARIO_JSON}
\`\`\`

**Agent's Actual Diff:**
\`\`\`diff
${DIFF_CONTENT}
\`\`\`

**Agent's Transcript (reasoning and actions):**
\`\`\`
${TRANSCRIPT_CONTENT}
\`\`\`

**Deterministic Gate Results:**
\`\`\`json
${GATE_RESULTS_JSON}
\`\`\`

---

Based on the above context, please evaluate the agent's implementation and provide your assessment as JSON.
EOF

# Invoke Claude judge with sonnet model
echo "Running LLM judge evaluation..."
if ! claude -p --model claude-sonnet-4-6 --max-turns 5 < "${TEMP_PROMPT}" > "${OUTPUT}" 2>/dev/null; then
    echo "Error: Claude judge invocation failed"
    exit 1
fi

# Strip markdown code fences if present (Claude often wraps JSON in ```json ... ```)
if grep -q '```' "${OUTPUT}" 2>/dev/null; then
    sed -n '/^```json\s*$/,/^```\s*$/{/^```/d;p}' "${OUTPUT}" > "${OUTPUT}.stripped"
    if [[ -s "${OUTPUT}.stripped" ]]; then
        mv "${OUTPUT}.stripped" "${OUTPUT}"
    else
        rm -f "${OUTPUT}.stripped"
    fi
fi

# Validate output is valid JSON
if ! jq . "${OUTPUT}" >/dev/null 2>&1; then
    echo "Error: Judge output is not valid JSON"
    echo "Raw output:"
    cat "${OUTPUT}"
    exit 1
fi

echo "Judge assessment saved to: ${OUTPUT}"

# Extract summary scores for quick reference
CORRECTNESS=$(jq -r '.correctness.score // "N/A"' "${OUTPUT}")
CONVENTION=$(jq -r '.convention_adherence.score // "N/A"' "${OUTPUT}")
TEST_QUALITY=$(jq -r '.test_quality.score // "N/A"' "${OUTPUT}")
COMMIT_QUALITY=$(jq -r '.commit_quality.score // "N/A"' "${OUTPUT}")
REVIEWER_READY=$(jq -r '.reviewer_readiness.score // "N/A"' "${OUTPUT}")

echo "Summary scores - Correctness: ${CORRECTNESS}, Convention: ${CONVENTION}, Tests: ${TEST_QUALITY}, Commit: ${COMMIT_QUALITY}, Review Ready: ${REVIEWER_READY}"
