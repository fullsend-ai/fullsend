#!/bin/bash
# Integration test for install.sh — installs to temp dir, verifies, cleans up
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_SCRIPT="${SCRIPT_DIR}/install.sh"
TEST_DIR="/tmp/fullsend-install-test-$$"

cleanup() {
  rm -rf "${TEST_DIR}"
  echo "Cleaned up ${TEST_DIR}"
}

PASSED=0
FAILED=0

run_test() {
  local name="$1"
  shift
  echo -n "TEST: ${name}... "
  set +e
  "$@"
  local rc=$?
  set -e
  if [[ ${rc} -eq 0 ]]; then
    echo "PASS"
    ((PASSED++))
  else
    echo "FAIL"
    ((FAILED++))
  fi
}

# Test 1: Install latest
run_test "install latest" bash -c "
  rm -rf '${TEST_DIR}' && mkdir -p '${TEST_DIR}'
  bash '${INSTALL_SCRIPT}' --dir '${TEST_DIR}'
  test -x '${TEST_DIR}/fullsend'
  '${TEST_DIR}/fullsend' --version
"

# Test 2: Install specific version tag
run_test "install specific version" bash -c "
  rm -rf '${TEST_DIR}' && mkdir -p '${TEST_DIR}'
  bash '${INSTALL_SCRIPT}' --dir '${TEST_DIR}' --version v0.14.0
  test -x '${TEST_DIR}/fullsend'
  '${TEST_DIR}/fullsend' --version | grep -q '0.14.0'
"

# Test 3: Install without v prefix
run_test "install version without v prefix" bash -c "
  rm -rf '${TEST_DIR}' && mkdir -p '${TEST_DIR}'
  bash '${INSTALL_SCRIPT}' --dir '${TEST_DIR}' --version 0.14.0
  test -x '${TEST_DIR}/fullsend'
"

# Test 4: Install by release SHA (v0.18.0 commit)
# SHA 32f73a4f... = commit that v0.18.0 annotated tag points to
run_test "install by release SHA" bash -c "
  rm -rf '${TEST_DIR}' && mkdir -p '${TEST_DIR}'
  bash '${INSTALL_SCRIPT}' --dir '${TEST_DIR}' --version '32f73a4f93301493d2c31be3970aa4c51a26acc7'
  test -x '${TEST_DIR}/fullsend'
  '${TEST_DIR}/fullsend' --version | grep -q '0.18.0'
"

# Test 5: Non-release SHA should fail
# SHA 5d51fbc9... = arbitrary commit on main, not tagged as a release
run_test "non-release SHA fails" bash -c "
  rm -rf '${TEST_DIR}' && mkdir -p '${TEST_DIR}'
  ! bash '${INSTALL_SCRIPT}' --dir '${TEST_DIR}' --version '5d51fbc9ac5323aee019f521153c4e559d91b59f' 2>&1
"

# Test 6: Invalid option fails
run_test "invalid option fails" bash -c "
  ! bash '${INSTALL_SCRIPT}' --bogus 2>&1
"

cleanup
echo ""
echo "Results: ${PASSED} passed, ${FAILED} failed"
[[ "${FAILED}" -eq 0 ]]
