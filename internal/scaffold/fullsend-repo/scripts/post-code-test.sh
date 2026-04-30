#!/usr/bin/env bash
# post-code-test.sh — Test the PR title injection logic from post-code.sh.
#
# Extracts and tests the title-rewriting logic in isolation using shell
# functions. This avoids needing a full git repo or GitHub API access.
#
# Run from the repo root:
#   bash internal/scaffold/fullsend-repo/scripts/post-code-test.sh

set -euo pipefail

FAILURES=0

# ---------------------------------------------------------------------------
# Test helper — reimplements the title-rewriting logic from post-code.sh
# so we can test it without a git repo or network access.
# ---------------------------------------------------------------------------
rewrite_title() {
  local commit_subject="$1"
  local issue_number="$2"

  if echo "${commit_subject}" | grep -qE '^[a-z]+\('; then
    echo "${commit_subject}"
  elif echo "${commit_subject}" | grep -qE '^[a-z]+: '; then
    echo "${commit_subject}" | sed "s/^\([a-z]*\): /\1(#${issue_number}): /"
  else
    echo "${commit_subject}"
  fi
}

run_test() {
  local test_name="$1"
  local commit_subject="$2"
  local issue_number="$3"
  local expected="$4"

  local actual
  actual="$(rewrite_title "${commit_subject}" "${issue_number}")"

  if [ "${actual}" != "${expected}" ]; then
    echo "FAIL: ${test_name}"
    echo "  input:    '${commit_subject}' (issue #${issue_number})"
    echo "  expected: '${expected}'"
    echo "  actual:   '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Test cases ---

# Plain conventional commit — should inject issue reference
run_test "fix-without-scope" \
  "fix: correct placeholder text in secrets page dropdowns" \
  "837" \
  "fix(#837): correct placeholder text in secrets page dropdowns"

run_test "feat-without-scope" \
  "feat: add CSV export support" \
  "42" \
  "feat(#42): add CSV export support"

run_test "chore-without-scope" \
  "chore: update dependencies" \
  "100" \
  "chore(#100): update dependencies"

run_test "docs-without-scope" \
  "docs: update contributing guide" \
  "55" \
  "docs(#55): update contributing guide"

run_test "refactor-without-scope" \
  "refactor: simplify error handling" \
  "200" \
  "refactor(#200): simplify error handling"

# Already has a scope — should NOT modify
run_test "already-has-issue-scope" \
  "fix(#837): correct placeholder text" \
  "837" \
  "fix(#837): correct placeholder text"

run_test "already-has-jira-scope" \
  "fix(KFLUXUI-1200): correct placeholder text" \
  "837" \
  "fix(KFLUXUI-1200): correct placeholder text"

run_test "already-has-component-scope" \
  "feat(api): add new endpoint" \
  "42" \
  "feat(api): add new endpoint"

# Non-conventional titles — should NOT modify
run_test "non-conventional-title" \
  "Add CSV export support" \
  "42" \
  "Add CSV export support"

run_test "uppercase-type" \
  "Fix: correct placeholder text" \
  "42" \
  "Fix: correct placeholder text"

run_test "no-colon" \
  "fix the placeholder text" \
  "42" \
  "fix the placeholder text"

# Edge cases
run_test "test-type" \
  "test: add unit tests for export" \
  "99" \
  "test(#99): add unit tests for export"

run_test "ci-type" \
  "ci: update workflow permissions" \
  "10" \
  "ci(#10): update workflow permissions"

# ---------------------------------------------------------------------------
# Test helper — reimplements the PR body assembly logic from post-code.sh
# so we can test it without a git repo or network access.
# ---------------------------------------------------------------------------
build_pr_body() {
  local commit_body="$1"
  local issue_number="$2"
  local branch="$3"
  local scan_range="$4"

  local description
  if [ -z "${commit_body}" ]; then
    description="Automated implementation for issue #${issue_number}."
  else
    description="${commit_body}"
  fi

  echo "${description}

---

Closes #${issue_number}

### Post-script verification

- [x] Branch is not main/master (\`${branch}\`)
- [x] Secret scan passed (gitleaks — \`${scan_range}\`)
- [x] Pre-commit hooks passed (authoritative run on runner)
- [x] Tests ran inside sandbox"
}

run_body_test() {
  local test_name="$1"
  local commit_body="$2"
  local issue_number="$3"
  local branch="$4"
  local check_pattern="$5"
  local expect_present="$6"  # "yes" or "no"

  local actual
  actual="$(build_pr_body "${commit_body}" "${issue_number}" "${branch}" "abc123..def456")"

  if [ "${expect_present}" = "yes" ]; then
    if ! echo "${actual}" | grep -qF "${check_pattern}"; then
      echo "FAIL: ${test_name}"
      echo "  expected to find: '${check_pattern}'"
      echo "  in body:"
      echo "${actual}" | sed 's/^/    /'
      FAILURES=$((FAILURES + 1))
      return
    fi
  else
    if echo "${actual}" | grep -qF "${check_pattern}"; then
      echo "FAIL: ${test_name}"
      echo "  expected NOT to find: '${check_pattern}'"
      echo "  in body:"
      echo "${actual}" | sed 's/^/    /'
      FAILURES=$((FAILURES + 1))
      return
    fi
  fi

  echo "PASS: ${test_name}"
}

# --- PR body test cases ---

# Body should contain exactly one Closes line (the footer one)
run_body_test "closes-appears-once" \
  "Fix the widget rendering." \
  "42" "agent/42-fix-widget" \
  "Closes #42" "yes"

# Body should NOT contain a Changed files section
run_body_test "no-changed-files-section" \
  "Fix the widget rendering." \
  "42" "agent/42-fix-widget" \
  "Changed files" "no"

# Body should NOT contain a Created by footer
run_body_test "no-created-by-footer" \
  "Fix the widget rendering." \
  "42" "agent/42-fix-widget" \
  "Created by" "no"

# Empty commit body should use fallback description
run_body_test "empty-body-fallback" \
  "" \
  "99" "agent/99-add-feature" \
  "Automated implementation for issue #99." "yes"

# Empty commit body should still not have Changed files
run_body_test "empty-body-no-changed-files" \
  "" \
  "99" "agent/99-add-feature" \
  "Changed files" "no"

# Empty commit body should still not have Created by
run_body_test "empty-body-no-created-by" \
  "" \
  "99" "agent/99-add-feature" \
  "Created by" "no"

# Verify the Closes line count is exactly 1
count_closes_test() {
  local test_name="$1"
  local commit_body="$2"
  local issue_number="$3"

  local actual
  actual="$(build_pr_body "${commit_body}" "${issue_number}" "branch" "range")"
  local count
  count="$(echo "${actual}" | grep -c "Closes #${issue_number}" || true)"

  if [ "${count}" -ne 1 ]; then
    echo "FAIL: ${test_name}"
    echo "  expected exactly 1 'Closes #${issue_number}', found ${count}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

count_closes_test "single-closes-with-body" \
  "Fix rendering bug in the widget component." "42"

count_closes_test "single-closes-empty-body" \
  "" "99"

# --- Summary ---

echo ""
if [ ${FAILURES} -gt 0 ]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
