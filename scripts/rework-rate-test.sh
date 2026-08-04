#!/usr/bin/env bash
# rework-rate-test.sh — Tests for rework-rate.sh with mock gh.
#
# Run from repo root: bash scripts/rework-rate-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REWORK_SCRIPT="${SCRIPT_DIR}/rework-rate.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

MOCK_BIN="${TMPDIR}/bin"
mkdir -p "${MOCK_BIN}"

# Fixture files the mock gh reads from
SEARCH_RESULTS="${TMPDIR}/search_results.json"
PR_FILES="${TMPDIR}/pr_files.json"
PR_DETAIL="${TMPDIR}/pr_detail.json"
FOLLOWUP_COMMITS="${TMPDIR}/followup_commits.json"
COMMIT_DETAIL="${TMPDIR}/commit_detail.json"
GH_LOG="${TMPDIR}/gh.log"
GH_FAIL="false"

# Write mock gh using unquoted heredoc so paths interpolate at generation time.
# Parts that must stay literal at runtime use \$ escaping.
cat >"${MOCK_BIN}/gh" <<MOCK_EOF
#!/usr/bin/env bash
echo "gh \$*" >> "${GH_LOG}"
if [[ "\${GH_FAIL}" == "true" ]]; then
  echo "simulated gh failure" >&2
  exit 1
fi

# Extract --jq filter if present
JQ_FILTER=""
ARGS=("\$@")
for i in "\${!ARGS[@]}"; do
  if [[ "\${ARGS[\$i]}" == "--jq" ]]; then
    JQ_FILTER="\${ARGS[\$((\$i+1))]}"
    break
  fi
done

output=""
case "\$*" in
  *"search/issues"*)
    output=\$(cat "${SEARCH_RESULTS}")
    ;;
  *"/pulls/"*"/files"*)
    output=\$(cat "${PR_FILES}")
    ;;
  *"/pulls/"*)
    output=\$(cat "${PR_DETAIL}")
    ;;
  *"/commits?"*)
    output=\$(cat "${FOLLOWUP_COMMITS}")
    ;;
  *"/commits/"*)
    output=\$(cat "${COMMIT_DETAIL}")
    ;;
  *)
    echo "unexpected gh call: \$*" >&2
    exit 1
    ;;
esac

if [[ -n "\${JQ_FILTER}" ]]; then
  echo "\${output}" | jq -r "(\${JQ_FILTER}) | if type == \"object\" or type == \"array\" then tojson else . end"
else
  echo "\${output}"
fi
MOCK_EOF

chmod +x "${MOCK_BIN}/gh"
export PATH="${MOCK_BIN}:${PATH}"
export GH_FAIL="false"

run_case() {
  local name="$1"
  local expected_pattern="$2"
  local expected_exit="${3:-0}"

  : >"${GH_LOG}"

  local output exit_code
  set +e
  output="$("${REWORK_SCRIPT}" "test-org/test-repo" 30 7 2>&1)"
  exit_code=$?
  set -e

  local failed=""
  if ! echo "${output}" | grep -qE "${expected_pattern}"; then
    failed="pattern"
  fi
  if [ "$exit_code" -ne "$expected_exit" ]; then
    failed="${failed:+$failed+}exit_code"
  fi

  if [ -z "$failed" ]; then
    echo "PASS: ${name}"
  else
    echo "FAIL: ${name} (${failed})"
    echo "  expected pattern: ${expected_pattern}"
    echo "  expected exit: ${expected_exit}, got: ${exit_code}"
    echo "  got output:"
    echo "${output}" | sed 's/^/    /'
    FAILURES=$((FAILURES + 1))
  fi
}

# --- Test 1: Genuine single-parent rework ---
# Bot PR #10 merged, human commit abc1234 touches the same file -> rework
cat >"${SEARCH_RESULTS}" <<'EOF'
{"items":[{"number":10,"title":"bot fix","closed_at":"2026-01-01T10:00:00Z","pull_request":{"merged_at":"2026-01-01T10:00:00Z"}}]}
EOF
cat >"${PR_FILES}" <<'EOF'
[{"filename":"src/main.go"}]
EOF
cat >"${PR_DETAIL}" <<'EOF'
{"merge_commit_sha":"merge111"}
EOF
cat >"${FOLLOWUP_COMMITS}" <<'EOF'
[{"sha":"abc1234","author":{"type":"User","login":"human"},"parents":[{"sha":"p1"}]}]
EOF
cat >"${COMMIT_DETAIL}" <<'EOF'
{"sha":"abc1234","files":[{"filename":"src/main.go"}]}
EOF

run_case "genuine single-parent rework detected" "Rework rate: 100.0%"

# --- Test 2: Merge commit must NOT count as rework ---
# Follow-up commit has 2 parents (merge commit) touching same file -> no rework
cat >"${FOLLOWUP_COMMITS}" <<'EOF'
[{"sha":"merge999","author":{"type":"User","login":"human"},"parents":[{"sha":"p1"},{"sha":"p2"}]}]
EOF
cat >"${COMMIT_DETAIL}" <<'EOF'
{"sha":"merge999","files":[{"filename":"src/main.go"}]}
EOF

run_case "merge commit (2 parents) excluded from rework" "Rework rate: 0.0%"

# --- Test 3: PR's own merge SHA excluded ---
# Follow-up commit SHA matches the PR's merge_commit_sha -> must not count
cat >"${PR_DETAIL}" <<'EOF'
{"merge_commit_sha":"abc1234"}
EOF
cat >"${FOLLOWUP_COMMITS}" <<'EOF'
[{"sha":"abc1234","author":{"type":"User","login":"human"},"parents":[{"sha":"p1"}]}]
EOF

run_case "PR own merge commit SHA excluded" "Rework rate: 0.0%"

# --- Test 4: >100-item paginated search response ---
# Generate 101 PRs in search results; script should process all of them
ITEMS=""
for i in $(seq 1 101); do
  [ -n "${ITEMS}" ] && ITEMS="${ITEMS},"
  ITEMS="${ITEMS}{\"number\":${i},\"title\":\"bot pr ${i}\",\"closed_at\":\"2026-01-01T10:00:00Z\",\"pull_request\":{\"merged_at\":\"2026-01-01T10:00:00Z\"}}"
done
cat >"${SEARCH_RESULTS}" <<EOF
{"items":[${ITEMS}]}
EOF
cat >"${PR_DETAIL}" <<'EOF'
{"merge_commit_sha":"merge111"}
EOF
cat >"${FOLLOWUP_COMMITS}" <<'EOF'
[]
EOF

run_case "handles >100 PRs from paginated response" "Agent PRs checked: 101"

# --- Test 5: Null-author commits excluded with accounting ---
# Follow-up commit has null author -> must not count as rework, must report in output
cat >"${SEARCH_RESULTS}" <<'EOF'
{"items":[{"number":10,"title":"bot fix","closed_at":"2026-01-01T10:00:00Z","pull_request":{"merged_at":"2026-01-01T10:00:00Z"}}]}
EOF
cat >"${PR_FILES}" <<'EOF'
[{"filename":"src/main.go"}]
EOF
cat >"${PR_DETAIL}" <<'EOF'
{"merge_commit_sha":"merge111"}
EOF
cat >"${FOLLOWUP_COMMITS}" <<'EOF'
[{"sha":"null001","author":null,"parents":[{"sha":"p1"}]}]
EOF
cat >"${COMMIT_DETAIL}" <<'EOF'
{"sha":"null001","files":[{"filename":"src/main.go"}]}
EOF

run_case "null-author commit excluded with accounting" "no linked GitHub identity"

# --- Test 6: API failure on bot-PR search exits non-zero ---
cat >"${SEARCH_RESULTS}" <<'EOF'
{"items":[{"number":10,"title":"bot fix","closed_at":"2026-01-01T10:00:00Z","pull_request":{"merged_at":"2026-01-01T10:00:00Z"}}]}
EOF
export GH_FAIL="true"

run_case "API failure on bot-PR search exits with error" "ERROR: could not fetch bot PRs" 1
export GH_FAIL="false"

# --- Results ---
echo ""
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi

echo "All rework-rate tests passed."
