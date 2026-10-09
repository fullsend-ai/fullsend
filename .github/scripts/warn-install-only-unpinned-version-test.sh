#!/usr/bin/env bash
# warn-install-only-unpinned-version-test.sh — Tests for
# warn-install-only-unpinned-version.sh
#
# Run from the repo root:
#   bash .github/scripts/warn-install-only-unpinned-version-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/warn-install-only-unpinned-version.sh"
FAILURES=0

PIN_SHA="3cfa255000000000000000000000000000000000"

# run_test runs the warning script and asserts exit code, whether a
# warning was emitted, and (optionally) a substring of the annotation.
#   $1 — test name
#   $2 — AGENT
#   $3 — VERSION
#   $4 — ACTION_REF
#   $5 — expect_warning (yes/no)
#   $6 — expected substring (optional)
run_test() {
  local name="$1" agent="$2" version="$3" action_ref="$4" expect_warning="$5"
  local expected_substring="${6:-}"
  local actual_exit=0 output
  output=$(
    AGENT="${agent}" \
      VERSION="${version}" \
      ACTION_REF="${action_ref}" \
      bash "${SCRIPT}" 2>&1
  ) || actual_exit=$?

  if test "${actual_exit}" -ne 0; then
    echo "FAIL: ${name} — expected exit 0, got ${actual_exit}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  local has_warning=no
  case "${output}" in
    *"::warning::"*) has_warning=yes ;;
  esac

  if test "${has_warning}" != "${expect_warning}"; then
    echo "FAIL: ${name} — expected warning=${expect_warning}, got warning=${has_warning}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if test -n "${expected_substring}"; then
    case "${output}" in
      *"${expected_substring}"*) ;;
      *)
        echo "FAIL: ${name} — expected substring '${expected_substring}' not found"
        echo "  output: ${output}"
        FAILURES=$((FAILURES + 1))
        return
        ;;
    esac
  fi

  echo "PASS: ${name}"
}

echo "=== warn-install-only-unpinned-version tests ==="

run_test "non-install-only latest is silent" \
  "code" "latest" "v0.32.0" "no"

run_test "install-only with explicit tag is silent" \
  "__install_only__" "v0.32.0" "v0.32.0" "no"

run_test "install-only with explicit SHA is silent" \
  "__install_only__" "${PIN_SHA}" "${PIN_SHA}" "no"

run_test "install-only latest warns about latest resolution" \
  "__install_only__" "latest" "" "yes" "CLI will resolve to latest"

run_test "install-only empty version is treated as latest" \
  "__install_only__" "" "" "yes" "CLI will resolve to latest"

run_test "install-only whitespace version is treated as latest" \
  "__install_only__" "  " "" "yes" "CLI will resolve to latest"

run_test "install-only latest with semver action ref suggests pin" \
  "__install_only__" "latest" "v0.32.0" "yes" "version: v0.32.0"

run_test "install-only latest with SHA action ref suggests pin" \
  "__install_only__" "latest" "${PIN_SHA}" "yes" "version: ${PIN_SHA}"

run_test "install-only latest with floating action ref does not suggest pin" \
  "__install_only__" "latest" "main" "yes" "action ref: 'main'"

run_test "floating v0 action ref does not suggest version: v0" \
  "__install_only__" "latest" "v0" "yes" "Set the 'version' input"

run_test "annotation sanitizes :: in action ref" \
  "__install_only__" "latest" "v1::evil" "yes" "v1__evil"

run_test "other agent with empty version is silent" \
  "triage" "" "v0.32.0" "no"

echo ""
if test "${FAILURES}" -gt 0; then
  echo "${FAILURES} test(s) FAILED"
  exit 1
fi
echo "All tests passed"
