#!/usr/bin/env bash
# setup-new-e2e-org — provision a halfsend-NN org for e2e testing.
#
# Usage: hack/setup-new-e2e-org NN
#
# Idempotent: safe to run multiple times. Checks each prerequisite and
# only acts on what's missing. Pauses for manual steps and verifies
# before continuing.

set -euo pipefail

APP_SET="fullsend-ai"
ROLES=(fullsend triage coder review retro prioritize)
BOT_USER="botsend"

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

# --- arg parsing ---
if [[ $# -ne 1 ]] || ! [[ "$1" =~ ^[0-9]+$ ]]; then
  echo "Usage: $0 NN" >&2
  echo "  NN is the org number, e.g. 01, 02, 03" >&2
  exit 1
fi

NN="$1"
ORG="halfsend-${NN}"

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
    # Check ALLOWED_ORGS
    allowed_orgs=$(echo "${mint_env}" | jq -r '.ALLOWED_ORGS // ""')
    if echo "${allowed_orgs}" | tr ',' '\n' | grep -qx "${ORG}"; then
      echo "    OK: ${ORG} is in ALLOWED_ORGS"
    else
      echo "    MISSING: ${ORG} is NOT in ALLOWED_ORGS"
      mint_ok=false
    fi

    # Check ROLE_APP_IDS
    role_app_ids=$(echo "${mint_env}" | jq -r '.ROLE_APP_IDS // ""')
    missing_roles=()
    for role in "${ROLES[@]}"; do
      key="${ORG}/${role}"
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
  echo "    2. Uncomment \"${ORG}\" in e2e/admin/testutil.go orgPool"
  echo "    3. Run: make e2e-test"
else
  echo "    1. Uncomment \"${ORG}\" in e2e/admin/testutil.go orgPool"
  echo "    2. Run: make e2e-test"
fi
