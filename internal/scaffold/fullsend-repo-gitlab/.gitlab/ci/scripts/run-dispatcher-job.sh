#!/usr/bin/env bash
# run-dispatcher-job.sh — GitLab webhook dispatcher job body (#7771).
#
# Source this file from fullsend-dispatcher.yml (do not execute it) so
# FULLSEND_JOB_TOKEN and sibling-secret unsets persist for `fullsend poll`.
#
# Analogous to run-poll-job.sh, but for the source=trigger pipeline that
# GitLab's native "use a webhook" pipeline trigger starts (ADR 0125). The
# webhook body is GitLab's native file-type TRIGGER_PAYLOAD variable and is
# handed to the gitlab-webhook input driver (#7773) unchanged. The driver
# launches any agent pipeline through the same authenticated typed-input
# transport the cron poller uses — CreatePipelineWithInputs, producing a
# source=api agent pipeline carrying the signed scalar metadata and
# event_payload_chunk_NN inputs (ADR 0131) — never through caller-supplied
# pipeline variables, so this job needs no pipeline-variable overrides and
# keeps working under ci_pipeline_variables_minimum_override_role=
# no_one_allowed.

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

# Clear the webhook fast-path credentials. GitLab injects the protected
# FULLSEND_TRIGGER_TOKEN (a bearer that starts default-branch pipelines)
# and FULLSEND_WEBHOOK_SECRET into every protected-branch job, and the
# dispatcher needs neither: the trigger already started this pipeline and
# the webhook body is handed over as TRIGGER_PAYLOAD. Unset them before
# any later code can read them.
unset FULLSEND_TRIGGER_TOKEN FULLSEND_WEBHOOK_SECRET

# Pin job/pipeline/project identity to the CI_JOB_TOKEN job record
# and admit only source=trigger. Disjoint from the poller (schedule)
# and the agent (api). Runs before any PAT-bearing call so a caller who
# forges CI_PIPELINE_SOURCE=trigger on some other pipeline, or targets a
# protected-but-non-default ref, cannot reach the poller PAT.
# shellcheck disable=SC2034  # consumed by sourced pin-ci-job-identity.sh
FULLSEND_ADMIT_SOURCE=trigger
. "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/pin-ci-job-identity.sh"

# The gitlab-webhook driver targets this pinned API root, not the
# overridable CI_API_V4_URL / FULLSEND_GITLAB_URL / CI_SERVER_URL
# pipeline variables — same outrankable class as CI_PROJECT_ID (ADR
# 0125). Fail closed rather than silently fall back to an overridable
# variable if it is ever unexpectedly unset after a successful pin.
if [ -z "${FULLSEND_PINNED_GITLAB_URL:-}" ]; then
  echo "ERROR: FULLSEND_PINNED_GITLAB_URL is unset after a successful identity pin — refusing to make PAT-bearing calls without a pinned API root (fail-closed)" >&2
  exit 1
fi
if [ -z "${FULLSEND_PINNED_PROJECT_PATH:-}" ]; then
  echo "ERROR: FULLSEND_PINNED_PROJECT_PATH is unset after a successful identity pin — refusing to dispatch without a pinned project (fail-closed)" >&2
  exit 1
fi

# Native webhook payload. GitLab exposes TRIGGER_PAYLOAD as a file-type
# variable: its value is the path to a temporary file holding the webhook
# body, not the body itself. Only check that the path names a readable
# regular file — the contents are untrusted hinting that the
# gitlab-webhook driver parses and re-fetches through the pinned API; they
# are never read, expanded, or evaluated by this shell.
if [ -z "${TRIGGER_PAYLOAD:-}" ]; then
  echo "ERROR: TRIGGER_PAYLOAD is unset — the dispatcher only runs for GitLab webhook-triggered pipelines (fail-closed)" >&2
  exit 1
fi
if [ ! -f "${TRIGGER_PAYLOAD}" ] || [ ! -r "${TRIGGER_PAYLOAD}" ]; then
  echo "ERROR: TRIGGER_PAYLOAD does not name a readable file — refusing to dispatch (fail-closed)" >&2
  exit 1
fi
export TRIGGER_PAYLOAD

# Bot token from the registered Poller credential — the dispatcher is a
# second writer into the poller's dispatch spine and uses the same
# Developer-level runtime credential (ADR 0131). Selected
# unconditionally; there is no shared FULLSEND_FORGE_TOKEN fallback
# (ADR-0067 / gitlab-role-credentials.md).
# shellcheck disable=SC2034  # consumed by sourced select-gitlab-role-token.sh
FULLSEND_JOB_KIND=poller
. "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/select-gitlab-role-token.sh"

# Blank sibling role secrets for the rest of the dispatcher job's
# lifetime — same reasoning as run-poll-job.sh: GitLab injects every
# registered role secret plus FULLSEND_FORGE_TOKEN into every
# protected-branch job, and nothing later in this job needs the
# higher-privileged analyst/coder PATs. Mirrors
# clearSiblingGitLabRoleSecrets in internal/cli/gitlab_role.go.
for _fs_sibling in $(compgen -v | grep -E '^FULLSEND_(GITLAB_(ANALYST|CODER|POLLER|ROLE_[A-Z0-9_]+)_TOKEN|FORGE_TOKEN)$' || true); do
  if [ "${_fs_sibling}" != "${FULLSEND_JOB_TOKEN_NAME:-}" ]; then
    unset "${_fs_sibling}"
  fi
done
unset _fs_sibling

# Run the gitlab-webhook input driver (#7773). Uses the pinned project
# path/API root (from the CI_JOB_TOKEN job record), not the overridable
# CI_PROJECT_PATH / FULLSEND_GITLAB_URL / CI_SERVER_URL pipeline
# variables. `fullsend poll` reads the dispatch ref from
# CI_COMMIT_REF_NAME (falling back to CI_DEFAULT_BRANCH), both in the
# same overridable class, so export the pinned ref — see run-poll-job.sh.
export CI_COMMIT_REF_NAME="${FULLSEND_PINNED_REF}"
fullsend poll \
  --input-driver gitlab-webhook \
  --project "${FULLSEND_PINNED_PROJECT_PATH}" \
  --gitlab-url "${FULLSEND_PINNED_GITLAB_URL}" \
  --fullsend-dir .fullsend
