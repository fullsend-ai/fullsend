#!/usr/bin/env bash
# setup-new-e2e-org — provision a pool org for e2e testing.
#
# Usage: hack/setup-new-e2e-org.sh NN|ORG
#   NN  — org number, e.g. 01 (halfsend-01)
#   ORG — full org name, e.g. halfsend (STAGE)
#
# Idempotent: safe to run multiple times. Checks each prerequisite and
# only acts on what's missing. Pauses for manual steps and verifies
# before continuing. Test actors receive organization-level all-repository
# roles so access survives pool repo delete/recreate. The write test actor
# is also made an active member of the mint-status team, the allowlist for
# GitHub user-token auth on test mint status endpoints
# (--status-github-group=ORG/mint-status).

set -euo pipefail

TEST_APP_SET="fullsend-test"
TEST_ROLES=(fullsend triage coder review retro prioritize)
E2E_APP_SET="fullsend-ai"
BOT_USER="botsend"
TEST_WRITE_USER="fstest-write"
TEST_TRIAGE_USER="fstest-triage"
TEST_OUTSIDER_USER="fstest-outsider"
STATUS_TEAM_SLUG="mint-status"

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
    echo "    ERROR: failed to assign all-repository ${perm} to ${username}."
    return 1
  fi

  assigned=$(role_has_user "${org}" "${role_id}" "${username}") || return 1
  if [[ "${assigned}" != "yes" ]]; then
    echo "    ERROR: ${username} still lacks all-repository ${perm} after assignment."
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

# Tests source this file for its function definitions; stop here when
# sourced so nothing below runs against GitHub.
if [[ "${BASH_SOURCE[0]}" != "${0}" ]]; then
  return 0
fi

# --- arg parsing ---
if [[ $# -ne 1 ]]; then
  echo "Usage: $0 NN|ORG" >&2
  echo "  NN is the org number, e.g. 01, 02, 03 (halfsend-NN)" >&2
  echo "  ORG is a full org name, e.g. halfsend (STAGE)" >&2
  exit 1
fi

if [[ "$1" =~ ^[0-9]+$ ]]; then
  ORG="halfsend-$1"
else
  ORG="$1"
fi

# Validate against the known pool/STAGE org names. This script invites
# fstest-write/fstest-triage and assigns them all-repository write/triage
# roles; a mistyped or copy-pasted argument must not silently do that to
# an unintended org.
if ! [[ "${ORG}" =~ ^halfsend(-[0-9]+)?$ ]]; then
  echo "Error: '${ORG}' is not a recognized pool/STAGE org name." >&2
  echo "  Expected halfsend-NN (DEV pool) or halfsend (STAGE)." >&2
  exit 1
fi

echo "==> Setting up e2e org: ${ORG}"
echo

# --- check prerequisites ---
if ! command -v gh &>/dev/null; then
  echo "Error: gh CLI is required. Install from https://cli.github.com/" >&2
  exit 1
fi

if ! gh auth status &>/dev/null; then
  echo "Error: not authenticated with gh. Run 'gh auth login' first." >&2
  exit 1
fi

# --- 1. check if org exists ---
echo "==> Checking if org ${ORG} exists..."
if ! gh api "/orgs/${ORG}" --silent 2>/dev/null; then
  url="https://github.com/organizations/plan"
  echo "    Org ${ORG} does not exist. Opening creation page..."
  open_browser "${url}"
  echo
  echo "    Create the org at: ${url}"
  echo "    Use the name: ${ORG}"
  echo
  wait_for_user "Press Enter after creating the org..."

  if ! gh api "/orgs/${ORG}" --silent 2>/dev/null; then
    echo "    ERROR: org ${ORG} still does not exist."
    exit 1
  fi
fi
echo "    OK: org ${ORG} exists"
echo

# --- 2. check botsend membership ---
echo "==> Checking if ${BOT_USER} is an owner of ${ORG}..."
membership_role=$(gh api "/orgs/${ORG}/memberships/${BOT_USER}" --jq '.role' 2>/dev/null || echo "none")

if [[ "${membership_role}" != "admin" ]]; then
  if [[ "${membership_role}" == "member" ]]; then
    echo "    ${BOT_USER} is a member but not an owner."
    url="https://github.com/orgs/${ORG}/people"
    echo "    Promote them to owner at: ${url}"
    open_browser "${url}"
  else
    echo "    ${BOT_USER} is not a member. Sending invitation..."
    if gh api "/orgs/${ORG}/invitations" \
      -f login="${BOT_USER}" \
      -f role="admin" \
      --silent 2>/dev/null; then
      echo "    Invitation sent to ${BOT_USER}."
    else
      echo "    Could not send invitation (may already be pending)."
    fi
    echo "    Accept/check at: https://github.com/orgs/${ORG}/people"
  fi
  echo
  wait_for_user "Press Enter after ${BOT_USER} is an owner..."

  membership_role=$(gh api "/orgs/${ORG}/memberships/${BOT_USER}" --jq '.role' 2>/dev/null || echo "none")
  if [[ "${membership_role}" != "admin" ]]; then
    echo "    ERROR: ${BOT_USER} is still not an owner (role: ${membership_role})."
    exit 1
  fi
fi
echo "    OK: ${BOT_USER} is an owner"
echo

# --- 3. check test-repo exists ---
echo "==> Checking if ${ORG}/test-repo exists..."
if gh api "/repos/${ORG}/test-repo" --silent 2>/dev/null; then
  echo "    OK: test-repo exists"
else
  echo "    Creating test-repo..."
  gh api "/orgs/${ORG}/repos" \
    -f name="test-repo" \
    -f description="E2E test repo" \
    -F private=false \
    --silent
  echo "    OK: created test-repo"
fi
echo

# --- 3b. test actor org membership ---
echo "==> Checking test actor org membership..."

for actor_info in "${TEST_WRITE_USER}:TEST_ACTOR_WRITE_PAT" "${TEST_TRIAGE_USER}:TEST_ACTOR_TRIAGE_PAT"; do
  actor="${actor_info%%:*}"
  pat_var="${actor_info##*:}"

  membership_state=$(gh api "/orgs/${ORG}/memberships/${actor}" --jq '.state' 2>/dev/null || echo "none")

  if [[ "${membership_state}" == "active" ]]; then
    echo "    OK: ${actor} is an active org member"
    continue
  fi

  # Send or refresh the membership invitation.
  if [[ "${membership_state}" != "pending" ]]; then
    echo "    Inviting ${actor} as org member..."
    gh api "/orgs/${ORG}/memberships/${actor}" \
      -X PUT \
      -f role="member" \
      --silent 2>/dev/null || true
  fi

  # Accept the invitation using the actor's PAT if available.
  if [[ -n "${!pat_var:-}" ]]; then
    echo "    Accepting invitation for ${actor} using ${pat_var}..."
    GH_TOKEN="${!pat_var}" gh api "/user/memberships/orgs/${ORG}" \
      -X PATCH \
      -f state="active" \
      --silent 2>/dev/null || true

    membership_state=$(gh api "/orgs/${ORG}/memberships/${actor}" --jq '.state' 2>/dev/null || echo "none")
    if [[ "${membership_state}" == "active" ]]; then
      echo "    OK: ${actor} is now an active org member"
      continue
    fi
  fi

  echo "    PENDING: ${actor} invitation sent but not yet accepted."
  if [[ -z "${!pat_var:-}" ]]; then
    echo "    Set ${pat_var} env var to auto-accept, or accept manually."
  fi
  wait_for_user "Press Enter after ${actor} has accepted the invitation..."

  membership_state=$(gh api "/orgs/${ORG}/memberships/${actor}" --jq '.state' 2>/dev/null || echo "none")
  if [[ "${membership_state}" != "active" ]]; then
    echo "    ERROR: ${actor} is still not an active member (state: ${membership_state})."
    exit 1
  fi
  echo "    OK: ${actor} is now an active org member"
done

# Verify outsider has no org membership. Fail closed: this is a security
# invariant (#7777), not an advisory check. Any API response other than
# the documented "not a member" 404 aborts the script — an auth error or
# rate limit must not be silently reinterpreted as "none", and a real
# membership (active or pending) must not fall through as just a WARNING.
if outsider_membership_body=$(gh api "/orgs/${ORG}/memberships/${TEST_OUTSIDER_USER}" 2>&1); then
  outsider_state=$(echo "${outsider_membership_body}" | jq -r '.state')
  echo "    ERROR: ${TEST_OUTSIDER_USER} has org membership (state: ${outsider_state})." >&2
  echo "    Remove from ${ORG} to preserve the outsider test model." >&2
  exit 1
elif echo "${outsider_membership_body}" | grep -q "HTTP 404"; then
  echo "    OK: ${TEST_OUTSIDER_USER} has no org membership"
else
  echo "    ERROR: could not verify ${TEST_OUTSIDER_USER} membership status: ${outsider_membership_body}" >&2
  exit 1
fi
echo

# --- 3c. test actor all-repository organization roles ---
echo "==> Assigning all-repository organization roles to test actors..."
if ! assign_all_repo_role "${ORG}" "${TEST_WRITE_USER}" "write"; then
  echo "    ERROR: ${TEST_WRITE_USER} does not have the all-repository write role."
  exit 1
fi
if ! assign_all_repo_role "${ORG}" "${TEST_TRIAGE_USER}" "triage"; then
  echo "    ERROR: ${TEST_TRIAGE_USER} does not have the all-repository triage role."
  exit 1
fi

# Confirm outsider was not granted an all-repository role. Fail closed,
# same rationale as the membership check above: an API error must abort
# rather than silently reporting an empty (and therefore "OK") role list.
if ! check_no_all_repo_role "${ORG}" "${TEST_OUTSIDER_USER}"; then
  exit 1
fi
echo

# --- 3d. mint status team ---
# GitHub user-token auth on the test mint status endpoints allows members
# of ORG/mint-status (--status-github-group=ORG/mint-status).
echo "==> Checking ${STATUS_TEAM_SLUG} team (mint status allowlist)..."
if ! ensure_status_team "${ORG}" "${TEST_WRITE_USER}"; then
  echo "    ERROR: ${ORG}/${STATUS_TEAM_SLUG} is not provisioned." >&2
  exit 1
fi
echo "    Status auth group: ${ORG}/${STATUS_TEAM_SLUG}"
echo

# --- 4. check app installations ---
echo "==> Checking app installations..."
org_id=$(gh api "/orgs/${ORG}" --jq '.id')
installed_apps=$(gh api "/orgs/${ORG}/installations" --jq '.installations[].app_slug' 2>/dev/null || echo "")

ensure_app_installation() {
  local slug="$1"
  if echo "${installed_apps}" | grep -qx "${slug}"; then
    echo "    OK: ${slug} is installed"
    return
  fi

  url="https://github.com/apps/${slug}/installations/new/permissions?target_id=${org_id}&target_type=Organization"
  echo "    MISSING: ${slug}"
  echo "    Opening: ${url}"
  echo "    Grant access to 'All repositories' and click Install."
  open_browser "${url}"
  echo "    Waiting for installation (polling every ${POLL_INTERVAL}s, timeout ${POLL_TIMEOUT}s)..."

  if poll_for_app_install "${ORG}" "${slug}"; then
    echo "    OK: ${slug} is now installed"
  else
    echo "    Timed out waiting for ${slug}."
    wait_for_user "Press Enter if you've installed it manually..."

    if ! gh api "/orgs/${ORG}/installations" --jq '.installations[].app_slug' 2>/dev/null \
        | grep -qx "${slug}"; then
      echo "    ERROR: ${slug} is still not installed."
      exit 1
    fi
    echo "    OK: ${slug} is now installed"
  fi
}

for role in "${TEST_ROLES[@]}"; do
  ensure_app_installation "${TEST_APP_SET}-${role}"
done
ensure_app_installation "${E2E_APP_SET}-e2e"

# --- 4b. cross-org mint authorization for CI ---
echo
echo "==> Checking cross-org mint authorization (FULLSEND_FOREIGN_E2E_REPOS)..."
FOREIGN_VAR="FULLSEND_FOREIGN_E2E_REPOS"
FOREIGN_CALLER="fullsend-ai/fullsend"
foreign_value=$(gh api "/orgs/${ORG}/actions/variables/${FOREIGN_VAR}" --jq '.value' 2>/dev/null || echo "")

if echo "${foreign_value}" | tr ',' '\n' | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | grep -qx "${FOREIGN_CALLER}"; then
  echo "    OK: ${FOREIGN_VAR} lists ${FOREIGN_CALLER}"
else
  echo "    MISSING: ${FOREIGN_VAR} does not authorize ${FOREIGN_CALLER}"
  if command -v fullsend &>/dev/null; then
    echo "    Running: fullsend admin foreign allow --org ${ORG} --role e2e --caller ${FOREIGN_CALLER}"
    fullsend admin foreign allow --org "${ORG}" --role e2e --caller "${FOREIGN_CALLER}"
    echo "    OK: updated ${FOREIGN_VAR}"
  else
    new_value="${FOREIGN_CALLER}"
    if [[ -n "${foreign_value}" ]]; then
      new_value="${foreign_value}, ${FOREIGN_CALLER}"
    fi
    echo "    Setting ${FOREIGN_VAR} via gh api..."
    gh api "/orgs/${ORG}/actions/variables/${FOREIGN_VAR}" \
      -X PATCH \
      -f value="${new_value}" 2>/dev/null \
      || gh api "/orgs/${ORG}/actions/variables" \
        -f name="${FOREIGN_VAR}" \
        -f value="${new_value}" \
        -f visibility="all"
    echo "    OK: updated ${FOREIGN_VAR}"
  fi
fi
echo "    Verify with: fullsend admin foreign list --org ${ORG}"

echo
echo "==> ${ORG} is ready for e2e testing."
echo
echo "    Next steps:"
echo "    1. Uncomment \"${ORG}\" in internal/e2etest/testutil.go orgPool"
echo "    2. Run: make behaviour-test"
