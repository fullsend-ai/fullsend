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
DELETE_GCP="${SCRIPT_DIR}/delete-gcp-vm.sh"
DELETE_OCP="${SCRIPT_DIR}/delete-openshift-vm.sh"
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

assert_succeeds_with \
  "gcp: --help documents no Compute SA" \
  "with --no-service-account --no-scopes" \
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
  "ocp: RUNNER_TOKEN wins over GL_TOKEN (no scope required)" \
  "GITLAB_URL is required" \
  with_clean_env RUNNER_TOKEN=glrt-xxx GL_TOKEN=glpat-xxx bash "${CREATE_OCP}"

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

echo "== delete script validation =="
# Neither RUNNER_TOKEN nor GL_TOKEN must fail closed — omitting GL_TOKEN is
# not authorization to skip deregistration (Issue #7257 fail-open finding).
assert_fails_with \
  "delete-gcp-vm.sh: neither RUNNER_TOKEN nor GL_TOKEN fails closed" \
  "GL_TOKEN is required unless RUNNER_TOKEN is set" \
  with_clean_env GCP_PROJECT=my-gcp-project bash "${DELETE_GCP}" fullsend-gitlab-runner-01

assert_fails_with \
  "delete-openshift-vm.sh: neither RUNNER_TOKEN nor GL_TOKEN fails closed" \
  "GL_TOKEN is required unless RUNNER_TOKEN is set" \
  with_clean_env NAMESPACE=my-namespace bash "${DELETE_OCP}" fullsend-gitlab-runner-01

echo "== drain_runner_vm =="

# drain_runner_vm bounds every remote_exec call with `timeout`, which execs
# a real program and cannot invoke a shell function directly — it must run
# each mock (and, in production, gcp_drain_ssh/ocp_drain_ssh) via
# `export -f` + `bash -c`. Each such call is therefore a fresh subprocess:
# mocks below that need to remember state across calls (poll-fail,
# cap-overrun) use a counter file rather than an in-memory variable.

assert_fails_with \
  "drain_runner_vm: missing remote_exec fn" \
  "requires a remote exec function" \
  drain_runner_vm ""

# drain_runner_vm interpolates runner_user unquoted into remote shell
# command strings, so it must validate the value itself (the same Unix
# username regex both callers already enforce before calling it) rather
# than trusting every future caller to have validated first.
drain_noop_mock() { return 0; }
assert_fails_with \
  "drain_runner_vm: rejects invalid runner_user" \
  "runner_user must be a plain Unix user name" \
  drain_runner_vm drain_noop_mock "fedora; rm -rf /"

# drain_idle_mock: succeeds every call and never reports a runner-*
# container, so drain_runner_vm should detect idle on the first poll.
drain_idle_mock() { return 0; }

DRAIN_TIMEOUT_SEC=5 DRAIN_POLL_SEC=1 assert_succeeds_with \
  "drain_runner_vm: idle on first poll reports OK" \
  "OK: runner idle" \
  drain_runner_vm drain_idle_mock

# drain_signal_fail_mock: the drain-signal call itself fails (covers both a
# transport failure and, since the remote script now propagates in-guest
# systemctl/config-edit failures via its own exit status, a fully-failed
# in-guest drain). drain_runner_vm warns, then falls through to the poll
# loop (in case the runner is already draining despite the signal error).
# The mock fails every call, so every poll fails too and drain-then-proceed
# policy kicks in once the (short, for test speed) cap is reached. Assert
# on both WARN messages.
drain_signal_fail_mock() { return 1; }

DRAIN_TIMEOUT_SEC=2 DRAIN_POLL_SEC=1 assert_succeeds_with \
  "drain_runner_vm: signal failure warns and still polls" \
  "polling for idle anyway" \
  drain_runner_vm drain_signal_fail_mock

# drain_poll_fail_mock: signal succeeds, but every poll after that fails
# (a persistent transport failure, not a transient blip). drain_runner_vm
# must retry within the drain budget rather than proceeding on the very
# first failed poll (a single SSH/IAP blip must not delete a VM with a job
# still running) — it should only give up once DRAIN_TIMEOUT_SEC is
# reached. Counter lives in a file (DRAIN_POLL_FAIL_COUNTER) since
# drain_runner_vm runs every remote_exec call (including this mock) via
# `export -f` + `bash -c` under `timeout`, so each call is a separate
# subprocess and an in-memory counter would not persist across calls.
DRAIN_POLL_FAIL_COUNTER=$(mktemp)
echo 0 > "${DRAIN_POLL_FAIL_COUNTER}"
drain_poll_fail_mock() {
  local n
  n=$(( $(cat "${DRAIN_POLL_FAIL_COUNTER}") + 1 ))
  echo "${n}" > "${DRAIN_POLL_FAIL_COUNTER}"
  if [ "${n}" -eq 1 ]; then
    return 0
  fi
  return 1
}
export DRAIN_POLL_FAIL_COUNTER
DRAIN_TIMEOUT_SEC=2 DRAIN_POLL_SEC=1 \
  output=$(drain_runner_vm drain_poll_fail_mock 2>&1) && rc=0 || rc=$?
if [ "${rc}" -eq 0 ] \
  && printf '%s' "${output}" | grep -Fq "WARN: drain poll failed" \
  && printf '%s' "${output}" | grep -Fq "WARN: drain cap 2s reached"; then
  pass "drain_runner_vm: persistent poll failure retries then gives up at cap"
else
  fail "drain_runner_vm: persistent poll failure did not retry to the cap"
  printf '%s\n' "${output}" | tail -5 >&2
fi
rm -f "${DRAIN_POLL_FAIL_COUNTER}"
unset DRAIN_POLL_FAIL_COUNTER

# drain_transient_poll_fail_mock: the first poll fails (a transient blip),
# but the second succeeds and reports idle. A retry-then-proceed-on-cap
# implementation that instead proceeds immediately on the first failure
# would never see the recovered idle state — assert both that the WARN
# fires and that "OK: runner idle" is reached without hitting the cap.
DRAIN_TRANSIENT_COUNTER=$(mktemp)
echo 0 > "${DRAIN_TRANSIENT_COUNTER}"
drain_transient_poll_fail_mock() {
  local n
  n=$(( $(cat "${DRAIN_TRANSIENT_COUNTER}") + 1 ))
  echo "${n}" > "${DRAIN_TRANSIENT_COUNTER}"
  if [ "${n}" -eq 2 ]; then
    return 1
  fi
  return 0
}
export DRAIN_TRANSIENT_COUNTER
DRAIN_TIMEOUT_SEC=5 DRAIN_POLL_SEC=1 \
  output=$(drain_runner_vm drain_transient_poll_fail_mock 2>&1) && rc=0 || rc=$?
if [ "${rc}" -eq 0 ] \
  && printf '%s' "${output}" | grep -Fq "WARN: drain poll failed" \
  && printf '%s' "${output}" | grep -Fq "OK: runner idle" \
  && ! printf '%s' "${output}" | grep -Fq "drain cap"; then
  pass "drain_runner_vm: transient poll failure retries instead of proceeding immediately"
else
  fail "drain_runner_vm: transient poll failure did not retry within the drain budget"
  printf '%s\n' "${output}" | tail -5 >&2
fi
rm -f "${DRAIN_TRANSIENT_COUNTER}"
unset DRAIN_TRANSIENT_COUNTER

# drain_service_active_mock: podman ps is empty on the very first poll (a
# job still in `podman pull`/prepare_exec, before it has created its
# runner-${JOB_ID} container) but gitlab-runner.service is still reported
# active — a genuine "still shutting down" case, not idle. Idle-detection
# that short-circuits on an empty container list alone would report idle
# on the first poll (n=2); this mock only reports the service inactive
# starting on the second poll (n=3), so the counter must reach 3 before
# "OK: runner idle" appears.
DRAIN_SVC_ACTIVE_COUNTER=$(mktemp)
echo 0 > "${DRAIN_SVC_ACTIVE_COUNTER}"
drain_service_active_mock() {
  local n
  n=$(( $(cat "${DRAIN_SVC_ACTIVE_COUNTER}") + 1 ))
  echo "${n}" > "${DRAIN_SVC_ACTIVE_COUNTER}"
  if [ "${n}" -eq 1 ]; then
    return 0
  fi
  if [ "${n}" -eq 2 ]; then
    printf '__SVC__active\n'
    return 0
  fi
  printf '__SVC__inactive\n'
  return 0
}
export DRAIN_SVC_ACTIVE_COUNTER
DRAIN_TIMEOUT_SEC=5 DRAIN_POLL_SEC=1 \
  output=$(drain_runner_vm drain_service_active_mock 2>&1) && rc=0 || rc=$?
polls=$(cat "${DRAIN_SVC_ACTIVE_COUNTER}")
if [ "${rc}" -eq 0 ] \
  && printf '%s' "${output}" | grep -Fq "OK: runner idle" \
  && [ "${polls}" -ge 3 ]; then
  pass "drain_runner_vm: empty podman ps while service active is not idle on first poll"
else
  fail "drain_runner_vm: idle-detection short-circuited on empty podman ps without confirming the service stopped"
  printf '%s\n' "${output}" | tail -5 >&2
fi
rm -f "${DRAIN_SVC_ACTIVE_COUNTER}"
unset DRAIN_SVC_ACTIVE_COUNTER

# drain_cap_overrun_mock: signal succeeds, podman always reports a
# runner-* container still running — the cap must still be honored.
DRAIN_CAP_OVERRUN_COUNTER=$(mktemp)
echo 0 > "${DRAIN_CAP_OVERRUN_COUNTER}"
drain_cap_overrun_mock() {
  local n
  n=$(( $(cat "${DRAIN_CAP_OVERRUN_COUNTER}") + 1 ))
  echo "${n}" > "${DRAIN_CAP_OVERRUN_COUNTER}"
  if [ "${n}" -eq 1 ]; then
    return 0
  fi
  echo "runner-abc123"
  return 0
}
export DRAIN_CAP_OVERRUN_COUNTER
DRAIN_TIMEOUT_SEC=1 DRAIN_POLL_SEC=1 assert_succeeds_with \
  "drain_runner_vm: cap overrun warns and proceeds" \
  "WARN: drain cap 1s reached" \
  drain_runner_vm drain_cap_overrun_mock
rm -f "${DRAIN_CAP_OVERRUN_COUNTER}"
unset DRAIN_CAP_OVERRUN_COUNTER

# Non-numeric DRAIN_TIMEOUT_SEC / DRAIN_POLL_SEC must fall back to the
# documented defaults rather than being passed through to `test`/`timeout`.
DRAIN_TIMEOUT_SEC=notanumber DRAIN_POLL_SEC=1 assert_succeeds_with \
  "drain_runner_vm: non-numeric DRAIN_TIMEOUT_SEC falls back to 600" \
  "cap 600s" \
  drain_runner_vm drain_idle_mock

DRAIN_TIMEOUT_SEC=5 DRAIN_POLL_SEC=-1 assert_succeeds_with \
  "drain_runner_vm: negative DRAIN_POLL_SEC falls back to 5 (still idles fine)" \
  "OK: runner idle" \
  drain_runner_vm drain_idle_mock

# runner_user, when passed, must thread sudo -u into both the podman query
# and the remote config edit — not just rely on whichever identity the
# caller's SSH plumbing happens to connect as.
DRAIN_CAPTURE_FILE=$(mktemp)
drain_capture_mock() {
  printf '%s\n' "$1" >> "${DRAIN_CAPTURE_FILE}"
  return 0
}
export DRAIN_CAPTURE_FILE
DRAIN_TIMEOUT_SEC=5 DRAIN_POLL_SEC=1 drain_runner_vm drain_capture_mock "fedora" >/dev/null 2>&1
if grep -Fq "sudo -u fedora podman ps" "${DRAIN_CAPTURE_FILE}" \
  && grep -Fq "sudo -u fedora test -f" "${DRAIN_CAPTURE_FILE}" \
  && grep -Fq "sudo -u fedora sed -i" "${DRAIN_CAPTURE_FILE}"; then
  pass "drain_runner_vm: runner_user threads sudo -u into podman query and config edit"
else
  fail "drain_runner_vm: runner_user not threaded into remote commands"
  cat "${DRAIN_CAPTURE_FILE}" >&2
fi
: > "${DRAIN_CAPTURE_FILE}"

DRAIN_TIMEOUT_SEC=5 DRAIN_POLL_SEC=1 drain_runner_vm drain_capture_mock >/dev/null 2>&1
if grep -Fq "podman ps" "${DRAIN_CAPTURE_FILE}" && ! grep -Fq "sudo -u" "${DRAIN_CAPTURE_FILE}"; then
  pass "drain_runner_vm: without runner_user, remote commands have no sudo -u"
else
  fail "drain_runner_vm: expected no 'sudo -u' when runner_user is unset"
  cat "${DRAIN_CAPTURE_FILE}" >&2
fi

# Without runner_user, the config edit runs via plain `sudo`, whose `sed -i`
# rewrites config.toml through a new inode owned by root:root — clobbering
# setup.sh's chown of /etc/gitlab-runner to RUNNER_USER. drain_runner_vm
# must stat the pre-edit owner/mode and restore them via `sudo chown`/`sudo
# chmod` after the sed calls. This regression check pins that restore so a
# later removal of the chown/chmod lines fails a test instead of silently
# reintroducing the root:root ownership bug.
if grep -Fq "stat -c '%U:%G'" "${DRAIN_CAPTURE_FILE}" \
  && grep -Fq "stat -c '%a'" "${DRAIN_CAPTURE_FILE}" \
  && grep -Fq "sudo chown" "${DRAIN_CAPTURE_FILE}" \
  && grep -Fq "sudo chmod" "${DRAIN_CAPTURE_FILE}"; then
  pass "drain_runner_vm: without runner_user, config ownership/mode is stat'd and restored"
else
  fail "drain_runner_vm: expected stat+chown+chmod ownership restore when runner_user is unset"
  cat "${DRAIN_CAPTURE_FILE}" >&2
fi
rm -f "${DRAIN_CAPTURE_FILE}"
unset DRAIN_CAPTURE_FILE

# drain_runner_vm runs remote_exec via `export -f` + `timeout ... bash -c`,
# so the function body executes in a subprocess. Variables the function
# closes over must be exported by the caller — otherwise they're empty
# in the subprocess. This test verifies that a mock closing over an outer
# variable can see it when run through drain_runner_vm's timeout+bash-c
# mechanism.
DRAIN_CLOSURE_MARKER="closure-ok-$(date +%s)"
export DRAIN_CLOSURE_MARKER
drain_closure_mock() {
  if [ -z "${DRAIN_CLOSURE_MARKER:-}" ]; then
    echo "CLOSURE_EMPTY" >&2
    return 1
  fi
  echo "${DRAIN_CLOSURE_MARKER}"
  return 0
}
# shellcheck disable=SC2034  # DRAIN_TIMEOUT_SEC/DRAIN_POLL_SEC consumed by drain_runner_vm
DRAIN_TIMEOUT_SEC=5 DRAIN_POLL_SEC=1 \
  output=$(drain_runner_vm drain_closure_mock 2>&1) && rc=0 || rc=$?
if printf '%s' "${output}" | grep -Fq "OK: runner idle"; then
  pass "drain_runner_vm: exported closure variables visible in timeout+bash-c subprocess"
else
  fail "drain_runner_vm: closure variable not visible in subprocess"
  printf '%s\n' "${output}" | tail -5 >&2
fi
unset DRAIN_CLOSURE_MARKER

echo "== static regressions =="

# drain_runner_vm runs remote_exec via timeout+bash-c, so callers must
# export every variable the SSH helper closures read. Pin the export
# lines as a regression check — dropping them silently breaks drain.
if grep -Fq 'export vm_name GCP_PROJECT GCP_ZONE GCP_USE_IAP' "${DELETE_GCP}"; then
  pass "delete-gcp-vm.sh: exports closure variables before drain_runner_vm"
else
  fail "delete-gcp-vm.sh: missing 'export vm_name GCP_PROJECT GCP_ZONE GCP_USE_IAP' before drain_runner_vm"
fi
if grep -Fq 'export vm_name NAMESPACE VM_USER' "${DELETE_OCP}"; then
  pass "delete-openshift-vm.sh: exports closure variables before drain_runner_vm"
else
  fail "delete-openshift-vm.sh: missing 'export vm_name NAMESPACE VM_USER' before drain_runner_vm"
fi

# Idle detection must confirm gitlab-runner.service is no longer active, not
# just an empty podman ps — pin the is-active probe as a regression check.
if grep -Fq 'systemctl is-active gitlab-runner.service' "${SCRIPT_DIR}/lib.sh"; then
  pass "lib.sh: idle poll confirms gitlab-runner.service is no longer active"
else
  fail "lib.sh: idle poll missing gitlab-runner.service is-active probe"
fi

# The no-runner_user config edit must restore config.toml's pre-edit
# owner/mode after `sudo sed -i` (which rewrites through a new root:root
# inode) — pin the stat+chown+chmod restore as a regression check so a
# drive-by deletion is caught even if the capture-mock test above is
# refactored.
if grep -Fq 'sudo chown "\$cfg_owner"' "${SCRIPT_DIR}/lib.sh" \
  && grep -Fq 'sudo chmod "\$cfg_mode"' "${SCRIPT_DIR}/lib.sh"; then
  pass "lib.sh: no-runner_user config edit restores owner/mode after sed -i"
else
  fail "lib.sh: no-runner_user config edit missing owner/mode restore"
fi

# gcp_drain_ssh's non-IAP branch must use a per-run known-hosts file (like
# create-gcp-vm.sh's direct-IP path), not gcloud's default known-hosts file.
if grep -Fq 'UserKnownHostsFile=${GCP_KNOWN_HOSTS}' "${DELETE_GCP}"; then
  pass "delete-gcp-vm.sh: non-IAP drain SSH uses a per-run known-hosts file"
else
  fail "delete-gcp-vm.sh: non-IAP drain SSH missing per-run UserKnownHostsFile"
fi

# shutdown_timeout must be written to the global TOML table (line 1),
# not appended or inserted before [[runners]] (wrong table scope).
if grep -Fq "1i shutdown_timeout" "${SCRIPT_DIR}/lib.sh"; then
  pass "lib.sh: shutdown_timeout inserted at line 1 (global table)"
else
  fail "lib.sh: shutdown_timeout not inserted at line 1 — may land in wrong TOML table"
fi

if grep -E 'timeout[[:space:]]+"\$\{cmd_timeout\}"[[:space:]]+"\$\{remote_exec\}"' "${SCRIPT_DIR}/lib.sh" >/dev/null; then
  fail "lib.sh: timeout still wraps remote_exec directly (timeout cannot call shell functions)"
else
  pass "lib.sh: timeout wraps remote_exec via bash -c (shell functions inherit via export -f)"
fi
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

# Later trap sites (step 5 onward) call cleanup_runner unconditionally, so the
# RUNNER_TOKEN branch must alias it to cleanup_vm before those traps install —
# pin the alias down so a future refactor can't drop or reorder it silently.
if grep -Fq 'cleanup_runner() { cleanup_vm; }' "${CREATE_GCP}" \
  && grep -Fq 'cleanup_runner() { cleanup_vm; }' "${CREATE_OCP}"; then
  pass "both create scripts alias cleanup_runner to cleanup_vm in RUNNER_TOKEN mode"
else
  fail "create scripts missing 'cleanup_runner() { cleanup_vm; }' alias in RUNNER_TOKEN mode"
fi

if grep -Fq 'executor/gateway.sh' "${CREATE_GCP}" \
  && grep -Fq 'executor/gateway.sh' "${CREATE_OCP}"; then
  pass "both create scripts copy and checksum executor/gateway.sh"
else
  fail "create scripts missing executor/gateway.sh in copy/checksum lists"
fi

# --no-service-account --no-scopes must be actual create-command flags
# (indented, not only mentioned in comments) so new VMs get no default
# Compute SA. See #7254.
if grep -E '^[[:space:]]*--no-service-account' "${CREATE_GCP}" >/dev/null \
  && grep -E '^[[:space:]]*--no-scopes' "${CREATE_GCP}" >/dev/null; then
  pass "gcp: VM create passes --no-service-account and --no-scopes"
else
  fail "gcp: VM create missing --no-service-account/--no-scopes (metadata SA-token theft)"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
