#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "$TEST_DIR"' EXIT
FAILURES=0

mkdir -p "$TEST_DIR/bin"
export GH_CALL_LOG="$TEST_DIR/gh-calls"

cat > "$TEST_DIR/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >> "$GH_CALL_LOG"

case "$1 $2" in
  "issue view")
    printf '%s\n' '{"state":"OPEN","title":"Issue title","labels":[{"name":"bug"}],"assignees":[{"login":"octocat"}],"milestone":{"title":"v1"},"comments":[{"author":{"login":"reviewer"},"body":"Looks good","createdAt":"2026-09-02T10:00:00Z"}]}'
    ;;
  "pr view")
    printf '%s\n' '{"state":"OPEN","title":"PR title","labels":[{"name":"enhancement"}],"assignees":[{"login":"octocat"}],"milestone":null,"mergeable":"MERGEABLE","reviewDecision":"APPROVED","comments":[{"author":{"login":"reviewer"},"body":"Ship it","createdAt":"2026-09-02T11:00:00Z"}],"reviews":[{"author":{"login":"maintainer"},"state":"APPROVED","body":"Approved"}]}'
    ;;
  *)
    printf 'unexpected gh invocation: %s\n' "$*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$TEST_DIR/bin/gh"

export PATH="$TEST_DIR/bin:$PATH"
export EPHEMERAL_REPO="fullsend-ai/example"
export FIXTURE_NUMBER="42"

run_capture() {
  local fixture_type="$1"
  local url_path="issues"
  if [[ "$fixture_type" == "pull_request" ]]; then
    url_path="pull"
  fi
  export CASE_WORKSPACE="$TEST_DIR/$fixture_type"
  export FIXTURE_TYPE="$fixture_type"
  export FIXTURE_URL="https://github.com/$EPHEMERAL_REPO/$url_path/$FIXTURE_NUMBER"

  : > "$GH_CALL_LOG"
  "$SCRIPT_DIR/capture-fixture.sh" >/dev/null
}

assert_call_count() {
  local name="$1"
  local expected="$2"
  local actual
  actual="$(wc -l < "$GH_CALL_LOG")"

  if [[ "$actual" -ne "$expected" ]]; then
    echo "FAIL: $name — expected $expected gh call(s), got $actual"
    cat "$GH_CALL_LOG"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: $name"
}

assert_call_matches() {
  local name="$1"
  local pattern="$2"

  if ! grep -q "$pattern" "$GH_CALL_LOG"; then
    echo "FAIL: $name — gh call did not match expected arguments"
    cat "$GH_CALL_LOG"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: $name"
}

assert_fixture_matches() {
  local name="$1"
  local expected_filter="$2"
  local state_file="$CASE_WORKSPACE/output/fixture-state.json"

  if ! jq -e "$expected_filter" "$state_file" >/dev/null; then
    echo "FAIL: $name — fixture-state.json did not match expected output"
    cat "$state_file"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: $name"
}

run_capture issue
assert_call_count "issue uses one gh call" 1
assert_call_matches "issue requests all fields" \
  '^issue view .*--json state,labels,assignees,milestone,title,comments$'
assert_fixture_matches "issue fixture JSON shape" '
  . == {
    fixture_type: "issue",
    fixture_url: "https://github.com/fullsend-ai/example/issues/42",
    state: "OPEN",
    title: "Issue title",
    labels: ["bug"],
    assignees: ["octocat"],
    milestone: "v1",
    comments: [{author: "reviewer", body: "Looks good", created_at: "2026-09-02T10:00:00Z"}]
  }
'

run_capture pull_request
assert_call_count "pull request uses one gh call" 1
assert_call_matches "pull request requests all fields" \
  '^pr view .*--json state,labels,assignees,milestone,title,mergeable,reviewDecision,comments,reviews$'
assert_fixture_matches "pull request fixture JSON shape" '
  . == {
    fixture_type: "pull_request",
    fixture_url: "https://github.com/fullsend-ai/example/pull/42",
    state: "OPEN",
    title: "PR title",
    labels: ["enhancement"],
    assignees: ["octocat"],
    milestone: null,
    mergeable: "MERGEABLE",
    review_decision: "APPROVED",
    comments: [{author: "reviewer", body: "Ship it", created_at: "2026-09-02T11:00:00Z"}],
    reviews: [{author: "maintainer", state: "APPROVED", body: "Approved"}]
  }
'

if [[ "$FAILURES" -gt 0 ]]; then
  echo "FAILURES: $FAILURES"
  exit 1
fi

echo "All capture-fixture tests passed"
