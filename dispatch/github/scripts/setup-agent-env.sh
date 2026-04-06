#!/usr/bin/env bash
# Copy environment variables whose names start with STAGE_PREFIX into GITHUB_ENV,
# using the name with the prefix stripped (multiline-safe). STAGE_PREFIX must end
# with '_' (e.g. TRIAGE_). The workflow step should set STAGE_PREFIX and any
# STAGE_PREFIX* variables (e.g. secrets mapped under prefixed names).

set -euo pipefail

: "${GITHUB_ENV:?GITHUB_ENV must be set}"
: "${STAGE_PREFIX:?STAGE_PREFIX must be set}"

delim="ENV_${RANDOM}_${RANDOM}_$$"
while IFS= read -r name; do
  case "${name}" in
    "${STAGE_PREFIX}"*)
      dest="${name#"${STAGE_PREFIX}"}"
      [[ -n "${dest}" ]] || continue
      {
        printf '%s<<%s\n' "${dest}" "${delim}"
        printf '%s' "$(printenv "${name}")"
        printf '\n%s\n' "${delim}"
      } >> "${GITHUB_ENV}"
      ;;
  esac
done < <(compgen -e | sort -u)
