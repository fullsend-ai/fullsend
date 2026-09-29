#!/usr/bin/env bash
# podman-prune_test.sh — Tests for podman-prune.sh (in-flight skip, keep-list,
# dangling vs tagged removal) and for setup.sh installing the timer.
#
# Run from the repo root:
#   bash hack/gitlab-runner-vm/podman-prune_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PRUNE="${SCRIPT_DIR}/podman-prune.sh"
SETUP="${SCRIPT_DIR}/setup.sh"
CREATE_GCP="${SCRIPT_DIR}/create-gcp-vm.sh"
CREATE_OCP="${SCRIPT_DIR}/create-openshift-vm.sh"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

FAKE_HOME=$(mktemp -d)
SHIM_DIR=$(mktemp -d)
trap 'rm -rf "${FAKE_HOME}" "${SHIM_DIR}"' EXIT
export HOME="${FAKE_HOME}"
export PATH="${SHIM_DIR}:${PATH}"

PODMAN_LOG="${SHIM_DIR}/podman.log"
PS_A_FILE="${SHIM_DIR}/ps-a.txt"
IMAGES_FILE="${SHIM_DIR}/images.txt"
KEEP_FILE="${FAKE_HOME}/.config/fullsend-gitlab-runner/keep-images"
export PODMAN_LOG PS_A_FILE IMAGES_FILE

mkdir -p "${FAKE_HOME}/.config/fullsend-gitlab-runner"

write_podman_stub() {
  cat > "${SHIM_DIR}/podman" <<'STUB'
#!/bin/sh
echo "$@" >> "${PODMAN_LOG}"
# First non-option argument is the podman command.
cmd=""
for a in "$@"; do
  case "$a" in
    -*) ;;
    *)
      if [ -z "$cmd" ]; then cmd="$a"; fi
      ;;
  esac
done
case "$cmd" in
  ps)
    cat "${PS_A_FILE}"
    exit 0
    ;;
  images)
    cat "${IMAGES_FILE}"
    exit 0
    ;;
  container|image|rmi)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
STUB
  chmod +x "${SHIM_DIR}/podman"
}

reset_prune_fixtures() {
  : > "${PODMAN_LOG}"
  : > "${PS_A_FILE}"
  : > "${IMAGES_FILE}"
  cat > "${KEEP_FILE}" <<'KEEP'
# warm cache
ghcr.io/fullsend-ai/fullsend-runner:v1
ghcr.io/nvidia/openshell/supervisor:0.0.116
KEEP
}

run_prune() {
  RUN_PRUNE_RC=0
  RUN_PRUNE_OUT=$(
    FULLSEND_PODMAN_KEEP_IMAGES="${KEEP_FILE}" bash "${PRUNE}"
  ) && RUN_PRUNE_RC=0 || RUN_PRUNE_RC=$?
}

logged() {
  grep -Fq "$1" "${PODMAN_LOG}"
}

echo "== static contracts =="
if grep -Eq 'Idempotent: safe to re-run' "${PRUNE}"; then
  pass "podman-prune.sh carries an idempotency contract comment"
else
  fail "podman-prune.sh missing idempotency contract comment"
fi

if grep -Eq 'podman (stop|rm -f|rmi -f|system prune)' "${PRUNE}"; then
  fail "podman-prune.sh must not stop containers or force-remove images"
else
  pass "podman-prune.sh never stops containers or force-removes images"
fi

if grep -Fq 'install_podman_prune' "${SETUP}" \
  && grep -Fq 'fullsend-podman-prune.timer' "${SETUP}"; then
  pass "setup.sh installs fullsend-podman-prune.timer"
else
  fail "setup.sh missing install_podman_prune / fullsend-podman-prune.timer"
fi

if grep -Fq 'enable --now fullsend-podman-prune.timer' "${SETUP}"; then
  pass "setup.sh enables the prune timer"
else
  fail "setup.sh does not enable --now fullsend-podman-prune.timer"
fi

if grep -Fq 'keep-images' "${SETUP}"; then
  pass "setup.sh writes a prune keep-images file"
else
  fail "setup.sh does not write the prune keep-images file"
fi

if grep -Fq 'podman-prune.sh' "${CREATE_GCP}" \
  && grep -Fq 'podman-prune.sh' "${CREATE_OCP}"; then
  pass "both create scripts copy and checksum podman-prune.sh"
else
  fail "create scripts missing podman-prune.sh in copy/checksum lists"
fi

PREPARE="${SCRIPT_DIR}/executor/prepare.sh"
CLEANUP="${SCRIPT_DIR}/executor/cleanup.sh"
GATEWAY="${SCRIPT_DIR}/executor/gateway.sh"
if grep -Fq 'prune_unused_podman_storage' "${GATEWAY}" \
  && grep -Fq 'prune_unused_podman_storage' "${PREPARE}" \
  && grep -Fq 'prune_unused_podman_storage' "${CLEANUP}"; then
  pass "prepare.sh and cleanup.sh call prune_unused_podman_storage"
else
  fail "prepare.sh/cleanup.sh/gateway.sh missing prune_unused_podman_storage"
fi

if grep -Fq 'acquire_podman_prune_lock' "${GATEWAY}" \
  && grep -Fq 'release_podman_prune_lock' "${GATEWAY}" \
  && grep -Fq 'acquire_podman_prune_lock' "${PREPARE}" \
  && grep -Fq 'release_podman_prune_lock' "${PREPARE}"; then
  pass "prepare.sh serializes prune+pull+create via gateway.sh's lock helpers"
else
  fail "prepare.sh/gateway.sh missing acquire_podman_prune_lock/release_podman_prune_lock"
fi

if grep -Fq 'TimeoutStartSec=infinity' "${SETUP}"; then
  pass "fullsend-podman-prune.service does not inherit the systemd manager's default start timeout"
else
  fail "fullsend-podman-prune.service missing TimeoutStartSec=infinity"
fi

echo "== in-flight skip =="
write_podman_stub
reset_prune_fixtures
printf 'runner-42|running\n' > "${PS_A_FILE}"
run_prune
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "in-flight running container should skip with rc=0 (rc=${RUN_PRUNE_RC}): ${RUN_PRUNE_OUT}"
elif logged 'container prune' || logged 'image prune' || logged 'rmi'; then
  fail "in-flight running container still pruned: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'skipping prune: in-flight job container runner-42'; then
  pass "running runner-* container skips prune"
else
  fail "running runner-* did not report skip: ${RUN_PRUNE_OUT}"
fi

reset_prune_fixtures
printf 'openshell-abc|running\n' > "${PS_A_FILE}"
run_prune
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "in-flight openshell container should skip with rc=0 (rc=${RUN_PRUNE_RC})"
elif logged 'container prune' || logged 'rmi'; then
  fail "in-flight openshell container still pruned: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'openshell-abc'; then
  pass "running openshell-* container skips prune"
else
  fail "running openshell-* did not report skip: ${RUN_PRUNE_OUT}"
fi

reset_prune_fixtures
printf 'runner-7|created\n' > "${PS_A_FILE}"
run_prune
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "created runner-* should skip with rc=0 (rc=${RUN_PRUNE_RC})"
elif logged 'container prune' || logged 'rmi'; then
  fail "created runner-* still pruned (would race prepare.sh): $(tr '\n' '|' < "${PODMAN_LOG}")"
elif printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'runner-7 (created)'; then
  pass "created runner-* container skips prune (prepare.sh create→start race)"
else
  fail "created runner-* did not report skip: ${RUN_PRUNE_OUT}"
fi

echo "== idle prune =="
reset_prune_fixtures
printf 'runner-9|exited\n' > "${PS_A_FILE}"
cat > "${IMAGES_FILE}" <<'IMAGES'
ghcr.io/fullsend-ai/fullsend-runner:v1
ghcr.io/fullsend-ai/fullsend-runner:old
<none>:<none>
ghcr.io/nvidia/openshell/supervisor:0.0.116
IMAGES
run_prune
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "idle prune should succeed (rc=${RUN_PRUNE_RC}): ${RUN_PRUNE_OUT}"
elif ! logged 'container prune -f --filter until=30m'; then
  fail "idle prune did not prune old stopped containers: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif ! logged 'image prune -f'; then
  fail "idle prune did not prune dangling images: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif ! logged 'rmi -- ghcr.io/fullsend-ai/fullsend-runner:old'; then
  fail "idle prune did not rmi superseded tagged image: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif logged 'rmi -- ghcr.io/fullsend-ai/fullsend-runner:v1' \
  || logged 'rmi -- ghcr.io/nvidia/openshell/supervisor:0.0.116'; then
  fail "idle prune removed a keep-list image: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif logged 'rmi -- <none>:<none>' || logged 'rmi -- sha256:ccc'; then
  fail "idle prune rmi'd a dangling image (image prune should handle those)"
elif printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'keeping ghcr.io/fullsend-ai/fullsend-runner:v1' \
  && printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'removing unused image ghcr.io/fullsend-ai/fullsend-runner:old'; then
  pass "idle prune removes superseded images and preserves the warm cache"
else
  fail "idle prune output mismatch: ${RUN_PRUNE_OUT}"
fi

echo "== missing keep-file =="
reset_prune_fixtures
rm -f "${KEEP_FILE}"
: > "${PS_A_FILE}"
cat > "${IMAGES_FILE}" <<'IMAGES'
ghcr.io/fullsend-ai/fullsend-runner:v1
IMAGES
run_prune
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "missing keep-file should still dangling-prune (rc=${RUN_PRUNE_RC}): ${RUN_PRUNE_OUT}"
elif logged 'rmi'; then
  fail "missing keep-file still rmi'd tagged images: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif logged 'image prune -f' \
  && printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'keep-file'; then
  pass "missing keep-file skips tagged rmi and still prunes dangling images"
else
  fail "missing keep-file path mismatch: ${RUN_PRUNE_OUT} log=$(tr '\n' '|' < "${PODMAN_LOG}")"
fi

echo "== extra keep-ref protects the job's own image =="
reset_prune_fixtures
: > "${PS_A_FILE}"
cat > "${IMAGES_FILE}" <<'IMAGES'
ghcr.io/fullsend-ai/fullsend-runner:v1
registry.example.com/job:latest
ghcr.io/nvidia/openshell/supervisor:0.0.116
IMAGES
RUN_PRUNE_RC=0
RUN_PRUNE_OUT=$(
  FULLSEND_PODMAN_KEEP_IMAGES="${KEEP_FILE}" \
  FULLSEND_PODMAN_PRUNE_EXTRA_KEEP="registry.example.com/job:latest" \
  bash "${PRUNE}"
) && RUN_PRUNE_RC=0 || RUN_PRUNE_RC=$?
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "extra keep-ref run should succeed (rc=${RUN_PRUNE_RC}): ${RUN_PRUNE_OUT}"
elif logged 'rmi -- registry.example.com/job:latest'; then
  fail "extra keep-ref did not protect the job's own image: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'keeping registry.example.com/job:latest'; then
  pass "FULLSEND_PODMAN_PRUNE_EXTRA_KEEP protects a ref not in the keep-file"
else
  fail "extra keep-ref output mismatch: ${RUN_PRUNE_OUT}"
fi

echo "== unrelated running container does not skip =="
reset_prune_fixtures
printf 'buildkit|running\n' > "${PS_A_FILE}"
cat > "${IMAGES_FILE}" <<'IMAGES'
ghcr.io/fullsend-ai/fullsend-runner:old
IMAGES
run_prune
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "unrelated container should not skip (rc=${RUN_PRUNE_RC}): ${RUN_PRUNE_OUT}"
elif printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'skipping prune'; then
  fail "unrelated container skipped prune: ${RUN_PRUNE_OUT}"
elif logged 'container prune -f --filter until=30m'; then
  pass "unrelated running container does not skip prune"
else
  fail "unrelated container path did not prune: $(tr '\n' '|' < "${PODMAN_LOG}")"
fi

echo "== lock held by prepare.sh skips the timer's run =="
reset_prune_fixtures
: > "${PS_A_FILE}"
LOCK_DIR="${FAKE_HOME}/.local/state/fullsend-gitlab-runner"
mkdir -p "${LOCK_DIR}"
LOCK_FILE="${LOCK_DIR}/podman-prune.lock"
(
  exec 9>"${LOCK_FILE}"
  flock -x 9
  sleep 2
) &
HOLDER_PID=$!
sleep 0.3
run_prune
wait "${HOLDER_PID}"
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "run held by another lock holder should skip with rc=0 (rc=${RUN_PRUNE_RC}): ${RUN_PRUNE_OUT}"
elif logged 'container prune' || logged 'image prune' || logged 'rmi'; then
  fail "prune ran while another process held the lock: $(tr '\n' '|' < "${PODMAN_LOG}")"
elif printf '%s' "${RUN_PRUNE_OUT}" | grep -Fq 'skipping prune: lock held'; then
  pass "a lock held by another process (e.g. prepare.sh) skips the timer's run"
else
  fail "held-lock run did not report skip: ${RUN_PRUNE_OUT}"
fi

echo "== FULLSEND_PODMAN_PRUNE_LOCK_HELD trusts an already-serialized caller =="
reset_prune_fixtures
printf 'runner-9|exited\n' > "${PS_A_FILE}"
cat > "${IMAGES_FILE}" <<'IMAGES'
ghcr.io/fullsend-ai/fullsend-runner:old
IMAGES
(
  exec 9>"${LOCK_FILE}"
  flock -x 9
  sleep 2
) &
HOLDER_PID=$!
sleep 0.3
RUN_PRUNE_RC=0
RUN_PRUNE_OUT=$(
  FULLSEND_PODMAN_KEEP_IMAGES="${KEEP_FILE}" \
  FULLSEND_PODMAN_PRUNE_LOCK_HELD=1 \
  bash "${PRUNE}"
) && RUN_PRUNE_RC=0 || RUN_PRUNE_RC=$?
wait "${HOLDER_PID}"
if [ "${RUN_PRUNE_RC}" -ne 0 ]; then
  fail "FULLSEND_PODMAN_PRUNE_LOCK_HELD run should succeed (rc=${RUN_PRUNE_RC}): ${RUN_PRUNE_OUT}"
elif ! logged 'image prune -f'; then
  fail "FULLSEND_PODMAN_PRUNE_LOCK_HELD still skipped instead of trusting the caller: ${RUN_PRUNE_OUT}"
else
  pass "FULLSEND_PODMAN_PRUNE_LOCK_HELD bypasses the self-lock for a caller that already holds it"
fi

echo "== install_podman_prune =="
# Reuse setup_test.sh's pattern: source setup.sh and call the function with
# stubbed user_systemctl (systemctl --user).
SYSTEMCTL_LOG="${SHIM_DIR}/systemctl.log"
printf '#!/bin/sh\necho "$@" >> "%s"\nexit 0\n' "${SYSTEMCTL_LOG}" > "${SHIM_DIR}/systemctl"
chmod +x "${SHIM_DIR}/systemctl"
: > "${SYSTEMCTL_LOG}"

INSTALL_RC=0
RUNNER_IMAGE="ghcr.io/fullsend-ai/fullsend-runner:v1"
export RUNNER_IMAGE
INSTALL_OUT=$(
  # shellcheck source=setup.sh
  source "${SETUP}"
  install_podman_prune
) && INSTALL_RC=0 || INSTALL_RC=$?

INSTALLED_SCRIPT="${FAKE_HOME}/.local/lib/fullsend/podman-prune.sh"
INSTALLED_KEEP="${FAKE_HOME}/.config/fullsend-gitlab-runner/keep-images"
INSTALLED_SERVICE="${FAKE_HOME}/.config/systemd/user/fullsend-podman-prune.service"
INSTALLED_TIMER="${FAKE_HOME}/.config/systemd/user/fullsend-podman-prune.timer"

if [ "${INSTALL_RC}" -ne 0 ]; then
  fail "install_podman_prune should succeed (rc=${INSTALL_RC}): ${INSTALL_OUT}"
elif [ ! -x "${INSTALLED_SCRIPT}" ]; then
  fail "install_podman_prune did not install an executable prune script"
elif [ ! -f "${INSTALLED_SERVICE}" ] || [ ! -f "${INSTALLED_TIMER}" ]; then
  fail "install_podman_prune did not write systemd user units"
elif ! grep -Fq 'ghcr.io/fullsend-ai/fullsend-runner:v1' "${INSTALLED_KEEP}" \
  || ! grep -Fq 'ghcr.io/nvidia/openshell/supervisor:' "${INSTALLED_KEEP}"; then
  fail "keep-images missing runner or supervisor image"
elif ! grep -Fq 'enable --now fullsend-podman-prune.timer' "${SYSTEMCTL_LOG}"; then
  fail "install_podman_prune did not enable the timer: $(tr '\n' '|' < "${SYSTEMCTL_LOG}")"
elif ! grep -Fq 'WantedBy=timers.target' "${INSTALLED_TIMER}"; then
  fail "prune timer unit missing WantedBy=timers.target"
elif ! grep -Fq 'ExecStart=%h/.local/lib/fullsend/podman-prune.sh' "${INSTALLED_SERVICE}"; then
  fail "prune service ExecStart does not point at the installed script"
else
  pass "install_podman_prune writes script, keep-list, units, and enables the timer"
fi

# Re-run converges (overwrites units, re-enables).
: > "${SYSTEMCTL_LOG}"
INSTALL2_RC=0
RUNNER_IMAGE="ghcr.io/fullsend-ai/fullsend-runner:v2"
INSTALL2_OUT=$(
  # shellcheck source=setup.sh
  source "${SETUP}"
  install_podman_prune
) && INSTALL2_RC=0 || INSTALL2_RC=$?
if [ "${INSTALL2_RC}" -ne 0 ]; then
  fail "re-run of install_podman_prune should succeed (rc=${INSTALL2_RC}): ${INSTALL2_OUT}"
elif ! grep -Fq 'ghcr.io/fullsend-ai/fullsend-runner:v2' "${INSTALLED_KEEP}"; then
  fail "re-run did not refresh keep-images to the current RUNNER_IMAGE"
elif ! grep -Fq 'enable --now fullsend-podman-prune.timer' "${SYSTEMCTL_LOG}"; then
  fail "re-run did not re-enable the timer"
else
  pass "re-run of install_podman_prune refreshes keep-images and re-enables the timer"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
