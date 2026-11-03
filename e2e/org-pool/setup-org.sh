#!/usr/bin/env bash
# setup-org.sh — non-interactive configuration of a pool org for e2e
# testing. Safe to run in CI: it never pauses and never opens a browser.
# It verifies the manual prerequisites (org, botsend owner, test actor
# memberships, app installations) and fails with a clear message when one
# is missing; run setup-manual.sh first to complete them.
#
# Usage: e2e/org-pool/setup-org.sh NN|ORG
#   NN  — org number, e.g. 01 (halfsend-01)
#   ORG — full org name, e.g. halfsend (STAGE)
#
# Idempotent: safe to run multiple times. Checks each prerequisite and
# only acts on what's missing. Test actors receive organization-level
# all-repository roles so access survives pool repo delete/recreate. The
# write test actor is also made an active member of the mint-status team,
# the allowlist for GitHub user-token auth on test mint status endpoints
# (--status-github-group=ORG/mint-status).

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
source "${SCRIPT_DIR}/lib.sh"

# --- arg parsing ---
if [[ $# -ne 1 ]]; then
  echo "Usage: $0 NN|ORG" >&2
  echo "  NN is the org number, e.g. 01, 02, 03 (halfsend-NN)" >&2
  echo "  ORG is a full org name, e.g. halfsend (STAGE)." >&2
  exit 1
fi
ORG=$(parse_org_arg "$1")

echo "==> Setting up e2e org: ${ORG}"
echo

require_gh

# --- 1. verify the org exists (manual prerequisite) ---
echo "==> Checking if org ${ORG} exists..."
if ! gh api "/orgs/${ORG}" --silent 2>/dev/null; then
  echo "    ERROR: org ${ORG} does not exist." >&2
  echo "    Run e2e/org-pool/setup-manual.sh ${ORG} first." >&2
  exit 1
fi
echo "    OK: org ${ORG} exists"
echo

# --- 2. verify botsend is an owner (manual prerequisite) ---
echo "==> Checking if ${BOT_USER} is an owner of ${ORG}..."
membership=$(gh api "/orgs/${ORG}/memberships/${BOT_USER}" --jq '"\(.state) \(.role)"' 2>/dev/null || echo "none none")
if [[ "${membership}" != "active admin" ]]; then
  echo "    ERROR: ${BOT_USER} is not an active owner of ${ORG} (membership: ${membership})." >&2
  if [[ "${membership}" == "pending admin" ]]; then
    echo "    The invitation is pending; ${BOT_USER} must accept it." >&2
  fi
  echo "    Run e2e/org-pool/setup-manual.sh ${ORG} first." >&2
  exit 1
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
# The invitations are sent by setup-manual.sh; accepting them only needs
# the actor's own PAT, so CI can do it. PATs come from the repository
# secrets TEST_ACTOR_WRITE_PAT and TEST_ACTOR_TRIAGE_PAT.
echo "==> Checking test actor org membership..."

for actor_info in "${TEST_WRITE_USER}:TEST_ACTOR_WRITE_PAT" "${TEST_TRIAGE_USER}:TEST_ACTOR_TRIAGE_PAT"; do
  actor="${actor_info%%:*}"
  pat_var="${actor_info##*:}"

  membership_state=$(gh api "/orgs/${ORG}/memberships/${actor}" --jq '.state' 2>/dev/null || echo "none")

  if [[ "${membership_state}" == "active" ]]; then
    echo "    OK: ${actor} is an active org member"
    continue
  fi

  if [[ "${membership_state}" != "pending" ]]; then
    echo "    ERROR: ${actor} has no org membership (state: ${membership_state})." >&2
    echo "    Run e2e/org-pool/setup-manual.sh ${ORG} first." >&2
    exit 1
  fi

  if [[ -z "${!pat_var:-}" ]]; then
    echo "    ERROR: ${actor} has a pending invitation but ${pat_var} is not set." >&2
    echo "    Export ${pat_var} to accept it here, or accept the invitation" >&2
    echo "    as ${actor} at https://github.com/orgs/${ORG}/invitation, then re-run." >&2
    exit 1
  fi

  echo "    Accepting ${actor}'s invitation using ${pat_var}..."
  GH_TOKEN="${!pat_var}" gh api "/user/memberships/orgs/${ORG}" \
    -X PATCH \
    -f state="active" \
    --silent

  membership_state=$(gh api "/orgs/${ORG}/memberships/${actor}" --jq '.state' 2>/dev/null || echo "none")
  if [[ "${membership_state}" != "active" ]]; then
    echo "    ERROR: ${actor} is still not an active member (state: ${membership_state})." >&2
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
  echo "    ERROR: ${TEST_WRITE_USER} does not have the all-repository write role." >&2
  exit 1
fi
if ! assign_all_repo_role "${ORG}" "${TEST_TRIAGE_USER}" "triage"; then
  echo "    ERROR: ${TEST_TRIAGE_USER} does not have the all-repository triage role." >&2
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

# --- 4. verify app installations (manual prerequisite) ---
echo "==> Checking app installations..."
installed_apps=$(gh api "/orgs/${ORG}/installations" --jq '.installations[].app_slug' 2>/dev/null || echo "")

ensure_app_installation() {
  local slug="$1"
  if echo "${installed_apps}" | grep -qx "${slug}"; then
    echo "    OK: ${slug} is installed"
    return
  fi
  echo "    ERROR: ${slug} is not installed." >&2
  echo "    Run e2e/org-pool/setup-manual.sh ${ORG} first." >&2
  exit 1
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
if [[ -z "${CI:-}" ]]; then
  echo "    Next steps:"
  echo "    1. Add \"${ORG}\" to orgPool in internal/e2etest/testutil.go"
  echo "       (and update DevPoolOrgCount/DevPoolLayout and their tests)"
  echo "    2. Run: make behaviour-test"
fi
