#!/usr/bin/env bash
# run-poll-job.sh — GitLab poll job body.
#
# Source this file from fullsend-poll.yml (do not execute it) so
# FULLSEND_JOB_TOKEN and sibling-secret unsets persist for `fullsend poll`.

set -euo pipefail

# CI_DEBUG_TRACE guard — deny-before-admit. YAML rules also refuse to
# start the job when debug tracing is enabled (secrets materialize at
# job init, so a mid-script abort is too late). This script-level
# guard is defense-in-depth and MUST run before any identity pin,
# token select, or admit/allowlist logic. Matches the full truthy set
# gitlab-runner accepts for this variable (Go strconv.ParseBool: "1",
# "t"/"T", "true"/"TRUE"/"True"), not just an exact "true".
case "${CI_DEBUG_TRACE:-}" in
  1|[tT]|[tT][rR][uU][eE])
    echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets" >&2
    exit 1
    ;;
esac

# Pin job/pipeline/project identity to the CI_JOB_TOKEN job record
# and admit only source=schedule. Disjoint from the agent (api) and
# the dispatcher (trigger, #7771). Runs before any PAT-bearing call
# so a trigger-token holder who forges CI_PIPELINE_SOURCE=schedule
# cannot reach the poller PAT.
# shellcheck disable=SC2034  # consumed by sourced pin-ci-job-identity.sh
FULLSEND_ADMIT_SOURCE=schedule
. "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/pin-ci-job-identity.sh"

# The PAT-bearing fullsend_gate_curl call below targets this pinned API
# root, not the overridable CI_API_V4_URL pipeline variable — same
# outrankable class as CI_PROJECT_ID (ADR 0125). Fail closed rather than
# silently fall back to the overridable variable if it is ever
# unexpectedly unset after a successful identity pin.
if [ -z "${FULLSEND_PINNED_GITLAB_URL:-}" ]; then
  echo "ERROR: FULLSEND_PINNED_GITLAB_URL is unset after a successful identity pin — refusing to make PAT-bearing calls without a pinned API root (fail-closed)" >&2
  exit 1
fi
FULLSEND_PINNED_API_V4_URL="${FULLSEND_PINNED_GITLAB_URL}/api/v4"

# Bot token from the registered Poller credential — selected
# unconditionally; there is no shared FULLSEND_FORGE_TOKEN fallback
# (ADR-0067 / gitlab-role-credentials.md).
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
#
# Unconditional for every job: select-gitlab-role-token.sh has no
# shared-token path left where every role resolved to the same
# value, so a sibling secret is always a real, distinct
# higher-privileged credential that must be cleared.
for _fs_sibling in $(compgen -v | grep -E '^FULLSEND_(GITLAB_(ANALYST|CODER|POLLER|ROLE_[A-Z0-9_]+)_TOKEN|FORGE_TOKEN)$' || true); do
  if [ "${_fs_sibling}" != "${FULLSEND_JOB_TOKEN_NAME:-}" ]; then
    unset "${_fs_sibling}"
  fi
done
unset _fs_sibling

# Validate FULLSEND_POLL_MODE before use in URLs and commands.
case "${FULLSEND_POLL_MODE:-events}" in
  slash|events) ;;
  *)
    echo "ERROR: FULLSEND_POLL_MODE must be 'slash' or 'events', got '${FULLSEND_POLL_MODE:-}'" >&2
    exit 1
    ;;
esac

# Resource group self-heal — set process_mode so stale locks from
# cancelled/deleted pipelines are preempted by new jobs.
# slash mode: newest_first (latest command wins)
# events mode: oldest_first (complete long-running discovery)
# Best-effort: failures don't block the job. Routed through
# fullsend_gate_curl (not plain curl) so a trigger-supplied
# HTTP_PROXY/HTTPS_PROXY cannot intercept this PAT-bearing call. Uses
# the pinned project id (from the CI_JOB_TOKEN job record), not the
# overridable CI_PROJECT_ID pipeline variable.
PROCESS_MODE="newest_first"
if [ "${FULLSEND_POLL_MODE:-events}" = "events" ]; then
  PROCESS_MODE="oldest_first"
fi
fullsend_gate_curl \
  -X PUT "${FULLSEND_PINNED_API_V4_URL}/projects/${FULLSEND_PINNED_PROJECT_ID}/resource_groups/fullsend-poll-${FULLSEND_POLL_MODE:-events}" \
  -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "process_mode=${PROCESS_MODE}" > /dev/null 2>&1 || true

# Run the poller — dispatches pipelines directly via the GitLab API.
# No child pipeline generation needed: the poller creates standalone
# pipelines for each discovered event and logs clickable URLs. Uses
# the pinned project path/API root (from the CI_JOB_TOKEN job
# record), not the overridable CI_PROJECT_PATH / FULLSEND_GITLAB_URL /
# CI_SERVER_URL pipeline variables — same outrankable class as
# CI_PROJECT_ID (ADR 0125).
#
# `fullsend poll` has no --ref flag; internal/cli/poll.go instead reads
# the dispatch ref from CI_COMMIT_REF_NAME, falling back to
# CI_DEFAULT_BRANCH — both overridable pipeline variables in the same
# outrankable class. A pipeline/schedule variable of either name could
# retarget dispatched pipelines to a different (possibly stale,
# pre-hardening) ref while this poller job itself stays correctly
# pinned. Export the pinned ref so the CLI picks it up instead.
export CI_COMMIT_REF_NAME="${FULLSEND_PINNED_REF}"
fullsend poll \
  --forge gitlab \
  --project "${FULLSEND_PINNED_PROJECT_PATH}" \
  --gitlab-url "${FULLSEND_PINNED_GITLAB_URL}" \
  --fullsend-dir .fullsend \
  --mode "${FULLSEND_POLL_MODE:-events}"
