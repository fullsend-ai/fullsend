#!/usr/bin/env bash
# pad-e2e-org-repos — add empty filler repositories to a pool org so the
# e2e App installation's hourly API limit rises to its 12,500 cap (#9414).
#
# Usage: hack/pad-e2e-org-repos.sh [--apply] [--target N] [--max N] NN|ORG...
#   NN       — org number, e.g. 07 (halfsend-07)
#   ORG      — full org name, e.g. halfsend-07
#   --apply  — create the repositories; without it the script only reports
#              what it would create
#   --target — permanent repositories the org should hold (default 200)
#   --max    — most repositories to create in this run, across all orgs
#              (default 450, under GitHub's 500-an-hour content-creation limit)
#
# GitHub sets an App installation's hourly limit at 5,000 plus 50 for each
# repository over 20, capped at 12,500, so about 170 repositories reach the
# cap. Slot repositories (test-repo-NN and names starting with it) come and
# go with leases, so they do not count toward the target.
#
# Filler repositories are private, empty and named quota-filler-NNN. The
# slot driver and slot GC only touch names derived from the pool layout, so
# they never touch these. Do not delete them.
#
# Idempotent: existing filler names are skipped and an org already at the
# target gets nothing. Creation is paced (FILLER_PACE_SECONDS, default 8) to
# stay under 80 a minute and 500 an hour. A rate-limit error stops the run; run it
# again later to continue where it stopped.
#
# Requires gh authenticated as an owner of the pool org (or a member allowed
# to create repositories).

set -euo pipefail

FILLER_PREFIX="quota-filler-"
FILLER_DESCRIPTION="Quota filler for the e2e App installation rate limit (fullsend-ai/fullsend#9414). Do not delete."
FILLER_PACE_SECONDS="${FILLER_PACE_SECONDS:-8}"

# org_name turns NN into halfsend-NN and passes a full name through.
org_name() {
  if [[ "$1" =~ ^[0-9]{2}$ ]]; then
    echo "halfsend-$1"
  else
    echo "$1"
  fi
}

# is_slot_repo reports whether name is a lease-managed slot repository.
is_slot_repo() {
  [[ "$1" =~ ^test-repo-[0-9]{2}($|-) ]]
}

# list_repo_names prints every repository name in org, one per line.
# Errors go to stderr and return 1.
list_repo_names() {
  local org="$1" out
  if ! out=$(gh api --paginate "/orgs/${org}/repos?type=all&per_page=100" --jq '.[].name' 2>&1); then
    echo "    ERROR: could not list repositories in ${org}: ${out}" >&2
    return 1
  fi
  [[ -n "${out}" ]] && echo "${out}"
  return 0
}

# plan_fillers prints the filler names to create in org so its permanent
# repositories reach target. Reads existing names from stdin.
plan_fillers() {
  local target="$1" names name permanent=0 need i candidate
  names=$(cat)
  while IFS= read -r name; do
    [[ -z "${name}" ]] && continue
    is_slot_repo "${name}" || permanent=$((permanent + 1))
  done <<< "${names}"
  need=$((target - permanent))
  i=1
  while (( need > 0 )); do
    candidate=$(printf '%s%03d' "${FILLER_PREFIX}" "${i}")
    if ! grep -qxF "${candidate}" <<< "${names}"; then
      echo "${candidate}"
      need=$((need - 1))
    fi
    i=$((i + 1))
  done
}

# count_permanent prints how many names on stdin are not slot repositories.
count_permanent() {
  local name n=0
  while IFS= read -r name; do
    [[ -z "${name}" ]] && continue
    is_slot_repo "${name}" || n=$((n + 1))
  done
  echo "${n}"
}

# create_filler creates one private, empty filler repository. Returns 0 when
# created, 3 when the name already exists, 2 when GitHub rate limits the
# request, and 1 on any other error.
#
# No --silent: gh prints only the top-level message ("Repository creation
# failed.") to stderr, and the "name already exists" detail is in the JSON
# body it writes to stdout.
create_filler() {
  local org="$1" name="$2" out
  if out=$(gh api -X POST "/orgs/${org}/repos" \
      -f name="${name}" -f description="${FILLER_DESCRIPTION}" \
      -F private=true -F has_issues=false -F has_projects=false -F has_wiki=false \
      2>&1); then
    return 0
  fi
  if grep -qiE 'name already exists' <<< "${out}"; then
    return 3
  fi
  if grep -qiE 'HTTP 429|rate limit' <<< "${out}"; then
    echo "    ERROR: GitHub rate limited creating ${org}/${name}: ${out}" >&2
    return 2
  fi
  echo "    ERROR: could not create ${org}/${name}: ${out}" >&2
  return 1
}

# usage prints the header comment, up to its first blank line.
usage() {
  sed -n '2,/^$/{/^$/d;s/^# \{0,1\}//;p;}' "${BASH_SOURCE[0]}"
}

# whole_number prints a validated whole number in base 10, so a leading
# zero is not read as octal. Returns 1 for anything else.
whole_number() {
  [[ "$1" =~ ^[0-9]+$ ]] || return 1
  echo "$((10#$1))"
}

main() {
  local apply=false target=200 max=450 orgs=() arg value org
  while [[ $# -gt 0 ]]; do
    arg="$1"
    case "${arg}" in
      --apply) apply=true; shift ;;
      --target|--max)
        if [[ $# -lt 2 ]]; then
          echo "${arg} needs a value" >&2
          return 1
        fi
        if ! value=$(whole_number "$2"); then
          echo "${arg} must be a whole number, got '$2'" >&2
          return 1
        fi
        if [[ "${arg}" == --target ]]; then target="${value}"; else max="${value}"; fi
        shift 2 ;;
      -h|--help) usage; return 0 ;;
      -*) echo "unknown option ${arg}" >&2; return 1 ;;
      *)
        org=$(org_name "${arg}")
        # A mistyped argument must not fill an unintended org with
        # permanent repositories (same check as setup-new-e2e-org.sh).
        if ! [[ "${org}" =~ ^halfsend(-[0-9]+)?$ ]]; then
          echo "${arg} is not a pool org (halfsend or halfsend-NN)" >&2
          return 1
        fi
        orgs+=("${org}"); shift ;;
    esac
  done
  if [[ ${#orgs[@]} -eq 0 ]]; then
    echo "usage: hack/pad-e2e-org-repos.sh [--apply] [--target N] [--max N] NN|ORG..." >&2
    return 1
  fi

  local names permanent fillers name created=0 skipped=0 rc
  for org in "${orgs[@]}"; do
    echo "==> ${org}"
    names=$(list_repo_names "${org}") || return 1
    permanent=$(count_permanent <<< "${names}")
    fillers=$(plan_fillers "${target}" <<< "${names}")
    if [[ -z "${fillers}" ]]; then
      echo "    ${permanent} permanent repositories, target ${target}: nothing to do"
      continue
    fi
    echo "    ${permanent} permanent repositories, target ${target}: $(wc -l <<< "${fillers}" | tr -d ' ') to create"
    if [[ "${apply}" != true ]]; then
      echo "    dry run: would create $(head -1 <<< "${fillers}") … $(tail -1 <<< "${fillers}")"
      continue
    fi
    while IFS= read -r name; do
      if (( created >= max )); then
        echo "==> stopped at --max ${max}; run again to continue"
        return 0
      fi
      rc=0
      create_filler "${org}" "${name}" || rc=$?
      case "${rc}" in
        0) created=$((created + 1)); echo "    created ${org}/${name}" ;;
        3) skipped=$((skipped + 1)); echo "    ${org}/${name} already exists, skipped" ;;
        2) echo "==> stopped after ${created} repositories; run again later to continue" >&2; return 1 ;;
        *) return 1 ;;
      esac
      sleep "${FILLER_PACE_SECONDS}"
    done <<< "${fillers}"
  done
  echo "==> created ${created} repositories, skipped ${skipped} that already existed"
}

# Tests source this file for its function definitions; stop here when
# sourced.
if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
  return 0
fi

main "$@"
