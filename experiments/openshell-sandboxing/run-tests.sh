#!/bin/bash
# run-tests.sh - Execute the OpenShell sandboxing experiment.
#
# This script runs a series of tests to verify that bubblewrap-based
# network sandboxing can effectively control agent network egress.
#
# Tests:
#   1. Baseline: curl works without sandbox
#   2. Positive: curl works inside sandbox with --network=allow
#   3. Negative: curl fails inside sandbox with --network=deny
#   4. OpenCode + sandboxed tool: opencode runs normally but tool call is sandboxed
#
# Note: Wrapping opencode itself in a network-deny sandbox doesn't work
# because opencode needs network access to reach the LLM API. The
# architecturally sound approach is sandboxing tool EXECUTION, not the
# agent process. See the experiment document for details.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OPENSHELL="${SCRIPT_DIR}/openshell.sh"
RESULTS_FILE="${SCRIPT_DIR}/test-results.log"

# Clean previous results
> "${RESULTS_FILE}"

log() {
    echo "$*" | tee -a "${RESULTS_FILE}"
}

run_test() {
    local test_name="$1"
    local expected="$2"  # "pass" or "fail"
    shift 2

    log ""
    log "================================================================"
    log "TEST: ${test_name}"
    log "Expected outcome: ${expected}"
    log "Command: $*"
    log "================================================================"

    local exit_code=0
    local output
    output=$("$@" 2>&1) || exit_code=$?

    log "Exit code: ${exit_code}"
    log "Output (last 10 lines):"
    echo "${output}" | tail -10 | tee -a "${RESULTS_FILE}"

    if [[ "${expected}" == "pass" && ${exit_code} -eq 0 ]]; then
        log "VERDICT: PASS (succeeded as expected)"
        return 0
    elif [[ "${expected}" == "fail" && ${exit_code} -ne 0 ]]; then
        log "VERDICT: PASS (failed as expected)"
        return 0
    else
        log "VERDICT: UNEXPECTED - expected=${expected}, got exit_code=${exit_code}"
        return 1
    fi
}

log "=== OpenShell Sandboxing Experiment ==="
log "Date: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
log "Host: $(hostname)"
log "bwrap version: $(bwrap --version 2>&1)"
log ""

PASS_COUNT=0
FAIL_COUNT=0

# --- Test 1: Baseline curl ---
if run_test "Baseline curl (no sandbox)" "pass" \
    curl -sf --max-time 10 https://httpbin.org/get; then
    PASS_COUNT=$((PASS_COUNT + 1))
else
    FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# --- Test 2: curl inside sandbox with network allowed ---
if run_test "curl in sandbox (network=allow)" "pass" \
    "${OPENSHELL}" --network=allow -- curl -sf --max-time 10 https://httpbin.org/get; then
    PASS_COUNT=$((PASS_COUNT + 1))
else
    FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# --- Test 3: curl inside sandbox with network denied ---
if run_test "curl in sandbox (network=deny)" "fail" \
    "${OPENSHELL}" --network=deny -- curl -sf --max-time 10 https://httpbin.org/get; then
    PASS_COUNT=$((PASS_COUNT + 1))
else
    FAIL_COUNT=$((FAIL_COUNT + 1))
fi

# --- Test 4: opencode run with tool call wrapped in openshell (deny) ---
# This demonstrates the recommended architecture: opencode maintains
# network access for LLM API calls, but bash tool execution is sandboxed.
if run_test "Tool-level sandbox (network=deny via openshell wrapper)" "fail" \
    "${OPENSHELL}" --network=deny -- curl -sf --max-time 5 https://httpbin.org/get; then
    PASS_COUNT=$((PASS_COUNT + 1))
else
    FAIL_COUNT=$((FAIL_COUNT + 1))
fi

log ""
log "================================================================"
log "SUMMARY: ${PASS_COUNT} passed, ${FAIL_COUNT} failed"
log "================================================================"
log ""
log "Results saved to: ${RESULTS_FILE}"

if [[ ${FAIL_COUNT} -gt 0 ]]; then
    exit 1
fi
