#!/usr/bin/env bash
# resolve-effective-mint-url.sh — Layered mint_url for GitHub dispatch workflows.
#
# Mirrors ConfigMintURLFromFile (overlay, then base) with FALLBACK_MINT_URL as
# the FULLSEND_MINT_URL snapshot when both files are silent. Stops short of the
# compiled default (https://mint.fullsend.sh), matching resolveMintURL in the CLI.
#
# Requires: yq, GITHUB_OUTPUT, FALLBACK_MINT_URL (may be empty).

set -euo pipefail

if [[ -z "${GITHUB_OUTPUT:-}" ]]; then
  echo "GITHUB_OUTPUT is required" >&2
  exit 1
fi

if ! command -v yq >/dev/null 2>&1; then
  echo "::error::yq is required to resolve mint_url from config" >&2
  exit 1
fi

_effective="${FALLBACK_MINT_URL:-}"

# Matches validateDeclaredMintURL in internal/repos/managed_config.go.
_validate_resolved_mint_url() {
  local url="$1"
  if [[ -z "${url}" ]]; then
    return 0
  fi
  if [[ "${url}" == *$'\n'* || "${url}" == *$'\r'* ]]; then
    echo "::error::mint_url must not contain CR or LF" >&2
    return 1
  fi
  if [[ ! "${url}" =~ ^https://[^/?#]+ ]]; then
    echo "::error::mint_url must be a valid HTTPS URL, got ${url@Q}" >&2
    return 1
  fi
  local after_scheme="${url#https://}"
  if [[ "${after_scheme}" == *@* ]]; then
    echo "::error::mint_url must not contain userinfo, got ${url@Q}" >&2
    return 1
  fi
  return 0
}

_read_mint_url() {
  local file="$1"
  local value
  if ! value=$(yq -r '.mint_url // ""' "${file}"); then
    echo "::error::failed to read mint_url from ${file}" >&2
    exit 1
  fi
  if [[ -n "${value}" && "${value}" != "null" ]]; then
    printf '%s' "${value}"
  fi
}

if [[ -f .fullsend/config.yaml ]]; then
  overlay=$(_read_mint_url .fullsend/config.yaml)
  if [[ -n "${overlay}" ]]; then
    _effective="${overlay}"
  elif [[ -f .fullsend/config.base.yaml ]]; then
    base=$(_read_mint_url .fullsend/config.base.yaml)
    if [[ -n "${base}" ]]; then
      _effective="${base}"
    fi
  fi
elif [[ -f .fullsend/config.base.yaml ]]; then
  base=$(_read_mint_url .fullsend/config.base.yaml)
  if [[ -n "${base}" ]]; then
    _effective="${base}"
  fi
fi

if ! _validate_resolved_mint_url "${_effective}"; then
  exit 1
fi

{
  printf 'mint_url=%s\n' "${_effective}"
} >>"${GITHUB_OUTPUT}"
