#!/usr/bin/env bash
# pre-code-auth-test.sh — harness test for pre-code authorization gate
set -euo pipefail

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

export ISSUE_NUMBER=1
export REPO_FULL_NAME="test-org/test-repo"
export GITHUB_ISSUE_URL="https://github.com/test-org/test-repo/issues/1"
export GH_TOKEN="fake"
GITHUB_OUTPUT="$(mktemp)"
export GITHUB_OUTPUT

MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"
cat > "${MOCK_BIN}/fullsend" <<'MOCKEOF'
#!/usr/bin/env bash
if [[ "$1" == "auth" ]]; then
  exit 10
fi
exit 0
MOCKEOF
chmod +x "${MOCK_BIN}/fullsend"
export PATH="${MOCK_BIN}:${PATH}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUTPUT_FILE="$(mktemp)"
if bash "${SCRIPT_DIR}/pre-code.sh" >"${OUTPUT_FILE}" 2>&1; then
  if grep -q 'skipped=true' "${GITHUB_OUTPUT}"; then
    echo "OK: pre-code auth gate skipped agent"
    exit 0
  fi
  echo "FAIL: expected skipped=true in output" >&2
  cat "${OUTPUT_FILE}" >&2
  exit 1
fi
echo "FAIL: pre-code should exit 0 with skipped=true" >&2
cat "${OUTPUT_FILE}" >&2
exit 1
