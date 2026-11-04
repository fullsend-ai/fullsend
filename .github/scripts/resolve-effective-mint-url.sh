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

_effective="${FALLBACK_MINT_URL:-}"

_read_mint_url() {
  local file="$1"
  local value
  value=$(yq -r '.mint_url // ""' "${file}" 2>/dev/null || echo "")
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

{
  printf 'mint_url=%s\n' "${_effective}"
} >>"${GITHUB_OUTPUT}"
