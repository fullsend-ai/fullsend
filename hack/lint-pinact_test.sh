#!/usr/bin/env bash
# lint-pinact_test.sh — Tests for hack/lint-pinact
#
# Run from repo root:
#   bash hack/lint-pinact_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/lint-pinact"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

echo "=== lint-pinact tests ==="

# Build a clean PATH with only bash symlinked, guaranteeing pinact is absent
NO_PINACT_DIR="${TMPDIR}/no_pinact_bin"
mkdir -p "${NO_PINACT_DIR}"
ln -s "$(command -v bash)" "${NO_PINACT_DIR}/bash"

# 1. When pinact is not on PATH and CI is not set: warning and exit 0
actual_exit=0
output=$(PATH="${NO_PINACT_DIR}" CI="" "${SCRIPT}" 2>&1) || actual_exit=$?

if [[ "${actual_exit}" -eq 0 ]] && [[ "${output}" == *"WARNING: pinact is not installed"* ]]; then
  echo "PASS: missing pinact skips gracefully locally (exit 0)"
else
  echo "FAIL: expected exit 0 with warning, got exit ${actual_exit}"
  echo "  output: ${output}"
  FAILURES=$((FAILURES + 1))
fi

# 2. When pinact is not on PATH and CI=true: error and exit 1
actual_exit=0
output=$(PATH="${NO_PINACT_DIR}" CI="true" "${SCRIPT}" 2>&1) || actual_exit=$?

if [[ "${actual_exit}" -eq 1 ]] && [[ "${output}" == *"ERROR: pinact is required in CI"* ]]; then
  echo "PASS: missing pinact fails in CI (exit 1)"
else
  echo "FAIL: expected exit 1 with error, got exit ${actual_exit}"
  echo "  output: ${output}"
  FAILURES=$((FAILURES + 1))
fi

# 3. When mock pinact is on PATH: passes arguments through
MOCK_BIN_DIR="${TMPDIR}/mock_bin"
mkdir -p "${MOCK_BIN_DIR}"
cat > "${MOCK_BIN_DIR}/pinact" <<'EOF'
#!/usr/bin/env bash
echo "MOCK PINACT: $*"
exit 0
EOF
chmod +x "${MOCK_BIN_DIR}/pinact"

actual_exit=0
output=$(PATH="${MOCK_BIN_DIR}:${NO_PINACT_DIR}" "${SCRIPT}" extra-arg 2>&1) || actual_exit=$?

if [[ "${actual_exit}" -eq 0 ]] && [[ "${output}" == *"MOCK PINACT: run --fix=false --no-api extra-arg"* ]]; then
  echo "PASS: mock pinact runs with correct arguments"
else
  echo "FAIL: expected mock pinact invocation, got exit ${actual_exit}"
  echo "  output: ${output}"
  FAILURES=$((FAILURES + 1))
fi

echo ""
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) FAILED"
  exit 1
else
  echo "All tests passed"
fi
