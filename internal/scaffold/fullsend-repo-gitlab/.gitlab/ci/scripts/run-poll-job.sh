#!/usr/bin/env bash
# run-poll-job.sh — GitLab poll job body.
#
# Source this file from fullsend-poll.yml (do not execute it) so
# FULLSEND_JOB_TOKEN and sibling-secret unsets persist for `fullsend poll`.

set -euo pipefail

# CI_DEBUG_TRACE guard — prevents PAT exposure via debug trace logging.
if [ "${CI_DEBUG_TRACE:-}" = "true" ]; then
  echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets"
  exit 1
fi

# Bot token from the registered Poller credential when the
# migration gate is migrating/enforced; otherwise the shared
# FULLSEND_FORGE_TOKEN (ADR-0067 / gitlab-role-credentials.md).
# shellcheck disable=SC2034  # consumed by sourced select-gitlab-role-token.sh
FULLSEND_JOB_KIND=poller
. "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/select-gitlab-role-token.sh"

# Blank sibling role secrets for the rest of the poller job's
# lifetime. GitLab injects every registered role secret
# (FULLSEND_GITLAB_ANALYST_TOKEN, FULLSEND_GITLAB_CODER_TOKEN, any
# custom FULLSEND_GITLAB_ROLE_*_TOKEN) plus FULLSEND_FORGE_TOKEN
# into every protected-branch job regardless of which one this job
# selected above. Leaving them present would let anything later in
# this job (or a host-side pre/post-script that inherits the
# process environment) read a higher-privileged analyst/coder PAT
# directly, weakening the blast-radius goal of role separation —
# mirrors clearSiblingGitLabRoleSecrets in internal/cli/gitlab_role.go.
# select-gitlab-role-token.sh itself is left unchanged: the agent
# template still needs these siblings present until its own HMAC
# reselect and the STAGE=fix analyst-identity lookup.
_fs_poll_mode=$(printf '%s' "${FULLSEND_GITLAB_ROLE_MIGRATION:-}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
case "${_fs_poll_mode}" in
  migrating|enforced)
    for _fs_sibling in $(compgen -v | grep -E '^FULLSEND_(GITLAB_(ANALYST|CODER|POLLER|ROLE_[A-Z0-9_]+)_TOKEN|FORGE_TOKEN)$' || true); do
      if [ "${_fs_sibling}" != "${FULLSEND_JOB_TOKEN_NAME:-}" ]; then
        unset "${_fs_sibling}"
      fi
    done
    ;;
esac
unset _fs_poll_mode _fs_sibling

# Validate FULLSEND_POLL_MODE before use in URLs and commands.
case "${FULLSEND_POLL_MODE:-events}" in
  slash|events) ;;
  *)
    echo "ERROR: FULLSEND_POLL_MODE must be 'slash' or 'events', got '${FULLSEND_POLL_MODE:-}'"
    exit 1
    ;;
esac

# Resource group self-heal — set process_mode so stale locks from
# cancelled/deleted pipelines are preempted by new jobs.
# slash mode: newest_first (latest command wins)
# events mode: oldest_first (complete long-running discovery)
# Best-effort: failures don't block the job.
PROCESS_MODE="newest_first"
if [ "${FULLSEND_POLL_MODE:-events}" = "events" ]; then
  PROCESS_MODE="oldest_first"
fi
curl -sf --retry 2 --retry-delay 1 --retry-all-errors \
  -X PUT "${CI_API_V4_URL}/projects/${CI_PROJECT_ID}/resource_groups/fullsend-poll-${FULLSEND_POLL_MODE:-events}" \
  -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "process_mode=${PROCESS_MODE}" > /dev/null 2>&1 || true

# Run the poller — dispatches pipelines directly via the GitLab API.
# No child pipeline generation needed: the poller creates standalone
# pipelines for each discovered event and logs clickable URLs.
fullsend poll \
  --forge gitlab \
  --project "${CI_PROJECT_PATH}" \
  --gitlab-url "${FULLSEND_GITLAB_URL:-${CI_SERVER_URL}}" \
  --fullsend-dir .fullsend \
  --mode "${FULLSEND_POLL_MODE:-events}"

