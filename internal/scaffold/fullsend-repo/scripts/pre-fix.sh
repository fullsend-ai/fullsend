#!/usr/bin/env bash
# Pre-script: validate workflow_dispatch inputs before the fix agent runs.
#
# Prevents malformed or malicious event_payload from reaching the sandbox.
# Also enforces the iteration cap — blocks the run if too many fix cycles
# have already occurred on this PR.
#
# Required environment variables (set by the workflow):
#   PR_NUMBER          — must be a positive integer
#   REPO_FULL_NAME     — must be owner/repo format
#   TRIGGER_SOURCE     — GitHub username that triggered the fix (usernames ending in [bot] are bot triggers)
#
# Optional environment variables:
#   FIX_ITERATION      — current iteration count (default: 1)
#   ITERATION_CAP      — max bot-triggered iterations (default: 5)
#   ITERATION_CAP_HUMAN — max human-triggered iterations (default: 10)
#   HUMAN_INSTRUCTION  — instruction text (only when TRIGGER_SOURCE doesn't end in [bot])
set -euo pipefail

# ---------------------------------------------------------------------------
# Helper: Bot user detection
# ---------------------------------------------------------------------------
is_bot_user() {
  [[ "${1:-}" =~ \[bot\]$ ]]
}

errors=0

# ---------------------------------------------------------------------------
# Input validation
# ---------------------------------------------------------------------------
if [[ ! "${PR_NUMBER:-}" =~ ^[1-9][0-9]*$ ]]; then
  echo "::error::PR_NUMBER must be a positive integer, got: '${PR_NUMBER:-}'"
  errors=$((errors + 1))
fi

if [[ ! "${REPO_FULL_NAME:-}" =~ ^[a-zA-Z0-9._-]+/[a-zA-Z0-9._-]+$ ]]; then
  echo "::error::REPO_FULL_NAME must be owner/repo format, got: '${REPO_FULL_NAME:-}'"
  errors=$((errors + 1))
fi

if [[ -z "${TRIGGER_SOURCE:-}" ]]; then
  echo "::error::TRIGGER_SOURCE is required (GitHub username that triggered the fix)"
  errors=$((errors + 1))
fi

if [[ "${errors}" -gt 0 ]]; then
  echo "::error::Input validation failed with ${errors} error(s). Aborting."
  exit 1
fi

# ---------------------------------------------------------------------------
# Human instruction length cap (defense against DoS via oversized input)
# ---------------------------------------------------------------------------
MAX_INSTRUCTION_BYTES=10000
if ! is_bot_user "${TRIGGER_SOURCE}" && [[ -n "${HUMAN_INSTRUCTION:-}" ]]; then
  INSTRUCTION_LEN="${#HUMAN_INSTRUCTION}"
  if [[ "${INSTRUCTION_LEN}" -gt "${MAX_INSTRUCTION_BYTES}" ]]; then
    echo "::error::HUMAN_INSTRUCTION is ${INSTRUCTION_LEN} bytes (max: ${MAX_INSTRUCTION_BYTES}). Truncate the instruction."
    exit 1
  fi
fi

# ---------------------------------------------------------------------------
# Iteration cap check
# ---------------------------------------------------------------------------
ITERATION="${FIX_ITERATION:-1}"
BOT_CAP="${ITERATION_CAP:-5}"
HUMAN_CAP="${ITERATION_CAP_HUMAN:-10}"

if is_bot_user "${TRIGGER_SOURCE}"; then
  CAP="${BOT_CAP}"
else
  CAP="${HUMAN_CAP}"
fi

if [[ "${ITERATION}" -gt "${CAP}" ]]; then
  if is_bot_user "${TRIGGER_SOURCE}"; then
    echo "::error::Fix iteration ${ITERATION} exceeds bot cap of ${CAP}. Escalating to human."
    echo "::error::The review→fix loop has run ${ITERATION} times without converging."
    echo "::error::A human can still direct the agent with /fix (up to ${HUMAN_CAP} total iterations)."
  else
    echo "::error::Fix iteration ${ITERATION} exceeds human cap of ${CAP}."
    echo "::error::The /fix loop has run ${ITERATION} times. Further attempts are blocked."
  fi
  exit 1
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo "Input validation passed:"
echo "  PR_NUMBER=${PR_NUMBER}"
echo "  REPO_FULL_NAME=${REPO_FULL_NAME}"
echo "  TRIGGER_SOURCE=${TRIGGER_SOURCE}"
echo "  FIX_ITERATION=${ITERATION} of ${CAP}"
if ! is_bot_user "${TRIGGER_SOURCE}" && [[ -n "${HUMAN_INSTRUCTION:-}" ]]; then
  # Truncate instruction in logs to avoid leaking long user input.
  INSTR_PREVIEW="${HUMAN_INSTRUCTION:0:200}"
  echo "  HUMAN_INSTRUCTION=${INSTR_PREVIEW}..."
fi
