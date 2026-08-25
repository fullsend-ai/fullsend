#!/usr/bin/env bash
# check-agents-gate-pin-test.sh — Tests for check-agents-gate-pin.sh
#
# Run from the repo root:
#   bash scripts/check-agents-gate-pin-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/check-agents-gate-pin.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# build_release_yml creates a fake release.yml with a pinned SHA.
#   $1 — SHA to pin (or "missing" to omit the pin line)
build_release_yml() {
  local sha="$1"
  local dir="${TMPDIR}/workflow"
  rm -rf "${dir}"
  mkdir -p "${dir}"

  if [[ "${sha}" == "missing" ]]; then
    cat > "${dir}/release.yml" <<'EOF'
name: Release
on:
  push:
    tags: ["v*"]
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
EOF
  else
    cat > "${dir}/release.yml" <<EOF
name: Release
on:
  push:
    tags: ["v*"]
jobs:
  validate-agents:
    uses: fullsend-ai/agents/.github/workflows/functional-tests.yml@${sha} # main
    with:
      fullsend_ref: \${{ github.ref_name }}
EOF
  fi

  echo "${dir}/release.yml"
}

# build_mock creates a mock gh binary.
#   $1 — SHA to return for commits/main endpoint
#   $2 — ahead_by value for compare endpoint (optional)
#   $3 — "fail" to make the commits/main call fail (optional)
build_mock() {
  local main_sha="$1"
  local ahead_by="${2:-0}"
  local fail_mode="${3:-}"
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"

  cat > "${mock_bin}/gh" <<MOCKEOF
#!/usr/bin/env bash
if [[ "\$1" == "api" ]]; then
  endpoint="\$2"
  jq_expr=""
  while [[ \$# -gt 0 ]]; do
    case "\$1" in
      --jq) shift; jq_expr="\$1" ;;
      *) ;;
    esac
    shift
  done

  case "\${endpoint}" in
    repos/fullsend-ai/agents/commits/main)
      if [[ "${fail_mode}" == "fail" ]]; then
        echo "gh: API error" >&2
        exit 1
      fi
      echo '{"sha": "${main_sha}"}' | jq -r "\${jq_expr}"
      ;;
    repos/fullsend-ai/agents/compare/*)
      echo '{"ahead_by": ${ahead_by}}' | jq -r "\${jq_expr}"
      ;;
    *)
      echo "mock gh: unexpected endpoint: \${endpoint}" >&2
      exit 1
      ;;
  esac
  exit 0
fi
echo "mock gh: unexpected command: \$*" >&2
exit 1
MOCKEOF

  chmod +x "${mock_bin}/gh"
  echo "${mock_bin}"
}

# run_test runs the drift-check script and asserts exit code and output.
#   $1 — test name
#   $2 — expected exit code
#   $3 — pinned SHA in release.yml (or "missing")
#   $4 — agents main SHA from mock
#   $5 — ahead_by value (optional)
#   $6 — expected output substring (optional)
#   $7 — mock fail mode (optional, "fail" to simulate API error)
run_test() {
  local name="$1" expected_exit="$2" pinned_sha="$3" main_sha="$4"
  local ahead_by="${5:-0}" expected_output="${6:-}" fail_mode="${7:-}"

  local release_yml mock_bin
  release_yml=$(build_release_yml "${pinned_sha}")
  mock_bin=$(build_mock "${main_sha}" "${ahead_by}" "${fail_mode}")

  local actual_exit=0 output
  output=$(
    PATH="${mock_bin}:${PATH}" \
    RELEASE_YML="${release_yml}" \
    GH_TOKEN="fake" \
    bash "${SCRIPT}" 2>&1
  ) || actual_exit=$?

  if [[ "${actual_exit}" -ne "${expected_exit}" ]]; then
    echo "FAIL: ${name} — expected exit ${expected_exit}, got ${actual_exit}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${expected_output}" ]] && [[ "${output}" != *"${expected_output}"* ]]; then
    echo "FAIL: ${name} — expected '${expected_output}' not found in output"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${name}"
}

echo "=== check-agents-gate-pin tests ==="

# Pin matches agents main — exits 0
run_test "pin is current" 0 \
  "abc123def456abc123def456abc123def456abcd" \
  "abc123def456abc123def456abc123def456abcd" \
  0 "pin is current"

# Pin is behind agents main — exits 1
run_test "pin is stale" 1 \
  "aaa111bbb222ccc333ddd444eee555fff666aaa1" \
  "fff666eee555ddd444ccc333bbb222aaa111fff6" \
  42 "42 commit(s) behind"

# Pin line missing from release.yml — exits 1
run_test "pin line missing" 1 \
  "missing" \
  "does-not-matter" \
  0 "Could not find fullsend-ai/agents workflow pin"

# gh API failure — exits 1
run_test "gh api failure" 1 \
  "abc123def456abc123def456abc123def456abcd" \
  "does-not-matter" \
  0 "Failed to fetch" "fail"

# Multiple pins in release.yml — exits 1 (ambiguous)
run_test_multi_pin() {
  local dir="${TMPDIR}/workflow"
  rm -rf "${dir}"
  mkdir -p "${dir}"

  cat > "${dir}/release.yml" <<'EOF'
name: Release
jobs:
  validate-agents-a:
    uses: fullsend-ai/agents/.github/workflows/functional-tests.yml@aaa111bbb222ccc333ddd444eee555fff666aaa1
  validate-agents-b:
    uses: fullsend-ai/agents/.github/workflows/functional-tests.yml@fff666eee555ddd444ccc333bbb222aaa111fff6
EOF

  local mock_bin
  mock_bin=$(build_mock "does-not-matter" 0)

  local actual_exit=0 output
  output=$(
    PATH="${mock_bin}:${PATH}" \
    RELEASE_YML="${dir}/release.yml" \
    GH_TOKEN="fake" \
    bash "${SCRIPT}" 2>&1
  ) || actual_exit=$?

  if [[ "${actual_exit}" -ne 1 ]]; then
    echo "FAIL: multi-pin ambiguity — expected exit 1, got ${actual_exit}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${output}" != *"Ambiguous"* ]]; then
    echo "FAIL: multi-pin ambiguity — expected 'Ambiguous' in output"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: multi-pin ambiguity"
}
run_test_multi_pin

# release.yml does not exist — exits 1
run_test_missing_file() {
  local actual_exit=0 output
  output=$(
    RELEASE_YML="${TMPDIR}/nonexistent/release.yml" \
    GH_TOKEN="fake" \
    bash "${SCRIPT}" 2>&1
  ) || actual_exit=$?

  if [[ "${actual_exit}" -ne 1 ]]; then
    echo "FAIL: missing release.yml — expected exit 1, got ${actual_exit}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ "${output}" != *"Release workflow not found"* ]]; then
    echo "FAIL: missing release.yml — expected 'Release workflow not found' in output"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: missing release.yml"
}
run_test_missing_file

echo ""
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) FAILED"
  exit 1
else
  echo "All tests passed"
fi
