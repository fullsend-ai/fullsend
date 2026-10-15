#!/usr/bin/env bash
#
# create-gcp-vm.sh — Create and provision a GitLab Runner VM on GCE.
#
# This script:
#   1. Auto-numbers the VM (fullsend-gitlab-runner-01, -02, ...)
#   2. Creates a GCE VM via gcloud compute instances create
#      with --no-service-account --no-scopes (the VM needs no Compute
#      SA; the default editor SA would expose a stealable metadata token)
#   3. Waits for SSH readiness, installs packages via dnf, then grows the
#      root partition and Btrfs filesystem to fill the 30 GiB boot disk and
#      verifies disk/partition/filesystem capacity (grow-root-fs.sh)
#   4. Registers a new runner via the GitLab API, or joins an existing
#      runner pool when RUNNER_TOKEN is set (runner-hub)
#   5. Copies setup files and runs setup.sh to configure the custom
#      executor, OpenShell gateway, and pre-pull images, then verifies the
#      service, executor, rootless Podman, registration, and storage
#
# When done, the runner is online and accepting jobs tagged with RUNNER_TAG.
#
# Recovering from a failed run, or converging an existing runner onto the
# current provisioning: re-run with --resume NUMBER and the same environment.
# Resume targets the existing VM NUMBER in GCP_PROJECT/GCP_ZONE (it refuses a
# missing or stopped VM, never creates one) and repeats steps 3-5 with the
# same files and setup.sh as a fresh create, so later provisioning changes
# reach existing runners. Everything it runs is idempotent, so it is safe to
# repeat until it succeeds; on a healthy runner it changes nothing. It also:
#   - grows the boot disk to BOOT_DISK_GB (30 GiB) when it is smaller — a
#     larger disk is left alone, a disk is never shrunk — then grows the
#     root partition and filesystem;
#   - runs setup as the VM's existing gitlab-runner service user (the User=
#     of its systemd drop-in / owner of /etc/gitlab-runner), not as whoever
#     the SSH login maps to, so a different operator keeps the runner's
#     home, workspace, and rootless Podman storage. A VM with no service user
#     yet uses the login user, as a fresh create does. RUNNER_USER, if set,
#     must name that user; conflicting state is refused;
#   - never adds a second registration for the VM:
#       - RUNNER_TOKEN mode never creates registrations (setup.sh refuses a
#         VM configured with another token or GitLab instance).
#       - GL_TOKEN mode looks up the runner registered for this VM
#         ("GCP_PROJECT/vm-name") first. If that runner exists and the VM holds
#         its config, it is reused. If no runner exists (a failed run
#         deregisters the runner it created), a new one is registered and any
#         stale VM-side config is replaced. If a runner exists but the VM never
#         received its token, or the state is ambiguous, resume refuses —
#         delete and recreate instead.
# setup.sh stops gitlab-runner while it reconfigures and restarts it at the
# end, so a job running on the VM is interrupted: resume an idle runner, or
# pause it in GitLab first.
#
# setup.sh (step 5) is idempotent — safe to re-run in place as a
# developer/debug convenience. Recreation is the two-command compliance
# path: drain and delete with ./delete-gcp-vm.sh, then re-run create.
#
# Two modes:
#   RUNNER_TOKEN — join an existing runner pool. Multiple VMs share one
#                  GitLab runner registration (glrt-* token). GL_TOKEN and
#                  PROJECT_ID/GROUP_ID are not required.
#   GL_TOKEN     — register a new runner via the GitLab API (existing
#                  behavior). Requires GL_TOKEN and exactly one of
#                  PROJECT_ID or GROUP_ID.
#
# Prerequisites (one-time GCP setup):
#   - Compute Engine API and Cloud IAP API enabled in the target project
#   - VPC network "gitlab-runners" (auto-subnets, or set GCP_SUBNET for custom-mode)
#   - Firewall rules:
#     - gitlab-runners-allow-iap (ingress TCP:22 from 35.235.240.0/20, tag: gitlab-runner)
#     - gitlab-runners-allow-egress (egress TCP:80,443, tag: gitlab-runner)
#   - IAM: operator needs roles/iap.tunnelResourceAccessor on the project
#
# Required environment variables:
#   GITLAB_URL   — GitLab instance URL (e.g. https://gitlab.example.com)
#   GCP_PROJECT  — GCP project ID
#   RUNNER_IMAGE — image pre-pulled as warm cache (e.g. ghcr.io/org/runner:v1.2.3)
#
# Mode-specific environment variables:
#   RUNNER_TOKEN — GitLab runner authentication token (glrt-*). When set,
#                  the VM joins an existing runner pool; GL_TOKEN and
#                  PROJECT_ID/GROUP_ID are not required.
#   GL_TOKEN     — GitLab personal access token (Owner role on the target
#                  group or project, scopes: create_runner + manage_runner + api).
#                  Required unless RUNNER_TOKEN is set.
#   PROJECT_ID   — GitLab project ID (mutually exclusive with GROUP_ID).
#                  Required with GL_TOKEN unless RUNNER_TOKEN is set.
#   GROUP_ID     — GitLab group ID  (mutually exclusive with PROJECT_ID).
#                  Required with GL_TOKEN unless RUNNER_TOKEN is set.
#                  GROUP_ID is recommended for platform-service deployments.
#
# Optional environment variables:
#   GCP_ZONE              — GCE zone (default: us-east1-b)
#   GCP_MACHINE_TYPE      — machine type (default: e2-standard-4)
#   GCP_NETWORK           — VPC network (default: gitlab-runners)
#   GCP_SUBNET            — VPC subnet (required for custom-mode VPCs; omit for auto-mode)
#   GCP_IMAGE_FAMILY      — GCE image family (default: fedora-cloud-43-x86-64)
#   GCP_IMAGE_PROJECT     — GCE image project (default: fedora-cloud)
#   RUNNER_TAG            — runner tag for job matching (default: fullsend-gitlab-runner)
#   GITLAB_RUNNER_VERSION — gitlab-runner version to install (default: 19.2.1)
#   OPENSHELL_VERSION     — OpenShell version (default: from .github/scripts/openshell-version.sh)
#   GCP_USE_IAP           — use IAP tunneling for SSH (default: true). Set to
#                           false to create the VM with an external IP and SSH
#                           directly. Useful for initial provisioning; the
#                           external IP can be removed afterward.
#   RUNNER_ACCESS_LEVEL   — not_protected (default) or ref_protected. Protected
#                           runners only pick up jobs on protected branches and
#                           tags, so merge-request pipelines never match.
#   RUNNER_USER           — --resume only: the gitlab-runner service user you
#                           expect on the VM (default: detected). Resume refuses
#                           when the VM's service user differs.
#
# Arguments:
#   [NUMBER]          — optional runner number (e.g. 01, 03). Auto-increments if omitted.
#   --resume NUMBER   — finish provisioning, or repair, the existing VM NUMBER
#                       (see "Recovering from a failed run" above). Takes the
#                       same environment variables as the create run.
#
# Examples:
#   # Group-scoped runner (recommended):
#   GL_TOKEN=glpat-xxx GROUP_ID=12345 \
#     GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-gcp-vm.sh
#
#   # Project-scoped runner:
#   GL_TOKEN=glpat-xxx PROJECT_ID=12345 \
#     GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-gcp-vm.sh
#
#   # Explicit runner number:
#   GL_TOKEN=glpat-xxx GROUP_ID=12345 \
#     GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-gcp-vm.sh 01
#
#   # Join an existing runner pool (runner-hub):
#   RUNNER_TOKEN=glrt-xxx \
#     GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-gcp-vm.sh 05
#
#   # Finish or repair VM 05 (same environment as the create):
#   RUNNER_TOKEN=glrt-xxx \
#     GITLAB_URL=https://gitlab.example.com \
#     GCP_PROJECT=my-gcp-project \
#     RUNNER_IMAGE=ghcr.io/org/runner:v1.2.3 ./create-gcp-vm.sh --resume 05
#
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
GCP_PROJECT="${GCP_PROJECT:-}"
GCP_ZONE="${GCP_ZONE:-us-east1-b}"
GCP_MACHINE_TYPE="${GCP_MACHINE_TYPE:-e2-standard-4}"
GCP_NETWORK="${GCP_NETWORK:-gitlab-runners}"
GCP_SUBNET="${GCP_SUBNET:-}"
GCP_IMAGE_FAMILY="${GCP_IMAGE_FAMILY:-fedora-cloud-43-x86-64}"
GCP_IMAGE_PROJECT="${GCP_IMAGE_PROJECT:-fedora-cloud}"
GCP_USE_IAP="${GCP_USE_IAP:-true}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
RUNNER_IMAGE="${RUNNER_IMAGE:-}"
# ref_protected restricts the runner to jobs on protected branches and tags.
# Merge-request pipelines run on the (unprotected) source ref, so the default
# is not_protected; scoping comes from runner_type + locked/run_untagged settings.
RUNNER_ACCESS_LEVEL="${RUNNER_ACCESS_LEVEL:-not_protected}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Source the central gitlab-runner version pin (shared with setup.sh).
_runner_version_sh="${SCRIPT_DIR}/gitlab-runner-version.sh"
if [ -f "${_runner_version_sh}" ]; then
  # shellcheck source=gitlab-runner-version.sh
  source "${_runner_version_sh}"
fi
GITLAB_RUNNER_VERSION="${GITLAB_RUNNER_VERSION:-19.2.1}"
# Source the repo-wide OpenShell version pin (Renovate-tracked).
_openshell_version_sh="${SCRIPT_DIR}/../../.github/scripts/openshell-version.sh"
if [ -f "${_openshell_version_sh}" ]; then
  # shellcheck source=../../.github/scripts/openshell-version.sh
  source "${_openshell_version_sh}"
fi
# Fallback only when the pin file is absent; keep in step with openshell-version.sh.
OPENSHELL_VERSION="${OPENSHELL_VERSION:-0.1.2}"
PREFIX="fullsend-gitlab-runner"
# Boot disk size in GiB (gcloud "GB" is GiB). Matches the ~30 GiB guest disks
# of the OpenShift runner fleet (#8163).
BOOT_DISK_GB=30

# Validate GCP_USE_IAP early — it controls flag construction below, so an
# invalid value (e.g. "yes") must not silently skip --tunnel-through-iap.
if [[ "${GCP_USE_IAP}" != "true" && "${GCP_USE_IAP}" != "false" ]]; then
  echo "ERROR: GCP_USE_IAP must be true or false (got: ${GCP_USE_IAP})" >&2
  exit 1
fi

if [ "${GCP_USE_IAP}" = "false" ]; then
  echo "  WARN: GCP_USE_IAP=false — SSH will connect over the public internet." >&2
  echo "        Host-key verification uses accept-new (TOFU within this session)." >&2
  echo "        Secrets (REGISTRATION_TOKEN) are transmitted over this session." >&2
fi

# Common flags for gcloud compute ssh. IAP tunneling is enabled by default
# (no public IP); set GCP_USE_IAP=false to SSH directly via an external IP.
#
# Host-key handling differs by mode:
#   IAP (default):  StrictHostKeyChecking=no + /dev/null — the IAP relay has no
#                   stable host key, and the tunnel itself authenticates via IAM.
#   Direct IP:      StrictHostKeyChecking=accept-new + temp known-hosts file —
#                   the VM has a stable public IP, so we trust-on-first-use and
#                   reject key changes for all subsequent connections in this run.
GCE_SSH_FLAGS=(
  --ssh-flag="-o ConnectTimeout=10"
  --ssh-flag="-o LogLevel=ERROR"
)
if [ "${GCP_USE_IAP}" = "true" ]; then
  GCE_SSH_FLAGS=(
    --tunnel-through-iap
    --ssh-flag="-o StrictHostKeyChecking=no"
    --ssh-flag="-o UserKnownHostsFile=/dev/null"
    "${GCE_SSH_FLAGS[@]}"
  )
else
  _known_hosts=$(mktemp)
  GCE_SSH_FLAGS=(
    --ssh-flag="-o StrictHostKeyChecking=accept-new"
    --ssh-flag="-o UserKnownHostsFile=${_known_hosts}"
    "${GCE_SSH_FLAGS[@]}"
  )
fi

# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

# Helper to run commands on the GCE VM via gcloud compute ssh.
gce_ssh() {
  gcloud compute ssh "${vm_name}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    "${GCE_SSH_FLAGS[@]}" \
    --command="$1"
}

# Retry a command with exponential backoff for IAP tunnel resilience.
# After long-running SSH sessions, IAP may rate-limit or exhaust its
# connection pool, causing subsequent connections to fail with
# "Connection timed out during banner exchange" or "4003: failed to
# connect to backend". Diagnostic output goes to stderr to avoid stdout
# contamination (see docs/contributing/shell-scripting.md).
with_backoff() {
  local max_attempts=5 attempt=1 delay=5 rc
  while true; do
    rc=0
    "$@" || rc=$?
    if [ "${rc}" -eq 0 ]; then return 0; fi
    if [ "${attempt}" -ge "${max_attempts}" ]; then
      echo "  ERROR: command failed after ${max_attempts} attempts (exit ${rc})" >&2
      return "${rc}"
    fi
    echo "  WARN: attempt ${attempt}/${max_attempts} failed (exit ${rc}), retrying in ${delay}s..." >&2
    sleep "${delay}"
    delay=$(( delay * 2 ))
    (( attempt++ ))
  done
}

# ----------------------------------------------------------------------
# Validate inputs
# ----------------------------------------------------------------------
usage() {
  echo "Usage: {RUNNER_TOKEN=glrt-xxx | GL_TOKEN=glpat-xxx {GROUP_ID=<id>|PROJECT_ID=<id>}} GCP_PROJECT=<project> $0 [NUMBER | --resume NUMBER]"
  echo ""
  echo "Run '$0' with --help for details."
}

if [ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ]; then
  # Extract the comment block after the shebang until the first non-comment
  # line, stripping the leading "# " prefix.  This is immune to header edits
  # (no hardcoded line numbers).
  awk 'NR==1{next} /^[^#]/{exit} {sub(/^# ?/, ""); print}' "$0"
  exit 0
fi

resume=false
if [ "${1:-}" = "--resume" ]; then
  resume=true
  shift
  if [ -z "${1:-}" ]; then
    echo "ERROR: --resume requires the NUMBER of the VM to finish provisioning" >&2
    usage >&2
    exit 1
  fi
fi

RUNNER_USER="${RUNNER_USER:-}"
if [ "${resume}" = "true" ] && [ -n "${RUNNER_USER}" ] \
  && { ! [[ "${RUNNER_USER}" =~ ^[a-z_][a-z0-9_-]*$ ]] || [ "${RUNNER_USER}" = "root" ]; }; then
  echo "ERROR: RUNNER_USER must be a plain, non-root Unix user name (got: ${RUNNER_USER})" >&2
  exit 1
fi

if uses_runner_token; then
  if ! [[ "${RUNNER_TOKEN}" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "ERROR: RUNNER_TOKEN contains invalid characters" >&2
    exit 1
  fi
else
  if [ -z "${GL_TOKEN:-}" ]; then
    echo "ERROR: GL_TOKEN or RUNNER_TOKEN is required" >&2
    usage >&2
    exit 1
  fi
  if ! [[ "${GL_TOKEN}" =~ ^[A-Za-z0-9._-]+$ ]]; then
    echo "ERROR: GL_TOKEN contains invalid characters" >&2
    exit 1
  fi

  if ! validate_runner_scope; then
    usage >&2
    exit 1
  fi
fi

if [ -z "${GITLAB_URL}" ]; then
  echo "ERROR: GITLAB_URL is required (e.g. https://gitlab.example.com)" >&2
  usage >&2
  exit 1
fi
if ! [[ "${GITLAB_URL}" =~ ^https://[a-zA-Z0-9._-]+(:[0-9]+)?$ ]]; then
  echo "ERROR: GITLAB_URL must start with https:// (got: ${GITLAB_URL})" >&2
  exit 1
fi

if [ -z "${GCP_PROJECT}" ]; then
  echo "ERROR: GCP_PROJECT is required (GCP project ID)" >&2
  usage >&2
  exit 1
fi

if [ -z "${RUNNER_IMAGE}" ]; then
  echo "ERROR: RUNNER_IMAGE is required (e.g. ghcr.io/fullsend-ai/fullsend-runner:v1.2.3)" >&2
  usage >&2
  exit 1
fi

if [[ "${RUNNER_ACCESS_LEVEL}" != "not_protected" && "${RUNNER_ACCESS_LEVEL}" != "ref_protected" ]]; then
  echo "ERROR: RUNNER_ACCESS_LEVEL must be not_protected or ref_protected (got: ${RUNNER_ACCESS_LEVEL})" >&2
  exit 1
fi

# GCP_USE_IAP is validated early (before GCE_SSH_FLAGS construction).

# Preflight — every tool and file this run depends on. Without this, a missing
# executor script or an absent `timeout` is discovered only after the VM has
# booted and a runner has been registered, so the failure costs a rollback.
REPO_ROOT="$(cd "${SCRIPT_DIR}" && cd ../.. && pwd)"
_missing=0
for tool in gcloud python3 curl timeout sha256sum; do
  if ! command -v "${tool}" >/dev/null 2>&1; then
    echo "ERROR: required tool not found in PATH: ${tool}" >&2
    _missing=1
  fi
done
for _f in setup.sh create-gcp-vm.sh gitlab-runner-version.sh podman-prune.sh grow-root-fs.sh \
  executor/job_id.sh executor/prepare.sh executor/run.sh executor/cleanup.sh executor/gateway.sh; do
  if [ ! -f "${SCRIPT_DIR}/${_f}" ]; then
    echo "ERROR: required file not found: ${SCRIPT_DIR}/${_f}" >&2
    _missing=1
  fi
done
for _f in install-openshell.sh openshell-version.sh; do
  if [ ! -f "${REPO_ROOT}/.github/scripts/${_f}" ]; then
    echo "ERROR: required file not found: ${REPO_ROOT}/.github/scripts/${_f}" >&2
    _missing=1
  fi
done
if [ "${_missing}" -ne 0 ]; then
  exit 1
fi

# ----------------------------------------------------------------------
# 1. Pick the VM number (explicit arg or auto-increment)
# ----------------------------------------------------------------------
if [ -n "${1:-}" ]; then
  if ! [[ "$1" =~ ^[0-9]+$ ]]; then
    echo "ERROR: NUMBER must be numeric (got: $1)" >&2
    exit 1
  fi
  next=$(printf "%02d" "$((10#$1))")
  vm_name="${PREFIX}-${next}"
else
  max=0
  while IFS= read -r name; do
    [ -z "${name}" ] && continue
    num="${name#"${PREFIX}"-}"
    if [[ "${num}" =~ ^[0-9]+$ ]] && [ "$((10#${num}))" -gt "$((10#${max}))" ]; then
      max="${num}"
    fi
  done < <(gcloud compute instances list \
    --project="${GCP_PROJECT}" \
    --filter="name~'^${PREFIX}-'" \
    --format="value(name)" 2>/dev/null || true)

  next=$(printf "%02d" $((10#${max} + 1)))
  vm_name="${PREFIX}-${next}"
fi

if ! [[ "${vm_name}" =~ ^[a-z0-9-]+$ ]]; then
  echo "ERROR: vm_name contains invalid characters: ${vm_name}" >&2
  exit 1
fi

# ----------------------------------------------------------------------
# 2. Create the GCE VM (skipped by --resume, which reuses the VM)
# ----------------------------------------------------------------------
if [ "${resume}" = "true" ]; then
  echo "==> Resuming provisioning of VM: ${vm_name} in ${GCP_PROJECT} (${GCP_ZONE})"
  if ! vm_status=$(gcloud compute instances describe "${vm_name}" \
    --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" --format="value(status)" 2>/dev/null); then
    echo "ERROR: VM ${vm_name} not found in ${GCP_PROJECT}/${GCP_ZONE} — nothing to resume. Run without --resume to create it (or check GCP_PROJECT, GCP_ZONE, and your gcloud credentials)." >&2
    exit 1
  fi
  # A stopped or suspended VM may have been stopped on purpose; starting it
  # would put a runner back into service, so leave that to the operator.
  case "${vm_status}" in
    RUNNING | PROVISIONING | STAGING) ;;
    *)
      echo "ERROR: VM ${vm_name} is ${vm_status:-in an unknown state}, not RUNNING — start it first (gcloud compute instances start ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE}), then re-run $0 --resume ${next}" >&2
      exit 1
      ;;
  esac
else
  echo "==> Creating VM: ${vm_name} in ${GCP_PROJECT} (${GCP_ZONE})"
  if gcloud compute instances describe "${vm_name}" \
    --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" >/dev/null 2>&1; then
    echo "ERROR: VM ${vm_name} already exists in ${GCP_PROJECT}/${GCP_ZONE}. If its provisioning failed, or it needs the current provisioning, finish it with: $0 --resume ${next} (same environment). To recreate it, drain and delete with ./delete-gcp-vm.sh ${vm_name} (which drains in-flight jobs), then re-run create. Or choose a different number." >&2
    exit 1
  fi

  subnet_flag=()
  if [ -n "${GCP_SUBNET}" ]; then
    subnet_flag=(--subnet="${GCP_SUBNET}")
  fi

  address_flag=()
  if [ "${GCP_USE_IAP}" = "true" ]; then
    address_flag=(--no-address)
  fi

  # No Compute SA / no OAuth scopes. The VM does not need a service
  # account (operator gcloud is workstation-side; inference uses GitLab
  # OIDC → WIF). The default Compute SA is roles/editor, and anything in
  # the orchestration container can steal its token from the metadata
  # server — #7254.
  gcloud compute instances create "${vm_name}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    --machine-type="${GCP_MACHINE_TYPE}" \
    --network="${GCP_NETWORK}" \
    "${subnet_flag[@]+"${subnet_flag[@]}"}" \
    --tags="gitlab-runner" \
    "${address_flag[@]+"${address_flag[@]}"}" \
    --no-service-account \
    --no-scopes \
    --image-family="${GCP_IMAGE_FAMILY}" \
    --image-project="${GCP_IMAGE_PROJECT}" \
    --boot-disk-size="${BOOT_DISK_GB}GB" \
    --boot-disk-type="pd-balanced" \
    --quiet
fi

# The user setup.sh runs as when it is not the SSH login user (set by the
# service-user step on --resume; empty means the login user).
RUN_AS=""
RUN_AS_UID=""

# Recovery hint for every failure from here on: resume is idempotent and
# never duplicates a registration, so it is the first thing to try.
resume_hint() {
  echo "  NOTE: VM ${vm_name} is not fully provisioned. Fix the cause above, then finish it with the same environment:" >&2
  echo "    $0 --resume ${next}" >&2
}
cleanup_vm() {
  resume_hint
  echo "  Or delete it:" >&2
  echo "    GCP_PROJECT=${GCP_PROJECT} GCP_ZONE=${GCP_ZONE} GL_TOKEN=\$GL_TOKEN GITLAB_URL=${GITLAB_URL} ${RUN_AS:+RUNNER_USER=${RUN_AS} }./delete-gcp-vm.sh ${vm_name}" >&2
}
trap cleanup_vm ERR
# ERR does not fire on Ctrl-C; the boot and package-install waits below can
# take up to 20 minutes, so print the cleanup hint on interrupt as well.
trap 'cleanup_vm; exit 130' INT
trap 'cleanup_vm; exit 143' TERM

# On --resume, bring an existing VM's boot disk up to BOOT_DISK_GB (a VM
# created before #8163 has 20 GiB). The resize is online; grow-root-fs.sh
# below rescans the disk and grows the partition and filesystem into it.
# Only ever grows: a disk at or above BOOT_DISK_GB is left alone.
if [ "${resume}" = "true" ]; then
  echo "==> Checking the boot disk of ${vm_name}"
  boot_disk_source=""
  if ! boot_disk_source=$(gcloud compute instances describe "${vm_name}" \
    --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" --format=json \
    | python3 -c '
import json, sys
boot = [d for d in json.load(sys.stdin).get("disks", []) if d.get("boot")]
if len(boot) != 1:
    sys.exit("expected exactly one boot disk, found %d" % len(boot))
print(boot[0].get("source", ""))'); then
    echo "ERROR: could not identify the boot disk of ${vm_name}" >&2
    cleanup_vm
    exit 1
  fi
  boot_disk="${boot_disk_source##*/}"
  if [[ "${boot_disk_source}" != */zones/"${GCP_ZONE}"/disks/"${boot_disk}" ]] \
    || ! [[ "${boot_disk}" =~ ^[a-z0-9-]+$ ]]; then
    echo "ERROR: boot disk '${boot_disk_source}' of ${vm_name} is not a zonal disk in ${GCP_ZONE} — resize it to at least ${BOOT_DISK_GB} GiB yourself, then re-run --resume" >&2
    cleanup_vm
    exit 1
  fi
  boot_disk_gb=""
  if ! boot_disk_gb=$(gcloud compute disks describe "${boot_disk}" \
    --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" --format="value(sizeGb)") \
    || ! [[ "${boot_disk_gb}" =~ ^[0-9]+$ ]]; then
    echo "ERROR: could not read the size of boot disk ${boot_disk} (got: '${boot_disk_gb}')" >&2
    cleanup_vm
    exit 1
  fi
  if [ "${boot_disk_gb}" -lt "${BOOT_DISK_GB}" ]; then
    echo "  Growing boot disk ${boot_disk} from ${boot_disk_gb} GiB to ${BOOT_DISK_GB} GiB (online)"
    if ! gcloud compute disks resize "${boot_disk}" \
      --project="${GCP_PROJECT}" --zone="${GCP_ZONE}" --size="${BOOT_DISK_GB}GB" --quiet; then
      echo "ERROR: could not resize boot disk ${boot_disk} to ${BOOT_DISK_GB} GiB" >&2
      cleanup_vm
      exit 1
    fi
    echo "  OK: boot disk resized"
  else
    echo "  OK: boot disk is ${boot_disk_gb} GiB (>= ${BOOT_DISK_GB} GiB, left as is)"
  fi
fi

# ----------------------------------------------------------------------
# 3. Wait for the VM to boot, accept SSH, and install packages
# ----------------------------------------------------------------------
echo "==> Waiting for ${vm_name} to boot..."
for i in $(seq 1 60); do
  if gce_ssh "true" >/dev/null 2>&1; then
    echo "  OK: VM is up (${i}0s)"
    break
  fi
  if [ "${i}" -eq 60 ]; then
    echo "ERROR: VM did not become reachable after 10 minutes" >&2
    cleanup_vm
    exit 1
  fi
  sleep 10
done

# Install packages via dnf over SSH. Fedora GCE images ship google-guest-agent,
# not cloud-init, so #cloud-config user-data is silently ignored. Direct SSH
# dnf install is the reliable path on all Fedora GCE images.
echo "==> Installing packages via SSH (dnf)..."
# timeout cannot invoke a shell function (it execs an external binary), so
# inline gcloud compute ssh. Wrap in with_backoff for IAP tunnel resilience;
# `dnf install -y` is idempotent, so retrying after a dropped session is safe.
install_packages() {
  timeout 600 gcloud compute ssh "${vm_name}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    "${GCE_SSH_FLAGS[@]}" \
    --command="sudo dnf install -y podman curl git python3 openssl"
}
if ! with_backoff install_packages; then
  echo "ERROR: dnf install failed or timed out — check the VM for dnf errors" >&2
  cleanup_vm
  exit 1
fi
echo "  OK: packages installed"

# Grow the root partition and Btrfs filesystem to fill the boot disk, then
# verify disk, partition, and filesystem capacity. The Fedora image's own
# first-boot growth was observed not to run on GCE, leaving an ~8 GiB root on
# a 20 GiB disk (#8163). grow-root-fs.sh is idempotent and ends with its main
# call, so a dropped stream runs nothing and a retry is safe. Done before
# runner registration so a failure needs no deregistration.
echo "==> Growing root filesystem to fill the ${BOOT_DISK_GB} GiB boot disk..."
#
# A stream cut at a command boundary makes `bash -s` exit 0 without running
# main, so success also requires the completion marker that grow-root-fs.sh
# prints only after verification passes.
GROW_ROOT_FS_OK_MARKER="OK: root filesystem spans the disk"
grow_root_fs() {
  local out rc=0
  out=$(timeout 600 gcloud compute ssh "${vm_name}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    "${GCE_SSH_FLAGS[@]}" \
    -- "sudo env MIN_DISK_GIB=${BOOT_DISK_GB} bash -s" < "${SCRIPT_DIR}/grow-root-fs.sh" 2>&1) || rc=$?
  printf '%s\n' "${out}"
  [ "${rc}" -eq 0 ] || return "${rc}"
  if ! grep -Fq "==> ${GROW_ROOT_FS_OK_MARKER}" <<<"${out}"; then
    echo "  ERROR: grow-root-fs.sh exited 0 without its completion marker (truncated stream?)" >&2
    return 1
  fi
}
if ! with_backoff grow_root_fs; then
  echo "ERROR: root filesystem growth or capacity verification failed — see grow-root-fs.sh output above" >&2
  cleanup_vm
  exit 1
fi
echo "  OK: root filesystem spans the boot disk"

# On --resume, pick the user setup.sh runs as. setup.sh configures the user
# it runs as (systemd User=, /etc/gitlab-runner owner, executor paths, builds
# and cache under its home, rootless Podman storage). gcloud compute ssh logs
# in as a per-operator user, so a resume by another operator would otherwise
# move the runner to that operator's account and strand its workspace and
# Podman storage. The VM's existing service user therefore wins; only a VM
# that never got one uses the login user, as a fresh create does. Done before
# registration so a refusal needs no deregistration.
if [ "${resume}" = "true" ]; then
  echo "==> Identifying the gitlab-runner service user on ${vm_name}"
  user_probe_out=""
  if ! user_probe_out=$(gce_ssh 'sudo -n true || exit 1
f=/etc/systemd/system/gitlab-runner.service.d/user.conf
printf "login=%s\n" "$(id -un)"
if sudo test -f "$f"; then printf "unit=%s\n" "$(sudo grep "^User=" "$f" | tail -n 1 | cut -d= -f2-)"; fi
if sudo test -d /etc/gitlab-runner; then printf "owner=%s\n" "$(sudo stat -c %U /etc/gitlab-runner)"; fi'); then
    echo "ERROR: could not inspect ${vm_name} as the SSH login user (it needs passwordless sudo, as setup.sh does)" >&2
    cleanup_vm
    exit 1
  fi
  login_user=$(printf '%s\n' "${user_probe_out}" | sed -n 's/^login=//p')
  unit_user=$(printf '%s\n' "${user_probe_out}" | sed -n 's/^unit=//p')
  owner_user=$(printf '%s\n' "${user_probe_out}" | sed -n 's/^owner=//p')
  # setup.sh creates /etc/gitlab-runner as root before chowning it, so a
  # root-owned directory records no service user.
  if [ "${owner_user}" = "root" ]; then
    owner_user=""
  fi
  if [ -n "${unit_user}" ] && [ -n "${owner_user}" ] && [ "${unit_user}" != "${owner_user}" ]; then
    echo "ERROR: ${vm_name} has conflicting gitlab-runner service users (systemd User=${unit_user}, /etc/gitlab-runner owned by ${owner_user}) — refusing to guess; fix the VM by hand or recreate it" >&2
    cleanup_vm
    exit 1
  fi
  existing_user="${unit_user:-${owner_user}}"
  if [ -n "${RUNNER_USER}" ] && [ -n "${existing_user}" ] && [ "${RUNNER_USER}" != "${existing_user}" ]; then
    echo "ERROR: RUNNER_USER=${RUNNER_USER}, but ${vm_name}'s gitlab-runner service user is ${existing_user} — refusing to move the runner to another account; unset RUNNER_USER or set it to ${existing_user}" >&2
    cleanup_vm
    exit 1
  fi
  service_user="${existing_user:-${RUNNER_USER:-${login_user}}}"
  if ! [[ "${service_user}" =~ ^[a-z_][a-z0-9_-]*$ ]] || [ "${service_user}" = "root" ]; then
    echo "ERROR: ${vm_name}'s gitlab-runner service user '${service_user}' is not a plain, non-root user name — refusing to run setup as it" >&2
    cleanup_vm
    exit 1
  fi
  if [ -n "${existing_user}" ]; then
    echo "  OK: service user ${service_user} (existing; SSH login user ${login_user})"
  else
    echo "  OK: service user ${service_user} (VM has none yet; SSH login user ${login_user})"
  fi
  if [ "${service_user}" != "${login_user}" ]; then
    # setup.sh calls sudo throughout, and systemctl --user / rootless Podman
    # need the user's lingering systemd instance (/run/user/UID).
    passwd_entry=""
    if ! passwd_entry=$(gce_ssh "getent passwd ${service_user}"); then
      echo "ERROR: service user ${service_user} does not exist on ${vm_name}" >&2
      cleanup_vm
      exit 1
    fi
    RUN_AS_UID=$(printf '%s\n' "${passwd_entry}" | head -n 1 | cut -d: -f3)
    if ! [[ "${RUN_AS_UID}" =~ ^[0-9]+$ ]]; then
      echo "ERROR: could not resolve the UID of ${service_user} on ${vm_name}" >&2
      cleanup_vm
      exit 1
    fi
    if ! gce_ssh "sudo -n -u ${service_user} sudo -n true" >/dev/null; then
      echo "ERROR: ${service_user} has no passwordless sudo on ${vm_name}; setup.sh needs it. Grant it (or have ${service_user} run the resume), then re-run --resume" >&2
      cleanup_vm
      exit 1
    fi
    if ! gce_ssh "sudo loginctl enable-linger ${service_user} && for i in \$(seq 1 30); do test -S /run/user/${RUN_AS_UID}/bus && exit 0; sleep 1; done; exit 1" >/dev/null; then
      echo "ERROR: the systemd user instance of ${service_user} did not start on ${vm_name} (/run/user/${RUN_AS_UID}/bus missing)" >&2
      cleanup_vm
      exit 1
    fi
    RUN_AS="${service_user}"
    echo "  OK: files and setup.sh will run as ${RUN_AS} (via sudo from ${login_user})"
  fi
fi

# as_runner <command> — print <command> wrapped to run on the VM as the
# service user (with that user's home, login name, and user systemd/Podman
# runtime directory), or unchanged when that is the SSH login user.
as_runner() {
  if [ -z "${RUN_AS}" ]; then
    printf '%s' "$1"
  else
    printf 'sudo -n -u %s -H env USER=%s LOGNAME=%s XDG_RUNTIME_DIR=/run/user/%s DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%s/bus bash -c %q' \
      "${RUN_AS}" "${RUN_AS}" "${RUN_AS}" "${RUN_AS_UID}" "${RUN_AS_UID}" "cd || exit; $1"
  fi
}

# ----------------------------------------------------------------------
# 4. Register a runner via the GitLab API, or join an existing pool
# ----------------------------------------------------------------------
# On --resume in GL_TOKEN mode, decide whether this VM already has a runner
# before registering one, so repeating resume never duplicates a
# registration (see resume_registration_check in lib.sh).
reuse_runner=false
stale_vm_config=false
if [ "${resume}" = "true" ] && ! uses_runner_token; then
  echo "==> Checking for a runner already registered for ${vm_name}"
  check_rc=0
  resume_registration_check gce_ssh "${vm_name}" "${GCP_PROJECT}/${vm_name}" \
    "./delete-gcp-vm.sh ${vm_name}" || check_rc=$?
  if [ "${check_rc}" -eq 2 ]; then
    exit 1
  elif [ "${check_rc}" -ne 0 ]; then
    cleanup_vm
    exit 1
  fi
  reuse_runner="${RESUME_REUSE_RUNNER}"
  stale_vm_config="${RESUME_STALE_CONFIG}"
  runner_id="${RESUME_RUNNER_ID}"
fi

if uses_runner_token; then
  echo "==> Joining existing runner pool (RUNNER_TOKEN)"
  REGISTRATION_TOKEN="${RUNNER_TOKEN}"
  runner_id=""
  # No runner to deregister on failure — keep the cleanup_vm traps from step 2.
  # Later trap sites call cleanup_runner; alias it to cleanup_vm in this mode.
  cleanup_runner() { cleanup_vm; }
  echo "  OK: using provided runner token"
elif [ "${reuse_runner}" = "true" ]; then
  echo "  OK: reusing runner ID ${runner_id} (already configured on ${vm_name})"
  # setup.sh skips registration when config.toml already has a runner.
  REGISTRATION_TOKEN=""
  # This run did not create the runner, so a failure must not deregister it.
  cleanup_runner() { cleanup_vm; }
else
  echo "==> Registering runner with ${GITLAB_URL} (${RUNNER_SCOPE} ${SCOPE_ID})"

  build_scope_args

  # shellcheck disable=SC2154  # scope_args set by build_scope_args
  runner_json=$(gl_curl -X POST \
    "${GITLAB_URL}/api/v4/user/runners" \
    "${scope_args[@]}" \
    --data-urlencode "tag_list=${RUNNER_TAG}" \
    --data-urlencode "description=${GCP_PROJECT}/${vm_name}" \
    --data-urlencode "run_untagged=false" \
    --data-urlencode "access_level=${RUNNER_ACCESS_LEVEL}" 2>&1) || {
    echo "ERROR: GitLab runner registration failed. Response: ${runner_json}" >&2
    cleanup_vm
    exit 1
  }

  if [ -z "${runner_json}" ]; then
    echo "ERROR: GitLab runner registration returned empty response" >&2
    cleanup_vm
    exit 1
  fi

  # Set up rollback before extracting fields — a malformed API response would
  # orphan the runner if the trap weren't active yet.
  runner_id=""
  cleanup_runner() {
    if [ -z "${runner_id}" ]; then
      echo "ERROR: provisioning failed — runner may have been created but ID is unknown" >&2
      echo "  Check ${GITLAB_URL} for orphaned runners in ${RUNNER_SCOPE} ${SCOPE_ID}" >&2
    else
      echo "ERROR: provisioning failed — deregistering runner ${runner_id}" >&2
      if gl_curl -X DELETE "${GITLAB_URL}/api/v4/runners/${runner_id}" >/dev/null 2>&1; then
        echo "  OK: runner ${runner_id} deregistered" >&2
      else
        echo "  WARN: failed to deregister runner ${runner_id} — remove it manually at ${GITLAB_URL}" >&2
      fi
    fi
    cleanup_vm
  }
  trap cleanup_runner ERR
  # ERR does not fire on Ctrl-C, and the window below spans a ~20-minute setup
  # run — without this, an interrupt leaves the runner registered with nobody
  # tracking it.
  trap 'cleanup_runner; exit 130' INT
  trap 'cleanup_runner; exit 143' TERM

  REGISTRATION_TOKEN=$(echo "${runner_json}" | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])" 2>&1) || {
    echo "ERROR: failed to parse registration token from API response (length: ${#runner_json})" >&2
    cleanup_runner
    exit 1
  }
  runner_id=$(echo "${runner_json}" | python3 -c "import sys,json; print(json.load(sys.stdin)['id'])" 2>&1) || {
    echo "ERROR: failed to parse runner ID from API response (length: ${#runner_json})" >&2
    cleanup_runner
    exit 1
  }

  echo "  OK: runner ID ${runner_id} created"
fi

if [ "${stale_vm_config}" = "true" ]; then
  # Without this, setup.sh would see the old [[runners]] entry, skip
  # registration, and leave the VM on a token GitLab no longer accepts.
  # The file is moved aside rather than deleted so operator-added settings
  # (concurrent, check_interval, logging, ...) can be recovered.
  echo "==> Moving stale runner config on ${vm_name} aside"
  stale_config_backup="/etc/gitlab-runner/config.toml.stale-$(date +%s)"
  with_backoff gce_ssh "sudo mv /etc/gitlab-runner/config.toml ${stale_config_backup} && sudo chmod 600 ${stale_config_backup}"
  echo "  OK: stale config moved to ${stale_config_backup} on ${vm_name}"
fi

# ----------------------------------------------------------------------
# 5. Copy setup files to the VM
# ----------------------------------------------------------------------
echo "==> Copying setup files to ${vm_name}"

with_backoff gce_ssh "$(as_runner "mkdir -p ~/gitlab-runner-vm")"

# Stage files in a local temp directory for batch transfer.
_stage_dir=$(mktemp -d)
trap 'rm -rf "${_stage_dir}"; cleanup_runner' ERR
trap 'rm -rf "${_stage_dir}"; cleanup_runner; exit 130' INT
trap 'rm -rf "${_stage_dir}"; cleanup_runner; exit 143' TERM
cp "${SCRIPT_DIR}/setup.sh" "${_stage_dir}/"
cp "${SCRIPT_DIR}/create-gcp-vm.sh" "${_stage_dir}/"
cp "${SCRIPT_DIR}/gitlab-runner-version.sh" "${_stage_dir}/"
cp "${SCRIPT_DIR}/podman-prune.sh" "${_stage_dir}/"
mkdir -p "${_stage_dir}/executor"
for file in job_id.sh prepare.sh run.sh cleanup.sh gateway.sh; do
  cp "${SCRIPT_DIR}/executor/${file}" "${_stage_dir}/executor/"
done
mkdir -p "${_stage_dir}/.github/scripts"
for file in install-openshell.sh openshell-version.sh; do
  cp "${REPO_ROOT}/.github/scripts/${file}" "${_stage_dir}/.github/scripts/"
done

# Transfer all files in a single SSH connection via tar to minimize IAP
# tunnel usage. This replaces multiple scp calls that each open a separate
# IAP tunnel, which triggers rate-limiting after long-running sessions.
copy_files_to_vm() {
  tar -C "${_stage_dir}" -cf - . \
    | gcloud compute ssh "${vm_name}" \
        --project="${GCP_PROJECT}" \
        --zone="${GCP_ZONE}" \
        "${GCE_SSH_FLAGS[@]}" \
        -- "$(as_runner "tar -C ~/gitlab-runner-vm -xf -")"
}
with_backoff copy_files_to_vm

rm -rf "${_stage_dir}"
trap cleanup_runner ERR
trap 'cleanup_runner; exit 130' INT
trap 'cleanup_runner; exit 143' TERM

with_backoff gce_ssh "$(as_runner "chmod +x ~/gitlab-runner-vm/setup.sh ~/gitlab-runner-vm/create-gcp-vm.sh ~/gitlab-runner-vm/podman-prune.sh ~/gitlab-runner-vm/executor/*.sh ~/gitlab-runner-vm/.github/scripts/*.sh")"

# Verify every copy against a locally computed manifest before running it.
# A dropped SSH channel can leave a truncated setup.sh that then executes an
# arbitrary prefix of provisioning.
echo "==> Verifying copied files"
verify_copied_files() {
  {
    (cd "${SCRIPT_DIR}" && sha256sum setup.sh create-gcp-vm.sh gitlab-runner-version.sh podman-prune.sh \
      executor/job_id.sh executor/prepare.sh executor/run.sh executor/cleanup.sh executor/gateway.sh)
    (cd "${REPO_ROOT}/.github/scripts" \
      && sha256sum install-openshell.sh openshell-version.sh \
      | sed 's|  |  .github/scripts/|')
  } | gce_ssh "$(as_runner "cd ~/gitlab-runner-vm && sha256sum -c --quiet -")"
}
if ! verify_copied_files; then
  echo "ERROR: copied files failed checksum verification — transfer was truncated" >&2
  cleanup_runner
  exit 1
fi

echo "  OK: files copied"

# ----------------------------------------------------------------------
# 6. Run setup.sh on the VM
# ----------------------------------------------------------------------
echo "==> Running setup.sh on ${vm_name}"

# Write env vars to a file on the VM to avoid exposing secrets in the process list.
# Values are single-quoted to prevent interpretation of special characters.
for val in "${REGISTRATION_TOKEN}" "${GITLAB_URL}" "${RUNNER_TAG}" "${RUNNER_IMAGE}" "${OPENSHELL_VERSION}" "${GITLAB_RUNNER_VERSION}"; do
  if [[ "${val}" == *"'"* ]] || [[ "${val}" == *\\* ]] || [[ "${val}" =~ [[:cntrl:]] ]]; then
    echo "ERROR: environment variable values must not contain single quotes, backslashes, or control characters" >&2
    cleanup_runner
    exit 1
  fi
done
# One remote session: install the .env removal trap first, receive the env
# file on stdin, then run setup.sh. Doing this in one session means there is
# no window where the token-bearing file exists without a trap covering it.
# The signal handlers must terminate the shell (which then fires EXIT): a
# handler that merely returns would swallow the SIGHUP from a dropped SSH
# connection and let setup.sh keep running while the local side deregisters
# the runner. Bounded at 20 minutes (image pulls and binary downloads are the
# bottleneck). Wrapped in with_backoff for IAP tunnel resilience — setup.sh
# is idempotent (each function checks existing state before acting), so
# retrying the full invocation after a dropped SSH connection is safe.
# Note: each retry re-transmits REGISTRATION_TOKEN over a new SSH session.
# The remote EXIT trap (`rm -f .env`) cleans up the token file on disconnect,
# and umask 077 ensures it is never world-readable between attempts.
run_setup_on_vm() {
  {
    printf "REGISTRATION_TOKEN='%s'\n" "${REGISTRATION_TOKEN}"
    printf "GITLAB_URL='%s'\n" "${GITLAB_URL}"
    printf "RUNNER_TAG='%s'\n" "${RUNNER_TAG}"
    printf "RUNNER_IMAGE='%s'\n" "${RUNNER_IMAGE}"
    printf "OPENSHELL_VERSION='%s'\n" "${OPENSHELL_VERSION}"
    printf "GITLAB_RUNNER_VERSION='%s'\n" "${GITLAB_RUNNER_VERSION}"
  } | timeout 1200 gcloud compute ssh "${vm_name}" \
    --project="${GCP_PROJECT}" \
    --zone="${GCP_ZONE}" \
    "${GCE_SSH_FLAGS[@]}" \
    -- "$(as_runner "trap 'rm -f ~/gitlab-runner-vm/.env' EXIT; trap 'exit 129' HUP; trap 'exit 130' INT; trap 'exit 143' TERM; umask 077 && cat > ~/gitlab-runner-vm/.env && set -a && . ~/gitlab-runner-vm/.env && set +a && bash ~/gitlab-runner-vm/setup.sh")"
}
with_backoff run_setup_on_vm

# Setup succeeded — clear every rollback trap so a stray signal during the
# final output cannot deregister a healthy runner.
trap - ERR INT TERM
rm -f "${_known_hosts:-}"

echo ""
if uses_runner_token; then
  echo "Done. Runner ${vm_name} joined existing pool."
else
  echo "Done. Runner ${vm_name} (ID ${runner_id}) is ready."
fi
echo "  Tag:       ${RUNNER_TAG}"
echo "  Project:   ${GCP_PROJECT}"
echo "  Zone:      ${GCP_ZONE}"
if [ "${GCP_USE_IAP}" = "true" ]; then
  echo "  SSH:       gcloud compute ssh ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE} --tunnel-through-iap"
else
  echo "  SSH:       gcloud compute ssh ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE}"
  echo "  NOTE:      VM has an external IP. To remove it after provisioning:"
  echo "             gcloud compute instances delete-access-config ${vm_name} --project=${GCP_PROJECT} --zone=${GCP_ZONE}"
fi
