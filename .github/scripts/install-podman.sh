#!/usr/bin/env bash
# Install a pinned podman static bundle from mgoltzsche/podman-static.
#
# Runner images ship a static podman 5.8.4 bundle under /usr/local/, which
# wins on PATH over anything apt installs at /usr/bin/ -- apt pinning to
# 4.x (#5738) never actually took effect, so podman --version kept
# reporting 5.8.4 regardless. This script installs the *same* version the
# image already ships, explicitly and verifiably, instead of fighting it:
# pinning our own copy over the same /usr/local location guarantees a
# known-good version+checksum regardless of what a future runner image
# update silently changes underneath us. No version change from what's
# already running in the image; smoke-tested end to end via
# functional-tests (real sandbox creation through the OpenShell
# gateway/API socket, not just the diagnostic `podman ps`/`logs` calls
# in internal/sandbox).
#
# This approach mirrors how runner-images itself installs podman
# (self-contained static tarball bundling podman + crun/runc + conmon +
# netavark + aardvark-dns + pasta) and is portable across distros.
#
# See #5733, #5742.
#
# Usage:
#   .github/scripts/install-podman.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
# shellcheck source=retry-with-backoff.sh
source "${SCRIPT_DIR}/retry-with-backoff.sh"

# Pinned podman-static release tag -- matches the version actions/runner-images
# itself pins for the same bundle, so we're never fighting the image's own
# install, just making it explicit and verified.
PODMAN_STATIC_TAG="v5.8.4"

# SHA-256 checksums for the pinned release archives, independently verified
# against the upstream release. These match the values actions/runner-images
# itself pins for this same tag. To update: download each archive and run
# `sha256sum podman-linux-<arch>.tar.gz`.
declare -A EXPECTED_SHA256=(
  [amd64]="a58765fe8be6ab3fb79f892f1a027b4ce4a7e8eb589df1ef960c167cbde08d69"
  [arm64]="a2f6b73cc0f7018e2e8518338a4ec27db70148e1af86e16719235605aefd1df3"
)

case "$(uname -m)" in
  x86_64)  arch="amd64" ;;
  aarch64) arch="arm64" ;;
  *)
    echo "::error::Unsupported architecture: $(uname -m)"
    exit 1
    ;;
esac

archive_url="https://github.com/mgoltzsche/podman-static/releases/download/${PODMAN_STATIC_TAG}/podman-linux-${arch}.tar.gz"
archive_path="$(mktemp)"
trap 'rm -f "${archive_path}"' EXIT

echo "Downloading podman static ${PODMAN_STATIC_TAG} (${arch})..."
# Transient failures (5xx, timeouts) are retried with backoff (#9203).
if ! download_with_retry "Podman ${PODMAN_STATIC_TAG} (${arch}) download" \
  "${archive_url}" "${archive_path}"; then
  echo "::error::Podman setup failed at stage 'download' for ${archive_url}" >&2
  exit 1
fi

# Verify the downloaded archive against the pinned checksum before
# extracting anything as root. Fail closed on mismatch: a mismatch is
# never retried or bypassed.
expected="${EXPECTED_SHA256[${arch}]}"
if ! echo "${expected}  ${archive_path}" | sha256sum -c --strict; then
  echo "::error::Podman ${PODMAN_STATIC_TAG} (${arch}) setup failed at stage 'verify checksum' (category: integrity); not retried" >&2
  exit 1
fi
echo "SHA-256 checksum verified for podman-linux-${arch}.tar.gz"

# The archive contains a top-level podman-linux-<arch>/ directory with
# usr/ and etc/ sub-trees. Extract only usr/ (the binaries): the bundled
# etc/containers/*.conf are generic Fedora-oriented defaults (e.g. a
# deprecated v1-format registries.conf) that would silently override
# whatever the runner image already has correctly configured for its own
# static bundle -- actions/runner-images re-applies its own post-install
# config fixes after this same extraction for exactly this reason, and
# that follow-up config has already changed upstream once. Skipping
# etc/ avoids re-chasing that moving target: existing config is left
# untouched, and podman falls back to sane compiled-in defaults if none
# is present.
sudo tar -xzf "${archive_path}" -C / --strip-components=1 \
  "podman-linux-${arch}/usr"

# Without the bundled etc/containers/containers.conf (intentionally not
# extracted above), podman falls back to the base OS default at
# /usr/share/containers/containers.conf -- on runner images that also
# have a distro-packaged podman, that default points at the distro's
# own crun (/usr/bin/crun), not our freshly-extracted one. Confirmed via
# `podman info`: conmon/netavark/aardvark-dns/pasta all correctly
# resolved to the new bundle, but ociRuntime still resolved to
# /usr/bin/crun at the old distro version. Force both possible
# resolution paths to the same verified binary rather than depending on
# podman's config-driven runtime search order.
sudo ln -sf /usr/local/bin/crun /usr/bin/crun
if [[ "$(readlink -f /usr/bin/crun)" != "/usr/local/bin/crun" ]]; then
  echo "::error::/usr/bin/crun does not resolve to our pinned /usr/local/bin/crun (see #5742)"
  exit 1
fi

# On Ubuntu >= 23.10, AppArmor restricts unprivileged user namespaces by
# default. The distro-packaged podman ships an /etc/apparmor.d/podman
# profile granting the `userns` permission; this static binary has none,
# so rootless podman fails with "failed to reexec: Permission denied"
# without it. Mirrors actions/runner-images' own install script.
# `flags=(unconfined)` is intentional and provides no sandboxing of
# podman itself beyond the explicit `userns` grant -- it exists solely
# to satisfy the kernel's "process must be AppArmor-confined" gate.
if [[ "$(cat /proc/sys/kernel/apparmor_restrict_unprivileged_userns 2>/dev/null)" == "1" ]]; then
  sudo tee /etc/apparmor.d/podman >/dev/null <<'EOF'
abi <abi/4.0>,
include <tunables/global>

profile podman /usr/{bin,local/bin}/podman flags=(unconfined) {
  userns,

  include if exists <local/podman>
}
EOF
  sudo apparmor_parser -r -W /etc/apparmor.d/podman
fi

installed_version="$(podman --version)"
case "${installed_version}" in
  *"version ${PODMAN_STATIC_TAG#v}"*) ;;
  *)
    echo "::error::Failed to install podman ${PODMAN_STATIC_TAG} (see #5742); got: ${installed_version}"
    exit 1
    ;;
esac

echo "${installed_version}"
