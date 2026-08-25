#!/usr/bin/env bash
# Reproducible setup for a GitLab Runner VM with Podman custom executor
# and OpenShell gateway for fullsend agent jobs.
#
# Prerequisites:
#   - VM created from vm.yaml (provides Fedora + podman + gitlab-runner)
#   - sudo access for the running user
#
# Normally called by create-vm.sh. Can also be run standalone:
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
# Note: The OpenShell version is pinned in .github/scripts/openshell-version.sh
# (Renovate-tracked). The SHA-pinned installer installs the repo-pinned version.
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
OPENSHELL_VERSION="${OPENSHELL_VERSION:-0.0.83}"

EXECUTOR_DIR="${HOME}/gitlab-runner-executor"
BUILDS_DIR="${HOME}/builds"
CACHE_DIR="${HOME}/cache"
CONFIG_TOML="/etc/gitlab-runner/config.toml"
RUNNER_USER="${USER:-$(whoami)}"

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
  local changed=0
  for repo_file in /etc/yum.repos.d/fedora*.repo; do
    [ -f "${repo_file}" ] || continue
    if grep -q '^metalink=' "${repo_file}"; then
      sudo sed -i -e 's/^metalink=/#metalink=/' -e 's/^#baseurl=/baseurl=/' "${repo_file}"
      changed=1
    fi
  done

  if [ "${changed}" -eq 1 ]; then
    ok "switched Fedora repos from metalink to baseurl"
  else
    ok "Fedora repos already using baseurl"
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
  # Overall runtime is already bounded by create-vm.sh's `timeout`.
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
    --executor shell

  ok "runner registered with ${GITLAB_URL}"
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

  # Reuse the repo's SHA-pinned installer for supply-chain integrity.
  # Try VM layout first (.github/scripts/ as child), then repo layout.
  local install_sh
  install_sh="${SCRIPT_DIR}/.github/scripts/install-openshell.sh"
  if [ ! -f "${install_sh}" ]; then
    install_sh="${SCRIPT_DIR}/../../.github/scripts/install-openshell.sh"
  fi
  if [ -f "${install_sh}" ]; then
    bash "${install_sh}"
  else
    fail "install-openshell.sh not found — run from the VM layout or repo checkout"
  fi

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

  # Pin supervisor_image to the Renovate-tracked version (matching action.yml).
  local gateway_toml="${HOME}/.config/openshell/gateway.toml"
  local supervisor_image="ghcr.io/nvidia/openshell/supervisor:${OPENSHELL_VERSION}"
  if [ -f "${gateway_toml}" ] && grep -qF "supervisor_image = \"${supervisor_image}\"" "${gateway_toml}"; then
    ok "supervisor_image already pinned to ${supervisor_image}"
  elif [ -f "${gateway_toml}" ] && grep -q "supervisor_image" "${gateway_toml}"; then
    sed -i "s|supervisor_image = .*|supervisor_image = \"${supervisor_image}\"|" "${gateway_toml}"
    ok "supervisor_image updated to ${supervisor_image}"
  else
    mkdir -p "$(dirname "${gateway_toml}")"
    if [ -f "${gateway_toml}" ] && grep -q '^\[openshell\.gateway\]' "${gateway_toml}"; then
      sed -i "/^\[openshell\.gateway\]/a supervisor_image = \"${supervisor_image}\"" "${gateway_toml}"
    elif [ -f "${gateway_toml}" ]; then
      printf '\n[openshell]\nversion = 1\n\n[openshell.gateway]\nsupervisor_image = "%s"\n' "${supervisor_image}" >> "${gateway_toml}"
    else
      # The RPM's user unit only seeds gateway.toml.default when no config
      # exists, and this function runs before the first start — so writing a
      # bare stub here would permanently drop the packaged defaults (without
      # compute_drivers the gateway auto-detects Kubernetes before Podman).
      # Seed from the packaged default so the values track the installed RPM;
      # the literal fallback mirrors v0.0.83's gateway.toml.default.
      local packaged_default=/usr/share/openshell-gateway/gateway.toml.default
      if [ -f "${packaged_default}" ]; then
        install -m 0644 "${packaged_default}" "${gateway_toml}"
        if grep -q '^\[openshell\.gateway\]' "${gateway_toml}"; then
          sed -i "/^\[openshell\.gateway\]/a supervisor_image = \"${supervisor_image}\"" "${gateway_toml}"
        else
          printf '\n[openshell.gateway]\nsupervisor_image = "%s"\n' "${supervisor_image}" >> "${gateway_toml}"
        fi
      else
        printf '[openshell]\nversion = 1\n\n[openshell.gateway]\nbind_address = "0.0.0.0:17670"\ncompute_drivers = ["podman"]\nsupervisor_image = "%s"\n' "${supervisor_image}" > "${gateway_toml}"
      fi
    fi
    ok "supervisor_image pinned to ${supervisor_image}"
  fi
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
  cp /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem \
     "${HOME}/.local/share/ca-trust/ca-bundle.pem"
  chmod 644 "${HOME}/.local/share/ca-trust/ca-bundle.pem"

  # Install the hook script ($HOME expands at install time via unquoted heredoc).
  local ca_src="${HOME}/.local/share/ca-trust/ca-bundle.pem"
  sudo tee /usr/local/bin/inject-ca-certs.sh > /dev/null <<HOOKSCRIPT
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

  local i
  for i in $(seq 1 10); do
    if systemctl --user is-active --quiet openshell-gateway.service; then
      break
    fi
    if [ "${i}" -eq 10 ]; then
      fail "gateway did not start after 10s — check: journalctl --user -u openshell-gateway"
    fi
    sleep 1
  done

  # Register the gateway with the CLI so openshell commands can find it.
  # Check for an active gateway (line starting with *).
  if ! openshell gateway list 2>/dev/null | grep -Eq '^[[:space:]]*\*'; then
    # `gateway add` is not idempotent — it refuses when metadata for the
    # canonical "openshell" loopback name already exists — so fall back to
    # selecting that name. Both failing must fail setup: every job's agent
    # depends on this registration, and verify() only checks the systemd unit.
    local add_err
    if ! add_err=$(openshell gateway add --local https://127.0.0.1:17670 2>&1) \
      && ! openshell gateway select openshell >/dev/null 2>&1; then
      fail "could not register or select the OpenShell gateway: ${add_err}"
    fi
    if ! openshell gateway list 2>/dev/null | grep -Eq '^[[:space:]]*\*'; then
      fail "no active OpenShell gateway after add/select"
    fi
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

  for script in job_id.sh prepare.sh run.sh cleanup.sh; do
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
    sudo chmod 700 "$(dirname "${CONFIG_TOML}")"
  fi

  if ! [ -f "${CONFIG_TOML}" ]; then
    fail "config.toml not found at ${CONFIG_TOML}"
  fi

  if grep -q 'executor = "custom"' "${CONFIG_TOML}"; then
    ok "already using custom executor"
    return
  fi

  local runner_count
  runner_count=$(grep -c '^\[\[runners\]\]' "${CONFIG_TOML}")
  if [ "${runner_count}" -ne 1 ]; then
    fail "expected exactly 1 [[runners]] block in config.toml, found ${runner_count} — patch manually"
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

  # The unit being active says nothing about CLI registration, which is what
  # the agent inside job containers actually resolves the gateway through.
  if openshell gateway list 2>/dev/null | grep -Eq '^[[:space:]]*\*'; then
    ok "gateway registered with the CLI"
  else
    echo "  WARN: no active gateway in 'openshell gateway list'"; errors=$((errors + 1))
  fi

  if systemctl --user is-active --quiet podman.socket; then
    ok "podman socket active"
  else
    echo "  WARN: podman socket not active"; errors=$((errors + 1))
  fi

  if systemctl is-active --quiet gitlab-runner; then
    ok "gitlab-runner service running"
  else
    echo "  WARN: gitlab-runner service not running"; errors=$((errors + 1))
  fi

  if grep -q 'executor = "custom"' "${CONFIG_TOML}"; then
    ok "custom executor configured"
  else
    echo "  WARN: custom executor not in config"; errors=$((errors + 1))
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

  for script in job_id.sh prepare.sh run.sh cleanup.sh; do
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
start_gateway
install_executor
patch_config
prepull_images
sudo systemctl restart gitlab-runner
verify
