#!/usr/bin/env bash
# setup-manual.sh — interactive operator steps to prepare a pool org for
# e2e testing. These steps cannot run in CI: they need a human to create
# the org, accept invitations, and click through GitHub App installs.
#
# Usage: e2e/org-pool/setup-manual.sh NN|ORG
#   NN  — org number, e.g. 01 (halfsend-01)
#   ORG — full org name, e.g. halfsend (STAGE)
#
# After this script finishes, run setup-org.sh (locally or in CI) to
# complete the non-interactive configuration.

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

# The org contact email is operator-supplied; never hardcode it here.
ORG_CONTACT_EMAIL="${ORG_CONTACT_EMAIL:-}"
if [[ -z "${ORG_CONTACT_EMAIL}" ]]; then
  echo "Error: ORG_CONTACT_EMAIL is not set." >&2
  echo "  Set it to the contact email to use when creating the org." >&2
  exit 1
fi

echo "==> Manual setup for e2e org: ${ORG}"
echo

require_gh

# --- 1. check if org exists ---
echo "==> Checking if org ${ORG} exists..."
if ! gh api "/orgs/${ORG}" --silent 2>/dev/null; then
  url="https://github.com/organizations/plan"
  echo "    Org ${ORG} does not exist. Opening creation page..."
  open_browser "${url}"
  echo
  echo "    Create the org at: ${url}"
  echo "    Use the name: ${ORG}"
  echo "    Use the contact email: ${ORG_CONTACT_EMAIL}"
  echo
  wait_for_user "Press Enter after creating the org..."

  if ! org_err=$(gh api "/orgs/${ORG}" --silent 2>&1); then
    echo "    ERROR: could not verify org ${ORG}: ${org_err}" >&2
    exit 1
  fi
fi
echo "    OK: org ${ORG} exists"
echo

# --- 2. check botsend membership ---
echo "==> Checking if ${BOT_USER} is an owner of ${ORG}..."
membership=$(gh api "/orgs/${ORG}/memberships/${BOT_USER}" --jq '"\(.state) \(.role)"' 2>/dev/null || echo "none none")

if [[ "${membership}" != "active admin" ]]; then
  case "${membership}" in
    "active member")
      echo "    ${BOT_USER} is a member but not an owner."
      url="https://github.com/orgs/${ORG}/people"
      echo "    Promote them to owner at: ${url}"
      open_browser "${url}"
      ;;
    "pending admin")
      echo "    ${BOT_USER} has a pending owner invitation."
      echo "    They must accept it at: https://github.com/orgs/${ORG}/people"
      ;;
    *)
      echo "    ${BOT_USER} is not a member. Sending invitation..."
      if gh api -X PUT "/orgs/${ORG}/memberships/${BOT_USER}" \
        -f role="admin" \
        --silent; then
        echo "    Invitation sent to ${BOT_USER}."
      else
        echo "    ERROR: could not send invitation to ${BOT_USER}." >&2
        exit 1
      fi
      echo "    Accept/check at: https://github.com/orgs/${ORG}/people"
      ;;
  esac
  echo
  wait_for_user "Press Enter after ${BOT_USER} is an active owner..."

  membership=$(gh api "/orgs/${ORG}/memberships/${BOT_USER}" --jq '"\(.state) \(.role)"' 2>/dev/null || echo "none none")
  if [[ "${membership}" != "active admin" ]]; then
    echo "    ERROR: ${BOT_USER} is still not an active owner (membership: ${membership})." >&2
    exit 1
  fi
fi
echo "    OK: ${BOT_USER} is an owner"
echo

# --- 3. test actor org membership ---
echo "==> Checking test actor org membership..."

for actor in "${TEST_WRITE_USER}" "${TEST_TRIAGE_USER}"; do

  membership_state=$(gh api "/orgs/${ORG}/memberships/${actor}" --jq '.state' 2>/dev/null || echo "none")

  if [[ "${membership_state}" == "active" ]]; then
    echo "    OK: ${actor} is an active org member"
    continue
  fi

  # Send or refresh the membership invitation. Acceptance is not
  # required here: setup-org.sh accepts pending invitations in CI with
  # the actor PATs, so pending is an acceptable end state.
  if [[ "${membership_state}" != "pending" ]]; then
    echo "    Inviting ${actor} as org member..."
    gh api "/orgs/${ORG}/memberships/${actor}" \
      -X PUT \
      -f role="member" \
      --silent 2>/dev/null || true
  fi

  membership_state=$(gh api "/orgs/${ORG}/memberships/${actor}" --jq '.state' 2>/dev/null || echo "none")
  case "${membership_state}" in
    active)
      echo "    OK: ${actor} is an active org member"
      ;;
    pending)
      echo "    OK: ${actor} has a pending invitation (accepted later by setup-org.sh)"
      ;;
    *)
      echo "    ERROR: could not invite ${actor} (state: ${membership_state})." >&2
      exit 1
      ;;
  esac
done
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
    wait_for_user "Press Enter if you've installed it manually..."

    if ! gh api "/orgs/${ORG}/installations" --jq '.installations[].app_slug' 2>/dev/null \
        | grep -qx "${slug}"; then
      echo "    ERROR: ${slug} is still not installed." >&2
      exit 1
    fi
    echo "    OK: ${slug} is now installed"
  fi
}

for role in "${TEST_ROLES[@]}"; do
  ensure_app_installation "${TEST_APP_SET}-${role}"
done
ensure_app_installation "${E2E_APP_SET}-e2e"

echo
echo "==> Manual setup for ${ORG} is complete."
echo
echo "    Next step: run e2e/org-pool/setup-org.sh ${ORG}"
