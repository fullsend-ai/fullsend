#!/usr/bin/env bash
# route-review-model-test.sh — Tests for route-review-model.sh.
#
# `gh` is stubbed so the script's real logic runs against a controlled file
# list. The classifier is exercised through the script itself rather than a
# copy, so the tests cannot drift from what ships.

set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/route-review-model.sh"

FAILURES=0
TMPROOT="$(mktemp -d)"
trap 'rm -rf "${TMPROOT}"' EXIT

# run <files-json-lines> [env assignments...] -> echoes the FULLSEND_MODEL
# line the script exported, or "" when it left the model alone.
run() {
  local files="$1"
  shift
  local dir
  dir="$(mktemp -d "${TMPROOT}/case.XXXXXX")"

  # Stub gh: prints the supplied file list, ignores its arguments.
  printf '#!/usr/bin/env bash\ncat <<'\''PAYLOAD'\''\n%s\nPAYLOAD\n' "${files}" >"${dir}/gh"
  chmod +x "${dir}/gh"

  : >"${dir}/env"
  ( PATH="${dir}:${PATH}" \
    GH_TOKEN=x SOURCE_REPO=o/r PR_NUMBER=1 \
    GITHUB_ENV="${dir}/env" GITHUB_STEP_SUMMARY="${dir}/summary" \
    env "$@" bash "${SCRIPT}" >"${dir}/out" 2>&1 )
  local rc=$?
  if [[ ${rc} -ne 0 ]]; then
    echo "SCRIPT_EXIT_${rc}"
    return
  fi
  grep '^FULLSEND_MODEL=' "${dir}/env" 2>/dev/null | tail -1 || true
}

check() {
  local name="$1" expected="$2" actual="$3"
  if [[ "${actual}" == "${expected}" ]]; then
    echo "PASS: ${name}"
  else
    echo "FAIL: ${name}"
    echo "      expected: '${expected}'"
    echo "      actual:   '${actual}'"
    FAILURES=$((FAILURES + 1))
  fi
}

DATA='{"filename":"versions.json","status":"modified","changes":2}'
CODE='{"filename":"internal/cli/run.go","status":"modified","changes":2}'
WORKFLOW='{"filename":".github/workflows/ci.yml","status":"modified","changes":2}'
LOCKFILE='{"filename":"package-lock.json","status":"modified","changes":2}'
ADDED='{"filename":"config/new.yaml","status":"added","changes":2}'
RENAMED='{"filename":"config/old.yaml","status":"renamed","changes":2}'
SKILL='{"filename":"skills/pr-review/SKILL.md","status":"modified","changes":2}'
BIG='{"filename":"versions.json","status":"modified","changes":40}'

# The case from #5777: a one-line versions.json bump that cost $0.40 on Opus.
check "trivial data edit routes to the cheap model" \
  "FULLSEND_MODEL=sonnet" "$(run "${DATA}")"

# Code is never trivial, however small.
check "code is never trivial" "" "$(run "${CODE}")"

# A small YAML edit under .github/ is a CI change, not a value edit.
check "workflow files are not trivial" "" "$(run "${WORKFLOW}")"

# npm resolves from the lockfile: a one-line integrity swap is a
# supply-chain change wearing a trivial diff.
check "lockfiles are not trivial" "" "$(run "${LOCKFILE}")"

# Structural changes are not value edits even when they are small.
check "an added file is not trivial" "" "$(run "${ADDED}")"
check "a renamed file is not trivial" "" "$(run "${RENAMED}")"

# Agent-behaviour markdown is executable instruction, not prose data.
check "skills markdown is not trivial" "" "$(run "${SKILL}")"

# One offending file poisons an otherwise trivial diff.
check "a mixed diff is not trivial" "" "$(run "${DATA}
${CODE}")"

# Size gate.
check "a large data edit is not trivial" "" "$(run "${BIG}")"
check "the threshold is configurable upward" \
  "FULLSEND_MODEL=sonnet" "$(run "${BIG}" TRIVIAL_MAX_LINES=100)"

# Several small data files still add up.
check "line counts accumulate across files" "" \
  "$(run '{"filename":"a.yaml","status":"modified","changes":6}
{"filename":"b.yaml","status":"modified","changes":6}')"

# Opt-out and model selection.
check "routing can be disabled" "" "$(run "${DATA}" TRIVIAL_MODEL=off)"
check "the target model is configurable" \
  "FULLSEND_MODEL=haiku" "$(run "${DATA}" TRIVIAL_MODEL=haiku)"

# Never fail the review over cost routing: a malformed threshold, an empty
# diff, or an unreadable file list all leave the harness model standing.
check "a non-numeric threshold is ignored" "" "$(run "${DATA}" TRIVIAL_MAX_LINES=abc)"
check "an empty file list changes nothing" "" "$(run "")"
check "a missing PR number changes nothing" "" "$(run "${DATA}" PR_NUMBER=)"

echo
if [[ ${FAILURES} -eq 0 ]]; then
  echo "All route-review-model tests passed"
else
  echo "${FAILURES} test(s) failed"
  exit 1
fi
