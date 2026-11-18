#!/usr/bin/env bash
# enroll.sh — one-time per-repo mint enrollment for a pool org.
#
# Usage: e2e/org-pool/enroll.sh NN|ORG
#   NN  — org number, e.g. 01 (halfsend-01)
#   ORG — full org name, e.g. halfsend (STAGE)
#
# Enrolls the behaviour pool slots test-repo-01 … test-repo-12 on the
# hosted mint project. The singular test-repo is not enrolled: it is a
# scratch repo, not a behaviour test slot. Idempotent:
# safe to run multiple times. The GitHub repositories need not exist —
# enroll is a mint allowlist / WIF-provider update only.
#
# Do NOT enroll *-fork names (forks are ephemeral PR sources and mint
# against the enrolled base repo), nor the *-mint-host and
# *-mint-unapproved probe hosts of the GitHub mint scope scenarios.
#
# Requires GCP admin IAM on the hosted mint project. Re-run when adding a
# new pool org or after mint infrastructure changes that drop
# PER_REPO_WIF_REPOS entries. See
# docs/guides/infrastructure/mint-administration.md for operator IAM.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
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

require_gh

if ! gh api "/orgs/${ORG}" --silent 2>/dev/null; then
  echo "Error: org ${ORG} does not exist on GitHub." >&2
  exit 1
fi

GCP_PROJECT="${GCP_PROJECT:-}"
if [[ -z "${GCP_PROJECT}" ]]; then
  echo "Error: GCP_PROJECT is not set." >&2
  echo "  Set it to the hosted mint GCP project (see" >&2
  echo "  docs/guides/infrastructure/mint-administration.md)." >&2
  exit 1
fi
REGION="${REGION:-us-central1}"

echo "==> Enrolling ${ORG} on mint project ${GCP_PROJECT} (${REGION})"

cd "${REPO_ROOT}"
for j in $(seq -w 1 12); do
  go run ./cmd/fullsend mint enroll "${ORG}/test-repo-${j}" \
    --project="${GCP_PROJECT}" --region="${REGION}"
done

echo "==> ${ORG} is enrolled."
