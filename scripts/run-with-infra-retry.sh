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
# surfaces immediately, no retry.
#
# Usage: run-with-infra-retry.sh <command> [args...]
# Example: ./scripts/run-with-infra-retry.sh go test -tags behaviour ./e2e/behaviour/

set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: ${0} <command> [args...]" >&2
  exit 2
fi

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

# Attempt 1. `|| status=$?` keeps set -e from aborting the script on the
# command's failure while still capturing its exit status (an `if` guard would
# reset $? to the if-statement's own status).
status=0
"$@" || status=$?

if [[ "${status}" -eq 0 ]]; then
  exit 0
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

echo "[infra-retry] attempt 1 classified as infrastructure (report: ${marker}); retrying once" >&2

# Attempt 2 — the only retry. The final exit status is attempt 2's. The
# mktemp'd local marker is deliberately left in place after exit: the suite's
# failure message references the report path, so it stays resolvable for
# post-mortem inspection.
status=0
"$@" || status=$?
exit "${status}"
