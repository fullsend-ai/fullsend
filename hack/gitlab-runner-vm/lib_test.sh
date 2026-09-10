#!/usr/bin/env bash
# lib_test.sh — Tests for lib.sh helpers and create-script RUNNER_TOKEN mode.
#
# Run from the repo root:
#   bash hack/gitlab-runner-vm/lib_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

CREATE_GCP="${SCRIPT_DIR}/create-gcp-vm.sh"
CREATE_OCP="${SCRIPT_DIR}/create-openshift-vm.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

# Run a create script with token/scope env vars unset, plus optional assignments.
# Usage: with_clean_env [KEY=val ...] bash SCRIPT [args]
with_clean_env() {
  env -u GL_TOKEN -u RUNNER_TOKEN -u PROJECT_ID -u GROUP_ID \
    -u GITLAB_URL -u GCP_PROJECT -u RUNNER_IMAGE -u NAMESPACE \
    "$@"
}

# assert_fails_with <name> <needle> <command...>
assert_fails_with() {
  local name="$1" needle="$2" output rc=0
  shift 2
  output=$("$@" 2>&1) && rc=0 || rc=$?
  if [ "${rc}" -eq 0 ]; then
    fail "${name}: expected non-zero exit"
    return
  fi
  if ! printf '%s' "${output}" | grep -Fq "${needle}"; then
    fail "${name}: expected '${needle}' in output"
    printf '%s\n' "${output}" | tail -5 >&2
    return
  fi
  pass "${name}"
}

# assert_succeeds_with <name> <needle> <command...>
assert_succeeds_with() {
  local name="$1" needle="$2" output rc=0
  shift 2
  output=$("$@" 2>&1) && rc=0 || rc=$?
  if [ "${rc}" -ne 0 ]; then
    fail "${name}: expected success (exit ${rc})"
    printf '%s\n' "${output}" | tail -5 >&2
    return
  fi
  if ! printf '%s' "${output}" | grep -Fq "${needle}"; then
    fail "${name}: expected '${needle}' in output"
    return
  fi
  pass "${name}"
}

echo "== uses_runner_token =="
unset RUNNER_TOKEN || true
if uses_runner_token; then
  fail "unset RUNNER_TOKEN should be false"
else
  pass "unset RUNNER_TOKEN is false"
fi

export RUNNER_TOKEN=""
if uses_runner_token; then
  fail "empty RUNNER_TOKEN should be false"
else
  pass "empty RUNNER_TOKEN is false"
fi

export RUNNER_TOKEN="glrt-xxx"
if uses_runner_token; then
  pass "set RUNNER_TOKEN is true"
else
  fail "set RUNNER_TOKEN should be true"
fi
unset RUNNER_TOKEN

echo "== create-gcp-vm.sh validation =="
assert_fails_with \
  "gcp: neither token" \
  "GL_TOKEN or RUNNER_TOKEN is required" \
  with_clean_env bash "${CREATE_GCP}"

assert_fails_with \
  "gcp: GL_TOKEN without scope" \
  "one of PROJECT_ID or GROUP_ID is required" \
  with_clean_env GL_TOKEN=glpat-xxx bash "${CREATE_GCP}"

assert_fails_with \
  "gcp: GL_TOKEN with PROJECT_ID missing GITLAB_URL" \
  "GITLAB_URL is required" \
  with_clean_env GL_TOKEN=glpat-xxx PROJECT_ID=12345 bash "${CREATE_GCP}"

assert_fails_with \
  "gcp: RUNNER_TOKEN skips scope, requires GITLAB_URL" \
  "GITLAB_URL is required" \
  with_clean_env RUNNER_TOKEN=glrt-xxx bash "${CREATE_GCP}"

assert_fails_with \
  "gcp: RUNNER_TOKEN wins over GL_TOKEN (no scope required)" \
  "GITLAB_URL is required" \
  with_clean_env RUNNER_TOKEN=glrt-xxx GL_TOKEN=glpat-xxx bash "${CREATE_GCP}"

assert_fails_with \
  "gcp: RUNNER_TOKEN invalid characters" \
  "RUNNER_TOKEN contains invalid characters" \
  with_clean_env RUNNER_TOKEN='glrt-xxx;evil' bash "${CREATE_GCP}"

assert_fails_with \
  "gcp: RUNNER_TOKEN with GITLAB_URL requires GCP_PROJECT" \
  "GCP_PROJECT is required" \
  with_clean_env RUNNER_TOKEN=glrt-xxx GITLAB_URL=https://gitlab.example.com bash "${CREATE_GCP}"

assert_succeeds_with \
  "gcp: --help documents RUNNER_TOKEN" \
  "RUNNER_TOKEN" \
  with_clean_env bash "${CREATE_GCP}" --help

assert_succeeds_with \
  "gcp: --help documents runner-hub example" \
  "Join an existing runner pool" \
  with_clean_env bash "${CREATE_GCP}" --help

assert_succeeds_with \
  "gcp: --help documents x86-64 image family" \
  "fedora-cloud-43-x86-64" \
  with_clean_env bash "${CREATE_GCP}" --help

echo "== create-openshift-vm.sh validation =="
assert_fails_with \
  "ocp: neither token" \
  "GL_TOKEN or RUNNER_TOKEN is required" \
  with_clean_env bash "${CREATE_OCP}"

assert_fails_with \
  "ocp: GL_TOKEN without scope" \
  "one of PROJECT_ID or GROUP_ID is required" \
  with_clean_env GL_TOKEN=glpat-xxx bash "${CREATE_OCP}"

assert_fails_with \
  "ocp: RUNNER_TOKEN skips scope, requires GITLAB_URL" \
  "GITLAB_URL is required" \
  with_clean_env RUNNER_TOKEN=glrt-xxx bash "${CREATE_OCP}"

assert_fails_with \
  "ocp: RUNNER_TOKEN invalid characters" \
  "RUNNER_TOKEN contains invalid characters" \
  with_clean_env RUNNER_TOKEN='glrt-xxx;evil' bash "${CREATE_OCP}"

assert_succeeds_with \
  "ocp: --help documents RUNNER_TOKEN" \
  "RUNNER_TOKEN" \
  with_clean_env bash "${CREATE_OCP}" --help

assert_succeeds_with \
  "ocp: --help documents runner-hub example" \
  "Join an existing runner pool" \
  with_clean_env bash "${CREATE_OCP}" --help

echo "== static regressions =="
if grep -E 'timeout[[:space:]]+[0-9]+[[:space:]]+gce_ssh' "${CREATE_GCP}"; then
  fail "gcp: timeout still wraps gce_ssh (timeout cannot call shell functions)"
else
  pass "gcp: timeout no longer wraps gce_ssh"
fi

if grep -Fq 'with_backoff install_packages' "${CREATE_GCP}" \
  && grep -Fq 'timeout 600 gcloud compute ssh' "${CREATE_GCP}"; then
  pass "gcp: dnf install uses inlined gcloud wrapped in with_backoff"
else
  fail "gcp: dnf install should inline gcloud compute ssh and wrap with_backoff"
fi

if grep -Fq 'GCP_IMAGE_FAMILY:-fedora-cloud-43}' "${CREATE_GCP}"; then
  fail "gcp: default image family still fedora-cloud-43 (missing -x86-64)"
else
  pass "gcp: default image family is not the un-suffixed fedora-cloud-43"
fi

if grep -Fq 'GCP_IMAGE_FAMILY:-fedora-cloud-43-x86-64}' "${CREATE_GCP}"; then
  pass "gcp: default image family is fedora-cloud-43-x86-64"
else
  fail "gcp: default image family is not fedora-cloud-43-x86-64"
fi

if grep -Fq 'joined existing pool' "${CREATE_GCP}" \
  && grep -Fq 'joined existing pool' "${CREATE_OCP}"; then
  pass "both create scripts print joined-existing-pool in RUNNER_TOKEN mode"
else
  fail "create scripts missing 'joined existing pool' final output"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
