#!/usr/bin/env bash
# pre-fetch-resolved-threads-test.sh — Tests for pre-fetch-resolved-threads.sh
#
# Verifies GraphQL response parsing, human vs. bot filtering, finding id
# extraction, and fail-safe behavior.
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/pre-fetch-resolved-threads-test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/pre-fetch-resolved-threads.sh"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

# --- Helpers ---

# build_mock creates a mock gh binary that returns a preconfigured
# GraphQL response.
#   $1 — the JSON response to return (empty string = error)
build_mock() {
  local response_json="$1"
  local mock_bin="${TMPDIR}/bin"

  rm -rf "${mock_bin}"
  mkdir -p "${mock_bin}"

  printf '%s' "${response_json}" > "${TMPDIR}/graphql-response.txt"

  cat > "${mock_bin}/gh" <<'MOCKEOF'
#!/usr/bin/env bash
RESPONSE_FILE="RESPONSE_PLACEHOLDER"

if [[ "$1" == "api" && "$2" == "graphql" ]]; then
  if [[ ! -s "${RESPONSE_FILE}" ]]; then
    exit 1
  fi
  cat "${RESPONSE_FILE}"
  exit 0
fi

exit 0
MOCKEOF

  local escaped="${TMPDIR//\//\\/}\/graphql-response.txt"
  perl -pi -e "s/RESPONSE_PLACEHOLDER/${escaped}/g" "${mock_bin}/gh"
  chmod +x "${mock_bin}/gh"

  echo "${mock_bin}"
}

# make_graphql_response builds a GraphQL response wrapping the given
# thread nodes array.
make_graphql_response() {
  local nodes_json="$1"
  cat <<ENDJSON
{
  "data": {
    "repository": {
      "pullRequest": {
        "reviewThreads": {
          "pageInfo": {"hasNextPage": false, "endCursor": null},
          "nodes": ${nodes_json}
        }
      }
    }
  }
}
ENDJSON
}

run_test() {
  local test_name="$1"
  local graphql_response="$2"
  local expected_count="$3"
  local extra_check="${4:-}"   # optional jq expression to validate output

  local mock_bin
  mock_bin="$(build_mock "${graphql_response}")"

  local github_output="${TMPDIR}/github-output.txt"
  local workspace="${TMPDIR}/workspace"
  mkdir -p "${workspace}"
  : > "${github_output}"

  local exit_code=0
  env \
    PATH="${mock_bin}:${PATH}" \
    GH_TOKEN="fake-token" \
    ORG_NAME="test-org" \
    PR_NUM="42" \
    SOURCE_REPO="test-org/test-repo" \
    GITHUB_OUTPUT="${github_output}" \
    GITHUB_WORKSPACE="${workspace}" \
    bash "${SCRIPT}" > "${TMPDIR}/stdout.log" 2>&1 || exit_code=$?

  if [[ ${exit_code} -ne 0 ]]; then
    echo "FAIL: ${test_name} — script exited with ${exit_code}"
    echo "--- stdout ---"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  local output_file
  output_file="$(grep '^human_resolved_file=' "${github_output}" | head -1 | cut -d= -f2)"

  if [[ -z "${output_file}" || ! -f "${output_file}" ]]; then
    echo "FAIL: ${test_name} — output file not found"
    FAILURES=$((FAILURES + 1))
    return
  fi

  local actual_count
  actual_count="$(jq '.resolved_threads | length' "${output_file}")"

  if [[ "${actual_count}" -ne "${expected_count}" ]]; then
    echo "FAIL: ${test_name} — expected ${expected_count} resolved threads, got ${actual_count}"
    echo "--- output ---"
    jq . "${output_file}"
    echo "--- stdout ---"
    cat "${TMPDIR}/stdout.log"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${extra_check}" ]]; then
    if ! jq -e "${extra_check}" "${output_file}" >/dev/null 2>&1; then
      echo "FAIL: ${test_name} — extra check failed: ${extra_check}"
      echo "--- output ---"
      jq . "${output_file}"
      FAILURES=$((FAILURES + 1))
      return
    fi
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# 1. No review threads at all.
run_test "no-threads" \
  "$(make_graphql_response '[]')" \
  0

# 2. One human-resolved thread with explicit dismissal.
THREAD_HUMAN_RESOLVED='[{
  "id": "T_1",
  "isResolved": true,
  "path": "internal/cli/github.go",
  "line": 42,
  "originalLine": 42,
  "resolvedBy": {"login": "alice", "__typename": "User"},
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding: MarkHidden vs MarkDeprecated", "createdAt": "2026-09-01T10:00:00Z"},
      {"author": {"login": "alice", "__typename": "User"}, "body": "We explicitly asked for MarkHidden.", "createdAt": "2026-09-01T11:00:00Z"}
    ]
  }
}]'

run_test "human-resolved-explicit" \
  "$(make_graphql_response "${THREAD_HUMAN_RESOLVED}")" \
  1 \
  '.resolved_threads[0].resolved_by == "alice" and .resolved_threads[0].resolution_context == "explicit_dismissal"'

# 3. Bot-resolved thread (auto-resolution) — should be excluded.
THREAD_BOT_RESOLVED='[{
  "id": "T_2",
  "isResolved": true,
  "path": "internal/cli/github.go",
  "line": 10,
  "originalLine": 10,
  "resolvedBy": {"login": "test-org-review[bot]", "__typename": "Bot"},
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding: something", "createdAt": "2026-09-01T10:00:00Z"}
    ]
  }
}]'

run_test "bot-resolved-excluded" \
  "$(make_graphql_response "${THREAD_BOT_RESOLVED}")" \
  0

# 4. Unresolved thread — should be excluded.
THREAD_UNRESOLVED='[{
  "id": "T_3",
  "isResolved": false,
  "path": "internal/cli/github.go",
  "line": 20,
  "originalLine": 20,
  "resolvedBy": null,
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding: open issue", "createdAt": "2026-09-01T10:00:00Z"}
    ]
  }
}]'

run_test "unresolved-excluded" \
  "$(make_graphql_response "${THREAD_UNRESOLVED}")" \
  0

# 5. Human-resolved with finding id marker in bot comment.
THREAD_WITH_FINDING_ID='[{
  "id": "T_4",
  "isResolved": true,
  "path": "cmd/main.go",
  "line": 100,
  "originalLine": 100,
  "resolvedBy": {"login": "developer", "__typename": "User"},
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "<!-- finding:f_abc123 -->\n**[logic-error]** Missing nil check", "createdAt": "2026-09-01T10:00:00Z"},
      {"author": {"login": "developer", "__typename": "User"}, "body": "Handled by the caller.", "createdAt": "2026-09-01T11:00:00Z"}
    ]
  }
}]'

run_test "finding-id-extracted" \
  "$(make_graphql_response "${THREAD_WITH_FINDING_ID}")" \
  1 \
  '.resolved_threads[0].finding_id == "f_abc123"'

# 6. Human-resolved without any human comment (silent resolution).
THREAD_SILENT='[{
  "id": "T_5",
  "isResolved": true,
  "path": "pkg/util.go",
  "line": 55,
  "originalLine": 55,
  "resolvedBy": {"login": "reviewer", "__typename": "User"},
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding: naming convention", "createdAt": "2026-09-01T10:00:00Z"}
    ]
  }
}]'

run_test "silent-resolution" \
  "$(make_graphql_response "${THREAD_SILENT}")" \
  1 \
  '.resolved_threads[0].resolution_context == "silent_resolution" and .resolved_threads[0].human_response == null'

# 7. Mixed threads — only the human-resolved ones are included.
THREAD_MIXED='[
  {
    "id": "T_6",
    "isResolved": true,
    "path": "a.go",
    "line": 1,
    "originalLine": 1,
    "resolvedBy": {"login": "human1", "__typename": "User"},
    "comments": {"pageInfo": {"hasNextPage": false}, "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding 1", "createdAt": "2026-09-01T10:00:00Z"}
    ]}
  },
  {
    "id": "T_7",
    "isResolved": false,
    "path": "b.go",
    "line": 2,
    "originalLine": 2,
    "resolvedBy": null,
    "comments": {"pageInfo": {"hasNextPage": false}, "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding 2", "createdAt": "2026-09-01T10:00:00Z"}
    ]}
  },
  {
    "id": "T_8",
    "isResolved": true,
    "path": "c.go",
    "line": 3,
    "originalLine": 3,
    "resolvedBy": {"login": "test-org-review[bot]", "__typename": "Bot"},
    "comments": {"pageInfo": {"hasNextPage": false}, "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding 3", "createdAt": "2026-09-01T10:00:00Z"}
    ]}
  },
  {
    "id": "T_9",
    "isResolved": true,
    "path": "d.go",
    "line": 4,
    "originalLine": 4,
    "resolvedBy": {"login": "human2", "__typename": "User"},
    "comments": {"pageInfo": {"hasNextPage": false}, "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding 4", "createdAt": "2026-09-01T10:00:00Z"},
      {"author": {"login": "human2", "__typename": "User"}, "body": "Not applicable.", "createdAt": "2026-09-01T11:00:00Z"}
    ]}
  }
]'

run_test "mixed-threads" \
  "$(make_graphql_response "${THREAD_MIXED}")" \
  2

# 8. GraphQL failure — script should exit 0 with empty file.
run_test "graphql-failure" \
  "" \
  0

# 9. Shared vendor bot identity (fullsend-ai-review[bot]) resolved — excluded.
THREAD_SHARED_BOT_RESOLVED='[{
  "id": "T_10",
  "isResolved": true,
  "path": "x.go",
  "line": 1,
  "originalLine": 1,
  "resolvedBy": {"login": "fullsend-ai-review[bot]", "__typename": "Bot"},
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "fullsend-ai-review[bot]", "__typename": "Bot"}, "body": "Finding", "createdAt": "2026-09-01T10:00:00Z"}
    ]
  }
}]'

run_test "shared-bot-resolved-excluded" \
  "$(make_graphql_response "${THREAD_SHARED_BOT_RESOLVED}")" \
  0

# 10. Metadata fields are populated.
run_test "metadata-populated" \
  "$(make_graphql_response "${THREAD_HUMAN_RESOLVED}")" \
  1 \
  '.metadata.pr_number == 42 and .metadata.repo == "test-org/test-repo" and .metadata.thread_count > 0'

# 11. Incomplete comment page — skip rather than guess hidden context.
THREAD_INCOMPLETE_COMMENTS='[{
  "id": "T_11",
  "isResolved": true,
  "path": "e.go",
  "line": 8,
  "originalLine": 8,
  "resolvedBy": {"login": "alice", "__typename": "User"},
  "comments": {
    "pageInfo": {"hasNextPage": true},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding", "createdAt": "2026-09-01T10:00:00Z"}
    ]
  }
}]'

run_test "incomplete-comment-page-excluded" \
  "$(make_graphql_response "${THREAD_INCOMPLETE_COMMENTS}")" \
  0

# 12. Other GitHub App resolver (login ends with [bot]) — excluded.
THREAD_DEPENDABOT_RESOLVED='[{
  "id": "T_12",
  "isResolved": true,
  "path": "f.go",
  "line": 9,
  "originalLine": 9,
  "resolvedBy": {"login": "dependabot[bot]", "__typename": "Bot"},
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding", "createdAt": "2026-09-01T10:00:00Z"}
    ]
  }
}]'

run_test "other-bot-resolved-excluded" \
  "$(make_graphql_response "${THREAD_DEPENDABOT_RESOLVED}")" \
  0

# 13. Resolver's comment is used, not another human's.
#     Bob comments but Alice resolves — human_response should be from
#     Alice (the resolver), and if Alice didn't comment it's silent.
THREAD_DIFFERENT_COMMENTER='[{
  "id": "T_13",
  "isResolved": true,
  "path": "g.go",
  "line": 15,
  "originalLine": 15,
  "resolvedBy": {"login": "alice", "__typename": "User"},
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding: something wrong", "createdAt": "2026-09-01T10:00:00Z"},
      {"author": {"login": "bob", "__typename": "User"}, "body": "I will fix this.", "createdAt": "2026-09-01T11:00:00Z"}
    ]
  }
}]'

run_test "different-commenter-silent-resolution" \
  "$(make_graphql_response "${THREAD_DIFFERENT_COMMENTER}")" \
  1 \
  '.resolved_threads[0].resolution_context == "silent_resolution" and .resolved_threads[0].human_response == null'

# 14. Resolver left a comment — should be explicit_dismissal with their text.
THREAD_RESOLVER_COMMENTED='[{
  "id": "T_14",
  "isResolved": true,
  "path": "h.go",
  "line": 20,
  "originalLine": 20,
  "resolvedBy": {"login": "alice", "__typename": "User"},
  "comments": {
    "pageInfo": {"hasNextPage": false},
    "nodes": [
      {"author": {"login": "test-org-review[bot]", "__typename": "Bot"}, "body": "Finding: issue here", "createdAt": "2026-09-01T10:00:00Z"},
      {"author": {"login": "bob", "__typename": "User"}, "body": "I think this is fine.", "createdAt": "2026-09-01T10:30:00Z"},
      {"author": {"login": "alice", "__typename": "User"}, "body": "Intentional, closing.", "createdAt": "2026-09-01T11:00:00Z"}
    ]
  }
}]'

run_test "resolver-commented-explicit-dismissal" \
  "$(make_graphql_response "${THREAD_RESOLVER_COMMENTED}")" \
  1 \
  '.resolved_threads[0].resolution_context == "explicit_dismissal" and .resolved_threads[0].human_response == "Intentional, closing."'

# 15. Metadata includes truncated flag (false when under page cap).
run_test "metadata-truncated-false" \
  "$(make_graphql_response "${THREAD_HUMAN_RESOLVED}")" \
  1 \
  '.metadata.truncated == false'

# --- Summary ---

echo ""
if [[ ${FAILURES} -gt 0 ]]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
