#!/usr/bin/env bash
# check-dco-signoff_test.sh — Tests for hack/check-dco-signoff
#
# Run from repo root:
#   bash hack/check-dco-signoff_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT="${SCRIPT_DIR}/check-dco-signoff"
FAILURES=0

TMPDIR="$(mktemp -d)"
trap 'rm -rf "${TMPDIR}"' EXIT

TEST_USER_NAME="Jane Contributor"
TEST_USER_EMAIL="jane@example.com"
TEST_AUTHOR="${TEST_USER_NAME} <${TEST_USER_EMAIL}>"

# Create a clean isolated git repository to run tests in
TEST_REPO="${TMPDIR}/test_repo"
git init -q "${TEST_REPO}"

# Helper to create commit message file
write_msg() {
  local path="${TMPDIR}/msg_$1.txt"
  shift
  cat > "${path}" <<EOF
$*
EOF
  echo "${path}"
}

run_test() {
  local name="$1"
  local expected_exit="$2"
  local msg_file="$3"
  local expected_output="${4:-}"
  local author_name="${5:-${TEST_USER_NAME}}"
  local author_email="${6:-${TEST_USER_EMAIL}}"
  local extra_env="${7:-}"
  local committer_name="${8:-${author_name}}"
  local committer_email="${9:-${author_email}}"

  local actual_exit=0
  local output

  output=$(
    cd "${TEST_REPO}"
    # Scrub inherited bypass flags and author variables
    unset SKIP_DCO SKIP_COMMIT_MSG_HOOK || true
    export GIT_AUTHOR_NAME="${author_name}"
    export GIT_AUTHOR_EMAIL="${author_email}"
    export GIT_COMMITTER_NAME="${committer_name}"
    export GIT_COMMITTER_EMAIL="${committer_email}"
    if [[ -n "${extra_env}" ]]; then
      eval "export ${extra_env}"
    fi
    bash "${SCRIPT}" "${msg_file}" 2>&1
  ) || actual_exit=$?

  if [[ "${actual_exit}" -ne "${expected_exit}" ]]; then
    echo "FAIL: ${name} — expected exit ${expected_exit}, got ${actual_exit}"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  if [[ -n "${expected_output}" ]] && [[ "${output}" != *"${expected_output}"* ]]; then
    echo "FAIL: ${name} — expected '${expected_output}' in output"
    echo "  output: ${output}"
    FAILURES=$((FAILURES + 1))
    return
  fi

  echo "PASS: ${name}"
}

echo "=== check-dco-signoff tests ==="

# 1. Valid sign-off matching author in trailer block
f=$(write_msg 1 "feat: add feature

Description here.

Signed-off-by: ${TEST_AUTHOR}")
run_test "valid sign-off matches author" 0 "${f}"

# 2. Case-insensitive email match
f=$(write_msg 2 "feat: add feature

Signed-off-by: ${TEST_USER_NAME} <JANE@EXAMPLE.COM>")
run_test "case-insensitive email match" 0 "${f}"

# 3. Multiple trailers in trailer block
f=$(write_msg 3 "feat: co-authored feature

Signed-off-by: Bob Builder <bob@example.com>
Signed-off-by: ${TEST_AUTHOR}")
run_test "multiple sign-offs with author matching" 0 "${f}"

# 4. Missing sign-off rejected
f=$(write_msg 4 "feat: add feature without signoff

Description.")
run_test "missing sign-off rejected" 1 "${f}" "Commit message is missing DCO sign-off trailer"

# 5. Mismatched sign-off rejected
f=$(write_msg 5 "feat: add feature

Signed-off-by: Bob Builder <bob@example.com>")
run_test "mismatched sign-off rejected" 1 "${f}" "DCO Signed-off-by trailer does not match git author or committer"

# 6. Body-embedded sign-off rejected (not in final trailer block)
f=$(write_msg 6 "feat: add feature

Signed-off-by: ${TEST_AUTHOR}

This is a body paragraph following the pseudo-trailer.")
run_test "body-embedded sign-off rejected" 1 "${f}" "Commit message is missing DCO sign-off trailer"

# 7. Indented sign-off rejected
f=$(write_msg 7 "feat: add feature

    Signed-off-by: ${TEST_AUTHOR}")
run_test "indented sign-off rejected" 1 "${f}" "Commit message is missing DCO sign-off trailer"

# 8. Bot author skips validation
f=$(write_msg 8 "chore(deps): bump dependency")
run_test "bot author skipped ([bot] in name)" 0 "${f}" "" "renovate[bot]" "renovate@example.com"
run_test "bot author skipped (coder app)" 0 "${f}" "" "fullsend-ai-coder[bot]" "coder@users.noreply.github.com"

# 9. Active merge with MERGE_HEAD skipped
touch "${TEST_REPO}/.git/MERGE_HEAD"
f=$(write_msg 9 "Merge branch 'main' into feature")
run_test "active merge with MERGE_HEAD skipped" 0 "${f}"
rm -f "${TEST_REPO}/.git/MERGE_HEAD"

# 10. Merge-shaped subject without MERGE_HEAD is NOT skipped (requires sign-off)
f=$(write_msg 10 "Merge application settings without signoff")
run_test "merge-shaped subject without MERGE_HEAD rejected" 1 "${f}" "Commit message is missing DCO sign-off trailer"

f=$(write_msg 10b "Merge application settings with signoff

Signed-off-by: ${TEST_AUTHOR}")
run_test "merge-shaped subject with signoff accepted" 0 "${f}"

# 11. Autosquash commits skipped
f=$(write_msg 11a "fixup! feat: add feature")
run_test "fixup commit skipped" 0 "${f}"

f=$(write_msg 11b "squash! feat: add feature")
run_test "squash commit skipped" 0 "${f}"

f=$(write_msg 11c "amend! feat: add feature")
run_test "amend commit skipped" 0 "${f}"

# 12. SQUASH_MSG present does not exempt a human commit
touch "${TEST_REPO}/.git/SQUASH_MSG"
f=$(write_msg 12 "feat: squash-merged feature without signoff")
run_test "squash-merge with SQUASH_MSG still requires signoff" 1 "${f}" "Commit message is missing DCO sign-off trailer"

f=$(write_msg 12b "feat: squash-merged feature with signoff

Signed-off-by: ${TEST_AUTHOR}")
run_test "squash-merge with SQUASH_MSG and signoff accepted" 0 "${f}"
rm -f "${TEST_REPO}/.git/SQUASH_MSG"

# 13. Comments-only / empty commit message skipped
f=$(write_msg 13 "# Only comment lines
# another comment")
run_test "comments only skipped" 0 "${f}"

# 14. Regex-special core.commentChar handling
(cd "${TEST_REPO}" && git config core.commentChar ".")
f=$(write_msg 14a ". this is a comment
feat: title with dot commentChar

Signed-off-by: ${TEST_AUTHOR}")
run_test "core.commentChar = . stripped and signed commit passes" 0 "${f}"

f=$(write_msg 14b ". this is a comment
feat: unsigned title with dot commentChar")
run_test "core.commentChar = . unsigned commit fails" 1 "${f}" "Commit message is missing DCO sign-off trailer"

(cd "${TEST_REPO}" && git config core.commentChar "[")
f=$(write_msg 14c "[ this is a comment
feat: title with bracket commentChar

Signed-off-by: ${TEST_AUTHOR}")
run_test "core.commentChar = [ stripped and signed commit passes" 0 "${f}"
(cd "${TEST_REPO}" && git config --unset core.commentChar)

# 15. Large message (~100KB) does not SIGPIPE
big_msg=$(python3 -c "print('feat: big message\n\n' + 'A' * 100000 + '\n\nSigned-off-by: ${TEST_AUTHOR}')")
f=$(write_msg 15 "${big_msg}")
run_test "large message does not SIGPIPE" 0 "${f}"

# 16. SKIP_DCO environment variable bypass
f=$(write_msg 16 "feat: un-signed commit")
run_test "SKIP_DCO bypass" 0 "${f}" "" "${TEST_USER_NAME}" "${TEST_USER_EMAIL}" "SKIP_DCO=1"

# 17. SKIP_COMMIT_MSG_HOOK environment variable bypass
f=$(write_msg 17 "feat: un-signed commit")
run_test "SKIP_COMMIT_MSG_HOOK bypass" 0 "${f}" "" "${TEST_USER_NAME}" "${TEST_USER_EMAIL}" "SKIP_COMMIT_MSG_HOOK=1"

# 18. Commented-out sign-off rejected
f=$(write_msg 18 "feat: commit with commented signoff
# Signed-off-by: ${TEST_AUTHOR}")
run_test "commented sign-off rejected" 1 "${f}" "Commit message is missing DCO sign-off trailer"

# 19. Nonexistent file rejected
actual_exit=0
output=$(cd "${TEST_REPO}" && bash "${SCRIPT}" "${TMPDIR}/nonexistent.txt" 2>&1) || actual_exit=$?
if [[ "${actual_exit}" -eq 1 ]] && [[ "${output}" == *"Usage:"* ]]; then
  echo "PASS: nonexistent file rejected"
else
  echo "FAIL: nonexistent file test failed (exit ${actual_exit}, output: ${output})"
  FAILURES=$((FAILURES + 1))
fi

# 20. Author and committer differ: signed off by committer
f=$(write_msg 20 "feat: commit on behalf of author

Signed-off-by: Jane Contributor <jane@example.com>")
run_test "signed off by committer when author differs" 0 "${f}" "" "Bob Builder" "bob@example.com" "" "Jane Contributor" "jane@example.com"

# 21. Author and committer differ: signed off by author
f=$(write_msg 21 "feat: author commit

Signed-off-by: Bob Builder <bob@example.com>")
run_test "signed off by author when committer differs" 0 "${f}" "" "Bob Builder" "bob@example.com" "" "Jane Contributor" "jane@example.com"

# 22. Author and committer differ: signed off by neither
f=$(write_msg 22 "feat: third party signoff

Signed-off-by: Charlie <charlie@example.com>")
run_test "author and committer differ: signed off by neither rejected" 1 "${f}" "DCO Signed-off-by trailer does not match git author or committer" "Bob Builder" "bob@example.com" "" "Jane Contributor" "jane@example.com"

# 23. Verbose commit (commit.verbose=true) with scissors line and diff
f=$(write_msg 23 "feat: verbose commit

Signed-off-by: ${TEST_AUTHOR}

# ------------------------ >8 ------------------------
# Do not modify or remove the line above.
# Everything below it will be ignored.
diff --git a/foo b/foo
new file mode 100644
--- /dev/null
+++ b/foo
@@ -0,0 +1 @@
+bar")
run_test "verbose commit with scissors line passes" 0 "${f}"

# 24. core.commentChar = auto with Markdown heading and git instructions using ';'
(cd "${TEST_REPO}" && git config core.commentChar "auto")
f=$(write_msg 24 "# Markdown Heading

Some details.

Signed-off-by: ${TEST_AUTHOR}

; Please enter the commit message for your changes. Lines starting
; with ';' will be ignored, and an empty message aborts the commit.")
run_test "core.commentChar = auto with markdown heading passes" 0 "${f}"
(cd "${TEST_REPO}" && git config --unset core.commentChar)

echo ""
if [[ "${FAILURES}" -gt 0 ]]; then
  echo "${FAILURES} test(s) FAILED"
  exit 1
else
  echo "All tests passed"
fi
