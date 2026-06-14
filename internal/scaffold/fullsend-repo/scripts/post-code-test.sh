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

# ---------------------------------------------------------------------------
# Test helper — reimplements the no-op detection logic from post-code.sh
# so we can test it without a git repo or network access.
#
# Returns the exit code and message the postscript would produce.
# ---------------------------------------------------------------------------
detect_noop() {
  local branch="$1"
  local changed_files="$2"

  # Step 1: branch check (mirrors lines 64-67 of post-code.sh)
  if [ -z "${branch}" ] || [ "${branch}" = "main" ] || [ "${branch}" = "master" ]; then
    echo "noop:branch:Agent did not create a feature branch (current: '${branch:-detached HEAD}') — nothing to do"
    return 0
  fi

  # Step 2: changed files check (mirrors lines 84-87 of post-code.sh)
  if [ -z "${changed_files}" ]; then
    echo "noop:files:No changed files in agent's commit(s) — nothing to do"
    return 0
  fi

  echo "proceed"
  return 0
}

run_noop_test() {
  local test_name="$1"
  local branch="$2"
  local changed_files="$3"
  local expected_prefix="$4"  # "noop:branch", "noop:files", or "proceed"

  local actual
  actual="$(detect_noop "${branch}" "${changed_files}")"

  if [[ "${actual}" != ${expected_prefix}* ]]; then
    echo "FAIL: ${test_name}"
    echo "  branch:         '${branch}'"
    echo "  changed_files:  '${changed_files}'"
    echo "  expected prefix: '${expected_prefix}'"
    echo "  actual:          '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- No-op detection test cases ---

# On main with no changes → exit 0, noop via branch check
run_noop_test "noop-on-main-no-changes" \
  "main" "" "noop:branch"

# On master with no changes → exit 0, noop via branch check
run_noop_test "noop-on-master-no-changes" \
  "master" "" "noop:branch"

# Detached HEAD (empty branch) with no changes → exit 0, noop via branch check
run_noop_test "noop-detached-head" \
  "" "" "noop:branch"

# Feature branch with no file changes → exit 0, noop via files check
run_noop_test "noop-feature-branch-no-changes" \
  "agent/42-fix-widget" "" "noop:files"

# Feature branch WITH file changes → proceed (existing behavior)
run_noop_test "proceed-feature-branch-with-changes" \
  "agent/42-fix-widget" "src/widget.go" "proceed"

# On main but with changes → still noop (branch check comes first)
run_noop_test "noop-on-main-with-changes" \
  "main" "src/widget.go" "noop:branch"

# ---------------------------------------------------------------------------
# Test helper — reimplements the stale branch cleanup decision logic from
# post-code.sh section 7a. Given whether a remote branch exists and whether
# an open PR references it, returns the action the script would take.
# ---------------------------------------------------------------------------
decide_stale_branch_action() {
  local remote_ref="$1"   # non-empty if remote branch exists
  local open_pr_num="$2"  # non-empty if an open PR uses the branch

  if [ -z "${remote_ref}" ]; then
    echo "skip:no-remote-branch"
    return 0
  fi

  if [ -z "${open_pr_num}" ]; then
    echo "delete:stale-branch"
    return 0
  fi

  echo "keep:open-pr:${open_pr_num}"
  return 0
}

run_stale_branch_test() {
  local test_name="$1"
  local remote_ref="$2"
  local open_pr_num="$3"
  local expected_prefix="$4"

  local actual
  actual="$(decide_stale_branch_action "${remote_ref}" "${open_pr_num}")"

  if [[ "${actual}" != ${expected_prefix}* ]]; then
    echo "FAIL: ${test_name}"
    echo "  remote_ref:      '${remote_ref}'"
    echo "  open_pr_num:     '${open_pr_num}'"
    echo "  expected prefix: '${expected_prefix}'"
    echo "  actual:          '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Stale branch cleanup test cases ---

# No remote branch → skip (normal first push)
run_stale_branch_test "no-remote-branch" \
  "" "" "skip:no-remote-branch"

# Remote branch exists, no open PR → delete stale branch
run_stale_branch_test "stale-branch-no-pr" \
  "abc123 refs/heads/agent/42-fix-widget" "" "delete:stale-branch"

# Remote branch exists, open PR → keep branch (push will update PR)
run_stale_branch_test "branch-with-open-pr" \
  "abc123 refs/heads/agent/42-fix-widget" "99" "keep:open-pr"

# ---------------------------------------------------------------------------
# Test helper — reimplements the push retry logic from post-code.sh
# section 7b. Given a push exit code and output, returns the action.
# ---------------------------------------------------------------------------
decide_push_retry() {
  local push_rc="$1"
  local push_output="$2"

  if [ "${push_rc}" -eq 0 ]; then
    echo "success"
    return 0
  fi

  if echo "${push_output}" | grep -qi "non-fast-forward\|rejected\|fetch first"; then
    echo "retry:force-with-lease"
    return 0
  fi

  echo "fail:unexpected-error"
  return 0
}

run_push_retry_test() {
  local test_name="$1"
  local push_rc="$2"
  local push_output="$3"
  local expected_prefix="$4"

  local actual
  actual="$(decide_push_retry "${push_rc}" "${push_output}")"

  if [[ "${actual}" != ${expected_prefix}* ]]; then
    echo "FAIL: ${test_name}"
    echo "  push_rc:         '${push_rc}'"
    echo "  push_output:     '${push_output}'"
    echo "  expected prefix: '${expected_prefix}'"
    echo "  actual:          '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Push retry test cases ---

# Successful push → no retry needed
run_push_retry_test "push-success" \
  "0" "Everything up-to-date" "success"

# Non-fast-forward error → retry with --force-with-lease
run_push_retry_test "push-non-fast-forward" \
  "1" "error: failed to push some refs: non-fast-forward" "retry:force-with-lease"

# Rejected error → retry with --force-with-lease
run_push_retry_test "push-rejected" \
  "1" "! [rejected] agent/42 -> agent/42 (fetch first)" "retry:force-with-lease"

# Unknown error → fail
run_push_retry_test "push-unexpected-error" \
  "1" "fatal: repository not found" "fail:unexpected-error"

# ---------------------------------------------------------------------------
# Test helper — reimplements the error reporting comment builder from
# post-code.sh. Verifies the comment body contains expected content.
# ---------------------------------------------------------------------------
build_error_comment() {
  local exit_code="$1"
  local repo_full_name="$2"
  local run_id="$3"
  local github_repository="${4:-}"  # GITHUB_REPOSITORY override (org-mode)

  local run_repo="${github_repository:-${repo_full_name}}"
  local run_url="https://github.com/${run_repo}/actions/runs/${run_id}"
  echo "⚠️ **Post-code script failed** (exit code ${exit_code})

The code agent completed, but the post-code script failed while \
pushing the branch or creating the PR.

**Workflow run:** ${run_url}

Please check the workflow logs for details and retry with \`/fs-code\` \
if appropriate."
}

run_error_comment_test() {
  local test_name="$1"
  local exit_code="$2"
  local repo="$3"
  local run_id="$4"
  local check_pattern="$5"
  local expect_present="$6"
  local github_repository="${7:-}"  # optional GITHUB_REPOSITORY override

  local actual
  actual="$(build_error_comment "${exit_code}" "${repo}" "${run_id}" "${github_repository}")"

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

# --- Error comment test cases ---

run_error_comment_test "error-comment-has-exit-code" \
  "1" "my-org/my-repo" "12345" \
  "exit code 1" "yes"

run_error_comment_test "error-comment-has-workflow-link" \
  "1" "my-org/my-repo" "12345" \
  "https://github.com/my-org/my-repo/actions/runs/12345" "yes"

run_error_comment_test "error-comment-has-retry-hint" \
  "1" "my-org/my-repo" "12345" \
  "/fs-code" "yes"

run_error_comment_test "error-comment-has-warning-emoji" \
  "1" "my-org/my-repo" "12345" \
  "⚠️" "yes"

# Org-mode: GITHUB_REPOSITORY differs from REPO_FULL_NAME → URL uses dispatch repo
run_error_comment_test "error-comment-org-mode-uses-dispatch-repo" \
  "1" "test-org/my-app" "12345" \
  "https://github.com/test-org/.fullsend/actions/runs/12345" "yes" \
  "test-org/.fullsend"

# Org-mode: URL must NOT contain the source repo name
run_error_comment_test "error-comment-org-mode-not-source-repo" \
  "1" "test-org/my-app" "12345" \
  "https://github.com/test-org/my-app/actions/runs/12345" "no" \
  "test-org/.fullsend"

# Non-org-mode: no GITHUB_REPOSITORY → falls back to REPO_FULL_NAME
run_error_comment_test "error-comment-non-org-mode-fallback" \
  "1" "my-org/my-repo" "67890" \
  "https://github.com/my-org/my-repo/actions/runs/67890" "yes" \
  ""

# ---------------------------------------------------------------------------
# Test helper — reimplements the agent artifact stripping logic from
# post-code.sh section 2b. Given a list of changed files, returns which
# files would be stripped as agent artifacts.
# ---------------------------------------------------------------------------
strip_agent_artifacts() {
  local changed_files="$1"
  local agent_artifact_patterns=".agentready/ .fullsend-workspace/"
  local stripped=""

  for file in ${changed_files}; do
    local is_artifact=false
    for pattern in ${agent_artifact_patterns}; do
      local dir="${pattern%/}"
      case "${file}" in
        "${dir}"/*|"${dir}") is_artifact=true; break ;;
        */"${dir}"/*|*/"${dir}") is_artifact=true; break ;;
      esac
    done
    if [ "${is_artifact}" = "true" ]; then
      stripped="${stripped} ${file}"
    fi
  done

  echo "${stripped}" | xargs
}

run_artifact_test() {
  local test_name="$1"
  local changed_files="$2"
  local expected_stripped="$3"

  local actual
  actual="$(strip_agent_artifacts "${changed_files}")"

  if [ "${actual}" != "${expected_stripped}" ]; then
    echo "FAIL: ${test_name}"
    echo "  changed_files:     '${changed_files}'"
    echo "  expected stripped: '${expected_stripped}'"
    echo "  actual stripped:   '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Agent artifact stripping test cases ---

# .agentready/ files should be stripped
run_artifact_test "strip-agentready-file" \
  ".agentready/assessment.json src/main.go" \
  ".agentready/assessment.json"

# .fullsend-workspace/ files should be stripped
run_artifact_test "strip-fullsend-workspace-file" \
  ".fullsend-workspace/scratch.txt src/main.go" \
  ".fullsend-workspace/scratch.txt"

# Nested paths should also be stripped
run_artifact_test "strip-nested-agentready" \
  "subdir/.agentready/data.json src/main.go" \
  "subdir/.agentready/data.json"

# Normal files should not be stripped
run_artifact_test "keep-normal-files" \
  "src/main.go internal/handler.go" \
  ""

# Multiple artifacts stripped together
run_artifact_test "strip-multiple-artifacts" \
  ".agentready/a.json .fullsend-workspace/b.txt src/main.go" \
  ".agentready/a.json .fullsend-workspace/b.txt"

# Empty input should produce no stripping
run_artifact_test "strip-empty-input" \
  "" \
  ""

# ---------------------------------------------------------------------------
# Test helper — reimplements the Signed-off-by trailer detection logic from
# post-code.sh section 3b. Given commit body text, returns whether the
# trailer was detected.
# ---------------------------------------------------------------------------
detect_signed_off_by() {
  local commit_body="$1"

  if echo "${commit_body}" | grep -q '^Signed-off-by:'; then
    echo "blocked:signed-off-by"
  else
    echo "pass"
  fi
}

run_signoff_test() {
  local test_name="$1"
  local commit_body="$2"
  local expected="$3"

  local actual
  actual="$(detect_signed_off_by "${commit_body}")"

  if [ "${actual}" != "${expected}" ]; then
    echo "FAIL: ${test_name}"
    echo "  commit_body:  '${commit_body}'"
    echo "  expected:     '${expected}'"
    echo "  actual:       '${actual}'"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${test_name}"
}

# --- Signed-off-by detection test cases ---

# Commit with Signed-off-by trailer should be blocked
run_signoff_test "signoff-present-blocked" \
  "Fix widget rendering.

Signed-off-by: fullsend-ai-coder[bot] <123456+fullsend-ai-coder[bot]@users.noreply.github.com>" \
  "blocked:signed-off-by"

# Commit without Signed-off-by trailer should pass
run_signoff_test "signoff-absent-passes" \
  "Fix widget rendering.

Closes #42" \
  "pass"

# Empty commit body should pass
run_signoff_test "signoff-empty-body-passes" \
  "" \
  "pass"

# Signed-off-by mentioned mid-line (not a trailer) should pass
run_signoff_test "signoff-mid-line-passes" \
  "Removed the Signed-off-by: trailer from commits." \
  "pass"

# Multiple trailers including Signed-off-by should be blocked
run_signoff_test "signoff-among-other-trailers-blocked" \
  "Fix rendering bug.

Co-authored-by: someone <someone@example.com>
Signed-off-by: bot <bot@noreply.github.com>" \
  "blocked:signed-off-by"

# Variant casing should pass (detection is intentionally case-sensitive)
run_signoff_test "signoff-variant-casing-passes" \
  "Fix rendering bug.

signed-off-by: bot <bot@noreply.github.com>" \
  "pass"

# --- Summary ---

echo ""
if [ ${FAILURES} -gt 0 ]; then
  echo "${FAILURES} test(s) failed"
  exit 1
fi
echo "All tests passed"
