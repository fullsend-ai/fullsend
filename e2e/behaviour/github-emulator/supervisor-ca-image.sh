#!/usr/bin/env bash
# Make the agent sandboxes trust the emulator's per-run CA.
#
# OpenShell's egress proxy runs in the sandbox's supervisor container, and it
# verifies upstream TLS against bundled public roots plus the first system
# bundle in the SUPERVISOR image (/etc/ssl/certs/ca-certificates.crt). The
# sandbox image does not take part. So this builds the supervisor image the
# action's gateway config names (ghcr.io/nvidia/openshell/supervisor:<pinned
# version>) with the CA appended to that bundle, and tags it under the same
# name in the runner user's rootless podman store. The podman driver pulls a
# version-tagged supervisor only when it is missing, so the local image is
# used. Shipped agents pin their sandbox images by digest, which a local build
# could not match; the supervisor route needs no sandbox image change.
#
#   e2e/behaviour/github-emulator/supervisor-ca-image.sh CA_PEM
#
# Only for a throwaway VM: it replaces the local copy of a public image tag.
# SUPERVISOR_CA_TAG names another tag to build instead (a shared machine).
set -euo pipefail

ca="${1:?usage: supervisor-ca-image.sh CA_PEM}"
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
# shellcheck source=/dev/null
OPENSHELL_VERSION="$(GITHUB_ENV='' bash -c 'source "$1"; echo "$OPENSHELL_VERSION"' _ "$ROOT/.github/scripts/openshell-version.sh")"
image="ghcr.io/nvidia/openshell/supervisor:$OPENSHELL_VERSION"

ctx="$(mktemp -d)"
extract="supervisor-ca-extract-$$"
trap 'rm -rf "$ctx"; podman rm -f "$extract" >/dev/null 2>&1 || true' EXIT

podman pull -q "$image" >/dev/null
podman create --name "$extract" "$image" >/dev/null
podman cp "$extract:/etc/ssl/certs/ca-certificates.crt" "$ctx/bundle.pem"
chmod u+w "$ctx/bundle.pem"
printf '\n' >>"$ctx/bundle.pem"
cat "$ca" >>"$ctx/bundle.pem"
# The image has no shell, so the bundle is assembled here and copied in.
printf 'FROM %s\nCOPY bundle.pem /etc/ssl/certs/ca-certificates.crt\n' "$image" >"$ctx/Containerfile"
target="${SUPERVISOR_CA_TAG:-$image}"
podman build -q -t "$target" -f "$ctx/Containerfile" "$ctx" >/dev/null
echo "supervisor image $target ($image + CA) trusts $(openssl x509 -in "$ca" -noout -subject)"
