#!/usr/bin/env bash
# Install pre-commit hook dependencies on the GitHub Actions runner.
#
# Reads a JSON manifest produced by resolve-precommit-tools.py and
# installs the listed tools. Supports four install types:
#   binary — download from release URL with SHA256 verification
#   apt    — install via apt-get
#   pip    — install via pip
#   npm    — install via npm -g
#
# Binary downloads use architecture detection (uname -m) and pinned
# checksums for supply-chain safety. Same pattern as post-code.sh and
# images/code/Containerfile.
#
# Usage:
#   install-precommit-tools.sh <manifest.json>
#
# The manifest is the JSON output of resolve-precommit-tools.py.
#
# Exit codes:
#   0 — all tools installed (or already present)
#   1 — critical failure (missing required tool, checksum mismatch)
set -euo pipefail

MANIFEST="${1:?Usage: install-precommit-tools.sh <manifest.json>}"

if [ ! -f "${MANIFEST}" ]; then
  echo "::error::Manifest not found: ${MANIFEST}"
  exit 1
fi

INSTALL_DIR="${HOME}/.local/bin"
mkdir -p "${INSTALL_DIR}"
export PATH="${INSTALL_DIR}:${PATH}"

# Detect architecture once.
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64)
    TRIPLE="x86_64-unknown-linux-gnu"
    GOARCH="x64"
    ;;
  aarch64)
    TRIPLE="aarch64-unknown-linux-gnu"
    GOARCH="arm64"
    ;;
  *)
    echo "::warning::Unsupported architecture: ${ARCH} — skipping binary installs"
    TRIPLE=""
    GOARCH=""
    ;;
esac

# Print warnings from the resolver (sanitize to prevent GHA command injection).
WARNINGS="$(jq -r '.warnings[]' "${MANIFEST}" 2>/dev/null || true)"
if [ -n "${WARNINGS}" ]; then
  while IFS= read -r w; do
    w="${w//::/ }"
    w="${w//%0A/ }"
    w="${w//%0a/ }"
    w="${w//%0D/ }"
    w="${w//%0d/ }"
    echo "::warning::${w}"
  done <<< "${WARNINGS}"
fi

TOOL_COUNT="$(jq '.tools | length' "${MANIFEST}" 2>/dev/null || echo 0)"
if [ "${TOOL_COUNT}" -eq 0 ]; then
  echo "No additional pre-commit tools to install"
  exit 0
fi

echo "Installing ${TOOL_COUNT} pre-commit tool dependency(ies)..."

# Process each tool entry.
while IFS= read -r entry; do
  TYPE="$(echo "${entry}" | jq -r '.type')"
  NAME="$(echo "${entry}" | jq -r '.name')"

  # Skip entries marked as handled elsewhere (e.g., gitleaks in post-scripts).
  SKIP="$(echo "${entry}" | jq -r '.skip_install // "false"')"
  if [ "${SKIP}" = "true" ]; then
    echo "  ${NAME}: skipped (managed by post-script)"
    continue
  fi

  case "${TYPE}" in
    binary)
      VERSION="$(echo "${entry}" | jq -r '.version')"
      if command -v "${NAME}" >/dev/null 2>&1; then
        INSTALLED_VERSION="$("${NAME}" --version 2>&1 | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1 || true)"
        if [ "${INSTALLED_VERSION}" = "${VERSION}" ]; then
          echo "  ${NAME}: already available v${VERSION} ($(command -v "${NAME}"))"
          continue
        fi
        echo "  ${NAME}: found v${INSTALLED_VERSION:-unknown}, need v${VERSION} — installing pinned version"
      fi

      if [ -z "${TRIPLE}" ]; then
        echo "::warning::Cannot install ${NAME} — unsupported architecture"
        continue
      fi

      URL_TEMPLATE="$(echo "${entry}" | jq -r '.url_template')"
      BINARY_NAME="$(echo "${entry}" | jq -r '.binary_name // .name')"
      STRIP_PREFIX="$(echo "${entry}" | jq -r '.strip_prefix // ""')"

      # Resolve checksum for current architecture.
      CHECKSUM="$(echo "${entry}" | jq -r --arg arch "${ARCH}" '.checksums[$arch] // empty')"
      if [ -z "${CHECKSUM}" ]; then
        echo "::warning::No checksum for ${NAME} on ${ARCH} — skipping"
        continue
      fi

      # Resolve per-tool goarch override (e.g., actionlint uses "amd64" not "x64").
      TOOL_GOARCH="$(echo "${entry}" | jq -r --arg arch "${ARCH}" '.goarch_override[$arch] // empty')"
      if [ -z "${TOOL_GOARCH}" ]; then
        TOOL_GOARCH="${GOARCH}"
      fi

      # Resolve URL template.
      URL="${URL_TEMPLATE}"
      URL="${URL//\{version\}/${VERSION}}"
      URL="${URL//\{triple\}/${TRIPLE}}"
      URL="${URL//\{goarch\}/${TOOL_GOARCH}}"

      echo "  ${NAME} v${VERSION}: downloading..."
      DL_TMPDIR="$(mktemp -d)"
      TARBALL="${DL_TMPDIR}/${NAME}.tar.gz"

      if ! curl -fsSL "${URL}" -o "${TARBALL}"; then
        echo "::warning::Failed to download ${NAME} v${VERSION} — skipping"
        rm -rf "${DL_TMPDIR}"
        continue
      fi
      if ! echo "${CHECKSUM}  ${TARBALL}" | sha256sum -c -; then
        echo "::error::Checksum verification failed for ${NAME} v${VERSION}"
        rm -rf "${DL_TMPDIR}"
        exit 1
      fi

      if ! tar xzf "${TARBALL}" -C "${DL_TMPDIR}"; then
        echo "::warning::Failed to extract ${NAME} archive — skipping"
        rm -rf "${DL_TMPDIR}"
        continue
      fi

      # Find and install the binary.
      if [ -n "${STRIP_PREFIX}" ]; then
        RESOLVED_PREFIX="${STRIP_PREFIX//\{triple\}/${TRIPLE}}"
        RESOLVED_PREFIX="${RESOLVED_PREFIX//\{version\}/${VERSION}}"
        BIN_PATH="${DL_TMPDIR}/${RESOLVED_PREFIX}/${BINARY_NAME}"
      else
        BIN_PATH="${DL_TMPDIR}/${BINARY_NAME}"
      fi

      if [ ! -f "${BIN_PATH}" ]; then
        echo "::warning::Binary not found at expected path: ${BIN_PATH}"
        FOUND="$(find "${DL_TMPDIR}" -name "${BINARY_NAME}" -type f | head -1)"
        if [ -n "${FOUND}" ]; then
          BIN_PATH="${FOUND}"
        else
          echo "::error::Cannot find ${BINARY_NAME} in archive"
          rm -rf "${DL_TMPDIR}"
          continue
        fi
      fi

      if ! mv "${BIN_PATH}" "${INSTALL_DIR}/${BINARY_NAME}"; then
        echo "::warning::Failed to install ${NAME} binary — skipping"
        rm -rf "${DL_TMPDIR}"
        continue
      fi
      chmod +x "${INSTALL_DIR}/${BINARY_NAME}"

      # Install extra binaries (e.g., uvx alongside uv).
      EXTRAS="$(echo "${entry}" | jq -r '.extra_binaries[]? // empty' 2>/dev/null || true)"
      if [ -n "${EXTRAS}" ]; then
        while IFS= read -r extra; do
          EXTRA_PATH=""
          if [ -n "${STRIP_PREFIX}" ]; then
            EXTRA_PATH="${DL_TMPDIR}/${RESOLVED_PREFIX}/${extra}"
          fi
          if [ ! -f "${EXTRA_PATH:-}" ]; then
            EXTRA_PATH="$(find "${DL_TMPDIR}" -name "${extra}" -type f | head -1)"
          fi
          if [ -n "${EXTRA_PATH}" ] && [ -f "${EXTRA_PATH}" ]; then
            mv "${EXTRA_PATH}" "${INSTALL_DIR}/${extra}"
            chmod +x "${INSTALL_DIR}/${extra}"
            echo "  ${NAME}: installed extra binary: ${extra}"
          fi
        done <<< "${EXTRAS}"
      fi

      rm -rf "${DL_TMPDIR}"
      echo "  ${NAME} v${VERSION}: installed to ${INSTALL_DIR}/${BINARY_NAME}"
      ;;

    apt)
      if command -v "${NAME}" >/dev/null 2>&1; then
        echo "  ${NAME}: already available"
        continue
      fi
      echo "  ${NAME}: installing via apt-get..."
      sudo apt-get update -qq && sudo apt-get install -y -qq "${NAME}" 2>/dev/null \
        || echo "::warning::Failed to install ${NAME} via apt-get"
      ;;

    pip)
      VERSION="$(echo "${entry}" | jq -r '.version // ""')"
      if [ -z "${VERSION}" ]; then
        echo "::warning::No version pinned for pip package ${NAME} — skipping for supply-chain safety"
        continue
      fi
      PKG="${NAME}==${VERSION}"
      echo "  ${NAME}: installing via pip..."
      pip install --quiet --no-deps --break-system-packages "${PKG}" 2>/dev/null \
        || pip3 install --quiet --no-deps --break-system-packages "${PKG}" 2>/dev/null \
        || echo "::warning::Failed to install ${NAME} via pip"
      ;;

    npm)
      VERSION="$(echo "${entry}" | jq -r '.version // ""')"
      if [ -z "${VERSION}" ]; then
        echo "::warning::No version pinned for npm package ${NAME} — skipping for supply-chain safety"
        continue
      fi
      NPM_PKG="${NAME}@${VERSION}"
      echo "  ${NAME}: installing via npm..."
      npm install -g --ignore-scripts "${NPM_PKG}" 2>/dev/null \
        || echo "::warning::Failed to install ${NAME} via npm"
      ;;

    *)
      echo "::warning::Unknown install type '${TYPE}' for ${NAME}"
      ;;
  esac
done < <(jq -c '.tools[]' "${MANIFEST}")

echo "Pre-commit tool installation complete"
