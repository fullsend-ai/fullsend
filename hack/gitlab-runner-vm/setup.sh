#!/usr/bin/env bash
#
# setup.sh — Configure a GitLab Runner VM for fullsend agent jobs.
#
# What it provisions (on a VM created from vm.yaml / create-*-vm.sh):
#   - gitlab-runner (pinned version) with a Podman custom executor
#   - rootless Podman + an OCI hook that injects the host CA bundle
#   - OpenShell CLI (pinned) and a per-job gateway (no long-lived daemon)
#   - pre-pulled runner + supervisor images
#   - a user systemd timer that prunes unused Podman images (#7663)
#
# Idempotent: safe to re-run, and must stay that way. Each step is guarded
# (version checks, grep-for-existing-config, early returns) so a second run
# converges with no accumulated artifacts. In-place re-run is a developer/
# debug convenience — fast script iteration on a test VM without a ~20-min
# re-provision. Recreation (drain → delete → create) is the compliance
# path; see issue #7257.
#
# Prerequisites:
#   - VM created from vm.yaml (provides Fedora + podman + gitlab-runner)
#   - sudo access for the running user
#
# Normally called by create-openshift-vm.sh / create-gcp-vm.sh. Can also
# be run standalone:
#   GITLAB_URL=https://gitlab.example.com RUNNER_IMAGE=ghcr.io/org/runner:v1 \
#     REGISTRATION_TOKEN=glrt-xxx ./setup.sh
#
# Environment variables:
#   REGISTRATION_TOKEN    — GitLab runner token (required on first run)
#   GITLAB_URL            — GitLab instance URL (required)
#   RUNNER_IMAGE          — image pre-pulled as a warm cache (required);
#                          jobs must set image: in .gitlab-ci.yml
#   RUNNER_TAG            — runner tag for job matching (default: fullsend-gitlab-runner)
#                          Note: for glrt-* tokens (GitLab 16+), tags are set at
#                          token creation time and --tag-list is rejected (fatal) by
#                          gitlab-runner register. Use the API/UI to set tags.
#   GITLAB_RUNNER_VERSION — gitlab-runner version to install (default: 19.2.1)
#
# Note: The OpenShell version sourced here is the *provisioning* pin
# (.github/scripts/openshell-version.sh, Renovate-tracked). Per-job prepare.sh
# re-reads the job image's openshell --version and upgrades the host when they
# differ, so a VM that was created on an older pin still matches the job.
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
RUNNER_IMAGE="${RUNNER_IMAGE:-}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Source the repo-wide OpenShell version pin (Renovate-tracked). The version is
# not overridable — the SHA-pinned installer always installs the repo-pinned version.
# Try VM layout first (.github/scripts/ as child), then repo layout (../../.github/scripts/).
_openshell_version_sh="${SCRIPT_DIR}/.github/scripts/openshell-version.sh"
if [ ! -f "${_openshell_version_sh}" ]; then
  _openshell_version_sh="${SCRIPT_DIR}/../../.github/scripts/openshell-version.sh"
fi
if [ -f "${_openshell_version_sh}" ]; then
  # shellcheck source=../../.github/scripts/openshell-version.sh
  source "${_openshell_version_sh}"
fi
# Fallback only when the pin file is absent; keep in step with openshell-version.sh.
OPENSHELL_VERSION="${OPENSHELL_VERSION:-0.1.2}"

# Source the executor's gateway helpers (wait_for_openshell_gateway,
# user_systemctl) so configure_per_job_gateway can wait for the seed start
# and every systemctl --user call has a user-session bus.
_gateway_sh="${SCRIPT_DIR}/executor/gateway.sh"
if [ -f "${_gateway_sh}" ]; then
  # shellcheck source=executor/gateway.sh
  source "${_gateway_sh}"
fi

EXECUTOR_DIR="${HOME}/gitlab-runner-executor"
BUILDS_DIR="${HOME}/builds"
CACHE_DIR="${HOME}/cache"
CONFIG_TOML="/etc/gitlab-runner/config.toml"
RUNNER_USER="${USER:-$(whoami)}"
# Overridable so setup_test.sh can point the drop-in at a temp dir.
GITLAB_RUNNER_OVERRIDE_DIR="/etc/systemd/system/gitlab-runner.service.d"
# Overridable so setup_test.sh can install the CA hook into a temp root.
HOST_CA_BUNDLE="/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem"
CA_HOOK_SCRIPT="/usr/local/bin/inject-ca-certs.sh"
OCI_HOOKS_DIR="/etc/containers/oci/hooks.d"
# Overridable so setup_test.sh can repair Fedora repo files in a temp dir.
YUM_REPOS_DIR="/etc/yum.repos.d"

# Source the central gitlab-runner version pin.
_runner_version_sh="${SCRIPT_DIR}/gitlab-runner-version.sh"
if [ -f "${_runner_version_sh}" ]; then
  # shellcheck source=gitlab-runner-version.sh
  source "${_runner_version_sh}"
fi
GITLAB_RUNNER_VERSION="${GITLAB_RUNNER_VERSION:-19.2.1}"

info()  { echo "==> $*"; }
ok()    { echo "  OK: $*"; }
fail()  { echo "  FAIL: $*" >&2; exit 1; }

# --------------------------------------------------------------------------
# 0. Fix Fedora repo config for egress-restricted environments
# --------------------------------------------------------------------------
fix_fedora_repos() {
  info "Checking Fedora repo config for metalink usage"

  # Fedora repos ship with metalink= enabled by default. The metalink
  # response from mirrors.fedoraproject.org redirects to third-party mirror
  # hosts (e.g. mirror.math.princeton.edu, d2lzkl7pfhq30w.cloudfront.net)
  # that are not in the TenantEgress allowlist. Switch to baseurl= pointing
  # at dl.fedoraproject.org which is already in the allowlist.
  #
  # Standard Fedora repo files have baseurl= commented out and metalink=
  # active. This function comments out metalink= and uncomments baseurl=.
  #
  # The base URLs themselves are repaired independently of metalink=, so a
  # repo already switched to baseurl= (e.g. by vm.yaml's bootcmd from an
  # older template) is fixed too. Keep in step with vm.yaml's bootcmd.
  local changed=0
  for repo_file in "${YUM_REPOS_DIR}"/fedora*.repo; do
    [ -f "${repo_file}" ] || continue
    if grep -q '^metalink=' "${repo_file}"; then
      sudo sed -i -e 's/^metalink=/#metalink=/' -e 's/^#baseurl=/baseurl=/' "${repo_file}"
      changed=1
    fi
    # Stock Fedora cloud images ship a placeholder baseurl pointing at
    # download.example (not a real mirror), over plain HTTP. Replace it
    # with the real Fedora mirror that is already in the TenantEgress
    # allowlist, and use HTTPS: egress-restricted clusters may block
    # port 80 while HTTPS to dl.fedoraproject.org works.
    if grep -q 'download\.example' "${repo_file}" \
      || grep -q '^baseurl=http://dl\.fedoraproject\.org/' "${repo_file}"; then
      sudo sed -i -e 's|download\.example|dl.fedoraproject.org|g' \
        -e 's|^baseurl=http://dl\.fedoraproject\.org/|baseurl=https://dl.fedoraproject.org/|' \
        "${repo_file}"
      changed=1
    fi
  done

  # The Cisco openh264 repo has broken mirrors on Fedora 43 cloud images
  # and is not needed for runner operation. Disable it to prevent dnf
  # metadata refresh failures.
  local cisco_repo="${YUM_REPOS_DIR}/fedora-cisco-openh264.repo"
  if [ -f "${cisco_repo}" ]; then
    sudo dnf config-manager setopt fedora-cisco-openh264.enabled=0 2>/dev/null \
      || sudo sed -i 's/^enabled=1/enabled=0/' "${cisco_repo}"
    ok "disabled fedora-cisco-openh264 repo"
  fi

  if [ "${changed}" -eq 1 ]; then
    ok "switched Fedora repos to HTTPS dl.fedoraproject.org baseurl"
  else
    ok "Fedora repos already using HTTPS baseurl"
  fi
}

# --------------------------------------------------------------------------
# 0a. Install internal CA certificates (internal GitLab instances often use
#     a private CA that is not in the default Fedora trust store)
# --------------------------------------------------------------------------
install_ca_certs() {
  info "Checking CA trust for ${GITLAB_URL}"

  # curl exits 0 even on 401/403 without -f; exit 60 means untrusted cert.
  # Branch on the exit code rather than treating every failure as a trust
  # problem: with --max-time set, a slow instance (28) or a DNS/connect error
  # (6/7) would otherwise fall through to TOFU and overwrite the trust anchor.
  local probe_rc=0
  curl --max-time 10 --connect-timeout 5 -so /dev/null "${GITLAB_URL}" 2>/dev/null || probe_rc=$?
  case "${probe_rc}" in
    0)
      ok "CA already trusted"
      return
      ;;
    35|51|58|59|60|66|77|80|82|83|91)
      : # TLS/trust failures — continue to the TOFU path below
      ;;
    *)
      fail "curl exit ${probe_rc} probing ${GITLAB_URL} is not a CA-trust failure that TOFU can fix — see 'man curl' EXIT CODES; check network/DNS"
      ;;
  esac

  local host_port
  host_port=$(echo "${GITLAB_URL}" | sed 's|https\?://||;s|/.*||')
  local host="${host_port%%:*}"
  local port="${host_port##*:}"
  if [ "${port}" = "${host}" ]; then port=443; fi

  # TOFU (trust-on-first-use): the CA chain is fetched from the server itself.
  # For higher assurance, provide the CA bundle out-of-band via:
  #   sudo cp /path/to/ca-bundle.pem /etc/pki/ca-trust/source/anchors/gitlab-chain.pem
  #   sudo update-ca-trust
  # Stage into a temp file first and validate it before touching the trust
  # store: piping straight into `tee` truncates the anchor before we know
  # whether openssl produced anything. The TOFU anchor also gets its own
  # filename so it can never clobber an operator's out-of-band bundle at
  # gitlab-chain.pem. `timeout` bounds the handshake — openssl has no
  # equivalent of curl's --max-time here.
  local tofu_anchor=/etc/pki/ca-trust/source/anchors/gitlab-chain-tofu.pem
  local staged
  staged=$(mktemp)
  echo "  WARN: trust-on-first-use — fetching CA chain from ${host}:${port}"
  timeout 15 openssl s_client -connect "${host}:${port}" -servername "${host}" -showcerts </dev/null 2>/dev/null \
    | awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/' > "${staged}" || true

  # crl2pkcs7 | pkcs7 -print_certs parses every certificate in the bundle,
  # not just the first — a transfer that truncated mid-chain must not install.
  if [ ! -s "${staged}" ] \
    || ! openssl crl2pkcs7 -nocrl -certfile "${staged}" 2>/dev/null \
      | openssl pkcs7 -print_certs -noout >/dev/null 2>&1; then
    rm -f "${staged}"
    fail "failed to retrieve a valid CA chain from ${host}:${port}"
  fi

  sudo install -m 0644 "${staged}" "${tofu_anchor}"
  rm -f "${staged}"
  sudo update-ca-trust

  # Same branching as the first probe: only a trust-class failure means the
  # anchor is wrong. A timeout or connect error here must not delete it.
  probe_rc=0
  curl --max-time 10 --connect-timeout 5 -so /dev/null "${GITLAB_URL}" 2>/dev/null || probe_rc=$?
  case "${probe_rc}" in
    0)
      ok "CA certificates installed and trusted"
      ;;
    35|51|58|59|60|66|77|80|82|83|91)
      sudo rm -f "${tofu_anchor}"
      sudo update-ca-trust
      fail "CA install failed — ${GITLAB_URL} still not trusted (anchor removed)"
      ;;
    *)
      fail "cannot reach ${GITLAB_URL} after CA install (curl exit ${probe_rc}) — anchor left in place; check network/DNS"
      ;;
  esac
}

# --------------------------------------------------------------------------
# 0b. Install gitlab-runner binary
# --------------------------------------------------------------------------
install_gitlab_runner() {
  info "Checking gitlab-runner"

  if command -v gitlab-runner &>/dev/null; then
    local current
    current=$(gitlab-runner --version 2>&1 | head -1 | awk '{print $2}')
    if [ "${current}" = "${GITLAB_RUNNER_VERSION}" ]; then
      ok "gitlab-runner ${GITLAB_RUNNER_VERSION}"
      return
    fi
    info "upgrading gitlab-runner ${current} -> ${GITLAB_RUNNER_VERSION}"
  fi

  local arch
  arch=$(uname -m)
  case "${arch}" in
    x86_64)  arch="amd64" ;;
    aarch64) arch="arm64" ;;
    *) fail "unsupported architecture: ${arch}" ;;
  esac

  local runner_url="https://gitlab-runner-downloads.s3.amazonaws.com/v${GITLAB_RUNNER_VERSION}/binaries/gitlab-runner-linux-${arch}"
  local tmpbin
  tmpbin=$(mktemp)
  # Stall detection rather than a hard cap: the binary is tens of megabytes,
  # so --max-time turns a slow-but-working link into a deterministic failure.
  # Overall runtime is already bounded by create-openshift-vm.sh / create-gcp-vm.sh's `timeout`.
  curl --connect-timeout 10 --speed-limit 10240 --speed-time 60 \
    --retry 3 --retry-connrefused -fsSL -o "${tmpbin}" "${runner_url}"

  local checksums_url="https://gitlab-runner-downloads.s3.amazonaws.com/v${GITLAB_RUNNER_VERSION}/release.sha256"
  local expected
  expected=$(curl --max-time 30 --connect-timeout 10 -fsSL "${checksums_url}" | grep "/gitlab-runner-linux-${arch}$" | awk '{print $1}') || true
  if [ -z "${expected}" ]; then
    rm -f "${tmpbin}"
    fail "could not retrieve checksum for gitlab-runner-linux-${arch} from ${checksums_url}"
  fi
  local actual
  actual=$(sha256sum "${tmpbin}" | awk '{print $1}')
  if [ "${actual}" != "${expected}" ]; then
    rm -f "${tmpbin}"
    fail "gitlab-runner checksum mismatch (expected ${expected}, got ${actual})"
  fi

  sudo install -m 0755 "${tmpbin}" /usr/local/bin/gitlab-runner
  rm -f "${tmpbin}"
  if [ ! -f /etc/systemd/system/gitlab-runner.service ]; then
    sudo gitlab-runner install --user "${RUNNER_USER}" --working-directory "${HOME}"
  else
    sudo systemctl daemon-reload
  fi
  sudo mkdir -p /etc/gitlab-runner
  sudo chown -R "${RUNNER_USER}:${RUNNER_USER}" /etc/gitlab-runner

  ok "gitlab-runner ${GITLAB_RUNNER_VERSION} installed"
}

# stop_runner_service — stop the system gitlab-runner service, used when a
# registration check fails so the VM does not keep polling a GitLab instance
# it should not serve. The stop is confirmed with `systemctl is-active`: when
# it cannot be confirmed, an explicit containment-failure warning is printed
# and the function returns 1. Either way it sets RUNNER_STOP_NOTE, the phrase
# rejection messages use to say what actually happened to the service.
RUNNER_STOP_NOTE="gitlab-runner left stopped"
stop_runner_service() {
  sudo systemctl stop gitlab-runner 2>/dev/null || true
  if systemctl is-active --quiet gitlab-runner; then
    RUNNER_STOP_NOTE="CONTAINMENT FAILED: gitlab-runner is still running and polling — stop it now with: sudo systemctl stop gitlab-runner"
    echo "  WARN: ${RUNNER_STOP_NOTE}"
    return 1
  fi
  RUNNER_STOP_NOTE="gitlab-runner left stopped"
}

# read_config_runners — structural, offline read of config.toml. Prints the
# number of [[runners]] entries, then each entry's url, one per line. Parsed
# as TOML, so indented or otherwise reformatted headers (e.g. "[[ runners ]]")
# are counted too. Returns 1 when the file cannot be parsed.
read_config_runners() {
  python3 - "${CONFIG_TOML}" 2>/dev/null <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as f:
    runners = tomllib.load(f).get("runners", [])
print(len(runners))
for r in runners:
    print(r.get("url", ""))
PY
}

# check_registration_config — structural, offline check of config.toml: it
# must hold exactly one runner and that runner's url must be GITLAB_URL (the
# only instance this VM should serve).
check_registration_config() {
  local out count url
  if ! out=$(read_config_runners); then
    echo "  WARN: cannot parse ${CONFIG_TOML} as TOML"
    return 1
  fi
  count=$(printf '%s\n' "${out}" | head -n 1)
  if [ "${count}" -ne 1 ]; then
    echo "  WARN: expected exactly one [[runners]] entry in ${CONFIG_TOML}, found ${count}"
    return 1
  fi
  url=$(printf '%s\n' "${out}" | sed -n 2p)
  if [ "${url%/}" != "${GITLAB_URL%/}" ]; then
    echo "  WARN: runner is registered with '${url}', expected ${GITLAB_URL}"
    return 1
  fi
}

# config_runner_token_matches — succeeds when the single configured runner's
# token equals REGISTRATION_TOKEN. The supplied token is handed to python via
# the environment (not argv) and neither token is ever printed. Fails on an
# unparseable config or a mismatch.
config_runner_token_matches() {
  SUPPLIED_TOKEN="${REGISTRATION_TOKEN}" python3 - "${CONFIG_TOML}" 2>/dev/null <<'PY'
import hmac, os, sys, tomllib
with open(sys.argv[1], "rb") as f:
    runners = tomllib.load(f).get("runners", [])
supplied = os.environ.get("SUPPLIED_TOKEN", "")
ok = len(runners) == 1 and bool(supplied) and hmac.compare_digest(
    str(runners[0].get("token", "")).encode(), supplied.encode())
sys.exit(0 if ok else 1)
PY
}

# --------------------------------------------------------------------------
# 0c. Register runner with GitLab (first-time only)
# --------------------------------------------------------------------------
register_runner() {
  info "Checking runner registration"

  # Whether a registration exists is decided from the parsed TOML, not a
  # header grep, so every valid spelling of [[runners]] counts. An existing
  # config that cannot be parsed is treated as a registration to check, which
  # then fails closed.
  local config_out config_count=0
  if [ -f "${CONFIG_TOML}" ]; then
    if config_out=$(read_config_runners); then
      config_count=$(printf '%s\n' "${config_out}" | head -n 1)
    else
      config_count=-1
    fi
  fi

  if [ "${config_count}" -ne 0 ]; then
    # Reusing a config (e.g. --resume) must never leave this VM serving some
    # other GitLab instance: check the target before the service can start
    # or poll, and keep the service stopped if it is wrong.
    if ! check_registration_config; then
      stop_runner_service || true
      fail "existing runner config in ${CONFIG_TOML} is not a single registration with ${GITLAB_URL} — ${RUNNER_STOP_NOTE}; remove the config and re-run"
    fi
    # A supplied registration token (shared-pool mode) must be the one this
    # config already holds; otherwise the VM would keep serving another pool.
    # Without a token (GL_TOKEN reuse), the config is trusted as-is.
    if [ -n "${REGISTRATION_TOKEN:-}" ] && ! config_runner_token_matches; then
      echo "  WARN: the runner in ${CONFIG_TOML} holds a different runner token than the one supplied"
      stop_runner_service || true
      fail "existing runner config in ${CONFIG_TOML} does not match the supplied runner token — ${RUNNER_STOP_NOTE}; remove the config and re-run"
    fi
    ok "runner already registered"
    return
  fi

  if [ -z "${REGISTRATION_TOKEN:-}" ]; then
    fail "REGISTRATION_TOKEN required for first-time registration"
  fi

  sudo mkdir -p "$(dirname "${CONFIG_TOML}")"
  sudo chown -R "${RUNNER_USER}:${RUNNER_USER}" "$(dirname "${CONFIG_TOML}")"

  gitlab-runner register \
    --non-interactive \
    --config "${CONFIG_TOML}" \
    --url "${GITLAB_URL}" \
    --token "${REGISTRATION_TOKEN}" \
    --executor shell

  ok "runner registered with ${GITLAB_URL}"
}

# --------------------------------------------------------------------------
# 1. Switch gitlab-runner to run as the current user
# --------------------------------------------------------------------------
setup_runner_user() {
  info "Configuring gitlab-runner to run as ${RUNNER_USER}"

  local override_dir="${GITLAB_RUNNER_OVERRIDE_DIR}"
  local override_file="${override_dir}/user.conf"
  local runner_uid

  # GitLab Runner is a system service, so it does not go through pam_systemd
  # and does not inherit a user-session bus. Linger keeps user@UID.service
  # alive; these Environment= lines let systemctl --user and rootless podman
  # talk to it.
  #
  # Resolve the numeric UID at generation time. systemd %U/%u specifiers
  # expand to the *manager instance* (root / 0 for a system-scope unit),
  # not to User= — interpolating %U produced /run/user/0 and broke
  # rootless Podman (#7696).
  #
  # The skip path must require the env lines with this UID: a VM
  # provisioned before #7453 has User= already, and a VM provisioned with
  # the #7453 %U drop-in still expands to UID 0. Re-running setup.sh has
  # to rewrite the drop-in so existing runners converge without manual
  # repair.
  runner_uid="$(id -u "${RUNNER_USER}")" || fail "cannot resolve UID for ${RUNNER_USER}"

  if [ -f "${override_file}" ] \
    && grep -q "User=${RUNNER_USER}" "${override_file}" \
    && grep -Fq "XDG_RUNTIME_DIR=/run/user/${runner_uid}" "${override_file}" \
    && grep -Fq "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${runner_uid}/bus" "${override_file}"; then
    ok "systemd override already in place"
    return
  fi

  sudo mkdir -p "${override_dir}"
  sudo tee "${override_file}" > /dev/null <<EOF
[Service]
User=${RUNNER_USER}
Group=${RUNNER_USER}
WorkingDirectory=${HOME}
Environment=XDG_RUNTIME_DIR=/run/user/${runner_uid}
Environment=DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/${runner_uid}/bus
ExecStart=
ExecStart=/usr/local/bin/gitlab-runner run --config ${CONFIG_TOML} --working-directory ${HOME} --service gitlab-runner
EOF

  sudo systemctl daemon-reload
  ok "gitlab-runner systemd override installed for ${RUNNER_USER}"
}

# --------------------------------------------------------------------------
# 2. Enable rootless Podman prerequisites
# --------------------------------------------------------------------------
setup_podman() {
  info "Configuring rootless Podman"

  if ! test -f /sys/fs/cgroup/cgroup.controllers; then
    fail "cgroups v2 required but not available"
  fi

  if ! grep -q "^${RUNNER_USER}:" /etc/subuid 2>/dev/null; then
    sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "${RUNNER_USER}"
    podman system migrate
    ok "added subuid/subgid mapping"
  else
    ok "subuid/subgid already configured"
  fi

  sudo loginctl enable-linger "${RUNNER_USER}"
  ok "linger enabled for ${RUNNER_USER}"

  user_systemctl enable --now podman.socket
  ok "podman socket enabled"
}

# --------------------------------------------------------------------------
# 3. Install OpenShell CLI
# --------------------------------------------------------------------------
install_openshell() {
  info "Installing OpenShell ${OPENSHELL_VERSION}"

  if command -v openshell &>/dev/null && user_systemctl cat openshell-gateway.service &>/dev/null; then
    local current
    current=$(openshell --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' | head -1 || echo "")
    if [ "${current}" = "${OPENSHELL_VERSION}" ]; then
      ok "OpenShell ${OPENSHELL_VERSION} already installed"
      return
    fi
  fi

  # Reuse the repo's SHA-pinned installer for supply-chain integrity.
  # Try VM layout first (.github/scripts/ as child), then repo layout.
  local install_sh
  install_sh="${SCRIPT_DIR}/.github/scripts/install-openshell.sh"
  if [ ! -f "${install_sh}" ]; then
    install_sh="${SCRIPT_DIR}/../../.github/scripts/install-openshell.sh"
  fi
  if [ ! -f "${install_sh}" ]; then
    fail "install-openshell.sh not found — run from the VM layout or repo checkout"
  fi
  # The installer starts the new gateway: stop the old one, drop its state
  # and write the v2 config first (gateway.sh).
  prepare_openshell_upgrade "${OPENSHELL_VERSION}"
  # install.sh refuses a pre-0.1 -> 0.1 upgrade on a persistent VM without the ack.
  OPENSHELL_ACK_BREAKING_UPGRADE=1 bash "${install_sh}"

  if ! command -v openshell &>/dev/null; then
    fail "openshell binary not found after install"
  fi
  if ! user_systemctl cat openshell-gateway.service &>/dev/null; then
    fail "openshell-gateway.service not found after RPM install"
  fi

  openshell --version
  ok "OpenShell ${OPENSHELL_VERSION} installed"
}

# --------------------------------------------------------------------------
# 4. Configure OpenShell gateway
# --------------------------------------------------------------------------
configure_gateway() {
  info "Configuring OpenShell gateway"

  mkdir -p "${HOME}/.config/openshell"

  # Bind the gateway to all interfaces — required for the Podman compute driver.
  # Sandbox containers use bridge networking on the 'openshell' network and
  # register back via host.containers.internal:17670, which arrives on a
  # non-loopback address. mTLS protects the wider bind.
  if [ -f "${HOME}/.config/openshell/gateway.env" ] && grep -q '^OPENSHELL_BIND_ADDRESS=0\.0\.0\.0$' "${HOME}/.config/openshell/gateway.env"; then
    ok "gateway binding already at 0.0.0.0"
  elif [ -f "${HOME}/.config/openshell/gateway.env" ]; then
    if grep -q '^OPENSHELL_BIND_ADDRESS=' "${HOME}/.config/openshell/gateway.env"; then
      sed -i 's/^OPENSHELL_BIND_ADDRESS=.*/OPENSHELL_BIND_ADDRESS=0.0.0.0/' "${HOME}/.config/openshell/gateway.env"
    else
      echo 'OPENSHELL_BIND_ADDRESS=0.0.0.0' >> "${HOME}/.config/openshell/gateway.env"
    fi
    ok "gateway binding set to 0.0.0.0"
  else
    echo 'OPENSHELL_BIND_ADDRESS=0.0.0.0' > "${HOME}/.config/openshell/gateway.env"
    ok "gateway binding set to 0.0.0.0"
  fi

  # Pin supervisor_image to the Renovate-tracked version in a schema-v2
  # gateway.toml (matching action.yml). install_openshell writes it before
  # any install; this covers its already-installed return and is otherwise
  # a no-op.
  pin_supervisor_image "${OPENSHELL_VERSION}"
  ok "supervisor_image pinned to $(openshell_supervisor_image "${OPENSHELL_VERSION}")"
}

# --------------------------------------------------------------------------
# 4b. Inject host CA trust into sandbox containers (OCI hook)
# --------------------------------------------------------------------------
install_ca_hook() {
  info "Installing OCI hook for sandbox CA trust"

  # The OpenShell supervisor (PID 1 inside sandbox containers) reads the
  # system CA bundle from a fixed list — /etc/ssl/certs/ca-certificates.crt,
  # /etc/pki/tls/certs/ca-bundle.crt, /etc/ssl/ca-bundle.pem, /etc/ssl/cert.pem
  # (SYSTEM_CA_PATHS, first non-empty wins) — to build the L7 egress proxy's
  # upstream trust and the SSL_CERT_FILE bundle handed to sandboxed processes.
  # The default sandbox image ships standard Mozilla CAs but not the
  # internal CA installed by install_ca_certs(). An OCI createRuntime
  # hook copies the host trust bundle into every container's rootfs
  # before PID 1 starts, so the supervisor trusts internal endpoints.
  # The candidate list below must stay a subset of SYSTEM_CA_PATHS: writing
  # only to a path OpenShell never reads leaves the CA invisible to it.

  # Stage the host CA bundle in a user-writable location.
  mkdir -p "${HOME}/.local/share/ca-trust"
  cp "${HOST_CA_BUNDLE}" \
     "${HOME}/.local/share/ca-trust/ca-bundle.pem"
  chmod 644 "${HOME}/.local/share/ca-trust/ca-bundle.pem"

  # Install the hook script ($HOME expands at install time via unquoted heredoc).
  local ca_src="${HOME}/.local/share/ca-trust/ca-bundle.pem"
  sudo tee "${CA_HOOK_SCRIPT}" > /dev/null <<HOOKSCRIPT
#!/bin/bash
STATE=\$(cat)
# Resolve rootfs from OCI hook state. Primary: bundle + config.json (OCI spec).
# Fallback: 'root' string if emitted by the runtime (non-standard extension).
ROOTFS=\$(echo "\$STATE" | python3 -c "
import json, sys, os
state = json.load(sys.stdin)
bundle = state.get('bundle', '')
if bundle:
    cfg = os.path.join(bundle, 'config.json')
    if os.path.isfile(cfg):
        with open(cfg) as f:
            root_path = json.load(f).get('root', {}).get('path', '')
            if root_path and not os.path.isabs(root_path):
                root_path = os.path.join(bundle, root_path)
            if root_path:
                print(root_path)
                sys.exit(0)
root = state.get('root', '')
if isinstance(root, str) and root:
    print(root)
")
CA_SRC="${ca_src}"

if [ -z "\$ROOTFS" ]; then
  echo "inject-ca-certs: no rootfs from container state" >&2
  exit 0
fi
if [ ! -f "\$CA_SRC" ]; then
  echo "inject-ca-certs: CA source not found: \$CA_SRC" >&2
  exit 0
fi

# Resolve a path inside the rootfs, following symlinks relative to the rootfs
# (not the host root). Resolves to a fixpoint with a hop limit to defeat
# chained symlinks. The final path must not itself be a symlink.
resolve_in_rootfs() {
  python3 -c "
import os, sys
rootfs, rel = sys.argv[1], sys.argv[2]
parts = rel.strip('/').split('/')
cur = rootfs
hops = 0
for p in parts:
    cur = os.path.join(cur, p)
    while os.path.islink(cur):
        if hops >= 40:
            sys.exit(1)
        t = os.readlink(cur)
        cur = os.path.join(rootfs, t.lstrip('/')) if os.path.isabs(t) else os.path.join(os.path.dirname(cur), t)
        cur = os.path.normpath(cur)
        if not cur.startswith(rootfs + '/') and cur != rootfs:
            sys.exit(1)
        hops += 1
    cur = os.path.normpath(cur)
    if not cur.startswith(rootfs + '/') and cur != rootfs:
        sys.exit(1)
if os.path.islink(cur):
    sys.exit(1)
print(cur)
" "\$1" "\$2"
}

# Known CA bundle paths, in OpenShell's own SYSTEM_CA_PATHS order:
# Debian/Ubuntu, RHEL/CentOS, SUSE, then Alpine. Keep this list aligned with
# upstream — a path outside it is not read by the supervisor.
for ca_rel in \\
  etc/ssl/certs/ca-certificates.crt \\
  etc/pki/tls/certs/ca-bundle.crt \\
  etc/ssl/ca-bundle.pem \\
  etc/ssl/cert.pem; do
  resolved=\$(resolve_in_rootfs "\$ROOTFS" "\$ca_rel") || continue
  [ -f "\$resolved" ] || continue
  # Write to a sibling temp file and atomically replace the target so a
  # mid-write failure (ENOSPC/EDQUOT) never truncates the original bundle.
  # O_NOFOLLOW on the temp prevents symlink-following.
  python3 -c "
import os, sys, tempfile
target, src_path = sys.argv[1], sys.argv[2]
target_dir = os.path.dirname(target)
try:
    with open(src_path, 'rb') as src:
        data = src.read()
    fd, tmp = tempfile.mkstemp(dir=target_dir, prefix='.ca-inject-')
    try:
        with os.fdopen(fd, 'wb') as f:
            f.write(data)
        os.chmod(tmp, 0o644)
        os.replace(tmp, target)
    except BaseException:
        os.unlink(tmp)
        raise
except OSError:
    sys.exit(1)
" "\$resolved" "\$CA_SRC" && {
    echo "inject-ca-certs: injected CA bundle to \${ca_rel}" >&2
    exit 0
  }
done

echo "inject-ca-certs: no writable CA bundle path found in rootfs" >&2
exit 0
HOOKSCRIPT
  # Rootless Podman reads the hook JSON and execs the script as the runner
  # user, so both must be world-readable and every directory on the path
  # traversable. sudo tee creates new files under root's umask and keeps
  # the mode of existing ones, so set explicit modes on every run: a
  # restrictive umask (or an earlier root-only install) otherwise fails
  # every job with "setting up OCI Hooks: ... permission denied" (#8153).
  # These files hold hook config and public CA trust, not credentials.
  sudo chmod 0755 "${CA_HOOK_SCRIPT}"

  # Install the hook JSON.
  local oci_dir
  oci_dir="$(dirname "${OCI_HOOKS_DIR}")"
  sudo mkdir -p "${OCI_HOOKS_DIR}"
  sudo chmod 0755 "${oci_dir}" "${OCI_HOOKS_DIR}"
  sudo tee "${OCI_HOOKS_DIR}/inject-ca-certs.json" > /dev/null <<HOOKJSON
{
  "version": "1.0.0",
  "hook": {
    "path": "${CA_HOOK_SCRIPT}"
  },
  "when": {
    "always": true
  },
  "stages": ["createRuntime"]
}
HOOKJSON
  sudo chmod 0644 "${OCI_HOOKS_DIR}/inject-ca-certs.json"

  # Tell Podman where to find hooks (required for rootless mode).
  mkdir -p "${HOME}/.config/containers"
  local conf="${HOME}/.config/containers/containers.conf"
  if [ -f "${conf}" ] && grep -q '/etc/containers/oci/hooks.d' "${conf}"; then
    ok "hooks_dir already configured in containers.conf"
  else
    if [ -f "${conf}" ]; then
      if grep -q '^\[engine\]' "${conf}"; then
        sed -i '/^\[engine\]/a hooks_dir = ["/etc/containers/oci/hooks.d"]' "${conf}"
      else
        printf '\n[engine]\nhooks_dir = ["/etc/containers/oci/hooks.d"]\n' >> "${conf}"
      fi
    else
      cat > "${conf}" <<EOF
[engine]
hooks_dir = ["/etc/containers/oci/hooks.d"]
EOF
    fi
  fi

  # Restart the Podman socket so it picks up the hooks_dir config.
  user_systemctl restart podman.socket

  ok "OCI hook installed for CA trust injection"
}

# --------------------------------------------------------------------------
# 5. Per-job OpenShell gateway (no long-lived daemon)
# --------------------------------------------------------------------------
# The RPM's user unit would otherwise stay up across every job and accumulate
# a stale profile registry plus a baked-in OpenShell version (#7218). prepare.sh
# starts a fresh, job-version-matched gateway; cleanup.sh tears it down. Setup
# only seeds config, PKI defaults, and image cache — then disables the unit so
# a reboot or lingering user session cannot resurrect it.
configure_per_job_gateway() {
  info "Configuring per-job OpenShell gateway (no long-lived daemon)"

  user_systemctl daemon-reload

  # Already-seeded re-run: a previous setup.sh patched the runner to the
  # custom executor and left the unit disabled and stopped. Skip the
  # start→stop→disable seed so re-running setup.sh is a true no-op for
  # the gateway. First run still seeds: patch_config (which writes
  # executor = "custom") runs after this function, so config.toml is
  # still executor = "shell" on a fresh VM.
  #
  # If the unit has been re-enabled or is running, fall through and
  # pin it back to per-job.
  if [ -f "${CONFIG_TOML}" ] \
    && config_uses_custom_executor \
    && user_systemctl cat openshell-gateway.service >/dev/null 2>&1 \
    && ! user_systemctl is-enabled --quiet openshell-gateway.service \
    && ! user_systemctl is-active --quiet openshell-gateway.service; then
    openshell gateway remove openshell >/dev/null 2>&1 || true
    ok "openshell-gateway.service already seeded and disabled; skipping seed start"
    return
  fi

  # Seed PKI + gateway.toml.default via a one-shot start, then stop and
  # disable. The next job's prepare.sh starts it for real with a wiped store.
  user_systemctl start openshell-gateway.service || true
  if ! wait_for_openshell_gateway; then
    fail "openshell-gateway.service did not become active during the seed start — check: journalctl --user -u openshell-gateway"
  fi
  user_systemctl stop openshell-gateway.service 2>/dev/null || true
  user_systemctl disable openshell-gateway.service 2>/dev/null || true

  # Drop any CLI registration from the seed start; prepare.sh re-adds it.
  openshell gateway remove openshell >/dev/null 2>&1 || true

  ok "openshell-gateway.service disabled; jobs start it in prepare.sh"
}

# --------------------------------------------------------------------------
# 6. Install custom executor scripts
# --------------------------------------------------------------------------
install_executor() {
  info "Installing custom executor scripts to ${EXECUTOR_DIR}"

  mkdir -p "${EXECUTOR_DIR}"

  for script in job_id.sh prepare.sh run.sh cleanup.sh gateway.sh; do
    local src="${SCRIPT_DIR}/executor/${script}"
    if [ ! -f "${src}" ]; then
      fail "executor script not found: ${src}"
    fi
    cp "${src}" "${EXECUTOR_DIR}/${script}"
    chmod +x "${EXECUTOR_DIR}/${script}"
  done

  # install_executor flattens the executor scripts into EXECUTOR_DIR with no
  # .github/scripts sibling, so gateway.sh's VM-layout and repo-checkout
  # relative guesses for openshell-version.sh both miss once prepare.sh/
  # cleanup.sh source the flattened copy. Ship the same pin file alongside it
  # so gateway.sh's flattened-layout guess (.github/scripts as a child of the
  # gateway.sh dir) resolves.
  if [ -f "${_openshell_version_sh}" ]; then
    mkdir -p "${EXECUTOR_DIR}/.github/scripts"
    cp "${_openshell_version_sh}" "${EXECUTOR_DIR}/.github/scripts/openshell-version.sh"
  else
    fail "openshell-version.sh not found (looked at ${_openshell_version_sh}); cannot provision the per-job gateway's version pin"
  fi

  ok "executor scripts installed"
}

# --------------------------------------------------------------------------
# 7. Patch gitlab-runner config.toml
# --------------------------------------------------------------------------
# Read or rewrite the custom-executor keys setup.sh manages in a config.toml.
#   custom_executor_keys read  <file>  — prints "key=value" per managed key,
#                                        value decoded (quotes, \\ and \"
#                                        escapes, any trailing comment removed),
#                                        plus "executor=value" for the
#                                        [[runners]] executor
#   custom_executor_keys write <file>  — prints the whole file with each managed
#                                        key whose decoded value differs from the
#                                        wanted one rewritten; every other line
#                                        (including a current key's comment)
#                                        verbatim
# Table headers are matched after dropping a trailing comment and CR, and
# values may be basic ("...") or literal ('...') strings, so a commented or
# CRLF config is parsed the same as a plain one.
# Keys are scoped to the table gitlab-runner reads them from — builds_dir and
# cache_dir in [[runners]], the *_exec keys in [runners.custom] — so a
# same-named key under another table is never read or touched.
# The wanted paths reach awk through ENVIRON, not -v, because -v interprets
# backslash escapes. Basic strings are written with \ and " escaped and read
# back by undoing exactly those two escapes; any other escape decodes to a
# value that never matches, so such a line is rewritten.
custom_executor_keys() {
  local mode="$1" file="$2"
  CE_BUILDS_DIR="${BUILDS_DIR}" \
    CE_CACHE_DIR="${CACHE_DIR}" \
    CE_PREPARE_EXEC="${EXECUTOR_DIR}/prepare.sh" \
    CE_RUN_EXEC="${EXECUTOR_DIR}/run.sh" \
    CE_CLEANUP_EXEC="${EXECUTOR_DIR}/cleanup.sh" \
    awk -v mode="${mode}" '
    function tomlenc(s,   i, c, out) {
      out = ""
      for (i = 1; i <= length(s); i++) {
        c = substr(s, i, 1)
        if (c == "\\" || c == "\"") out = out "\\"
        out = out c
      }
      return out
    }
    function tomldec(s,   i, c, n, out) {
      out = ""
      n = length(s)
      for (i = 1; i <= n; i++) {
        c = substr(s, i, 1)
        if (c == "\\" && i < n) {
          i++
          c = substr(s, i, 1)
          if (c != "\\" && c != "\"") out = out "\001"
        }
        out = out c
      }
      return out
    }
    BEGIN {
      want["builds_dir"] = ENVIRON["CE_BUILDS_DIR"]
      want["cache_dir"] = ENVIRON["CE_CACHE_DIR"]
      want["prepare_exec"] = ENVIRON["CE_PREPARE_EXEC"]
      want["run_exec"] = ENVIRON["CE_RUN_EXEC"]
      want["cleanup_exec"] = ENVIRON["CE_CLEANUP_EXEC"]
    }
    /^[ \t]*\[/ {
      section = $0
      sub(/^[ \t]+/, "", section)
      sub(/[ \t\r]*#.*$/, "", section)
      sub(/[ \t\r]+$/, "", section)
      if (mode == "write") print
      next
    }
    /^[ \t]*[a-z_]+[ \t]*=/ {
      key = $0
      sub(/^[ \t]+/, "", key)
      sub(/[ \t]*=.*$/, "", key)
      if (((key in want) \
        && ((section == "[[runners]]" && (key == "builds_dir" || key == "cache_dir")) \
          || (section == "[runners.custom]" && key ~ /_exec$/))) \
        || (mode == "read" && key == "executor" && section == "[[runners]]")) {
        val = $0
        sub(/^[^=]*=[ \t]*/, "", val)
        if (match(val, /^"([^"\\]|\\.)*"/)) {
          val = tomldec(substr(val, 2, RLENGTH - 2))
        } else if (match(val, /^\047[^\047]*\047/)) {
          val = substr(val, 2, RLENGTH - 2)
        } else {
          sub(/[ \t\r]*#.*$/, "", val)
          sub(/[ \t\r]+$/, "", val)
        }
        if (mode == "read") {
          print key "=" val
        } else if (val == want[key]) {
          print
        } else {
          indent = $0
          sub(/[^ \t].*$/, "", indent)
          eol = ($0 ~ /\r$/) ? "\r" : ""
          print indent key " = \"" tomlenc(want[key]) "\"" eol
        }
        next
      }
    }
    mode == "write" { print }
  ' "${file}"
}

# True when config.toml's [[runners]] executor decodes to "custom". Uses the
# same TOML-aware read as the managed keys, so `executor="custom"` and
# `executor = 'custom'` count like `executor = "custom"`.
config_uses_custom_executor() {
  local executor
  executor=$(custom_executor_keys read "${CONFIG_TOML}" | sed -n 's/^executor=//p')
  [ "${executor}" = "custom" ]
}

# Bring an existing custom-executor config's managed paths back in line with
# this user's EXECUTOR_DIR/BUILDS_DIR/CACHE_DIR. A runner whose config points
# at another home fails every job in prepare ("fork/exec .../prepare.sh: no
# such file or directory") while the scripts under EXECUTOR_DIR look healthy
# (#8160). Only the managed values are rewritten — registration (name, url,
# token, id) and every other setting stay byte-for-byte — and a config that
# already matches is left untouched (no write, no .bak).
reconcile_custom_executor_paths() {
  local current key count
  current=$(custom_executor_keys read "${CONFIG_TOML}")
  for key in builds_dir cache_dir prepare_exec run_exec cleanup_exec; do
    count=$(printf '%s\n' "${current}" | grep -c "^${key}=") || true
    if [ "${count}" -ne 1 ]; then
      fail "expected exactly 1 custom executor ${key} in config.toml, found ${count} — patch manually"
    fi
  done

  # The rewritten copy holds the runner token: remove it on any exit, not just
  # the success paths (global so the EXIT handler can still see it).
  RECONCILE_TMP=$(mktemp)
  trap 'rm -f "${RECONCILE_TMP:-}"' EXIT
  local tmp="${RECONCILE_TMP}"
  custom_executor_keys write "${CONFIG_TOML}" > "${tmp}"

  # Compare decoded values, not bytes: awk always ends the last line with a
  # newline, so a current config without one would otherwise look changed.
  local wanted old new
  wanted=$(custom_executor_keys read "${tmp}")
  if [ "${current}" = "${wanted}" ]; then
    rm -f "${tmp}"
    trap - EXIT
    ok "already using custom executor (executor, build and cache paths current)"
    return
  fi

  # The line-based writer cannot edit every valid TOML form (e.g. a multiline
  # string value would be left half-rewritten). Never install a rewrite that
  # does not parse; the EXIT trap removes the temp copy.
  if ! python3 -c 'import sys, tomllib; tomllib.load(open(sys.argv[1], "rb"))' "${tmp}" 2>/dev/null; then
    fail "rewritten config.toml is not valid TOML (multiline or otherwise unsupported managed value?) — ${CONFIG_TOML} left unchanged; patch manually"
  fi

  for key in builds_dir cache_dir prepare_exec run_exec cleanup_exec; do
    old=$(printf '%s\n' "${current}" | sed -n "s/^${key}=//p")
    new=$(printf '%s\n' "${wanted}" | sed -n "s/^${key}=//p")
    if [ "${old}" != "${new}" ]; then
      echo "  stale ${key}: ${old} -> ${new}"
    fi
  done

  cp "${CONFIG_TOML}" "${CONFIG_TOML}.bak"
  ok "backed up config.toml"
  cp "${tmp}" "${CONFIG_TOML}"
  rm -f "${tmp}"
  trap - EXIT
  ok "custom executor paths reconciled to ${HOME}"
}

patch_config() {
  info "Patching ${CONFIG_TOML}"

  # Ensure the config dir and file are accessible to the runner user.
  # The default RPM install creates these as root-owned 700/600.
  if [ -d "$(dirname "${CONFIG_TOML}")" ]; then
    sudo chown -R "${RUNNER_USER}:${RUNNER_USER}" "$(dirname "${CONFIG_TOML}")"
    sudo chmod 700 "$(dirname "${CONFIG_TOML}")"
  fi

  if ! [ -f "${CONFIG_TOML}" ]; then
    fail "config.toml not found at ${CONFIG_TOML}"
  fi

  # Single-runner VM assumption: these VMs register exactly one runner.
  # Both the shell -> custom patch and the custom-executor path
  # reconciliation edit "the" [[runners]] block, so a multi-runner config
  # is refused rather than partially patched. Patch those by hand.
  local runner_count
  # Headers may be indented (valid TOML); grep -c exits 1 on zero matches,
  # which must reach the diagnostic below rather than abort under set -e.
  runner_count=$(grep -c '^[[:space:]]*\[\[runners\]\]' "${CONFIG_TOML}") || true
  if [ "${runner_count}" -ne 1 ]; then
    fail "expected exactly 1 [[runners]] block in config.toml, found ${runner_count} — patch manually"
  fi

  # Re-run: do not trust an existing custom executor's paths — reconcile
  # them to this user's HOME (#8160). The executor value is read with the
  # same TOML-aware parsing as the managed keys, so `executor="custom"` and
  # `executor = 'custom'` are recognised too.
  if config_uses_custom_executor; then
    reconcile_custom_executor_paths
    mkdir -p "${BUILDS_DIR}" "${CACHE_DIR}"
    return
  fi

  # Single overwriting backup — a timestamped name accumulated a new
  # file on every real patch.
  cp "${CONFIG_TOML}" "${CONFIG_TOML}.bak"
  ok "backed up config.toml"

  # Build the replacement block
  local custom_block
  custom_block=$(cat <<EOF
  executor = "custom"
  builds_dir = "${BUILDS_DIR}"
  cache_dir = "${CACHE_DIR}"
  [runners.custom]
    prepare_exec = "${EXECUTOR_DIR}/prepare.sh"
    prepare_exec_timeout = 300
    run_exec = "${EXECUTOR_DIR}/run.sh"
    cleanup_exec = "${EXECUTOR_DIR}/cleanup.sh"
    cleanup_exec_timeout = 120
EOF
  )

  # Replace the executor line and inject the custom block.
  local tmp
  tmp=$(mktemp)
  awk -v block="${custom_block}" '
    /executor = "shell"/ { print block; next }
    { print }
  ' "${CONFIG_TOML}" > "${tmp}"

  cp "${tmp}" "${CONFIG_TOML}"
  rm -f "${tmp}"

  if ! grep -q 'executor = "custom"' "${CONFIG_TOML}"; then
    fail "failed to patch config.toml — 'executor = \"shell\"' not found in original config"
  fi

  mkdir -p "${BUILDS_DIR}" "${CACHE_DIR}"

  ok "config.toml patched"
}

# --------------------------------------------------------------------------
# 8. Pre-pull images
# --------------------------------------------------------------------------
prepull_images() {
  info "Pre-pulling images"

  podman pull -- "${RUNNER_IMAGE}"
  ok "pulled ${RUNNER_IMAGE}"

  podman pull -- "ghcr.io/nvidia/openshell/supervisor:${OPENSHELL_VERSION}"
  ok "pulled supervisor image"

  # The sandbox image is project-specific (set in .fullsend/ config) and pulled
  # on demand by the gateway — no pre-pull needed here.
}

# --------------------------------------------------------------------------
# 8b. Periodic Podman prune (user systemd timer)
# --------------------------------------------------------------------------
# Long-lived VMs accumulate superseded rootless images until the ~30 GiB
# root disk fills (#7663). A user-level timer reclaims unused images and
# stopped leftovers without touching in-flight job containers. Re-running
# setup.sh on an already-provisioned VM installs the timer (idempotent).
install_podman_prune() {
  info "Installing Podman prune timer"

  local src="${SCRIPT_DIR}/podman-prune.sh"
  if [ ! -f "${src}" ]; then
    fail "podman-prune.sh not found: ${src}"
  fi

  local libdir="${HOME}/.local/lib/fullsend"
  local unitdir="${HOME}/.config/systemd/user"
  local keepdir="${HOME}/.config/fullsend-gitlab-runner"
  mkdir -p "${libdir}" "${unitdir}" "${keepdir}"

  cp "${src}" "${libdir}/podman-prune.sh"
  chmod +x "${libdir}/podman-prune.sh"

  local supervisor="ghcr.io/nvidia/openshell/supervisor:${OPENSHELL_VERSION}"
  cat > "${keepdir}/keep-images" <<EOF
# Warm-cache images pre-pulled by setup.sh. podman-prune.sh will not rmi these.
${RUNNER_IMAGE}
${supervisor}
EOF

  cat > "${unitdir}/fullsend-podman-prune.service" <<'EOF'
[Unit]
Description=Prune unused rootless Podman images on the GitLab runner
After=podman.socket

[Service]
Type=oneshot
Nice=19
# The observed failure mode was ~45 GiB of unused images; without this,
# the systemd manager's DefaultTimeoutStartSec (typically 90s) SIGTERMs a
# still-running reclaim before it frees enough space, so the very next
# pull can still hit ENOSPC (review on #7663/#7669).
TimeoutStartSec=infinity
ExecStart=%h/.local/lib/fullsend/podman-prune.sh
EOF

  cat > "${unitdir}/fullsend-podman-prune.timer" <<'EOF'
[Unit]
Description=Periodically prune unused rootless Podman images on the GitLab runner

[Timer]
OnBootSec=10min
OnUnitActiveSec=1h
Persistent=true
RandomizedDelaySec=5min
Unit=fullsend-podman-prune.service

[Install]
WantedBy=timers.target
EOF

  user_systemctl daemon-reload
  user_systemctl enable --now fullsend-podman-prune.timer
  # Asynchronous first run so already-full VMs reclaim space without
  # waiting for OnBootSec. --no-block keeps setup.sh from waiting on a
  # multi-gigabyte prune.
  user_systemctl start --no-block fullsend-podman-prune.service || true
  ok "podman prune timer enabled"
}

# --------------------------------------------------------------------------
# 9. Verify
# --------------------------------------------------------------------------
# check_registration confirms config.toml holds exactly one runner, that it
# targets GITLAB_URL (the only instance this VM should serve), and that
# GitLab did not reject its token. `gitlab-runner verify` exits non-zero
# only when GitLab rejects a token (transport errors and unexpected HTTP
# statuses are non-fatal), so a stale registration left behind by an
# interrupted provisioning run fails here instead of as an idle runner. A
# zero exit only counts when verify's output says the runner is valid; an
# unconfirmed result (transport error, HTTP 503) is retried up to
# VERIFY_ATTEMPTS times, VERIFY_RETRY_SEC apart, then treated as a failure.
# Any failed check leaves gitlab-runner stopped.
check_registration() {
  local verify_out attempt=1
  local attempts="${VERIFY_ATTEMPTS:-3}" retry_sec="${VERIFY_RETRY_SEC:-5}"
  if ! check_registration_config; then
    stop_runner_service || true
    return 1
  fi
  while true; do
    if ! verify_out=$(gitlab-runner verify --config "${CONFIG_TOML}" 2>&1); then
      echo "  WARN: ${GITLAB_URL} rejected the runner token (gitlab-runner verify failed)"
      stop_runner_service || true
      return 1
    fi
    if printf '%s\n' "${verify_out}" | grep -qi 'is valid'; then
      ok "runner registered with ${GITLAB_URL} (token verified)"
      return 0
    fi
    if [ "${attempt}" -ge "${attempts}" ]; then
      echo "  WARN: GitLab did not confirm the runner token is valid after ${attempt} attempt(s)"
      stop_runner_service || true
      return 1
    fi
    echo "  WARN: GitLab did not confirm the runner token (attempt ${attempt}/${attempts}) — retrying in ${retry_sec}s"
    attempt=$((attempt + 1))
    sleep "${retry_sec}"
  done
}

# Check the paths config.toml actually points the custom executor at, not
# just the copies under EXECUTOR_DIR: a stale config fails every job while
# EXECUTOR_DIR looks healthy (#8160). setup.sh runs as RUNNER_USER, the
# service user setup_runner_user installs, so test(1) here checks access as
# that user. Returns the number of problems found.
verify_configured_executor_paths() {
  local errors=0 settings key path
  settings=$(custom_executor_keys read "${CONFIG_TOML}")
  for key in prepare_exec run_exec cleanup_exec builds_dir cache_dir; do
    path=$(printf '%s\n' "${settings}" | sed -n "s/^${key}=//p" | head -1)
    if [ -z "${path}" ]; then
      echo "  WARN: ${key} not configured in ${CONFIG_TOML}"; errors=$((errors + 1))
    elif [ "${key}" = "builds_dir" ] || [ "${key}" = "cache_dir" ]; then
      if [ -d "${path}" ] && [ -w "${path}" ] && [ -x "${path}" ]; then
        ok "configured ${key} ${path} writable"
      else
        echo "  WARN: configured ${key} ${path} missing or not writable/searchable by ${RUNNER_USER}"; errors=$((errors + 1))
      fi
    elif [ -f "${path}" ] && [ -r "${path}" ] && [ -x "${path}" ]; then
      ok "configured ${key} ${path} executable"
    else
      echo "  WARN: configured ${key} ${path} missing or not executable by ${RUNNER_USER}"; errors=$((errors + 1))
    fi
  done
  return "${errors}"
}

verify() {
  info "Verifying setup"

  local errors=0

  if openshell --version | grep -q "${OPENSHELL_VERSION}"; then
    ok "openshell version ${OPENSHELL_VERSION}"
  else
    echo "  WARN: openshell version mismatch"; errors=$((errors + 1))
  fi

  if user_systemctl is-enabled --quiet openshell-gateway.service 2>/dev/null; then
    echo "  WARN: openshell-gateway.service is enabled — jobs expect a per-job gateway"; errors=$((errors + 1))
  else
    ok "openshell-gateway.service not enabled (per-job)"
  fi
  if user_systemctl is-active --quiet openshell-gateway.service; then
    echo "  WARN: gateway is running at setup end — prepare.sh should start it per job"; errors=$((errors + 1))
  else
    ok "gateway not running (started per job in prepare.sh)"
  fi

  if user_systemctl is-active --quiet podman.socket; then
    ok "podman socket active"
  else
    echo "  WARN: podman socket not active"; errors=$((errors + 1))
  fi

  if user_systemctl is-enabled --quiet fullsend-podman-prune.timer; then
    ok "podman prune timer enabled"
  else
    echo "  WARN: podman prune timer not enabled"; errors=$((errors + 1))
  fi

  if systemctl is-active --quiet gitlab-runner; then
    ok "gitlab-runner service running"
  else
    echo "  WARN: gitlab-runner service not running"; errors=$((errors + 1))
  fi

  if config_uses_custom_executor; then
    ok "custom executor configured"
  else
    echo "  WARN: custom executor not in config"; errors=$((errors + 1))
  fi
  verify_configured_executor_paths || errors=$((errors + $?))

  if ! check_registration; then
    errors=$((errors + 1))
  fi

  # Smoke-test internal CA injection: verify a container can reach the internal
  # GitLab instance using the host CA bundle injected by the OCI hook.
  if podman run --rm --entrypoint sh --network=host -- "${RUNNER_IMAGE}" \
    -c 'command -v curl' >/dev/null 2>&1; then
    if podman run --rm --entrypoint "" --network=host "${RUNNER_IMAGE}" \
      curl -sf --max-time 10 --connect-timeout 5 -o /dev/null "${GITLAB_URL}" 2>/dev/null; then
      ok "container CA trust verified (${GITLAB_URL} reachable)"
    else
      echo "  WARN: container cannot reach ${GITLAB_URL} — CA hook may be broken"; errors=$((errors + 1))
    fi
  else
    echo "  INFO: container CA trust smoke test skipped (image lacks curl)"
  fi

  for script in job_id.sh prepare.sh run.sh cleanup.sh gateway.sh; do
    if test -x "${EXECUTOR_DIR}/${script}"; then
      ok "${script} executable"
    else
      echo "  WARN: ${script} not executable"; errors=$((errors + 1))
    fi
  done

  if [ "${errors}" -eq 0 ]; then
    echo ""
    echo "Setup complete. The runner is ready to accept fullsend-agent jobs."
    echo "Image: ${RUNNER_IMAGE}"
  else
    echo ""
    echo "Setup finished with ${errors} error(s) — review above."
    exit 1
  fi
}

# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------
# Sourced by setup_test.sh to call functions without provisioning.
if [ "${BASH_SOURCE[0]}" != "${0}" ]; then
  # shellcheck disable=SC2168
  return 0
fi

if [ -z "${GITLAB_URL}" ]; then
  fail "GITLAB_URL is required (e.g. GITLAB_URL=https://gitlab.example.com)"
fi
if ! [[ "${GITLAB_URL}" =~ ^https://[a-zA-Z0-9._-]+(:[0-9]+)?$ ]]; then
  fail "GITLAB_URL must start with https:// (got: ${GITLAB_URL})"
fi
if [ -z "${RUNNER_IMAGE}" ]; then
  fail "RUNNER_IMAGE is required (e.g. RUNNER_IMAGE=ghcr.io/fullsend-ai/fullsend-runner:v1.2.3)"
fi
if ! [[ "${GITLAB_RUNNER_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  fail "GITLAB_RUNNER_VERSION must be semver (got: ${GITLAB_RUNNER_VERSION})"
fi

echo "GitLab Runner VM Setup"
echo "======================"
echo "GitLab:       ${GITLAB_URL}"
echo "Runner tag:   ${RUNNER_TAG}"
echo "Runner image: ${RUNNER_IMAGE}"
echo "OpenShell:    ${OPENSHELL_VERSION}"
echo ""

fix_fedora_repos
install_gitlab_runner
install_ca_certs
register_runner
# Stop the runner while we configure the sandbox — it currently has
# executor = "shell" and would accept jobs before the custom executor is ready.
if sudo systemctl is-active --quiet gitlab-runner 2>/dev/null; then
  sudo systemctl stop gitlab-runner
  if sudo systemctl is-active --quiet gitlab-runner 2>/dev/null; then
    fail "gitlab-runner is still active after stop — refusing to continue with unsandboxed executor"
  fi
fi
setup_runner_user
setup_podman
install_openshell
configure_gateway
install_ca_hook
configure_per_job_gateway
install_executor
patch_config
prepull_images
install_podman_prune
sudo systemctl restart gitlab-runner
verify
