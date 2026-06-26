#!/bin/bash
# Usage: curl -fsSL https://raw.githubusercontent.com/fullsend-ai/fullsend/main/scripts/install.sh | bash
#        curl -fsSL https://raw.githubusercontent.com/fullsend-ai/fullsend/main/scripts/install.sh | bash -s -- --version v0.15.0
#        curl -fsSL https://raw.githubusercontent.com/fullsend-ai/fullsend/main/scripts/install.sh | bash -s -- --dir /usr/local/bin
set -euo pipefail

VERSION="${FULLSEND_VERSION:-latest}"
INSTALL_DIR="${FULLSEND_INSTALL_DIR:-$HOME/.local/bin}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version|-v) VERSION="$2"; shift 2 ;;
    --dir|-d) INSTALL_DIR="$2"; shift 2 ;;
    *) echo "Unknown option: $1" >&2; exit 1 ;;
  esac
done

retry() {
  local max=3 attempt=1 delay=5
  while true; do
    if "$@"; then return 0; fi
    if (( attempt >= max )); then echo "Failed after ${max} attempts" >&2; return 1; fi
    echo "Attempt ${attempt}/${max} failed, retrying in ${delay}s..." >&2
    sleep "${delay}"
    (( attempt++ ))
    (( delay *= 2 ))
  done
}

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "${OS}" in Darwin) OS=darwin ;; Linux) OS=linux ;; esac
case "${ARCH}" in x86_64) ARCH=amd64 ;; aarch64|arm64) ARCH=arm64 ;; esac

if [[ "${VERSION}" == "latest" ]]; then
  VERSION="$(retry curl -fsSL https://api.github.com/repos/fullsend-ai/fullsend/releases/latest | grep '"tag_name"' | cut -d'"' -f4)"
fi

# ponytail: 40-char SHA → resolve to tag via git refs API
# Annotated tags need dereference; lightweight tags match directly.
if [[ "${VERSION}" =~ ^[0-9a-f]{40}$ ]]; then
  INPUT_SHA="${VERSION}"
  TAG=""
  # Get all version tags, filter to semver (vX.Y.Z)
  REFS="$(retry curl -fsSL "https://api.github.com/repos/fullsend-ai/fullsend/git/matching-refs/tags/v")"
  for ROW in $(echo "${REFS}" | jq -r '.[] | @base64'); do
    REF_DATA="$(echo "${ROW}" | base64 -d)"
    REF_NAME="$(echo "${REF_DATA}" | jq -r '.ref' | sed 's|refs/tags/||')"
    # Skip non-semver tags (v0, rc tags, etc)
    [[ ! "${REF_NAME}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] && continue
    OBJ_SHA="$(echo "${REF_DATA}" | jq -r '.object.sha')"
    OBJ_TYPE="$(echo "${REF_DATA}" | jq -r '.object.type')"
    if [[ "${OBJ_TYPE}" == "commit" && "${OBJ_SHA}" == "${INPUT_SHA}" ]]; then
      TAG="${REF_NAME}"; break
    elif [[ "${OBJ_TYPE}" == "tag" ]]; then
      # Dereference annotated tag
      COMMIT_SHA="$(retry curl -fsSL "https://api.github.com/repos/fullsend-ai/fullsend/git/tags/${OBJ_SHA}" | jq -r '.object.sha')"
      if [[ "${COMMIT_SHA}" == "${INPUT_SHA}" ]]; then
        TAG="${REF_NAME}"; break
      fi
    fi
  done
  if [[ -z "${TAG}" ]]; then
    echo "Error: SHA ${VERSION} does not match any release" >&2
    exit 1
  fi
  echo "Resolved SHA ${INPUT_SHA:0:12} to ${TAG}"
  VERSION="${TAG}"
fi

[[ "${VERSION}" != v* ]] && VERSION="v${VERSION}"
ASSET_VERSION="${VERSION#v}"

URL="https://github.com/fullsend-ai/fullsend/releases/download/${VERSION}/fullsend_${ASSET_VERSION}_${OS}_${ARCH}.tar.gz"
echo "Downloading fullsend ${VERSION} for ${OS}/${ARCH}..."

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
retry curl -fsSL "${URL}" -o "${TMP}/fullsend.tar.gz"
tar -xzf "${TMP}/fullsend.tar.gz" -C "${TMP}"

mkdir -p "${INSTALL_DIR}"
mv "${TMP}/fullsend" "${INSTALL_DIR}/fullsend"
chmod +x "${INSTALL_DIR}/fullsend"

echo "Installed fullsend to ${INSTALL_DIR}/fullsend"
if [[ ":${PATH}:" != *":${INSTALL_DIR}:"* ]]; then
  echo "Add to PATH: export PATH=\"${INSTALL_DIR}:\$PATH\""
fi
