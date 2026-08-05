#!/usr/bin/env bash
# Reproducible setup for a GitLab Runner VM with Podman custom executor
# and OpenShell gateway for fullsend agent jobs.
#
# Prerequisites:
#   - VM created from vm.yaml (provides Fedora + podman + gitlab-runner)
#   - sudo access for the running user
#
# Normally called by create-vm.sh. Can also be run standalone:
#   REGISTRATION_TOKEN=glrt-xxx ./setup.sh
#
# Environment variables:
#   REGISTRATION_TOKEN    — GitLab runner token (required on first run)
#   GITLAB_URL            — GitLab instance URL (required)
#   RUNNER_IMAGE          — container image for jobs (required)
#   RUNNER_TAG            — runner tag for job matching (default: fullsend-gitlab-runner)
#   OPENSHELL_VERSION     — OpenShell version to install (default: 0.0.83)
#   GITLAB_RUNNER_VERSION — gitlab-runner version to install (default: 17.8.3)
set -euo pipefail

GITLAB_URL="${GITLAB_URL:-}"
RUNNER_TAG="${RUNNER_TAG:-fullsend-gitlab-runner}"
RUNNER_IMAGE="${RUNNER_IMAGE:-}"
OPENSHELL_VERSION="${OPENSHELL_VERSION:-0.0.83}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
EXECUTOR_DIR="${HOME}/gitlab-runner-executor"
BUILDS_DIR="${HOME}/builds"
CACHE_DIR="${HOME}/cache"
CONFIG_TOML="/etc/gitlab-runner/config.toml"
RUNNER_USER="${USER:-$(whoami)}"

GITLAB_RUNNER_VERSION="${GITLAB_RUNNER_VERSION:-17.8.3}"

info()  { echo "==> $*"; }
ok()    { echo "  OK: $*"; }
fail()  { echo "  FAIL: $*" >&2; exit 1; }

if [ -z "${GITLAB_URL}" ]; then
  fail "GITLAB_URL is required (e.g. GITLAB_URL=https://gitlab.example.com)"
fi
if [ -z "${RUNNER_IMAGE}" ]; then
  fail "RUNNER_IMAGE is required (e.g. RUNNER_IMAGE=ghcr.io/fullsend-ai/fullsend-runner:v1.2.3)"
fi

# --------------------------------------------------------------------------
# 0a. Install internal CA certificates (Red Hat internal GitLab uses a
#     private CA that is not in the default Fedora trust store)
# --------------------------------------------------------------------------
install_ca_certs() {
  info "Checking CA trust for ${GITLAB_URL}"

  # curl exits 0 even on 401/403 without -f; exit 60 means untrusted cert.
  if curl -so /dev/null "${GITLAB_URL}" 2>/dev/null; then
    ok "CA already trusted"
    return
  fi

  local host_port
  host_port=$(echo "${GITLAB_URL}" | sed 's|https\?://||;s|/.*||')
  local host="${host_port%%:*}"
  local port="${host_port##*:}"
  if [ "${port}" = "${host}" ]; then port=443; fi

  # TOFU (trust-on-first-use): the CA chain is fetched from the server itself.
  # For higher assurance, provide the CA bundle out-of-band via:
  #   sudo cp /path/to/ca-bundle.pem /etc/pki/ca-trust/source/anchors/gitlab-chain.pem
  #   sudo update-ca-trust
  echo "  WARN: trust-on-first-use — fetching CA chain from ${host}:${port}"
  echo | openssl s_client -connect "${host}:${port}" -showcerts 2>/dev/null \
    | awk '/BEGIN CERTIFICATE/,/END CERTIFICATE/' \
    | sudo tee /etc/pki/ca-trust/source/anchors/gitlab-chain.pem >/dev/null

  if [ ! -s /etc/pki/ca-trust/source/anchors/gitlab-chain.pem ]; then
    fail "failed to retrieve CA chain from ${host}:${port}"
  fi

  sudo update-ca-trust

  if curl -so /dev/null "${GITLAB_URL}" 2>/dev/null; then
    ok "CA certificates installed and trusted"
  else
    fail "CA install failed — ${GITLAB_URL} still not trusted"
  fi
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
  sudo curl -fsSL -o /usr/local/bin/gitlab-runner "${runner_url}"

  local checksums_url="https://gitlab-runner-downloads.s3.amazonaws.com/v${GITLAB_RUNNER_VERSION}/release.sha256"
  local expected
  expected=$(curl -fsSL "${checksums_url}" | grep " gitlab-runner-linux-${arch}$" | awk '{print $1}')
  if [ -z "${expected}" ]; then
    sudo rm -f /usr/local/bin/gitlab-runner
    fail "could not retrieve checksum for gitlab-runner-linux-${arch} from ${checksums_url}"
  fi
  local actual
  actual=$(sha256sum /usr/local/bin/gitlab-runner | awk '{print $1}')
  if [ "${actual}" != "${expected}" ]; then
    sudo rm -f /usr/local/bin/gitlab-runner
    fail "gitlab-runner checksum mismatch (expected ${expected}, got ${actual})"
  fi

  sudo chmod +x /usr/local/bin/gitlab-runner
  sudo gitlab-runner install --user "${RUNNER_USER}" --working-directory "${HOME}"
  sudo mkdir -p /etc/gitlab-runner
  sudo chown -R "${RUNNER_USER}:${RUNNER_USER}" /etc/gitlab-runner

  ok "gitlab-runner ${GITLAB_RUNNER_VERSION} installed"
}

# --------------------------------------------------------------------------
# 0c. Register runner with GitLab (first-time only)
# --------------------------------------------------------------------------
register_runner() {
  info "Checking runner registration"

  if [ -f "${CONFIG_TOML}" ] && grep -q '^\[\[runners\]\]' "${CONFIG_TOML}"; then
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
    --executor shell \
    --tag-list "${RUNNER_TAG}" \
    --description "$(hostname)"

  ok "runner registered with ${GITLAB_URL} (tag: ${RUNNER_TAG})"
}

# --------------------------------------------------------------------------
# 1. Switch gitlab-runner to run as the current user
# --------------------------------------------------------------------------
setup_runner_user() {
  info "Configuring gitlab-runner to run as ${RUNNER_USER}"

  local override_dir="/etc/systemd/system/gitlab-runner.service.d"
  local override_file="${override_dir}/user.conf"

  if [ -f "${override_file}" ] && grep -q "User=${RUNNER_USER}" "${override_file}"; then
    ok "systemd override already in place"
    return
  fi

  sudo mkdir -p "${override_dir}"
  sudo tee "${override_file}" > /dev/null <<EOF
[Service]
User=${RUNNER_USER}
Group=${RUNNER_USER}
WorkingDirectory=${HOME}
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

  systemctl --user enable --now podman.socket
  ok "podman socket enabled"
}

# --------------------------------------------------------------------------
# 3. Install OpenShell CLI
# --------------------------------------------------------------------------
install_openshell() {
  info "Installing OpenShell ${OPENSHELL_VERSION}"

  if command -v openshell &>/dev/null && systemctl --user cat openshell-gateway.service &>/dev/null; then
    local current
    current=$(openshell --version 2>/dev/null | grep -oP '\d+\.\d+\.\d+' | head -1 || echo "")
    if [ "${current}" = "${OPENSHELL_VERSION}" ]; then
      ok "OpenShell ${OPENSHELL_VERSION} already installed"
      return
    fi
  fi

  # Install from pre-built binaries to avoid dnf dependency on Fedora mirrors.
  # CLI: static musl tarball (same approach as images/runner/Containerfile)
  # Gateway: RPM installed with rpm --nodeps (no dnf repo access needed)
  local arch
  arch=$(uname -m)
  local release_url="https://github.com/NVIDIA/OpenShell/releases/download/v${OPENSHELL_VERSION}"

  curl -fsSL "${release_url}/openshell-checksums-sha256.txt" -o /tmp/openshell-checksums.txt

  # CLI binary (static musl)
  case "${arch}" in
    x86_64)  arch_triple="x86_64-unknown-linux-musl" ;;
    aarch64) arch_triple="aarch64-unknown-linux-musl" ;;
    *) fail "unsupported architecture: ${arch}" ;;
  esac
  local tarball="openshell-${arch_triple}.tar.gz"
  curl -fsSL "${release_url}/${tarball}" -o "/tmp/${tarball}"
  grep " ${tarball}\$" /tmp/openshell-checksums.txt \
    | sed "s| ${tarball}\$| /tmp/${tarball}|" \
    | sha256sum -c -
  mkdir -p /tmp/openshell-extract
  tar xzf "/tmp/${tarball}" -C /tmp/openshell-extract
  sudo install -m 0755 "$(find /tmp/openshell-extract -type f -name openshell | head -1)" /usr/local/bin/openshell
  rm -rf /tmp/openshell-extract "/tmp/${tarball}"

  # Gateway binary (RPM — includes systemd service and default config)
  local os_id os_version rpm_suffix
  os_id=$(. /etc/os-release && echo "${ID}")
  os_version=$(. /etc/os-release && echo "${VERSION_ID}")
  case "${os_id}" in
    fedora) rpm_suffix="fc${os_version}" ;;
    *)
      echo "  WARN: RPM suffix for '${os_id}' is a best guess — download may fail"
      rpm_suffix="${os_id}${os_version}"
      ;;
  esac
  local gateway_rpm="openshell-gateway-${OPENSHELL_VERSION}-1.${rpm_suffix}.${arch}.rpm"
  curl -fsSL "${release_url}/${gateway_rpm}" -o "/tmp/${gateway_rpm}"
  grep " ${gateway_rpm}\$" /tmp/openshell-checksums.txt \
    | sed "s| ${gateway_rpm}\$| /tmp/${gateway_rpm}|" \
    | sha256sum -c -
  if ! sudo rpm -Uvh --nodeps "/tmp/${gateway_rpm}" 2>&1; then
    rm -f "/tmp/${gateway_rpm}" /tmp/openshell-checksums.txt
    fail "RPM install of ${gateway_rpm} failed"
  fi
  rm -f "/tmp/${gateway_rpm}" /tmp/openshell-checksums.txt

  if ! command -v openshell &>/dev/null; then
    fail "openshell binary not found after install"
  fi
  if ! systemctl --user cat openshell-gateway.service &>/dev/null; then
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

  # The RPM's systemd service seeds ~/.config/openshell/gateway.toml from
  # /usr/share/openshell-gateway/gateway.toml.default on first start.
  # The default binds to 0.0.0.0:17670 and pins the Podman driver.
  # Only create gateway.env if it doesn't exist (backward compat).
  mkdir -p "${HOME}/.config/openshell"
  if [ -f "${HOME}/.config/openshell/gateway.env" ] && grep -q '^OPENSHELL_BIND_ADDRESS=127\.0\.0\.1$' "${HOME}/.config/openshell/gateway.env"; then
    ok "gateway binding already at 127.0.0.1"
  elif [ -f "${HOME}/.config/openshell/gateway.env" ]; then
    if grep -q '^OPENSHELL_BIND_ADDRESS=' "${HOME}/.config/openshell/gateway.env"; then
      sed -i 's/^OPENSHELL_BIND_ADDRESS=.*/OPENSHELL_BIND_ADDRESS=127.0.0.1/' "${HOME}/.config/openshell/gateway.env"
    else
      echo 'OPENSHELL_BIND_ADDRESS=127.0.0.1' >> "${HOME}/.config/openshell/gateway.env"
    fi
    ok "gateway binding set to 127.0.0.1"
  else
    echo 'OPENSHELL_BIND_ADDRESS=127.0.0.1' > "${HOME}/.config/openshell/gateway.env"
    ok "gateway binding set to 127.0.0.1"
  fi
}

# --------------------------------------------------------------------------
# 4b. Inject host CA trust into sandbox containers (OCI hook)
# --------------------------------------------------------------------------
install_ca_hook() {
  info "Installing OCI hook for sandbox CA trust"

  # The OpenShell supervisor (PID 1 inside sandbox containers) uses
  # rustls-native-certs which reads /etc/ssl/certs/ca-certificates.crt.
  # The default sandbox image ships standard Mozilla CAs but not the
  # internal CA installed by install_ca_certs(). An OCI createRuntime
  # hook copies the host trust bundle into every container's rootfs
  # before PID 1 starts, so the supervisor trusts internal endpoints.

  # Stage the host CA bundle in a user-writable location.
  mkdir -p "${HOME}/.local/share/ca-trust"
  cp /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem \
     "${HOME}/.local/share/ca-trust/ca-bundle.pem"
  chmod 644 "${HOME}/.local/share/ca-trust/ca-bundle.pem"

  # Install the hook script ($HOME expands at install time via unquoted heredoc).
  local ca_src="${HOME}/.local/share/ca-trust/ca-bundle.pem"
  sudo tee /usr/local/bin/inject-ca-certs.sh > /dev/null <<HOOKSCRIPT
#!/bin/bash
STATE=\$(cat)
# Try 'root' (crun/runc extension), fall back to 'bundle' (OCI spec) + config.json.
ROOTFS=\$(echo "\$STATE" | python3 -c "
import json, sys, os
state = json.load(sys.stdin)
root = state.get('root', '')
if root:
    print(root)
else:
    bundle = state.get('bundle', '')
    if bundle:
        cfg = os.path.join(bundle, 'config.json')
        if os.path.isfile(cfg):
            with open(cfg) as f:
                root_path = json.load(f).get('root', {}).get('path', '')
                if root_path and not os.path.isabs(root_path):
                    root_path = os.path.join(bundle, root_path)
                print(root_path)
" 2>/dev/null)
CA_SRC="${ca_src}"
CA_DST="\${ROOTFS}/etc/ssl/certs/ca-certificates.crt"
if [ -n "\$ROOTFS" ] && [ -f "\$CA_SRC" ] && [ -d "\${ROOTFS}/etc/ssl/certs" ]; then
  cp "\$CA_SRC" "\$CA_DST" 2>/dev/null || true
fi
exit 0
HOOKSCRIPT
  sudo chmod +x /usr/local/bin/inject-ca-certs.sh

  # Install the hook JSON.
  sudo mkdir -p /etc/containers/oci/hooks.d
  sudo tee /etc/containers/oci/hooks.d/inject-ca-certs.json > /dev/null <<'HOOKJSON'
{
  "version": "1.0.0",
  "hook": {
    "path": "/usr/local/bin/inject-ca-certs.sh"
  },
  "when": {
    "always": true
  },
  "stages": ["createRuntime"]
}
HOOKJSON

  # Tell Podman where to find hooks (required for rootless mode).
  mkdir -p "${HOME}/.config/containers"
  local conf="${HOME}/.config/containers/containers.conf"
  if [ -f "${conf}" ] && grep -q 'hooks_dir' "${conf}"; then
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
  systemctl --user restart podman.socket

  ok "OCI hook installed for CA trust injection"
}

# --------------------------------------------------------------------------
# 5. Start gateway via RPM-provided systemd service
# --------------------------------------------------------------------------
start_gateway() {
  info "Starting OpenShell gateway"

  # Use the RPM-provided service which handles config seeding, PKI
  # generation, and environment loading automatically.
  systemctl --user daemon-reload
  systemctl --user enable openshell-gateway.service
  systemctl --user restart openshell-gateway.service

  sleep 3
  if ! systemctl --user is-active --quiet openshell-gateway.service; then
    fail "gateway did not start — check: journalctl --user -u openshell-gateway"
  fi

  # Register the gateway with the CLI so openshell commands can find it.
  # Check for an active gateway (line starting with *).
  if ! openshell gateway list 2>/dev/null | grep -q '^\*'; then
    openshell gateway add --local https://127.0.0.1:17670
    ok "gateway registered and selected"
  else
    ok "gateway already registered"
  fi

  ok "gateway is running"
}

# --------------------------------------------------------------------------
# 6. Install custom executor scripts
# --------------------------------------------------------------------------
install_executor() {
  info "Installing custom executor scripts to ${EXECUTOR_DIR}"

  mkdir -p "${EXECUTOR_DIR}"

  for script in prepare.sh run.sh cleanup.sh; do
    local src="${SCRIPT_DIR}/executor/${script}"
    if [ ! -f "${src}" ]; then
      fail "executor script not found: ${src}"
    fi
    cp "${src}" "${EXECUTOR_DIR}/${script}"
    chmod +x "${EXECUTOR_DIR}/${script}"
  done

  ok "executor scripts installed"
}

# --------------------------------------------------------------------------
# 7. Patch gitlab-runner config.toml
# --------------------------------------------------------------------------
patch_config() {
  info "Patching ${CONFIG_TOML}"

  # Ensure the config dir and file are accessible to the runner user.
  # The default RPM install creates these as root-owned 700/600.
  if [ -d "$(dirname "${CONFIG_TOML}")" ]; then
    sudo chown -R "${RUNNER_USER}:${RUNNER_USER}" "$(dirname "${CONFIG_TOML}")"
    sudo chmod 755 "$(dirname "${CONFIG_TOML}")"
  fi

  if ! [ -f "${CONFIG_TOML}" ]; then
    fail "config.toml not found at ${CONFIG_TOML}"
  fi

  if grep -q 'executor = "custom"' "${CONFIG_TOML}"; then
    ok "already using custom executor"
    return
  fi

  cp "${CONFIG_TOML}" "${CONFIG_TOML}.bak.$(date +%Y%m%d%H%M%S)"
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

  sudo systemctl restart gitlab-runner
  ok "config.toml patched and runner restarted"
}

# --------------------------------------------------------------------------
# 8. Pre-pull images
# --------------------------------------------------------------------------
prepull_images() {
  info "Pre-pulling images"

  podman pull "${RUNNER_IMAGE}"
  ok "pulled ${RUNNER_IMAGE}"

  podman pull "ghcr.io/nvidia/openshell/supervisor:${OPENSHELL_VERSION}"
  ok "pulled supervisor image"

  local sandbox_image="ghcr.io/fullsend-ai/fullsend-sandbox:${OPENSHELL_VERSION}"
  podman pull "${sandbox_image}"
  ok "pulled sandbox image"
}

# --------------------------------------------------------------------------
# 9. Verify
# --------------------------------------------------------------------------
verify() {
  info "Verifying setup"

  local errors=0

  if openshell --version | grep -q "${OPENSHELL_VERSION}"; then
    ok "openshell version ${OPENSHELL_VERSION}"
  else
    echo "  WARN: openshell version mismatch"; errors=$((errors + 1))
  fi

  if systemctl --user is-active --quiet openshell-gateway.service; then
    ok "gateway running"
  else
    echo "  WARN: gateway not running"; errors=$((errors + 1))
  fi

  if systemctl --user is-active --quiet podman.socket; then
    ok "podman socket active"
  else
    echo "  WARN: podman socket not active"; errors=$((errors + 1))
  fi

  if grep -q 'executor = "custom"' "${CONFIG_TOML}"; then
    ok "custom executor configured"
  else
    echo "  WARN: custom executor not in config"; errors=$((errors + 1))
  fi

  for script in prepare.sh run.sh cleanup.sh; do
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
    echo "Setup finished with ${errors} warning(s) — review above."
  fi
}

# --------------------------------------------------------------------------
# Main
# --------------------------------------------------------------------------
echo "GitLab Runner VM Setup"
echo "======================"
echo "GitLab:       ${GITLAB_URL}"
echo "Runner tag:   ${RUNNER_TAG}"
echo "Runner image: ${RUNNER_IMAGE}"
echo "OpenShell:    ${OPENSHELL_VERSION}"
echo ""

install_ca_certs
install_gitlab_runner
register_runner
# Stop the runner while we configure the sandbox — it currently has
# executor = "shell" and would accept jobs before the custom executor is ready.
sudo systemctl stop gitlab-runner 2>/dev/null || true
setup_runner_user
setup_podman
install_openshell
configure_gateway
install_ca_hook
start_gateway
install_executor
patch_config
prepull_images
verify
