#!/usr/bin/env bash
# run-with-infra-retry.sh — Run a command, retrying it once when the behaviour
# suite's failure-class report classifies the run's failure as infrastructure.
#
# The wrapped suite writes failure-class.json at completion (see
# pkg/behaviourtest/report.go for the report shape and its destinations). This
# wrapper reads the report's class after a failed attempt: only class
# "infrastructure" on attempt 1 earns exactly one retry. Scenario failures, a
# missing report (crash, cancellation, death before the report write), and an
# unparseable report are all unclassified and fail closed — the failure
# surfaces immediately, no retry. The report alone cannot authorize a retry:
# attempt 1's captured output is also checked for fatal patterns (a data
# race, a panic, or a test timeout), and any of those refuses the retry even
# when the report says infrastructure, because a green second attempt must
# never mask a product defect.
#
# The retry reruns only the scenarios attempt 1 recorded as failed: attempt
# 2 gets BEHAVIOUR_RETRY_FROM pointing at attempt 1's report, and the suite
# selects those scenarios from it (pkg/behaviourtest/retryselect). A full
# rerun takes as long as attempt 1 and does not fit the merge queue's check
# window.
#
# INFRA_RETRY_MAX_ATTEMPT1_SECONDS, when set, refuses the retry if attempt 1
# ran longer than that many seconds, so a retry cannot start too late to
# finish, write its report, and release its repos before the job's
# timeout-minutes.
#
# Usage: run-with-infra-retry.sh <command> [args...]
# Example: ./scripts/run-with-infra-retry.sh go test -tags behaviour ./e2e/behaviour/

set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: ${0} <command> [args...]" >&2
  exit 2
fi

max_attempt1_seconds="${INFRA_RETRY_MAX_ATTEMPT1_SECONDS:-}"
if [[ -n "${max_attempt1_seconds}" && ! "${max_attempt1_seconds}" =~ ^[0-9]+$ ]]; then
  echo "${0}: INFRA_RETRY_MAX_ATTEMPT1_SECONDS must be a whole number of seconds, got '${max_attempt1_seconds}'" >&2
  exit 2
fi

# Attempt 1 always runs the whole suite: a selection left in the caller's
# environment must not narrow it.
unset BEHAVIOUR_RETRY_FROM

# Marker resolution mirrors the report writer's destinations: CI sets
# BEHAVIOUR_ARTIFACT_DIR so the report lands with the uploaded artifacts;
# local runs get a private temp file via BEHAVIOUR_FAILURE_CLASS_FILE without
# flipping the suite's "artifact dir unset => skip debug artifacts" behavior.
if [[ -n "${BEHAVIOUR_ARTIFACT_DIR:-}" ]]; then
  marker="${BEHAVIOUR_ARTIFACT_DIR}/failure-class.json"
  # A report left by an earlier run must never authorize a retry. The suite
  # writes a fresh report at completion, but delete any stale marker first.
  rm -f -- "${marker}"
else
  BEHAVIOUR_FAILURE_CLASS_FILE="$(mktemp)"
  # mktemp's permission defaults are platform-dependent and the report may
  # carry failure messages, so pin the marker to owner-only (the Go writer
  # also writes mode 0o600 via temp-file rename). The fresh empty marker is
  # not stale: an empty file reads as unclassified, which fails closed.
  chmod 600 "${BEHAVIOUR_FAILURE_CLASS_FILE}"
  export BEHAVIOUR_FAILURE_CLASS_FILE
  marker="${BEHAVIOUR_FAILURE_CLASS_FILE}"
fi

# Attempt 1's combined output, tee'd to a temp file so the retry decision can
# check it for fatal patterns a dying process cannot report on its own.
OUTPUT_FILE="$(mktemp)"
trap 'rm -f -- "${OUTPUT_FILE}"' EXIT

# Fatal output patterns that forbid a retry regardless of the marker's
# class: the race detector, a panic, or the go test hard timeout. A report
# written before the process died cannot make these retryable.
fatal_patterns=("WARNING: DATA RACE" "panic: " "test timed out")

# output_matches_fatal_pattern reports whether attempt 1's captured output
# contains any fatal pattern.
output_matches_fatal_pattern() {
  local pattern
  for pattern in "${fatal_patterns[@]}"; do
    if grep -F -q -- "${pattern}" "${OUTPUT_FILE}"; then
      return 0
    fi
  done
  return 1
}

# Attempt 1, captured through a single tee so the retry decision can inspect
# the combined output for fatal patterns. One tee means one writer: two tee
# process substitutions on the same file would each open it O_TRUNC and write
# at independent offsets, silently clobbering each other's bytes, and every
# fatal pattern arrives on stderr, the lower-volume stream a verbose run's
# stdout would overwrite. The foreground pipeline is also the barrier: bash
# waits for both the command and the tee before the pattern grep runs. `2>&1`
# merges stderr into the captured stream. PIPESTATUS keeps the command's exit
# status and tee's apart: under pipefail a failing tee would replace the
# command's status in the pipeline's. errexit is off for the pipeline so the
# command's failure reaches the checks below.
attempt1_start="${SECONDS}"
set +e
"$@" 2>&1 | tee "${OUTPUT_FILE}"
pipe_status=("${PIPESTATUS[@]}")
set -e
status="${pipe_status[0]}"
tee_status="${pipe_status[1]}"
attempt1_seconds=$((SECONDS - attempt1_start))

if [[ "${status}" -eq 0 ]]; then
  exit 0
fi

# A failed capture can't show that the output is free of fatal patterns, so
# it fails closed like a missing report.
if [[ "${tee_status}" -ne 0 ]]; then
  echo "[infra-retry] capturing attempt 1 output failed (tee status ${tee_status}); not retrying" >&2
  exit "${status}"
fi

# A fatal pattern in attempt 1's output forbids the retry outright, before
# any marker is consulted: a race, panic, or timeout is a product defect, and
# a green second attempt must not mask it.
if output_matches_fatal_pattern; then
  echo "[infra-retry] attempt 1 output contains a fatal pattern (race, panic, or timeout); not retrying" >&2
  exit "${status}"
fi

# Read the marker's class. Missing or unparseable means unclassified, which
# fails closed (no retry); per docs/contributing/shell-scripting.md the jq
# failure is surfaced with a warning instead of silently swallowed.
class=""
if [[ -f "${marker}" ]]; then
  if ! class="$(jq -r '.class // empty' "${marker}" 2>&1)"; then
    echo "[infra-retry] unparseable failure-class report (${marker}): ${class}" >&2
    class=""
  fi
else
  echo "[infra-retry] no failure-class report at ${marker}; not retrying" >&2
fi

if [[ "${class}" != "infrastructure" ]]; then
  exit "${status}"
fi

if [[ -n "${max_attempt1_seconds}" && "${attempt1_seconds}" -gt "${max_attempt1_seconds}" ]]; then
  echo "[infra-retry] attempt 1 classified as infrastructure but took ${attempt1_seconds}s, over INFRA_RETRY_MAX_ATTEMPT1_SECONDS=${max_attempt1_seconds}; not retrying" >&2
  exit "${status}"
fi

echo "[infra-retry] attempt 1 classified as infrastructure (report: ${marker}); retrying once, failed scenarios only" >&2

# Attempt 1's report moves aside so the active report only ever describes the
# final attempt: a retry that dies before writing its own leaves an empty
# report, which reads as unclassified. Truncating rather than deleting keeps
# the mktemp'd local marker's mode 600. If the copy fails, the report stays
# in place, since truncating it would lose attempt 1's classification and
# the failed scenarios attempt 2 needs. Attempt 2 reads it at startup and
# overwrites it on completion.
retry_from="${marker}.attempt-1"
if cp -p -- "${marker}" "${retry_from}"; then
  : >"${marker}"
else
  echo "[infra-retry] could not keep attempt 1's report at ${retry_from}; attempt 2 reads it from ${marker}" >&2
  retry_from="${marker}"
fi

# Attempt 2 — the only retry. The final exit status is attempt 2's. The
# mktemp'd local marker is deliberately left in place after exit: the suite's
# failure message references the report path, so it stays resolvable for
# post-mortem inspection.
status=0
BEHAVIOUR_RETRY_FROM="${retry_from}" "$@" || status=$?
exit "${status}"
