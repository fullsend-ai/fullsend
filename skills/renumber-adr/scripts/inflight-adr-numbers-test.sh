#!/usr/bin/env bash
# inflight-adr-numbers-test.sh — tests for inflight-adr-numbers.sh with mock gh.
#
# Run from repo root:
#   bash skills/renumber-adr/scripts/inflight-adr-numbers-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/inflight-adr-numbers.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
GH_LOG="${TMPDIR}/gh.log"
LIST_FILE="${TMPDIR}/pr-list"
DIFF_DIR="${TMPDIR}/diffs"
FAIL_LIST="${TMPDIR}/fail-list"
STDERR_FILE="${TMPDIR}/stderr"

mkdir -p "${MOCK_BIN}" "${DIFF_DIR}"

cat > "${MOCK_BIN}/gh" <<EOF
#!/usr/bin/env bash
echo "gh \$*" >> "${GH_LOG}"
if [[ "\$1" == "pr" && "\$2" == "list" ]]; then
  if [[ -f "${FAIL_LIST}" ]]; then
    echo "simulated gh pr list failure" >&2
    exit 1
  fi
  if [[ -f "${LIST_FILE}" ]]; then
    cat "${LIST_FILE}"
  fi
  exit 0
fi
if [[ "\$1" == "pr" && "\$2" == "diff" ]]; then
  pr="\$3"
  if [[ -f "${DIFF_DIR}/\${pr}" ]]; then
    cat "${DIFF_DIR}/\${pr}"
    exit 0
  fi
  echo "simulated gh pr diff failure" >&2
  exit 1
fi
echo "mock gh: unexpected command: \$*" >&2
exit 1
EOF
chmod +x "${MOCK_BIN}/gh"

reset_fixtures() {
  rm -f "${GH_LOG}" "${LIST_FILE}" "${FAIL_LIST}" "${STDERR_FILE}"
  rm -rf "${DIFF_DIR}"
  mkdir -p "${DIFF_DIR}"
  : > "${GH_LOG}"
}

run_script() {
  local output exit_code=0
  output="$(PATH="${MOCK_BIN}:${PATH}" bash "${SCRIPT}" "$@" 2>"${STDERR_FILE}")" || exit_code=$?
  printf '%s' "${output}"
  return "${exit_code}"
}

assert_eq() {
  local name="$1" expected="$2" actual="$3"
  if [[ "${expected}" != "${actual}" ]]; then
    echo "FAIL: ${name}"
    echo "  expected: $(printf '%q' "${expected}")"
    echo "  actual:   $(printf '%q' "${actual}")"
    FAILURES=$((FAILURES + 1))
  else
    echo "PASS: ${name}"
  fi
}

# --- gh pr list failure honors the always-0 exit contract ---
reset_fixtures
touch "${FAIL_LIST}"
output=""
exit_code=0
output="$(run_script main)" || exit_code=$?
assert_eq "list failure exit code" "0" "${exit_code}"
assert_eq "list failure stdout" "" "${output}"
stderr="$(cat "${STDERR_FILE}")"
assert_eq "list failure stderr suppressed" "" "${stderr}"

# --- no open PRs: empty success ---
reset_fixtures
: > "${LIST_FILE}"
output=""
exit_code=0
output="$(run_script main)" || exit_code=$?
assert_eq "empty list exit code" "0" "${exit_code}"
assert_eq "empty list stdout" "" "${output}"

# --- collects ADR numbers, skips the current PR, ignores non-ADR files ---
reset_fixtures
printf '%s\n' 10 11 12 > "${LIST_FILE}"
printf '%s\n' "docs/ADRs/0042-example.md" "README.md" > "${DIFF_DIR}/10"
printf '%s\n' "src/main.go" > "${DIFF_DIR}/11"
printf '%s\n' "docs/ADRs/0043-other.md" > "${DIFF_DIR}/12"
output=""
exit_code=0
output="$(run_script main 12)" || exit_code=$?
assert_eq "collect exit code" "0" "${exit_code}"
assert_eq "collect stdout" "0042" "${output}"

# --- gh pr list is invoked with a limit above the default of 30 ---
reset_fixtures
printf '%s\n' 1 > "${LIST_FILE}"
printf '%s\n' "docs/ADRs/0001-one.md" > "${DIFF_DIR}/1"
output=""
exit_code=0
output="$(run_script develop)" || exit_code=$?
assert_eq "limit flag exit code" "0" "${exit_code}"
list_line="$(grep 'gh pr list' "${GH_LOG}" || true)"
if printf '%s' "${list_line}" | grep -Eq -- '(^|[[:space:]])-L[[:space:]]*999([[:space:]]|$)|(^|[[:space:]])--limit(=|[[:space:]])999([[:space:]]|$)'; then
  echo "PASS: gh pr list uses -L/--limit 999"
else
  echo "FAIL: gh pr list missing -L/--limit 999"
  echo "  invocation: ${list_line}"
  FAILURES=$((FAILURES + 1))
fi
if printf '%s' "${list_line}" | grep -q -- '--base develop'; then
  echo "PASS: gh pr list forwards base branch"
else
  echo "FAIL: gh pr list did not forward base branch"
  echo "  invocation: ${list_line}"
  FAILURES=$((FAILURES + 1))
fi

# --- more than 30 listed PRs are all processed (no internal 30-cap) ---
reset_fixtures
expected=""
i=1
while [ "${i}" -le 31 ]; do
  echo "${i}" >> "${LIST_FILE}"
  num="$(printf '%04d' "${i}")"
  echo "docs/ADRs/${num}-item.md" > "${DIFF_DIR}/${i}"
  expected="${expected}${num}"$'\n'
  i=$((i + 1))
done
expected="$(printf '%s' "${expected}" | sort -u)"
output=""
exit_code=0
output="$(run_script main)" || exit_code=$?
assert_eq "over-30 exit code" "0" "${exit_code}"
assert_eq "over-30 stdout" "${expected}" "${output}"

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All tests passed"
