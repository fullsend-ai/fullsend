#!/usr/bin/env bash
# setup-fixture-test.sh — Tests for the retry_cmd function in setup-fixture.sh
#
# Run from the repo root:
#   bash eval/scripts/setup-fixture-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FAILURES=0
TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# Extract retry_cmd from setup-fixture.sh so we can test it in isolation.
# This avoids sourcing the whole script (which requires env vars and tools).
sed -n '/^retry_cmd()/,/^}/p' "${SCRIPT_DIR}/setup-fixture.sh" > "${TMPDIR}/retry_cmd.sh"

assert_eq() {
  local test_name="$1" expected="$2" actual="$3"
  if [[ "$expected" == "$actual" ]]; then
    echo "PASS: ${test_name}"
  else
    echo "FAIL: ${test_name} — expected '${expected}', got '${actual}'"
    (( FAILURES++ )) || true
  fi
}

# ── Test 1: succeeds on first attempt ──────────────────────────────
test_succeeds_first_attempt() {
  local out
  out=$(
    source "${TMPDIR}/retry_cmd.sh"
    retry_cmd true 2>/dev/null
  )
  assert_eq "succeeds on first attempt (exit 0)" "0" "$?"
}
test_succeeds_first_attempt

# ── Test 2: fails after max attempts ──────────────────────────────
test_fails_after_max_attempts() {
  local rc=0
  source "${TMPDIR}/retry_cmd.sh"
  retry_cmd false 2>/dev/null || rc=$?
  assert_eq "fails after max attempts (exit 1)" "1" "$rc"
}
test_fails_after_max_attempts

# ── Test 3: retries and eventually succeeds ────────────────────────
test_retries_then_succeeds() {
  # Create a command that fails twice then succeeds.
  local counter_file="${TMPDIR}/attempt_count"
  echo "0" > "$counter_file"

  cat > "${TMPDIR}/flaky_cmd.sh" <<SCRIPT
#!/usr/bin/env bash
count=\$(cat "${counter_file}")
count=\$((count + 1))
echo "\$count" > "${counter_file}"
if [[ \$count -lt 3 ]]; then
  exit 1
fi
exit 0
SCRIPT
  chmod +x "${TMPDIR}/flaky_cmd.sh"

  local rc=0
  source "${TMPDIR}/retry_cmd.sh"
  retry_cmd "${TMPDIR}/flaky_cmd.sh" 2>/dev/null || rc=$?
  assert_eq "retries then succeeds (exit 0)" "0" "$rc"

  local attempts
  attempts=$(cat "$counter_file")
  assert_eq "ran 3 attempts before success" "3" "$attempts"
}
test_retries_then_succeeds

# ── Test 4: diagnostic output goes to stderr, not stdout ──────────
test_stderr_not_stdout() {
  local counter_file="${TMPDIR}/attempt_count2"
  echo "0" > "$counter_file"

  cat > "${TMPDIR}/fail_once.sh" <<SCRIPT
#!/usr/bin/env bash
count=\$(cat "${counter_file}")
count=\$((count + 1))
echo "\$count" > "${counter_file}"
if [[ \$count -lt 2 ]]; then
  exit 1
fi
echo "command_output"
SCRIPT
  chmod +x "${TMPDIR}/fail_once.sh"

  local stdout stderr
  source "${TMPDIR}/retry_cmd.sh"
  stdout=$(retry_cmd "${TMPDIR}/fail_once.sh" 2>"${TMPDIR}/stderr.txt")
  stderr=$(cat "${TMPDIR}/stderr.txt")

  # stdout must contain only the command output, no retry warnings
  assert_eq "stdout is clean (no retry warnings)" "command_output" "$stdout"

  # stderr must contain the retry warning
  if echo "$stderr" | grep -q "WARNING.*attempt.*failed"; then
    echo "PASS: retry warning goes to stderr"
  else
    echo "FAIL: retry warning not found in stderr"
    (( FAILURES++ )) || true
  fi
}
test_stderr_not_stdout

# ── Test 5: preserves stdout from successful command ──────────────
test_preserves_stdout() {
  local out
  source "${TMPDIR}/retry_cmd.sh"
  out=$(retry_cmd echo "hello world" 2>/dev/null)
  assert_eq "preserves command stdout" "hello world" "$out"
}
test_preserves_stdout

# ── Summary ────────────────────────────────────────────────────────
echo ""
if [[ "$FAILURES" -gt 0 ]]; then
  echo "FAILED: ${FAILURES} test(s) failed"
  exit 1
else
  echo "ALL TESTS PASSED"
fi
