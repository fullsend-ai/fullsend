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
# These can make bootstrap commands execute or load untrusted code. The
# activation gate also rejects project and readable inherited overrides, but
# clear inherited runner values before this script invokes external commands.
unset LD_AUDIT LD_PRELOAD LD_LIBRARY_PATH BASH_ENV TAR_OPTIONS
# These select Python modules that later job scripts would load.
unset PYTHONPATH PYTHONSTARTUP PYTHONHOME
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
  if ! FS_RELEASE_JSON=$(curl -q -fsSL --retry 3 --retry-delay 2 --retry-all-errors \
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
    curl -q -fsSL --retry 3 --retry-delay 2 --retry-all-errors \
      "${FS_BASE}/fullsend_${FS_VER}_linux_${FS_ARCH}.tar.gz" \
      -o /tmp/fullsend.tar.gz
    curl -q -fsSL --retry 3 --retry-delay 2 --retry-all-errors \
      "${FS_BASE}/checksums.txt" -o /tmp/checksums.txt
    FS_CHECKSUM_LINE=$(grep "fullsend_${FS_VER}_linux_${FS_ARCH}.tar.gz$" /tmp/checksums.txt || true)
    if [ -z "${FS_CHECKSUM_LINE}" ]; then
      echo "ERROR: checksum entry not found for fullsend_${FS_VER}_linux_${FS_ARCH}.tar.gz" >&2
      exit 1
    fi
    echo "${FS_CHECKSUM_LINE}" \
      | sed "s|fullsend_${FS_VER}_linux_${FS_ARCH}.tar.gz|/tmp/fullsend.tar.gz|" \
      | sha256sum -c -
    TAR_OPTIONS='' tar --no-same-owner -xzf /tmp/fullsend.tar.gz -C /usr/local/bin fullsend
    rm -f /tmp/fullsend.tar.gz /tmp/checksums.txt
    ;;
  *)
    # Untagged SHA — clone and build from source (Go toolchain
    # is in the runner base image for this purpose).
    # These select the git helpers/config and the Go toolchain flags,
    # environment, version, root, or cache program that this source-pinned
    # path (git fetch / go build) would run. They are cleared only here: the
    # release-download path above runs none of them, and self-hosted or
    # air-gapped runners may set them deliberately (e.g. GOTOOLCHAIN=local).
    # Record which of them the runner set (names only, never values) so the
    # operator learns why the build ignores them; they are still cleared.
    FS_CLEARED_VARS=""
    for FS_VAR in GIT_EXEC_PATH GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS GIT_TEMPLATE_DIR \
      GOFLAGS GOENV GOTOOLCHAIN GOROOT GOCACHEPROG; do
      if [ -n "${!FS_VAR:-}" ]; then
        FS_CLEARED_VARS="${FS_CLEARED_VARS:+${FS_CLEARED_VARS} }${FS_VAR}"
      fi
    done
    unset GIT_EXEC_PATH GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS GIT_TEMPLATE_DIR
    unset GOFLAGS GOENV GOTOOLCHAIN GOROOT GOCACHEPROG
    if [ -n "${FS_CLEARED_VARS}" ]; then
      echo "NOTICE: building the fullsend CLI from source for untagged fullsend_ref ${FULLSEND_VERSION}; cleared runner settings: ${FS_CLEARED_VARS}" >&2
      echo "This build ignores them. If this runner needs them (e.g. GOTOOLCHAIN=local or GOFLAGS=-mod=vendor), pin fullsend_ref to a release version tag, which downloads the release binary and leaves them untouched" >&2
    fi
    if ! command -v go >/dev/null 2>&1; then
      echo "ERROR: Go 1.20+ toolchain not found — required for source builds (non-release refs)" >&2
      echo "Use a runner image with Go 1.20+ installed or pin to a release version tag" >&2
      exit 1
    fi
    export GOPATH="${RUNNER_TEMP:-/tmp}/go"
    export GOCACHE="${RUNNER_TEMP:-/tmp}/go-cache"
    FSSRC=$(mktemp -d)
    # An empty template and a null hooks path keep this checkout from
    # running any hook copied from a template directory.
    git init --template= "${FSSRC}"
    git -C "${FSSRC}" remote add origin "https://github.com/${FULLSEND_REPO}.git"
    if ! git -C "${FSSRC}" fetch --depth 1 origin "${FULLSEND_VERSION}" \
      || ! git -C "${FSSRC}" -c core.hooksPath=/dev/null checkout FETCH_HEAD \
      || ! CGO_ENABLED=0 go build -C "${FSSRC}" \
        -ldflags "-X github.com/fullsend-ai/fullsend/internal/cli.version=${FULLSEND_VERSION}" \
        -o /usr/local/bin/fullsend ./cmd/fullsend/; then
      rm -rf "${FSSRC}"
      echo "ERROR: source build of the fullsend CLI failed for untagged fullsend_ref ${FULLSEND_VERSION}" >&2
      if [ -n "${FS_CLEARED_VARS}" ]; then
        echo "The build ran without these runner settings, which it clears before building: ${FS_CLEARED_VARS}" >&2
      fi
      echo "Pin fullsend_ref to a release version tag to download the release binary instead; see the GitLab configuration guide" >&2
      exit 1
    fi
    rm -rf "${FSSRC}"
    ;;
esac
fullsend --version
