#!/usr/bin/env bash
# lib.sh — shared helpers and constants for the e2e org pool setup scripts.
#
# Sourced by setup-manual.sh (interactive operator steps) and setup-org.sh
# (non-interactive CI execution). Tests source this file for its function
# definitions; nothing here runs against GitHub on its own.
#
# shellcheck disable=SC2034  # constants are used by the sourcing scripts

TEST_APP_SET="fullsend-test"
TEST_ROLES=(fullsend triage coder review retro prioritize)
E2E_APP_SET="fullsend-ai"
BOT_USER="botsend"
TEST_WRITE_USER="fstest-write"
TEST_TRIAGE_USER="fstest-triage"
TEST_OUTSIDER_USER="fstest-outsider"
STATUS_TEAM_SLUG="mint-status"

# require_gh exits when the gh CLI is missing or not authenticated.
require_gh() {
  if ! command -v gh &>/dev/null; then
    echo "Error: gh CLI is required. Install from https://cli.github.com/" >&2
    exit 1
  fi
  if ! gh auth status &>/dev/null; then
    echo "Error: not authenticated with gh. Run 'gh auth login' first." >&2
    exit 1
  fi
}

# parse_org_arg prints the org name for a numeric NN (halfsend-NN) or a
# full org name, and validates it against the known pool/STAGE names.
# The setup scripts invite test actors and assign them all-repository
# roles; a mistyped or copy-pasted argument must not silently do that to
# an unintended org.
parse_org_arg() {
  local org="$1"
  if [[ "${org}" =~ ^[0-9]+$ ]]; then
    org="$(printf 'halfsend-%02d' "$((10#${org}))")"
  fi
  if ! [[ "${org}" =~ ^halfsend(-[0-9]{2})?$ ]]; then
    echo "Error: '${org}' is not a recognized pool/STAGE org name." >&2
    echo "  Expected halfsend-NN (DEV pool) or halfsend (STAGE)." >&2
    exit 1
  fi
  echo "${org}"
}

# open_browser tries to open a URL in the default browser.
open_browser() {
  local url="$1"
  if command -v xdg-open &>/dev/null; then
    xdg-open "${url}" 2>/dev/null || true
  elif command -v open &>/dev/null; then
    open "${url}" 2>/dev/null || true
  fi
}

# wait_for_user pauses until the user presses Enter.
wait_for_user() {
  local msg="${1:-Press Enter to continue...}"
  read -rp "    ${msg}" </dev/tty
  echo
}

# poll_for_app_install polls the installations API until the given app slug
# appears, or times out after POLL_TIMEOUT seconds (default 120).
# Returns 0 if found, 1 if timed out.
POLL_TIMEOUT="${POLL_TIMEOUT:-120}"
POLL_INTERVAL=3
poll_for_app_install() {
  local org="$1" slug="$2"
  local deadline=$((SECONDS + POLL_TIMEOUT))
  while (( SECONDS < deadline )); do
    sleep "${POLL_INTERVAL}"
    if gh api "/orgs/${org}/installations" --jq '.installations[].app_slug' 2>/dev/null \
        | grep -qx "${slug}"; then
      return 0
    fi
  done
  return 1
}

# normalize_role_name lowercases and turns spaces/hyphens into underscores.
normalize_role_name() {
  echo "$1" | tr '[:upper:]' '[:lower:]' | tr ' -' '__'
}

# resolve_all_repo_role_id looks up a predefined all-repository role id.
# Matches all_repo_<perm> and all_repository_<perm> (display-name) variants.
# Prints the id on stdout; errors go to stderr. Returns 1 when not found.
resolve_all_repo_role_id() {
  local org="$1" perm="$2"
  local payload names id n
  if ! payload=$(gh api "/orgs/${org}/organization-roles" 2>&1); then
    echo "    ERROR: could not list organization roles for ${org}: ${payload}" >&2
    return 1
  fi
  names=$(echo "${payload}" | jq -r '.roles[]? | "\(.id)\t\(.name)"')
  while IFS=$'\t' read -r id n; do
    [[ -z "${id}" ]] && continue
    case "$(normalize_role_name "${n}")" in
      "all_repo_${perm}"|"all_repository_${perm}")
        echo "${id}"
        return 0
        ;;
    esac
  done <<< "${names}"
  echo "    ERROR: no all-repository ${perm} role in ${org}." >&2
  echo "    Available roles:" >&2
  echo "${names}" >&2
  return 1
}

# role_has_user checks whether username holds org role role_id, directly or
# through a team, by listing the role's users
# (GET /orgs/{org}/organization-roles/{role_id}/users). GitHub has no
# per-user route for org role assignments. Prints "yes" or "no"; any API
# error goes to stderr and returns 1, so callers fail closed.
role_has_user() {
  local org="$1" role_id="$2" username="$3" logins login
  if ! logins=$(gh api --paginate "/orgs/${org}/organization-roles/${role_id}/users?per_page=100" \
      --jq '.[].login' 2>&1); then
    echo "    ERROR: could not list users of organization role ${role_id} in ${org}: ${logins}" >&2
    return 1
  fi
  while IFS= read -r login; do
    if [[ -n "${login}" && "$(lower "${login}")" == "$(lower "${username}")" ]]; then
      echo "yes"
      return 0
    fi
  done <<< "${logins}"
  echo "no"
}

# assign_all_repo_role grants username the all-repository <perm> org role.
# Idempotent: already-assigned roles are left in place and re-verified.
assign_all_repo_role() {
  local org="$1" username="$2" perm="$3"
  local role_id assigned
  role_id=$(resolve_all_repo_role_id "${org}" "${perm}") || return 1

  assigned=$(role_has_user "${org}" "${role_id}" "${username}") || return 1
  if [[ "${assigned}" == "yes" ]]; then
    echo "    OK: ${username} already has all-repository ${perm} (role ${role_id})"
    return 0
  fi

  echo "    Assigning all-repository ${perm} (role ${role_id}) to ${username}..."
  if ! gh api -X PUT "/orgs/${org}/organization-roles/users/${username}/${role_id}" --silent; then
    echo "    ERROR: failed to assign all-repository ${perm} to ${username}." >&2
    return 1
  fi

  assigned=$(role_has_user "${org}" "${role_id}" "${username}") || return 1
  if [[ "${assigned}" != "yes" ]]; then
    echo "    ERROR: ${username} still lacks all-repository ${perm} after assignment." >&2
    return 1
  fi
  echo "    OK: ${username} has all-repository ${perm}"
}

# check_no_all_repo_role verifies username holds neither the all-repository
# write nor the all-repository triage org role. Fails closed: this is a
# security invariant (#7777), so an API error returns 1 rather than being
# read as "no role".
check_no_all_repo_role() {
  local org="$1" username="$2"
  local perm role_id assigned held=""
  for perm in write triage; do
    role_id=$(resolve_all_repo_role_id "${org}" "${perm}") || return 1
    assigned=$(role_has_user "${org}" "${role_id}" "${username}") || return 1
    if [[ "${assigned}" == "yes" ]]; then
      held+=" ${perm}"
    fi
  done
  if [[ -n "${held}" ]]; then
    echo "    ERROR: ${username} has all-repository role(s):${held}" >&2
    echo "    Remove to preserve the outsider test model." >&2
    return 1
  fi
  echo "    OK: ${username} has no all-repository write/triage role"
}

# team_membership prints "STATE ROLE" for username's membership in org/slug
# (state "active" or "pending", role "member" or "maintainer"), or "none"
# when GitHub answers 404. Any other API error goes to stderr and returns 1,
# so callers fail closed.
team_membership() {
  local org="$1" slug="$2" username="$3" body
  if body=$(gh api "/orgs/${org}/teams/${slug}/memberships/${username}" 2>&1); then
    echo "${body}" | jq -r '"\(.state // "unknown") \(.role // "unknown")"'
  elif echo "${body}" | grep -q "HTTP 404"; then
    echo "none"
  else
    echo "    ERROR: could not read ${username} membership in ${org}/${slug}: ${body}" >&2
    return 1
  fi
}

# ensure_status_team creates the STATUS_TEAM_SLUG team in org when it is
# missing and makes member an active team member with the member role.
# member must be the only person on the team, so the authenticated gh user
# (the team creator) is removed on every run, and anyone else, pending
# invitations included, fails the step. Idempotent: an existing team and
# an active membership are verified, not recreated. Fails closed on any API
# error other than the documented 404.
ensure_status_team() {
  local org="$1" member="$2"
  local slug="${STATUS_TEAM_SLUG}"
  local body creator state actual_slug username logins invited

  if body=$(gh api "/orgs/${org}/teams/${slug}" 2>&1); then
    echo "    OK: team ${org}/${slug} exists"
  elif echo "${body}" | grep -q "HTTP 404"; then
    echo "    Creating team ${org}/${slug}..."
    if ! body=$(gh api -X POST "/orgs/${org}/teams" \
        -f name="${slug}" \
        -f privacy="closed" \
        -f description="Allowlist for GitHub user-token auth on test mint status endpoints" 2>&1); then
      echo "    ERROR: failed to create team ${org}/${slug}: ${body}" >&2
      return 1
    fi
    echo "    OK: created team ${org}/${slug}"
  else
    echo "    ERROR: could not read team ${org}/${slug}: ${body}" >&2
    return 1
  fi

  if ! actual_slug=$(gh api "/orgs/${org}/teams/${slug}" --jq '.slug' 2>&1); then
    echo "    ERROR: could not verify team ${org}/${slug}: ${actual_slug}" >&2
    return 1
  fi
  if [[ "${actual_slug}" != "${slug}" ]]; then
    echo "    ERROR: team slug is '${actual_slug}', expected '${slug}'." >&2
    return 1
  fi
  echo "    OK: team slug ${org}/${slug} verified"

  # GitHub adds the user who creates a team as a maintainer. Remove the
  # authenticated user on every run, not only when this run created the
  # team, so a creator left behind by an interrupted run is cleaned up.
  if ! creator=$(gh api /user --jq '.login' 2>&1); then
    echo "    ERROR: could not read the authenticated gh user: ${creator}" >&2
    return 1
  fi
  if [[ "$(lower "${creator}")" != "$(lower "${member}")" ]]; then
    state=$(team_membership "${org}" "${slug}" "${creator}") || return 1
    if [[ "${state}" != "none" ]]; then
      if ! body=$(gh api -X DELETE "/orgs/${org}/teams/${slug}/memberships/${creator}" 2>&1) \
          && ! echo "${body}" | grep -q "HTTP 404"; then
        echo "    ERROR: could not remove ${creator} from ${org}/${slug}: ${body}" >&2
        return 1
      fi
      echo "    Removed ${creator} (the authenticated gh user) from ${org}/${slug}"
    fi
  fi

  # The member must be active with the plain member role. A maintainer can
  # add others to the team, which would widen the allowlist.
  state=$(team_membership "${org}" "${slug}" "${member}") || return 1
  if [[ "${state}" != "active member" ]]; then
    echo "    Setting ${member} on ${org}/${slug} as member (current: ${state})..."
    if ! body=$(gh api -X PUT "/orgs/${org}/teams/${slug}/memberships/${member}" \
        -f role="member" 2>&1); then
      echo "    ERROR: failed to add ${member} to ${org}/${slug}: ${body}" >&2
      return 1
    fi
    state=$(team_membership "${org}" "${slug}" "${member}") || return 1
    if [[ "${state}" != "active member" ]]; then
      echo "    ERROR: ${member} membership in ${org}/${slug} is '${state}', not 'active member'." >&2
      echo "    ${member} must be an active member of ${org} first." >&2
      return 1
    fi
  fi
  echo "    OK: ${member} is an active member of ${org}/${slug}"

  # The team must hold only the write actor. List every member, including
  # maintainers, and every pending invitation; fail on anyone else.
  if ! logins=$(gh api --paginate "/orgs/${org}/teams/${slug}/members?per_page=100" \
      --jq '.[].login' 2>&1); then
    echo "    ERROR: could not list members of ${org}/${slug}: ${logins}" >&2
    return 1
  fi
  # An invitation sent by email has a null login; surface it as a name so it
  # fails the check below instead of printing an empty line that is skipped.
  if ! invited=$(gh api --paginate "/orgs/${org}/teams/${slug}/invitations?per_page=100" \
      --jq '.[] | (.login // "<email-invitation>")' 2>&1); then
    echo "    ERROR: could not list invitations of ${org}/${slug}: ${invited}" >&2
    return 1
  fi
  while IFS= read -r username; do
    [[ -n "${username}" ]] || continue
    if [[ "$(lower "${username}")" != "$(lower "${member}")" ]]; then
      echo "    ERROR: ${username} is on ${org}/${slug}." >&2
      echo "    Remove them so the team stays a write-actor-only allowlist." >&2
      return 1
    fi
  done <<< "${logins}"$'\n'"${invited}"
  echo "    OK: ${member} is the only member of ${org}/${slug}"
}

# lower prints its argument in lowercase; GitHub logins are case-insensitive.
lower() { echo "$1" | tr '[:upper:]' '[:lower:]'; }
