#!/usr/bin/env bash
# Copy environment variables whose names start with AGENT_PREFIX into GITHUB_ENV,
# using the name with the prefix stripped (multiline-safe). AGENT_PREFIX must end
# with '_' (e.g. TRIAGE_). The workflow step should set AGENT_PREFIX and any
# AGENT_PREFIX* variables (e.g. secrets mapped under prefixed names).
#
# Per-run override passthrough: when FULLSEND_REPO_VARS holds the caller
# repository's Actions variables as JSON (`${{ toJSON(vars) }}`), the
# allowlisted FULLSEND_* override variables are exported too, so a repo can
# switch a role's runtime/model/effort with a repository variable instead of
# a pull request. A role-prefixed variable (TRIAGE_FULLSEND_MODEL) wins over
# the plain one (FULLSEND_MODEL). Values must be single-line and limited to
# the characters a model id / runtime name can contain; anything else is
# skipped with a warning. fullsend validates the values themselves.
# The whole variable map is passed (not individual keys) because the
# custom-harness matrix job only knows its role at runtime and GitHub
# expressions cannot upper-case it to build the prefixed name; the map holds
# the caller repository's own non-secret Actions variables, which the
# workflow can already read, and only the allowlisted keys leave this script.

set -euo pipefail

: "${GITHUB_ENV:?GITHUB_ENV must be set}"
: "${AGENT_PREFIX:?AGENT_PREFIX must be set}"

delim="ENV_$(openssl rand -hex 16)"
while IFS= read -r name; do
  case "${name}" in
    "${AGENT_PREFIX}"*)
      dest="${name#"${AGENT_PREFIX}"}"
      [[ -n "${dest}" ]] || continue
      {
        printf '%s<<%s\n' "${dest}" "${delim}"
        printf '%s' "$(printenv "${name}")"
        printf '\n%s\n' "${delim}"
      } >> "${GITHUB_ENV}"
      ;;
  esac
done < <(compgen -e | sort -u)

# Override passthrough from repository variables (optional).
if [[ -n "${FULLSEND_REPO_VARS:-}" ]]; then
  # FULLSEND_PI_MODEL is the pre-#6526 pi-only name, honoured by the CLI as a
  # lower-precedence alias of FULLSEND_MODEL on pi runs.
  override_keys=(FULLSEND_RUNTIME FULLSEND_MODEL FULLSEND_EFFORT FULLSEND_FALLBACK_MODELS FULLSEND_PI_PROVIDER FULLSEND_PI_MODEL)
  for key in "${override_keys[@]}"; do
    # Role-prefixed first, then plain. jq -r yields "" when absent.
    value="$(printf '%s' "${FULLSEND_REPO_VARS}" | jq -r --arg k "${AGENT_PREFIX}${key}" --arg p "${key}" '(.[$k] // .[$p] // "") | tostring')"
    [[ -n "${value}" ]] || continue
    if [[ ! "${value}" =~ ^[A-Za-z0-9._/@:,-]+$ ]]; then
      echo "::warning::ignoring repository variable ${key}: value contains characters outside [A-Za-z0-9._/@:,-]"
      continue
    fi
    printf '%s=%s\n' "${key}" "${value}" >> "${GITHUB_ENV}"
    echo "${key}=${value} (repository variable)"
  done
fi
