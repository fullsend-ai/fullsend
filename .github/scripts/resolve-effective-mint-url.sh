#!/usr/bin/env bash
# resolve-effective-mint-url.sh — Layered mint_url for GitHub dispatch workflows.
#
# Mirrors ConfigMintURLFromFile (overlay, then base) with FALLBACK_MINT_URL as
# the FULLSEND_MINT_URL snapshot when both files are silent. Stops short of the
# compiled default (https://mint.fullsend.sh), matching resolveMintURL in the CLI.
#
# Declared file values use the same HTTPS/userinfo gate as
# validateDeclaredMintURL. The FULLSEND_MINT_URL fallback also allows loopback
# HTTP (localhost / 127.0.0.1), matching mintclient.MintToken.
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
_from_file=false

# Aligns with validateDeclaredMintURL for file-sourced values. Never echoes
# the URL: rejected values may contain userinfo.
_validate_resolved_mint_url() {
  local url="$1"
  local from_file="${2:-false}"
  if [[ -z "${url}" ]]; then
    return 0
  fi
  if [[ "${url}" == *$'\n'* || "${url}" == *$'\r'* ]]; then
    echo "::error::mint_url must not contain CR or LF" >&2
    return 1
  fi

  local scheme rest authority host
  if [[ "${url}" =~ ^([A-Za-z][A-Za-z0-9+.-]*)://(.*)$ ]]; then
    scheme="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    rest="${BASH_REMATCH[2]}"
  else
    echo "::error::mint_url must be a valid HTTPS URL" >&2
    return 1
  fi
  authority="${rest%%[/?#]*}"
  if [[ -z "${authority}" ]]; then
    echo "::error::mint_url must be a valid HTTPS URL" >&2
    return 1
  fi
  if [[ "${authority}" == *@* ]]; then
    echo "::error::mint_url must not contain userinfo" >&2
    return 1
  fi
  if [[ "${scheme}" == "https" ]]; then
    return 0
  fi
  if [[ "${from_file}" != "true" && "${scheme}" == "http" ]]; then
    host="${authority%%:*}"
    if [[ "${host}" == "localhost" || "${host}" == "127.0.0.1" ]]; then
      return 0
    fi
  fi
  echo "::error::mint_url must be a valid HTTPS URL" >&2
  return 1
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
    _from_file=true
  elif [[ -f .fullsend/config.base.yaml ]]; then
    base=$(_read_mint_url .fullsend/config.base.yaml)
    if [[ -n "${base}" ]]; then
      _effective="${base}"
      _from_file=true
    fi
  fi
elif [[ -f .fullsend/config.base.yaml ]]; then
  base=$(_read_mint_url .fullsend/config.base.yaml)
  if [[ -n "${base}" ]]; then
    _effective="${base}"
    _from_file=true
  fi
fi

if ! _validate_resolved_mint_url "${_effective}" "${_from_file}"; then
  exit 1
fi

{
  printf 'mint_url=%s\n' "${_effective}"
} >>"${GITHUB_OUTPUT}"
