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
# roles so access survives pool repo delete/recreate.

set -euo pipefail

APP_SET="fullsend-ai"
ROLES=(fullsend triage coder review retro prioritize e2e)
BOT_USER="botsend"
TEST_WRITE_USER="fstest-write"
TEST_TRIAGE_USER="fstest-triage"
TEST_OUTSIDER_USER="fstest-outsider"

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

# assign_all_repo_role grants username the all-repository <perm> org role.
# Idempotent: already-assigned roles are left in place and re-verified.
assign_all_repo_role() {
  local org="$1" username="$2" perm="$3"
  local role_id assigned
  role_id=$(resolve_all_repo_role_id "${org}" "${perm}") || return 1

  assigned=$(gh api "/orgs/${org}/organization-roles/users/${username}" \
    | jq -r --arg id "${role_id}" '.roles[]? | select(.id == ($id | tonumber)) | .id' \
    2>/dev/null || true)
  if [[ -n "${assigned}" ]]; then
    echo "    OK: ${username} already has all-repository ${perm} (role ${role_id})"
    return 0
  fi

  echo "    Assigning all-repository ${perm} (role ${role_id}) to ${username}..."
  if ! gh api -X PUT "/orgs/${org}/organization-roles/users/${username}/${role_id}" --silent; then
    echo "    ERROR: failed to assign all-repository ${perm} to ${username}."
    return 1
  fi

  assigned=$(gh api "/orgs/${org}/organization-roles/users/${username}" \
    | jq -r --arg id "${role_id}" '.roles[]? | select(.id == ($id | tonumber)) | .id' \
    2>/dev/null || true)
  if [[ -z "${assigned}" ]]; then
    echo "    ERROR: ${username} still lacks all-repository ${perm} after assignment."
    return 1
  fi
  echo "    OK: ${username} has all-repository ${perm}"
}

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
if outsider_roles_body=$(gh api "/orgs/${ORG}/organization-roles/users/${TEST_OUTSIDER_USER}" 2>&1); then
  outsider_role_names=$(echo "${outsider_roles_body}" | jq -r '.roles[]?.name')
elif echo "${outsider_roles_body}" | grep -q "HTTP 404"; then
  outsider_role_names=""
else
  echo "    ERROR: could not check ${TEST_OUTSIDER_USER} organization roles: ${outsider_roles_body}" >&2
  exit 1
fi

# Route names through normalize_role_name so a display-name variant (e.g.
# "All-repository write") is caught the same way resolve_all_repo_role_id
# already recognizes it for the assignment path above.
outsider_has_all_repo_role=false
while IFS= read -r role_name; do
  [[ -z "${role_name}" ]] && continue
  case "$(normalize_role_name "${role_name}")" in
    all_repo_write | all_repo_triage | all_repository_write | all_repository_triage)
      outsider_has_all_repo_role=true
      ;;
  esac
done <<< "${outsider_role_names}"

if "${outsider_has_all_repo_role}"; then
  echo "    ERROR: ${TEST_OUTSIDER_USER} has an all-repository role:" >&2
  echo "${outsider_role_names}" | sed 's/^/      /' >&2
  echo "    Remove to preserve the outsider test model." >&2
  exit 1
else
  echo "    OK: ${TEST_OUTSIDER_USER} has no all-repository write/triage role"
fi
echo

# --- 4. check app installations ---
echo "==> Checking app installations..."
org_id=$(gh api "/orgs/${ORG}" --jq '.id')
installed_apps=$(gh api "/orgs/${ORG}/installations" --jq '.installations[].app_slug' 2>/dev/null || echo "")

for role in "${ROLES[@]}"; do
  slug="${APP_SET}-${role}"
  if echo "${installed_apps}" | grep -qx "${slug}"; then
    echo "    OK: ${slug} is installed"
    continue
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
done

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

# --- 5. check mint enrollment ---
MINT_PROJECT="${MINT_PROJECT:?MINT_PROJECT must be set}"
MINT_REGION="${MINT_REGION:-us-central1}"
MINT_FUNCTION="${MINT_FUNCTION:?MINT_FUNCTION must be set}"

echo
echo "==> Checking mint enrollment (project: ${MINT_PROJECT}, region: ${MINT_REGION})..."
mint_ok=true

if ! command -v gcloud &>/dev/null; then
  echo "    SKIP: gcloud CLI not found, cannot check mint config."
  mint_ok=false
else
  mint_env=$(gcloud functions describe "${MINT_FUNCTION}" \
    --region "${MINT_REGION}" \
    --project "${MINT_PROJECT}" \
    --format json 2>/dev/null \
    | jq -r '.serviceConfig.environmentVariables' 2>/dev/null) || mint_env=""

  if [[ -z "${mint_env}" || "${mint_env}" == "null" ]]; then
    echo "    SKIP: could not read mint function env vars."
    echo "    You may need to run: gcloud auth login"
    mint_ok=false
  else
    # Check ROLE_APP_IDS
    role_app_ids=$(echo "${mint_env}" | jq -r '.ROLE_APP_IDS // ""')
    missing_roles=()
    for role in "${ROLES[@]}"; do
      # ROLE_APP_IDS uses role-only keys; org/role keys are legacy and ignored
      # by the mint during the migration window.
      key="${role}"
      if echo "${role_app_ids}" | jq -e --arg k "${key}" '.[$k]' &>/dev/null; then
        echo "    OK: ${key} is in ROLE_APP_IDS"
      else
        echo "    MISSING: ${key} is NOT in ROLE_APP_IDS"
        missing_roles+=("${key}")
        mint_ok=false
      fi
    done
  fi
fi

echo
if [[ "${mint_ok}" == "true" ]]; then
  echo "==> ${ORG} is fully ready for e2e testing."
else
  echo "==> ${ORG} GitHub setup is complete, but mint enrollment needs attention."
  echo "    Fix the issues above, then:"
fi
echo
echo "    Next steps:"
if [[ "${mint_ok}" != "true" ]]; then
  echo "    1. Enroll ${ORG} in the mint: run /mint-enroll in Claude Code"
  echo "    2. Uncomment \"${ORG}\" in internal/e2etest/testutil.go orgPool"
  echo "    3. Run: make behaviour-test"
else
  echo "    1. Uncomment \"${ORG}\" in internal/e2etest/testutil.go orgPool"
  echo "    2. Run: make behaviour-test"
fi
