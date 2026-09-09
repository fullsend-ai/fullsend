#!/usr/bin/env bash
# verify-release-tag-test.sh — Tests for verify-release-tag.sh
#
# Run from the repo root:
#   bash scripts/verify-release-tag-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/verify-release-tag.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

COMMIT_A="aaaa1111bbbb2222cccc3333dddd4444eeee5555"
COMMIT_B="5555eeee4444dddd3333cccc2222bbbb1111aaaa"
TAG_OBJ_1="1111111111111111111111111111111111111111"
TAG_OBJ_2="2222222222222222222222222222222222222222"

# build_mock creates a mock gh binary.
#   $1 — JSON body returned for git/ref/tags/<tag>, or "fail" to error
#   $2 — JSON body returned for git/tags/<TAG_OBJ_1> (optional)
#   $3 — JSON body returned for git/tags/<TAG_OBJ_2> (optional)
build_mock() {
  local ref_body="$1" tag_body_1="${2:-}" tag_body_2="${3:-}"
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"

  cat > "${mock_bin}/gh" <<MOCKEOF
#!/usr/bin/env bash
if [[ "\$1" != "api" ]]; then
  echo "mock gh: unexpected command: \$*" >&2
  exit 1
fi

case "\$2" in
  repos/fullsend-ai/fullsend/git/ref/tags/*)
    if [[ '${ref_body}' == "fail" ]]; then
      echo "gh: Not Found (HTTP 404)" >&2
      exit 1
    fi
    echo '${ref_body}'
    ;;
  repos/fullsend-ai/fullsend/git/tags/${TAG_OBJ_1})
    if [[ -z '${tag_body_1}' ]]; then
      echo "mock gh: no body configured for ${TAG_OBJ_1}" >&2
      exit 1
    fi
    echo '${tag_body_1}'
    ;;
  repos/fullsend-ai/fullsend/git/tags/${TAG_OBJ_2})
    if [[ -z '${tag_body_2}' ]]; then
      echo "mock gh: no body configured for ${TAG_OBJ_2}" >&2
      exit 1
    fi
    echo '${tag_body_2}'
    ;;
  *)
    echo "mock gh: unexpected endpoint: \$2" >&2
    exit 1
    ;;
esac
exit 0
MOCKEOF

  chmod +x "${mock_bin}/gh"
  echo "${mock_bin}"
}

# run_test runs the script under a mock gh and asserts exit code and output.
#   $1 — test name
#   $2 — expected exit code
#   $3 — expected output substring ("" to skip)
#   $4 — EXPECTED_SHA to pass in
#   $5 — git/ref/tags body (or "fail")
#   $6 — git/tags/<TAG_OBJ_1> body (optional)
#   $7 — git/tags/<TAG_OBJ_2> body (optional)
run_test() {
  local name="$1" expected_exit="$2" expected_output="$3" expected_sha="$4"
  local ref_body="$5" tag_body_1="${6:-}" tag_body_2="${7:-}"

  local mock_bin
  mock_bin=$(build_mock "${ref_body}" "${tag_body_1}" "${tag_body_2}")

  local actual_exit=0 output
  output=$(
    PATH="${mock_bin}:${PATH}" \
    TAG="v9.9.9" \
    EXPECTED_SHA="${expected_sha}" \
    REPO="fullsend-ai/fullsend" \
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

echo "=== verify-release-tag tests ==="

# Lightweight tag pointing straight at the release commit.
run_test "lightweight tag matches" 0 "resolves to ${COMMIT_A}" "${COMMIT_A}" \
  "{\"object\":{\"type\":\"commit\",\"sha\":\"${COMMIT_A}\"}}"

# Annotated tag: one peel to reach the commit.
run_test "annotated tag matches" 0 "resolves to ${COMMIT_A}" "${COMMIT_A}" \
  "{\"object\":{\"type\":\"tag\",\"sha\":\"${TAG_OBJ_1}\"}}" \
  "{\"object\":{\"type\":\"commit\",\"sha\":\"${COMMIT_A}\"}}"

# Nested annotated tag: two peels to reach the commit.
run_test "nested annotated tag matches" 0 "resolves to ${COMMIT_A}" "${COMMIT_A}" \
  "{\"object\":{\"type\":\"tag\",\"sha\":\"${TAG_OBJ_1}\"}}" \
  "{\"object\":{\"type\":\"tag\",\"sha\":\"${TAG_OBJ_2}\"}}" \
  "{\"object\":{\"type\":\"commit\",\"sha\":\"${COMMIT_A}\"}}"

# Tag moved after the run started — must fail.
run_test "moved tag fails" 1 "expected ${COMMIT_A}" "${COMMIT_A}" \
  "{\"object\":{\"type\":\"commit\",\"sha\":\"${COMMIT_B}\"}}"

# Annotated tag that was moved — the peel must not hide the mismatch.
run_test "moved annotated tag fails" 1 "expected ${COMMIT_A}" "${COMMIT_A}" \
  "{\"object\":{\"type\":\"tag\",\"sha\":\"${TAG_OBJ_1}\"}}" \
  "{\"object\":{\"type\":\"commit\",\"sha\":\"${COMMIT_B}\"}}"

# Tag ref cannot be read — must fail, never pass.
run_test "unreadable tag fails" 1 "Could not read tag" "${COMMIT_A}" "fail"

# Ref names something other than a commit.
run_test "non-commit tag target fails" 1 "does not name a commit" "${COMMIT_A}" \
  "{\"object\":{\"type\":\"tree\",\"sha\":\"${COMMIT_A}\"}}"

# API returned something that is not a 40-hex SHA.
run_test "malformed sha fails" 1 "unexpected value" "${COMMIT_A}" \
  '{"object":{"type":"commit","sha":"not-a-sha"}}'

# EXPECTED_SHA itself is malformed — fail before querying anything.
run_test "malformed expected sha fails" 1 "not a commit SHA" "HEAD" \
  "{\"object\":{\"type\":\"commit\",\"sha\":\"${COMMIT_A}\"}}"

# Tag object cycle: peeling never reaches a commit — must fail, not hang.
run_test "peel depth exceeded fails" 1 "nested more than" "${COMMIT_A}" \
  "{\"object\":{\"type\":\"tag\",\"sha\":\"${TAG_OBJ_1}\"}}" \
  "{\"object\":{\"type\":\"tag\",\"sha\":\"${TAG_OBJ_2}\"}}" \
  "{\"object\":{\"type\":\"tag\",\"sha\":\"${TAG_OBJ_1}\"}}"

# A tag object that cannot be dereferenced — must fail, never pass.
run_test "undereferenceable tag object fails" 1 "Could not dereference" "${COMMIT_A}" \
  "{\"object\":{\"type\":\"tag\",\"sha\":\"${TAG_OBJ_1}\"}}"

# Missing required inputs.
missing_input_test() {
  local mock_bin actual_exit=0 output
  mock_bin=$(build_mock "{\"object\":{\"type\":\"commit\",\"sha\":\"${COMMIT_A}\"}}")
  output=$(
    PATH="${mock_bin}:${PATH}" \
    TAG="" GITHUB_REF_NAME="" \
    EXPECTED_SHA="${COMMIT_A}" \
    REPO="fullsend-ai/fullsend" \
    bash "${SCRIPT}" 2>&1
  ) || actual_exit=$?

  if [[ "${actual_exit}" -ne 1 || "${output}" != *"requires TAG"* ]]; then
    echo "FAIL: missing TAG — expected exit 1 with usage error, got ${actual_exit}: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: missing TAG"

  actual_exit=0
  output=$(
    PATH="${mock_bin}:${PATH}" \
    TAG="v9.9.9" \
    EXPECTED_SHA="${COMMIT_A}" \
    REPO="" GITHUB_REPOSITORY="" \
    bash "${SCRIPT}" 2>&1
  ) || actual_exit=$?

  if [[ "${actual_exit}" -ne 1 || "${output}" != *"requires TAG"* ]]; then
    echo "FAIL: missing REPO — expected exit 1 with usage error, got ${actual_exit}: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi
  echo "PASS: missing REPO"
}
missing_input_test

echo
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
