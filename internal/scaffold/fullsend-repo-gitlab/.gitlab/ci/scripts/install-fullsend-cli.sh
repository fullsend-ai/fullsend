#!/usr/bin/env bash
# install-fullsend-cli.sh — Trust the GitLab CI CA and install the CLI.
#
# Source this file from the generated poll and agent jobs' before_script
# (do not execute it) so CA-trust exports persist into the job script.
# Installs the fullsend CLI at the version pinned during
# `fullsend repos install`. Mirrors the GitHub Actions
# install-fullsend-cli action (#6445).
#
# FULLSEND_VERSION is an install-time placeholder replaced when the
# scaffold is rendered.

set -euo pipefail
# CI_DEBUG_TRACE guard — must run before any token-bearing
# commands to prevent secret leakage via debug trace logging.
if [ "${CI_DEBUG_TRACE:-}" = "true" ]; then
  echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets" >&2
  exit 1
fi
# Private-CA trust for self-hosted GitLab. Must run before any
# GitLab network operation (curl, git, or the fullsend Go client).
# CI_SERVER_TLS_CA_FILE is a job-local path; it is not available on
# a separately provisioned sandbox host. See operations.md.
. "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/trust-ci-server-ca.sh"
# Install fullsend CLI at the version pinned during
# `fullsend repos install`. Mirrors the GitHub Actions
# install-fullsend-cli action (#6445).
FULLSEND_VERSION="__FULLSEND_VERSION__"
FULLSEND_REPO="fullsend-ai/fullsend"
# Resolve "latest" to the actual release tag.
if [ "${FULLSEND_VERSION}" = "latest" ]; then
  # Use GITHUB_TOKEN when available to avoid GitHub API rate
  # limits (60 req/hr unauthenticated vs 5000 authenticated).
  FS_AUTH_HEADER=""
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    FS_AUTH_HEADER="Authorization: token ${GITHUB_TOKEN}"
  fi
  if ! FS_RELEASE_JSON=$(curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors \
    ${FS_AUTH_HEADER:+-H "${FS_AUTH_HEADER}"} \
    "https://api.github.com/repos/${FULLSEND_REPO}/releases/latest"); then
    echo "ERROR: Failed to fetch latest release from GitHub API" >&2
    exit 1
  fi
  FULLSEND_VERSION=$(printf '%s' "${FS_RELEASE_JSON}" | jq -r '.tag_name')
  if [ -z "${FULLSEND_VERSION}" ] || [ "${FULLSEND_VERSION}" = "null" ]; then
    echo "ERROR: Could not resolve latest fullsend release tag" >&2
    exit 1
  fi
fi
case "${FULLSEND_VERSION}" in
  v*)
    # Release version — download pre-built binary with checksum
    # verification from GitHub Releases.
    FS_VER="${FULLSEND_VERSION#v}"
    FS_ARCH="$(uname -m)"
    case "${FS_ARCH}" in
      x86_64)  FS_ARCH="amd64" ;;
      aarch64) FS_ARCH="arm64" ;;
      *)       echo "ERROR: unsupported architecture: ${FS_ARCH} (supported: x86_64, aarch64)" >&2; exit 1 ;;
    esac
    FS_BASE="https://github.com/${FULLSEND_REPO}/releases/download/${FULLSEND_VERSION}"
    curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors \
      "${FS_BASE}/fullsend_${FS_VER}_linux_${FS_ARCH}.tar.gz" \
      -o /tmp/fullsend.tar.gz
    curl -fsSL --retry 3 --retry-delay 2 --retry-all-errors \
      "${FS_BASE}/checksums.txt" -o /tmp/checksums.txt
    FS_CHECKSUM_LINE=$(grep "fullsend_${FS_VER}_linux_${FS_ARCH}.tar.gz$" /tmp/checksums.txt || true)
    if [ -z "${FS_CHECKSUM_LINE}" ]; then
      echo "ERROR: checksum entry not found for fullsend_${FS_VER}_linux_${FS_ARCH}.tar.gz" >&2
      exit 1
    fi
    echo "${FS_CHECKSUM_LINE}" \
      | sed "s|fullsend_${FS_VER}_linux_${FS_ARCH}.tar.gz|/tmp/fullsend.tar.gz|" \
      | sha256sum -c -
    tar --no-same-owner -xzf /tmp/fullsend.tar.gz -C /usr/local/bin fullsend
    rm -f /tmp/fullsend.tar.gz /tmp/checksums.txt
    ;;
  *)
    # Untagged SHA — clone and build from source (Go toolchain
    # is in the runner base image for this purpose).
    if ! command -v go >/dev/null 2>&1; then
      echo "ERROR: Go 1.20+ toolchain not found — required for source builds (non-release refs)" >&2
      echo "Use a runner image with Go 1.20+ installed or pin to a release version tag" >&2
      exit 1
    fi
    export GOPATH="${RUNNER_TEMP:-/tmp}/go"
    export GOCACHE="${RUNNER_TEMP:-/tmp}/go-cache"
    FSSRC=$(mktemp -d)
    git init "${FSSRC}"
    git -C "${FSSRC}" remote add origin "https://github.com/${FULLSEND_REPO}.git"
    git -C "${FSSRC}" fetch --depth 1 origin "${FULLSEND_VERSION}"
    git -C "${FSSRC}" checkout FETCH_HEAD
    CGO_ENABLED=0 go build -C "${FSSRC}" \
      -ldflags "-X github.com/fullsend-ai/fullsend/internal/cli.version=${FULLSEND_VERSION}" \
      -o /usr/local/bin/fullsend ./cmd/fullsend/
    rm -rf "${FSSRC}"
    ;;
esac
fullsend --version
