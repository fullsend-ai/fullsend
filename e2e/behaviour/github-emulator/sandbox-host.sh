#!/usr/bin/env bash
# Make this VM the sandbox host for every emulator runner, once per job.
#
#   e2e/behaviour/github-emulator/sandbox-host.sh
#
# On GitHub each agent job gets its own VM, and the action sets that VM up as
# a sandbox host: it installs Podman, runs `podman system migrate`, writes the
# gateway config and installs OpenShell. Here all runners share one VM and one
# rootless Podman store, so doing that per job breaks the other jobs: migrate
# stops every running container (their sandboxes), and concurrent installs
# race. This does the same setup once, before any runner takes a job; the job
# then exports OPENSHELL_GATEWAY_ENDPOINT so the action skips its local setup
# (the "runners that use a remote OpenShell gateway" route).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
scripts="$ROOT/.github/scripts"

"$scripts/install-podman.sh"

# Same as the action's "Configure rootless Podman" step.
test -f /sys/fs/cgroup/cgroup.controllers || { echo "::error::cgroups v2 required"; exit 1; }
user="$(whoami)"
grep -q "^${user}:" /etc/subuid || sudo usermod --add-subuids 100000-165535 --add-subgids 100000-165535 "$user"
podman system migrate
podman info >/dev/null
systemctl --user start podman.socket

# Same as the action's "Configure OpenShell gateway" step.
OPENSHELL_VERSION="$(GITHUB_ENV='' bash -c 'source "$1"; echo "$OPENSHELL_VERSION"' _ "$scripts/openshell-version.sh")"
mkdir -p "$HOME/.config/openshell"
printf 'OPENSHELL_BIND_ADDRESS=0.0.0.0\n' >"$HOME/.config/openshell/gateway.env"
printf '%s\n' '[openshell]' 'version = 2' '' '[openshell.gateway]' 'compute_driver = "podman"' '' \
  '[openshell.drivers.podman]' \
  "supervisor_image = \"ghcr.io/nvidia/openshell/supervisor:${OPENSHELL_VERSION}\"" \
  'health_check_interval_secs = 10' >"$HOME/.config/openshell/gateway.toml"

"$scripts/install-openshell.sh"
# The gateway service starts with the install; wait until it answers at the
# endpoint the runners will use.
for _ in $(seq 30); do openshell sandbox list >/dev/null 2>&1 && break; sleep 1; done
openshell sandbox list >/dev/null
echo "sandbox host ready: podman $(podman version --format '{{.Client.Version}}'), openshell $(openshell --version)"
