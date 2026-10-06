#!/usr/bin/env bash
# run-agent-job.sh — GitLab agent job body.
#
# Source this file from fullsend-agent.yml (do not execute it) so
# credential exports, traps, and harness environment persist for
# the agent invocation in the same job shell.

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
# and FULLSEND_WEBHOOK_SECRET into every protected-branch job, and no
# agent step needs either: they exist only for the webhook and its
# provisioning. Unset them before any later code, including the agent's
# host-side scripts, can read them.
unset FULLSEND_TRIGGER_TOKEN FULLSEND_WEBHOOK_SECRET

# Pin job/pipeline/project identity to the CI_JOB_TOKEN job record
# and admit only source=api. Disjoint from the poller (schedule) and
# the dispatcher (trigger, #7771). Runs before any PAT-bearing call.
# shellcheck disable=SC2034  # consumed by sourced pin-ci-job-identity.sh
FULLSEND_ADMIT_SOURCE=api
. "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/pin-ci-job-identity.sh"
PIPELINE_SOURCE="${FULLSEND_PINNED_PIPELINE_SOURCE}"
PIPELINE_RESPONSE="${FULLSEND_PINNED_PIPELINE_RESPONSE}"

# Every PAT-bearing fullsend_gate_curl call below targets this pinned API
# root, not the overridable CI_API_V4_URL pipeline variable — same
# outrankable class as CI_PROJECT_ID (ADR 0125). FULLSEND_PINNED_GITLAB_URL
# is exported by pin-ci-job-identity.sh (derived from the same CI_API_V4_URL
# value, but only after that value passed the pin's validation and was used
# to fetch GET /job), so same-process env cannot diverge it from CI_API_V4_URL
# after the pin succeeds; routing through it here is for consistency with
# the pin's stated contract, not an independent new protection. Fail closed
# rather than silently fall back to the overridable variable if it is ever
# unexpectedly unset.
if [ -z "${FULLSEND_PINNED_GITLAB_URL:-}" ]; then
  echo "ERROR: FULLSEND_PINNED_GITLAB_URL is unset after a successful identity pin — refusing to make PAT-bearing calls without a pinned API root (fail-closed)" >&2
  exit 1
fi
FULLSEND_PINNED_API_V4_URL="${FULLSEND_PINNED_GITLAB_URL}/api/v4"

# Back-link to the poll job that dispatched this pipeline

# Inference credential setup — write a file-based credential config
# for Vertex AI so GOOGLE_APPLICATION_CREDENTIALS is available in the
# sandbox. Uses direct federated identity (no SA impersonation) — the
# inference WIF principal has roles/aiplatform.user granted directly.
# The credential_source.file points to the sandbox path because this
# config is read inside the sandbox container. GitLab id_tokens last
# the full job duration (~1 hour), so no OIDC refresh loop is needed
# (unlike GitHub's 5-min expiry tokens).
if [ -n "${FULLSEND_GCP_WIF_PROVIDER:-}" ]; then
  OIDC_TOKEN_FILE=$(mktemp)
  GCP_CRED_CONFIG_FILE=$(mktemp)
  trap 'rm -f "${OIDC_TOKEN_FILE}" "${GCP_CRED_CONFIG_FILE}"' EXIT
  echo "${FULLSEND_ID_TOKEN}" > "${OIDC_TOKEN_FILE}"
  cat > "${GCP_CRED_CONFIG_FILE}" <<INFERENCECRED
{
  "type": "external_account",
  "audience": "//iam.googleapis.com/${FULLSEND_GCP_WIF_PROVIDER}",
  "subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
  "token_url": "https://sts.googleapis.com/v1/token",
  "credential_source": { "file": "/sandbox/workspace/.gcp-oidc-token" }
}
INFERENCECRED
  export GOOGLE_APPLICATION_CREDENTIALS="${GCP_CRED_CONFIG_FILE}"
  export ANTHROPIC_VERTEX_PROJECT_ID="${FULLSEND_GCP_PROJECT_ID}"
  export GOOGLE_CLOUD_PROJECT="${FULLSEND_GCP_PROJECT_ID}"
  export GCP_OIDC_TOKEN_FILE="${OIDC_TOKEN_FILE}"
fi

# OpenAI static key (inference.auth openai-api-key) — `fullsend repos
# install` writes FULLSEND_OPENAI_API_KEY as a masked CI/CD variable.
# Map it to OPENAI_API_KEY, the name the fullsend CLI reads on the host.
# There is deliberately no fallback to an unprefixed OPENAI_API_KEY
# CI/CD variable (it may be shared with unrelated jobs): when
# FULLSEND_OPENAI_API_KEY is unset, any inherited OPENAI_API_KEY is
# cleared so it cannot satisfy the credential check. The prefixed name is
# unset after mapping so the real key is exported under only one name,
# which the runner treats as runner-only (oidcDenyKeys).
if [ -n "${FULLSEND_OPENAI_API_KEY:-}" ]; then
  export OPENAI_API_KEY="${FULLSEND_OPENAI_API_KEY}"
else
  unset OPENAI_API_KEY
fi
unset FULLSEND_OPENAI_API_KEY

# Bootstrap identity for the pre-verification calls below (resource
# group PUT, pipeline-metadata GET, bot-identity /user call): select
# the poller credential, not the STAGE-derived role. STAGE is an
# attacker-influenced pipeline variable at this point — the
# pipeline-source/bot-identity check and HMAC verification haven't
# run yet — so resolving a role token from it here would let a
# forged dispatch obtain the higher-privilege coder/analyst
# credential before it's authenticated. Poller's own responsibility
# already covers pipeline dispatch/resource-group management, and
# select-gitlab-role-token.sh resolves the poller credential
# unconditionally here — there is no migration gate or shared-token
# fallback to reason about. The real per-STAGE role token is
# re-selected further down, once verification passes.
# shellcheck disable=SC2034  # consumed by sourced select-gitlab-role-token.sh
FULLSEND_JOB_KIND=poller
. "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/select-gitlab-role-token.sh"

# Inference region — set when configured at install time
if [ -n "${FULLSEND_GCP_REGION:-}" ]; then
  export CLOUD_ML_REGION="${FULLSEND_GCP_REGION}"
fi

# Resource group self-heal — set process_mode to newest_first so stale
# locks from cancelled/deleted pipelines are preempted by new jobs.
# Best-effort: failures don't block the job. Routed through
# fullsend_gate_curl (not plain curl) so a trigger-supplied
# HTTP_PROXY/HTTPS_PROXY cannot intercept this PAT-bearing call. Uses
# the pinned project id (from the CI_JOB_TOKEN job record), not the
# overridable CI_PROJECT_ID pipeline variable — this call runs with
# the poller/role PAT before HMAC verification.
fullsend_gate_curl \
  -X PUT "${FULLSEND_PINNED_API_V4_URL}/projects/${FULLSEND_PINNED_PROJECT_ID}/resource_groups/fullsend-${STAGE}-${RESOURCE_KEY}" \
  -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "process_mode=newest_first" > /dev/null 2>&1 || true

# Bot identity verification (#5572 mitigation #1) — PIPELINE_SOURCE
# was already re-derived from the CI_JOB_TOKEN job record by
# pin-ci-job-identity.sh and admitted only as "api" (disjoint from
# the poller's schedule allowlist; parent_pipeline is no longer
# admitted). Confirm the pinned pipeline's creator is this bot.
# Some GitLab versions omit .user on the job-token pipeline GET;
# re-fetch with the poller PAT using the pinned IDs (never the
# overridable CI_PROJECT_ID / CI_PIPELINE_ID) when needed.
if [ -z "$(printf '%s' "${PIPELINE_RESPONSE}" | jq -r '.user.id // empty')" ]; then
  if ! PIPELINE_RESPONSE=$(fullsend_gate_curl \
    -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
    "${FULLSEND_PINNED_API_V4_URL}/projects/${FULLSEND_PINNED_PROJECT_ID}/pipelines/${FULLSEND_PINNED_PIPELINE_ID}"); then
    echo "ERROR: Cannot re-fetch pinned pipeline metadata — aborting (fail-closed)" >&2
    exit 1
  fi
fi
BOT_USER_ID=""
if BOT_RESPONSE=$(fullsend_gate_curl \
  "${FULLSEND_PINNED_API_V4_URL}/user" \
  -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}"); then
  BOT_USER_ID=$(printf '%s' "${BOT_RESPONSE}" | jq -r '.id // empty')
fi
if [ -z "${BOT_USER_ID}" ]; then
  echo "ERROR: Cannot verify bot identity — aborting (fail-closed)" >&2
  exit 1
fi
PIPELINE_CREATOR_ID=$(printf '%s' "${PIPELINE_RESPONSE}" | jq -r '.user.id // empty')
if [ -z "${PIPELINE_CREATOR_ID}" ]; then
  echo "ERROR: Cannot read pipeline creator — aborting (fail-closed)" >&2
  exit 1
fi
if [ "${PIPELINE_CREATOR_ID}" != "${BOT_USER_ID}" ]; then
  echo "ERROR: Pipeline created by user ${PIPELINE_CREATOR_ID}, expected bot ${BOT_USER_ID} — rejecting forged dispatch" >&2
  exit 1
fi

# Gate mode — every mode is role-aware now: select-gitlab-role-token.sh
# no longer has a shared-token path for disabled/rollback (it always
# resolves the registered per-role secret, matching gitlabroles.Resolve
# on the Go side — see internal/gitlabroles/gitlabroles.go). There is
# no mode left where the STAGE-derived re-select below is safe to trust
# without verification, so STAGE must always be cryptographically
# verified before it is allowed to select a role-specific credential.

# HMAC dispatch signature verification (#5572 mitigation #2) —
# verifies the dispatch variables were signed by the poller using
# a shared secret (FULLSEND_DISPATCH_SECRET). The identity pin
# above already admitted only source=api from the CI_JOB_TOKEN
# job record; HMAC authenticates the dispatch fields themselves.
# parent_pipeline is no longer admitted (disjoint allowlist).
#
# IMPORTANT: FULLSEND_DISPATCH_SECRET MUST be configured as a
# protected, masked CI/CD variable. Pipeline variables can be
# overridden by API-triggered pipelines — a protected variable
# prevents override by non-Maintainer callers, and masking
# prevents exposure in job logs. `repos install` auto-provisions
# this secret, so its absence now fails closed in every gate mode
# instead of silently skipping verification — an unsigned dispatch
# must never be trusted with a role-specific credential.
DISPATCH_VERIFIED=false
if [ "${PIPELINE_SOURCE}" = "api" ]; then
  if [ -n "${FULLSEND_DISPATCH_SECRET:-}" ]; then
    if [ -z "${FULLSEND_DISPATCH_HMAC:-}" ]; then
      echo "ERROR: FULLSEND_DISPATCH_HMAC missing — dispatch variables not signed (fail-closed)" >&2
      exit 1
    fi
    HMAC_MESSAGE=$(printf 'ACTOR_ID=%s\nEVENT_PAYLOAD_B64=%s\nEVENT_TYPE=%s\nFULLSEND_POLL_JOB_URL=%s\nIS_FORK=%s\nMR_AUTHOR_ID=%s\nORIGINATING_URL=%s\nREPO_FULL_NAME=%s\nRESOURCE_KEY=%s\nSTAGE=%s\nSTATUS_IID=%s' "${ACTOR_ID:-}" "${EVENT_PAYLOAD_B64:-}" "${EVENT_TYPE:-}" "${FULLSEND_POLL_JOB_URL:-}" "${IS_FORK:-}" "${MR_AUTHOR_ID:-}" "${ORIGINATING_URL:-}" "${REPO_FULL_NAME:-}" "${RESOURCE_KEY:-}" "${STAGE:-}" "${STATUS_IID:-}")
    if printf '%s' "${HMAC_MESSAGE}" | HMAC_SECRET="${FULLSEND_DISPATCH_SECRET}" python3 -c 'import hmac,hashlib,os,sys; expected=hmac.new(os.environ["HMAC_SECRET"].encode(),sys.stdin.read().encode(),hashlib.sha256).hexdigest(); sys.exit(0 if hmac.compare_digest(sys.argv[1],expected) else 1)' "${FULLSEND_DISPATCH_HMAC}"; then
      DISPATCH_VERIFIED=true
    else
      echo "ERROR: HMAC verification failed — dispatch variables may be forged (fail-closed)" >&2
      exit 1
    fi
  else
    echo "ERROR: FULLSEND_DISPATCH_SECRET is not configured — required in every gate mode to authenticate STAGE before a role-specific credential can be selected (fail-closed)" >&2
    exit 1
  fi
fi

# Fail closed for the rest of the job when STAGE has not actually
# been authenticated. Do not treat a skipped or impossible check as
# a pass: a missing dispatch secret (already fail-closed above)
# leaves DISPATCH_VERIFIED false. parent_pipeline is denied at the
# identity pin, so it never reaches this gate.
#
# An earlier revision of this template continued the job on the
# poller bootstrap credential in this situation instead of exiting.
# That is not sufficient: the CLI invocation further down this
# script (and the STAGE=fix review-body pre-fetch's Analyst
# identity lookup below) still resolve a STAGE-derived role
# credential internally — the `fullsend` binary's run command calls
# gitlabroles.SelectAgent(agentName, harnessRole, os.Getenv)
# (internal/cli/gitlab_role.go) using the same unverified STAGE
# value and reading role secrets straight from the process
# environment. DISPATCH_VERIFIED is a shell-local variable the Go
# binary never consults, so continuing on the poller credential at
# the shell level did not stop those later calls from promoting the
# STAGE-derived analyst/coder token anyway. Exiting here, before any
# of that later code runs, is the only way to keep an unverified
# STAGE from ever reaching a role-specific credential.
if [ "${DISPATCH_VERIFIED}" != "true" ]; then
  echo "ERROR: STAGE could not be cryptographically verified — refusing to continue rather than risk a role-specific credential being used downstream" >&2
  exit 1
fi

# Re-select FULLSEND_JOB_TOKEN for this stage's actual role —
# replacing the poller bootstrap identity used for the
# pre-verification calls above. Reachable only when STAGE has
# actually been authenticated (DISPATCH_VERIFIED, set only by a
# successful HMAC check above) — the fail-closed exit above already
# handles every other case, so this condition is always true here;
# it is kept explicit as a second, independent guard against a
# role-specific credential ever being selected on an unverified STAGE.
if [ "${DISPATCH_VERIFIED}" = "true" ]; then
  # Never log caller-controlled metadata before creator/HMAC authentication.
  if [ -n "${FULLSEND_POLL_JOB_URL:-}" ]; then
    case "${FULLSEND_POLL_JOB_URL}" in
      https://*) echo "Dispatched by: ${FULLSEND_POLL_JOB_URL}" ;;
      *) echo "WARNING: FULLSEND_POLL_JOB_URL is not a valid HTTPS URL — ignoring" ;;
    esac
  fi
  # shellcheck disable=SC2034  # consumed by sourced select-gitlab-role-token.sh
  FULLSEND_JOB_KIND=agent
  FULLSEND_JOB_AGENT="${STAGE:-}"
  . "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/select-gitlab-role-token.sh"

  # BOT_USER_ID/BOT_RESPONSE above were populated by the /user call
  # made with the poller bootstrap token, so they describe
  # the poller's bot identity — GitLab assigns a distinct bot user per
  # project access token (gitlab-role-credentials.md's "Identity
  # continuity" section). Downstream blocks (review's prior-review
  # lookup, the shared code|fix|review block's GIT_BOT_EMAIL, and
  # fix's prior-review-body lookup) must reflect the just-reselected
  # stage-role identity instead, so discard the poller-derived values
  # here and let those blocks re-fetch /user with the new
  # FULLSEND_JOB_TOKEN.
  unset BOT_USER_ID BOT_RESPONSE
fi

# Read config from default branch (trusted), not MR source branch.
# GitHub reads config from pull_request.base.sha; this is the GitLab
# equivalent. git fetch is needed because CI shallow clones only
# include the pipeline commit. Save the SHA so later steps (eval
# measurement manifests) can reuse the same trusted tip even if
# FETCH_HEAD moves.
#
# Fetch FULLSEND_PINNED_REF (the CI_JOB_TOKEN job record's ref,
# already proven equal to the project's enrolled protected default
# branch by pin-ci-job-identity.sh), not the overridable
# CI_DEFAULT_BRANCH pipeline variable — same outrankable class as
# CI_PROJECT_ID (ADR 0125). A pipeline/schedule variable named
# CI_DEFAULT_BRANCH could otherwise point this fetch at stale or
# attacker-influenced config, or empty it to skip the kill-switch and
# role-enablement checks below entirely (fail-open). FULLSEND_PINNED_REF
# is always non-empty here (pin-ci-job-identity.sh already fails closed
# above if it can't be resolved), so this fetch is now unconditional;
# fail closed rather than silently skip if it is ever unexpectedly unset.
CONFIG_YAML=""
DEFAULT_BRANCH_SHA=""
if [ -z "${FULLSEND_PINNED_REF:-}" ]; then
  echo "ERROR: FULLSEND_PINNED_REF is unset — refusing to fetch trusted config without a pinned ref (fail-closed)" >&2
  exit 1
fi
if ! git fetch origin "${FULLSEND_PINNED_REF}" --depth=1; then
  echo "ERROR: cannot fetch default branch — refusing to run without trusted config" >&2
  exit 1
fi
DEFAULT_BRANCH_SHA=$(git rev-parse FETCH_HEAD)
CONFIG_YAML=$(git show "${DEFAULT_BRANCH_SHA}:.fullsend/config.yaml" 2>/dev/null || echo "")

# Kill switch — halt all agent dispatch when active
if [ -n "${CONFIG_YAML}" ]; then
  if ! KILL_SWITCH=$(echo "${CONFIG_YAML}" | python3 -c "import sys,yaml; print('true' if str((yaml.safe_load(sys.stdin) or {}).get('kill_switch', False)).lower() in ('true','yes','1','on') else 'false')"); then
    echo "WARNING: invalid .fullsend/config.yaml — treating as unconfigured (no kill-switch)"
    KILL_SWITCH="false"
  fi
  if [ "${KILL_SWITCH}" = "true" ]; then
    echo "ERROR: Kill switch is active — all agent dispatch halted" >&2
    echo "Set kill_switch: false in .fullsend/config.yaml to resume" >&2
    exit 1
  fi
fi

# Role enablement — skip stage if its role is not in configured roles
if [ -n "${CONFIG_YAML}" ]; then
  STAGE_ROLE="${STAGE}"
  case "${STAGE}" in
    code|fix) STAGE_ROLE="coder" ;;
  esac
  if ! ROLES=$(echo "${CONFIG_YAML}" | python3 -c "import sys,yaml; v=(yaml.safe_load(sys.stdin) or {}); roles=v.get('roles') or []; roles=roles if isinstance(roles,list) else [roles]; print('\n'.join(str(r) for r in roles)) if roles else None"); then
    echo "WARNING: invalid .fullsend/config.yaml — treating as unconfigured (no role restriction)"
    ROLES=""
  fi
  if [ -n "${ROLES}" ] && ! echo "${ROLES}" | grep -Fqx "${STAGE_ROLE}"; then
    # Backward compat: "fullsend" in roles implies retro + prioritize
    if echo "${STAGE}" | grep -Eq '^(retro|prioritize)$' && echo "${ROLES}" | grep -Fqx "fullsend"; then
      echo "Stage '${STAGE}' allowed via 'fullsend' role — if customizing roles, add '${STAGE}' explicitly"
    else
      echo "Stage '${STAGE}' skipped — role '${STAGE_ROLE}' not in configured roles"
      exit 0
    fi
  fi
fi

# Authorization gate (ADR 0054) — check actor has Developer access.
# Read-only stages (retro, prioritize) are exempt to match GitHub
# behavior where any closer may trigger retro.
# Runs here (not in dispatch) because the Members API requires the
# bot PAT (from FULLSEND_JOB_TOKEN, selected by role).
# Fail-closed: API failure → access_level 0 → reject.
# ACTOR_ID is the generic actor identity, populated from MR_AUTHOR_ID
# for MR events or NoteAuthorID for issue/note events. Falls back to
# MR_AUTHOR_ID for backward compatibility with older dispatch pipelines.
if [ "${STAGE}" != "retro" ] && [ "${STAGE}" != "prioritize" ]; then
  AUTH_ACTOR_ID="${ACTOR_ID:-${MR_AUTHOR_ID:-}}"
  if [ -z "${AUTH_ACTOR_ID}" ]; then
    echo "WARNING: No actor identity available — skipping stage (fail-closed)"
    exit 0
  fi
  # CI_MERGE_REQUEST_PROJECT_ID is an overridable pipeline variable; only
  # trust it when it agrees with the project id pinned from the
  # CI_JOB_TOKEN job record, rather than letting it silently pick which
  # project's member list the role PAT is sent to.
  if [ -n "${CI_MERGE_REQUEST_PROJECT_ID:-}" ] && [ "${CI_MERGE_REQUEST_PROJECT_ID}" != "${FULLSEND_PINNED_PROJECT_ID}" ]; then
    echo "ERROR: CI_MERGE_REQUEST_PROJECT_ID '${CI_MERGE_REQUEST_PROJECT_ID}' does not match the pinned project ${FULLSEND_PINNED_PROJECT_ID} — refusing to trust an unverified CI variable to select the project (fail-closed)" >&2
    exit 1
  fi
  AUTHOR_ACCESS=0
  if MEMBER_RESPONSE=$(fullsend_gate_curl \
    "${FULLSEND_PINNED_API_V4_URL}/projects/${FULLSEND_PINNED_PROJECT_ID}/members/all/${AUTH_ACTOR_ID}" \
    -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}"); then
    AUTHOR_ACCESS=$(echo "${MEMBER_RESPONSE}" | jq -r '.access_level // 0')
  else
    echo "WARNING: Members API call failed for user ${AUTH_ACTOR_ID} — defaulting to access_level=0 (fail-closed)"
  fi
  case "${AUTHOR_ACCESS}" in ''|*[!0-9]*|??????????*) AUTHOR_ACCESS=0 ;; esac
  if [ "${AUTHOR_ACCESS}" -lt 30 ]; then
    echo "Actor does not have Developer access (access_level=${AUTHOR_ACCESS}) — skipping"
    exit 0
  fi
fi

# Fork MR protection — skip code/fix stages for fork MRs to prevent
# pushing commits to the target project from untrusted sources.
# CEL equivalent: !event.state.change_proposal.is_fork
#
# The "fix" stage's checkout-mr-source.sh already resolves, fetches, and
# validates the exact MR source revision in a fork/cross-project-aware
# way, including a `fullsend check-protected-branch` pre-push gate
# (#7814) — but that checkout-time validation is not sufficient on its
# own to lift this gate: the runner-side post-script that actually
# pushes the resulting fix commit still targets this job's own
# project/branch rather than the resolved MR source project, so a fork
# or cross-project "fix" dispatch remains denied here until a
# source-targeted publish path ships.
if [ "${STAGE}" = "code" ] || [ "${STAGE}" = "fix" ]; then
  if [ "${IS_FORK:-true}" = "true" ]; then
    echo "ERROR: Fork MR detected — refusing to run ${STAGE} stage" >&2
    exit 1
  fi
fi

# Harness environment variables — export the forge token, runner
# temp directory, and entity URL in the format that harnesses expect.
export GITLAB_TOKEN="${FULLSEND_JOB_TOKEN}"

# RUNNER_TEMP — GitHub Actions sets this automatically; GitLab CI
# does not. Harnesses that use ${RUNNER_TEMP} in host_files paths
# (e.g. the scribe harness) need it to resolve correctly.
export RUNNER_TEMP="${RUNNER_TEMP:-/tmp}"

# Construct the entity URL for the harness. EVENT_TYPE values from
# the poller use prefixed forms: issue_note, issue_label (→ issue
# URL) and mr_note, mr_event (→ merge request URL).
# Always export GITLAB_ISSUE_URL (empty is OK for harness env
# validation). Only set a real URL when the IID is non-empty and
# not "0" — inventing …/issues/0 passes EM-001's work_item check.
GITLAB_ISSUE_URL=""
case "${EVENT_TYPE:-}" in
  issue_*)
    if [[ -n "${STATUS_IID:-}" && "${STATUS_IID}" != "0" ]]; then
      GITLAB_ISSUE_URL="${FULLSEND_PINNED_GITLAB_URL}/${FULLSEND_PINNED_PROJECT_PATH}/-/issues/${STATUS_IID}"
    fi
    # This job's admit source is api only, so GitLab never natively
    # populates CI_MERGE_REQUEST_IID for an issue event — any value
    # present is an ordinary, outrankable project/group/pipeline CI/CD
    # variable. newGitLabClientFromEnv (internal/cli/reconcilestatus.go)
    # sets the note target to merge_requests whenever CI_MERGE_REQUEST_IID
    # is merely non-empty, regardless of FULLSEND_NOTE_TARGET, so unset it
    # here to keep status-comment API calls on the issues target.
    unset CI_MERGE_REQUEST_IID
    # FULLSEND_NOTE_TARGET is the same class of outrankable
    # project/group/pipeline CI/CD variable — a pre-set
    # FULLSEND_NOTE_TARGET=merge_requests would otherwise survive into
    # this arm and misdirect the status comment. Pin it explicitly.
    export FULLSEND_NOTE_TARGET="issues"
    ;;
  *)
    # STATUS_IID is part of the HMAC-signed dispatch message;
    # CI_MERGE_REQUEST_IID is not. This job's admit source is api
    # only, so GitLab never natively populates CI_MERGE_REQUEST_IID
    # here — any value comes solely from an ordinary, outrankable
    # project/group/pipeline CI/CD variable. Prefer the signed value,
    # fail closed if a non-empty CI_MERGE_REQUEST_IID disagrees with a
    # non-zero STATUS_IID, and — when STATUS_IID is empty or "0" —
    # ignore CI_MERGE_REQUEST_IID entirely rather than falling back to
    # it, mirroring the code|fix|review MR_IID resolution below.
    case "${CI_MERGE_REQUEST_IID:-}" in
      ''|*[!0-9]*) _fs_issue_url_mr_iid="" ;;
      *) _fs_issue_url_mr_iid="${CI_MERGE_REQUEST_IID}" ;;
    esac
    if [[ -n "${STATUS_IID:-}" && "${STATUS_IID}" != "0" ]]; then
      if [[ -n "${_fs_issue_url_mr_iid}" && "${_fs_issue_url_mr_iid}" != "${STATUS_IID}" ]]; then
        echo "ERROR: CI_MERGE_REQUEST_IID '${_fs_issue_url_mr_iid}' does not match the signed dispatch STATUS_IID '${STATUS_IID}' — refusing to trust an unverified CI variable to select the merge request" >&2
        exit 1
      fi
      _fs_mr_iid="${STATUS_IID}"
    else
      _fs_mr_iid=""
    fi
    if [[ -n "${_fs_mr_iid:-}" && "${_fs_mr_iid}" != "0" ]]; then
      GITLAB_ISSUE_URL="${FULLSEND_PINNED_GITLAB_URL}/${FULLSEND_PINNED_PROJECT_PATH}/-/merge_requests/${_fs_mr_iid}"
    fi
    unset _fs_mr_iid _fs_issue_url_mr_iid
    export FULLSEND_NOTE_TARGET="merge_requests"
    ;;
esac
export GITLAB_ISSUE_URL

# Extract the comment body for the retro agent. On GitHub,
# RETRO_COMMENT is set from the event payload's comment.body field;
# on GitLab the equivalent data is note_body inside EVENT_PAYLOAD_B64.
if [ "${STAGE}" = "retro" ] && [ -n "${EVENT_PAYLOAD_B64:-}" ]; then
  RETRO_COMMENT=$(printf '%s' "${EVENT_PAYLOAD_B64}" | base64 -d | jq -r '.note_body // ""')
  export RETRO_COMMENT
fi

# Pre-fetch prior review for the review agent — equivalent to
# pre-fetch-prior-review.sh in the GitHub scaffold. Queries the
# GitLab Notes API for the last bot review comment, validates
# authorship via author.id, extracts PRIOR_REVIEW_SHA from the
# **Head SHA:** marker, and writes the body to a temp file.
#
# Provenance: GitLab's author.id check ("bot-verified") is weaker
# than GitHub's performed_via_github_app.client_id ("app-verified").
# A compromised bot PAT could both create a note and pass the
# author check. This is an inherent GitLab API limitation — the
# Notes API has no app-level provenance metadata.
if [ "${STAGE}" = "review" ]; then
  # MR identity for the prior-review lookup below. STATUS_IID is part
  # of the HMAC-signed dispatch message (see HMAC_MESSAGE above);
  # CI_MERGE_REQUEST_IID is not. This job's admit source is api only,
  # so GitLab never natively populates CI_MERGE_REQUEST_IID here — any
  # value comes solely from an ordinary, outrankable project/group/
  # pipeline CI/CD variable. Prefer the signed value, fail closed if a
  # non-empty CI_MERGE_REQUEST_IID disagrees with a non-zero
  # STATUS_IID, and — when STATUS_IID is empty or "0" — ignore
  # CI_MERGE_REQUEST_IID entirely rather than falling back to it,
  # mirroring the shared code|fix|review MR_IID resolution further
  # down this script — this lookup runs before that block, so it
  # can't reuse its result and must apply the same fail-closed
  # cross-check itself rather than letting the unverified CI variable
  # win.
  case "${CI_MERGE_REQUEST_IID:-}" in
    ''|*[!0-9]*) _FS_REVIEW_CI_MR_IID="" ;;
    *) _FS_REVIEW_CI_MR_IID="${CI_MERGE_REQUEST_IID}" ;;
  esac
  if [ -n "${STATUS_IID:-}" ] && [ "${STATUS_IID}" != "0" ]; then
    if [ -n "${_FS_REVIEW_CI_MR_IID}" ] && [ "${_FS_REVIEW_CI_MR_IID}" != "${STATUS_IID}" ]; then
      echo "ERROR: CI_MERGE_REQUEST_IID '${_FS_REVIEW_CI_MR_IID}' does not match the signed dispatch STATUS_IID '${STATUS_IID}' — refusing to trust an unverified CI variable to select the merge request" >&2
      unset _FS_REVIEW_CI_MR_IID
      exit 1
    fi
    MR_IID="${STATUS_IID}"
  else
    MR_IID="0"
  fi
  unset _FS_REVIEW_CI_MR_IID
  PRIOR_REVIEW_FILE=$(mktemp)
  PRIOR_REVIEW_SHA=""
  PRIOR_REVIEW_PROVENANCE="none"

  # Reuse BOT_USER_ID from bot identity verification if available
  # (api-triggered pipelines); otherwise resolve from the forge token.
  BOT_ID="${BOT_USER_ID:-}"
  if [ -z "${BOT_ID}" ]; then
    if BOT_RESP=$(fullsend_gate_curl \
      "${FULLSEND_PINNED_API_V4_URL}/user" \
      -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}"); then
      BOT_ID=$(printf '%s' "${BOT_RESP}" | jq -r '.id // empty')
    fi
  fi

  if [ -n "${BOT_ID}" ] && [ "${MR_IID}" != "0" ]; then
    # Paginate through MR notes (newest first) to find the review marker.
    REVIEW_NOTE=""
    PAGE=1
    while [ "${PAGE}" -le 20 ] && [ -z "${REVIEW_NOTE}" ]; do
      NOTES_PAGE=$(fullsend_gate_curl \
        "${FULLSEND_PINNED_API_V4_URL}/projects/${FULLSEND_PINNED_PROJECT_ID}/merge_requests/${MR_IID}/notes?sort=desc&per_page=100&page=${PAGE}" \
        -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
        2>/dev/null || echo "[]")

      REVIEW_NOTE=$(printf '%s' "${NOTES_PAGE}" \
        | jq --arg bid "${BOT_ID}" '[.[] | select(.author.id == ($bid | tonumber) and (.body | contains("<!-- fullsend:review-agent -->")))] | first // empty' \
        2>/dev/null || echo "")

      if [ -n "${REVIEW_NOTE}" ] && [ "${REVIEW_NOTE}" != "null" ]; then
        break
      fi
      REVIEW_NOTE=""

      NOTE_COUNT=$(printf '%s' "${NOTES_PAGE}" | jq 'length' 2>/dev/null || echo "0")
      if [ "${NOTE_COUNT}" -lt 100 ]; then
        break
      fi

      PAGE=$((PAGE + 1))
    done

    if [ -n "${REVIEW_NOTE}" ] && [ "${REVIEW_NOTE}" != "null" ]; then
      # Defense-in-depth: re-verify author.id even though the jq
      # filter above already selects by it. Mirrors the GitHub
      # scaffold's post-filter provenance check pattern.
      NOTE_AUTHOR_ID=$(printf '%s' "${REVIEW_NOTE}" | jq -r '.author.id // empty')
      if [ "${NOTE_AUTHOR_ID}" = "${BOT_ID}" ]; then
        PRIOR_REVIEW_PROVENANCE="bot-verified"
        printf '%s' "${REVIEW_NOTE}" | jq -r '.body // ""' > "${PRIOR_REVIEW_FILE}"

        BYTE_COUNT=$(wc -c < "${PRIOR_REVIEW_FILE}")
        MAX_REVIEW_BYTES=1048576  # 1 MB
        if [ "${BYTE_COUNT}" -gt "${MAX_REVIEW_BYTES}" ]; then
          echo "WARNING: Prior review body too large (${BYTE_COUNT} bytes), skipping anchoring"
          : > "${PRIOR_REVIEW_FILE}"
        elif [ "${BYTE_COUNT}" -gt 1 ]; then
          CURRENT_SECTION=$(awk '/<!-- sticky:history-start -->/{exit} {print}' "${PRIOR_REVIEW_FILE}")
          # sed -nE, not grep -oP — kept in lockstep with the GitHub
          # scaffold's pre-fetch-prior-review.sh (see its comment for why).
          PRIOR_REVIEW_SHA=$(printf '%s' "${CURRENT_SECTION}" \
            | sed -nE 's/.*\*\*Head SHA:\*\* ([0-9a-f]{7,64}).*/\1/p' | head -1)
        fi
      else
        PRIOR_REVIEW_PROVENANCE="unverifiable-wrong-user"
      fi
    else
      echo "No prior review found (first review)"
    fi
  else
    echo "No prior review found (no bot identity or no MR IID)"
  fi

  export PRIOR_REVIEW_FILE
  export PRIOR_REVIEW_SHA
  export PRIOR_REVIEW_PROVENANCE
fi

# Shared environment variables for code, fix, and review stages —
# push credentials, git bot identity, and MR identity needed by
# post-scripts. Equivalent to the vars set by setup-agent-env.sh
# and the reusable workflows on GitHub. #6865.
if [ "${STAGE}" = "code" ] || [ "${STAGE}" = "fix" ] || [ "${STAGE}" = "review" ]; then
  # Resolve bot username for GIT_BOT_EMAIL. Reuse BOT_RESPONSE
  # from bot identity verification (api-triggered pipelines);
  # otherwise query the /user API.
  _BOT_USERNAME=""
  if [ -n "${BOT_RESPONSE:-}" ]; then
    _BOT_USERNAME=$(printf '%s' "${BOT_RESPONSE}" | jq -r '.username // empty')
  fi
  if [ -z "${_BOT_USERNAME}" ]; then
    if _BOT_RESP=$(fullsend_gate_curl \
      "${FULLSEND_PINNED_API_V4_URL}/user" \
      -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}"); then
      _BOT_USERNAME=$(printf '%s' "${_BOT_RESP}" | jq -r '.username // empty')
    fi
  fi

  # Push token — on GitLab the selected role PAT
  # (FULLSEND_JOB_TOKEN) serves as both the API token and the
  # push token. The post-script uses PUSH_TOKEN to push commits.
  # fullsend run may later blank PUSH_TOKEN for roles without
  # write_repository.
  export PUSH_TOKEN="${FULLSEND_JOB_TOKEN}"
  export PUSH_TOKEN_SOURCE="pat"

  # Bot git identity — construct a noreply-style email from the
  # bot username. GitLab project access tokens don't have real
  # email addresses. The post-script uses GIT_BOT_EMAIL to
  # configure git author/committer identity.
  GIT_BOT_EMAIL="${_BOT_USERNAME:-fullsend-${STAGE}}@noreply.${CI_SERVER_HOST:-gitlab.com}"
  export GIT_BOT_EMAIL

  # MR identity — used by forge.gitlab env config, by the fix
  # post-script for pushing and commenting, and (for the fix stage)
  # to key the merge-request API call in checkout-mr-source.sh that
  # selects which MR's source gets fetched into --target-repo.
  # STATUS_IID is part of the HMAC-signed dispatch message (see
  # HMAC_MESSAGE above); CI_MERGE_REQUEST_IID is not. This job's admit
  # source is api only, so GitLab never natively populates
  # CI_MERGE_REQUEST_IID here — any value comes solely from an
  # ordinary, outrankable project/group/pipeline CI/CD variable.
  # Prefer the signed value, fail closed if a non-empty
  # CI_MERGE_REQUEST_IID disagrees with a non-zero STATUS_IID, and —
  # when STATUS_IID is empty or "0" — ignore CI_MERGE_REQUEST_IID
  # entirely rather than letting an unverified CI variable pick which
  # merge request's source is checked out — mirroring the fail-closed
  # cross-checks already applied to CI_MERGE_REQUEST_SOURCE_* in
  # checkout-mr-source.sh.
  case "${CI_MERGE_REQUEST_IID:-}" in
    ''|*[!0-9]*) _FS_CI_MR_IID="" ;;
    *) _FS_CI_MR_IID="${CI_MERGE_REQUEST_IID}" ;;
  esac
  if [ -n "${STATUS_IID:-}" ] && [ "${STATUS_IID}" != "0" ]; then
    if [ -n "${_FS_CI_MR_IID}" ] && [ "${_FS_CI_MR_IID}" != "${STATUS_IID}" ]; then
      echo "ERROR: CI_MERGE_REQUEST_IID '${_FS_CI_MR_IID}' does not match the signed dispatch STATUS_IID '${STATUS_IID}' — refusing to trust an unverified CI variable to select the merge request" >&2
      unset _FS_CI_MR_IID
      exit 1
    fi
    MR_IID="${STATUS_IID}"
  else
    MR_IID="0"
  fi
  unset _FS_CI_MR_IID
  export MR_NUMBER="${MR_IID}"
  if [ "${MR_IID}" != "0" ]; then
    export GITLAB_MR_URL="${FULLSEND_PINNED_GITLAB_URL}/${FULLSEND_PINNED_PROJECT_PATH}/-/merge_requests/${MR_IID}"
  else
    export GITLAB_MR_URL=""
  fi
fi

# Pre-fetch review body for the fix agent — equivalent to the
# "Pre-fetch review body" step in reusable-fix.yml. Queries the
# GitLab Notes API for the last review bot comment, validates
# size and non-empty for bot-triggered runs, and exports
# REVIEW_BODY_FILE for the harness.
if [ "${STAGE}" = "fix" ]; then
  # MR_IID is already set by the shared code|fix|review block above.
  REVIEW_BODY_FILE=$(mktemp)

  # Review notes are authored by the analyst identity (STAGE=review
  # maps to the analyst role in select-gitlab-role-token.sh) — never
  # by the poller (BOT_USER_ID, populated from bot-identity
  # verification) and never by this fix stage's own coder identity
  # (FULLSEND_JOB_TOKEN). Reusing either would never match a real
  # review note, since analyst/coder always resolve to distinct
  # tokens, silently breaking the bot-triggered review->fix loop.
  # Resolve BOT_ID by temporarily re-selecting the analyst
  # credential for this one lookup; FULLSEND_JOB_TOKEN is restored
  # immediately after so GITLAB_TOKEN, PUSH_TOKEN, the TARGET_BRANCH
  # lookup below, and the eventual git push still use this stage's
  # own coder token.
  BOT_ID=""
  _FIX_STAGE_JOB_TOKEN="${FULLSEND_JOB_TOKEN}"
  _FIX_STAGE_JOB_TOKEN_NAME="${FULLSEND_JOB_TOKEN_NAME:-}"
  # shellcheck disable=SC2034  # consumed by sourced select-gitlab-role-token.sh
  FULLSEND_JOB_KIND=agent
  FULLSEND_JOB_AGENT=review
  if . "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/select-gitlab-role-token.sh"; then
    if BOT_RESP=$(fullsend_gate_curl \
      "${FULLSEND_PINNED_API_V4_URL}/user" \
      -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}"); then
      BOT_ID=$(printf '%s' "${BOT_RESP}" | jq -r '.id // empty')
    fi
  else
    echo "WARNING: could not resolve the analyst identity for the review-note author lookup"
  fi
  # shellcheck disable=SC2034  # restore STAGE after the analyst-identity lookup
  FULLSEND_JOB_AGENT="${STAGE:-}"
  export FULLSEND_JOB_TOKEN="${_FIX_STAGE_JOB_TOKEN}"
  export FULLSEND_JOB_TOKEN_NAME="${_FIX_STAGE_JOB_TOKEN_NAME}"
  unset _FIX_STAGE_JOB_TOKEN _FIX_STAGE_JOB_TOKEN_NAME

  # Bot username for TRIGGER_SOURCE/GIT_BOT_EMAIL is this fix
  # stage's own (coder) identity, normally set by the shared
  # code|fix|review block above. If that block's /user call failed
  # transiently, retry here with this stage's own (now-restored)
  # token — not the analyst token used for BOT_ID above.
  if [ -z "${_BOT_USERNAME}" ]; then
    if _BOT_RESP=$(fullsend_gate_curl \
      "${FULLSEND_PINNED_API_V4_URL}/user" \
      -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}"); then
      _BOT_USERNAME=$(printf '%s' "${_BOT_RESP}" | jq -r '.username // empty')
    fi
  fi

  if [ -n "${BOT_ID}" ] && [ "${MR_IID}" != "0" ]; then
    # Paginate through MR notes (newest first) to find the review marker.
    REVIEW_NOTE=""
    PAGE=1
    while [ "${PAGE}" -le 20 ] && [ -z "${REVIEW_NOTE}" ]; do
      NOTES_PAGE=$(fullsend_gate_curl \
        "${FULLSEND_PINNED_API_V4_URL}/projects/${FULLSEND_PINNED_PROJECT_ID}/merge_requests/${MR_IID}/notes?sort=desc&per_page=100&page=${PAGE}" \
        -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
        2>/dev/null || echo "[]")

      REVIEW_NOTE=$(printf '%s' "${NOTES_PAGE}" \
        | jq --arg bid "${BOT_ID}" '[.[] | select(.author.id == ($bid | tonumber) and (.body | contains("<!-- fullsend:review-agent -->")))] | first // empty' \
        2>/dev/null || echo "")

      if [ -n "${REVIEW_NOTE}" ] && [ "${REVIEW_NOTE}" != "null" ]; then
        break
      fi
      REVIEW_NOTE=""

      NOTE_COUNT=$(printf '%s' "${NOTES_PAGE}" | jq 'length' 2>/dev/null || echo "0")
      if [ "${NOTE_COUNT}" -lt 100 ]; then
        break
      fi

      PAGE=$((PAGE + 1))
    done

    if [ -n "${REVIEW_NOTE}" ] && [ "${REVIEW_NOTE}" != "null" ]; then
      # Defense-in-depth: re-verify author.id even though the jq
      # filter above already selects by it.
      NOTE_AUTHOR_ID=$(printf '%s' "${REVIEW_NOTE}" | jq -r '.author.id // empty')
      if [ "${NOTE_AUTHOR_ID}" = "${BOT_ID}" ]; then
        printf '%s' "${REVIEW_NOTE}" | jq -r '.body // ""' > "${REVIEW_BODY_FILE}"
      else
        echo "WARNING: Review note author (${NOTE_AUTHOR_ID}) does not match bot (${BOT_ID}) — review body not extracted"
      fi
    else
      echo "No review note found on MR !${MR_IID}"
    fi
  else
    echo "No review note found (no bot identity or no MR IID)"
  fi

  BYTE_COUNT=$(wc -c < "${REVIEW_BODY_FILE}")
  echo "Pre-fetched review body: ${BYTE_COUNT} bytes"

  MAX_REVIEW_BYTES=1048576  # 1 MB
  if [ "${BYTE_COUNT}" -gt "${MAX_REVIEW_BYTES}" ]; then
    echo "ERROR: Review body is ${BYTE_COUNT} bytes (max: ${MAX_REVIEW_BYTES})" >&2
    exit 1
  fi

  # For bot-triggered runs, the review body must not be empty —
  # there is nothing for the fix agent to act on. Human-triggered
  # (/fs-fix) runs may have an empty review body.
  # Check is_bot from the event payload, not PIPELINE_SOURCE —
  # on GitLab ALL fix dispatches are API-triggered (via poller).
  _IS_BOT_TRIGGER="false"
  if [ -n "${EVENT_PAYLOAD_B64:-}" ]; then
    _IS_BOT_TRIGGER=$(printf '%s' "${EVENT_PAYLOAD_B64}" | base64 -d \
      | jq -r '.is_bot // false' 2>/dev/null || echo "false")
  fi
  if [ "${_IS_BOT_TRIGGER}" = "true" ] && [ "${BYTE_COUNT}" -le 1 ]; then
    echo "ERROR: Bot-triggered run but review body is empty — nothing to fix" >&2
    exit 1
  fi

  export REVIEW_BODY_FILE

  # Fix-stage environment variables — equivalent to the env vars
  # set by reusable-fix.yml's "Extract PR number and context",
  # "Record pre-agent HEAD", and "Run fix agent" steps. These are
  # required by the fix harness env.runner and forge.gitlab blocks.

  # Target branch (MR base branch). This job's admit source is
  # api-only (parent_pipeline is not an admitted arm — see
  # FULLSEND_ADMIT_SOURCE above), so GitLab does not natively populate
  # CI_MERGE_REQUEST_TARGET_BRANCH_NAME here; a non-empty value is an
  # ordinary, overridable project/group/pipeline CI/CD variable of the
  # same outrankable class ADR 0125 already establishes for
  # CI_PROJECT_ID/CI_DEFAULT_BRANCH. Always resolve TARGET_BRANCH from
  # the pinned-project MR API, falling back to FULLSEND_PINNED_REF (the
  # CI_JOB_TOKEN job record's ref) — never from
  # CI_MERGE_REQUEST_TARGET_BRANCH_NAME. If that variable is non-empty
  # and disagrees with the resolved value, fail closed instead of
  # trusting an unverified CI variable to pick the fix push's target
  # branch (mirroring the CI_MERGE_REQUEST_IID vs STATUS_IID pattern
  # above).
  if [ "${MR_IID}" != "0" ]; then
    TARGET_BRANCH=$(fullsend_gate_curl \
      "${FULLSEND_PINNED_API_V4_URL}/projects/${FULLSEND_PINNED_PROJECT_ID}/merge_requests/${MR_IID}" \
      -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
      | jq -r '.target_branch // empty' 2>/dev/null || echo "")
    if [ -z "${TARGET_BRANCH}" ]; then
      TARGET_BRANCH="${FULLSEND_PINNED_REF}"
    fi
  else
    TARGET_BRANCH="${FULLSEND_PINNED_REF}"
  fi
  if [ -n "${CI_MERGE_REQUEST_TARGET_BRANCH_NAME:-}" ] && [ "${CI_MERGE_REQUEST_TARGET_BRANCH_NAME}" != "${TARGET_BRANCH}" ]; then
    echo "ERROR: CI_MERGE_REQUEST_TARGET_BRANCH_NAME '${CI_MERGE_REQUEST_TARGET_BRANCH_NAME}' does not match the pinned/API target branch '${TARGET_BRANCH}' — refusing to trust an unverified CI variable to select the fix push target" >&2
    exit 1
  fi
  export TARGET_BRANCH

  # Trigger source — the forge username that triggered this fix.
  # Bot-triggered (changes-requested note via poller): the bot's
  # username (ends in _bot_*, matching GitLab project access token
  # convention). Human-triggered (/fs-fix comment): the note
  # author's username. The agent uses this to determine mode.
  TRIGGER_SOURCE=""
  if [ "${PIPELINE_SOURCE}" = "api" ] && [ -n "${EVENT_PAYLOAD_B64:-}" ]; then
    if [ "${_IS_BOT_TRIGGER}" = "true" ]; then
      # Bot-triggered — use cached bot username.
      TRIGGER_SOURCE="${_BOT_USERNAME}"
      if [ -z "${TRIGGER_SOURCE}" ]; then
        echo "WARNING: Bot-triggered but could not resolve bot username for TRIGGER_SOURCE"
      fi
    else
      # Human-triggered — resolve note author username from ID.
      _NOTE_AUTHOR_ID=$(printf '%s' "${EVENT_PAYLOAD_B64}" | base64 -d \
        | jq -r '.note_author_id // empty' 2>/dev/null || echo "")
      case "${_NOTE_AUTHOR_ID}" in ''|*[!0-9]*) _NOTE_AUTHOR_ID="" ;; esac
      if [ -n "${_NOTE_AUTHOR_ID}" ]; then
        TRIGGER_SOURCE=$(fullsend_gate_curl \
          "${FULLSEND_PINNED_API_V4_URL}/users/${_NOTE_AUTHOR_ID}" \
          -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
          | jq -r '.username // empty' 2>/dev/null || echo "")
      fi
    fi
  fi
  if [ -z "${TRIGGER_SOURCE}" ]; then
    TRIGGER_SOURCE="unknown"
  fi
  export TRIGGER_SOURCE

  # Human instruction — extracted from the /fs-fix note body in
  # the event payload. Default to "none" so the env var is always
  # non-empty (the fullsend binary rejects empty runner_env values).
  # Bot-triggered runs always get "none" (matching reusable-fix.yml).
  HUMAN_INSTRUCTION="none"
  if [ "${_IS_BOT_TRIGGER}" != "true" ] && [ -n "${EVENT_PAYLOAD_B64:-}" ]; then
    _NOTE_BODY=$(printf '%s' "${EVENT_PAYLOAD_B64}" | base64 -d \
      | jq -r '.note_body // empty' 2>/dev/null || echo "")
    case "${_NOTE_BODY}" in
      /fs-fix*)
        _INSTRUCTION="${_NOTE_BODY#/fs-fix}"
        _INSTRUCTION="${_INSTRUCTION#"${_INSTRUCTION%%[![:space:]]*}"}"
        if [ -n "${_INSTRUCTION}" ]; then
          HUMAN_INSTRUCTION="${_INSTRUCTION}"
        fi
        ;;
    esac
  fi
  export HUMAN_INSTRUCTION

  # Fix iteration — count previous fix-agent commits on the MR to
  # enforce iteration caps. Mirrors the GitHub workflow's commit
  # counting via the API (no checkout needed). Falls back to 1 on
  # API failure so the agent always runs at least once.
  FIX_COMMITS=0
  if [ "${MR_IID}" != "0" ]; then
    _COMMIT_PAGE=1
    while [ "${_COMMIT_PAGE}" -le 5 ]; do
      _COMMITS_BATCH=$(fullsend_gate_curl \
        "${FULLSEND_PINNED_API_V4_URL}/projects/${FULLSEND_PINNED_PROJECT_ID}/merge_requests/${MR_IID}/commits?per_page=100&page=${_COMMIT_PAGE}" \
        -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" 2>/dev/null || echo "[]")
      _BATCH_FIX=$(printf '%s' "${_COMMITS_BATCH}" \
        | jq '[.[] | select(.author_name == "fullsend-fix")] | length' 2>/dev/null || echo "0")
      FIX_COMMITS=$((FIX_COMMITS + _BATCH_FIX))
      _BATCH_COUNT=$(printf '%s' "${_COMMITS_BATCH}" | jq 'length' 2>/dev/null || echo "0")
      if [ "${_BATCH_COUNT}" -lt 100 ]; then
        break
      fi
      _COMMIT_PAGE=$((_COMMIT_PAGE + 1))
    done
  fi
  FIX_ITERATION=$(( FIX_COMMITS + 1 ))
  echo "Fix iteration: ${FIX_ITERATION} (${FIX_COMMITS} previous fix commits)"
  export FIX_ITERATION

  # Check out the MR source revision into a subdirectory before the
  # sandbox is created. Dispatch pipelines run from the default branch;
  # TARGET_BRANCH is the MR *base* and is not the reviewed head. The
  # subdirectory keeps trusted .fullsend/ config on the default-branch
  # working tree (read above via DEFAULT_BRANCH_SHA) while --target-repo
  # hands the sandbox the exact source SHA. Fetching without checking
  # out the working tree is not enough.
  FIX_TARGET_REPO=""
  . "${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/checkout-mr-source.sh"

  # Pre-agent HEAD — record the MR source SHA before the agent
  # modifies the tree. The post-script uses this to detect whether
  # the agent committed, then pushes that commit back to the MR
  # source branch (and still rejects unsafe target branches).
  PRE_AGENT_HEAD=$(git -C "${FIX_TARGET_REPO}" rev-parse HEAD)
  export PRE_AGENT_HEAD
fi

# Run the agent — fullsend run resolves the harness file, reads
# the image field, and creates the sandbox container via Podman.
# Capture the run status so eval-measure still runs after a failed
# agent (failed runs still write run-telemetry.jsonl).
# Eval measurements (fail-open): same CLI as GitHub Actions. Never
# fail the agent job.
# Manifest trust (mirrors kill-switch config): prefer a local override
# from the default branch tip (DEFAULT_BRANCH_SHA), else SHA-pinned
# fetch from public fullsend-ai/agents (GitHub GetRef). Never read
# .fullsend/eval/measurements/ from the MR source tree — that would
# let an MR author change which scorers run or their id@version for
# the job's trend. Do not pass --fullsend-dir to eval-measure here.
# agents is public, so GetRef works without GH_TOKEN, but unauthenticated
# calls share GitHub's ~60 req/hr per-IP limit — export GH_TOKEN /
# GITHUB_TOKEN on busy shared runners.
# Write under $CI_PROJECT_DIR so GitLab can retain artifacts (not an
# ephemeral tmp path). BREAKING CHANGE vs older scaffolds: default
# --output-dir now lives inside the project dir (artifact retention
# + top-level output/ sandbox exclude). Re-sync adopts the new layout.
#
# Fix stage: --target-repo is the MR source checkout. Other stages
# keep the dispatch-ref working tree. --fullsend-dir stays the
# default-branch .fullsend/ so untrusted MR config is not used.
_FS_TARGET_REPO="."
if [ "${STAGE}" = "fix" ]; then
  if [ -z "${FIX_TARGET_REPO:-}" ] || [ ! -d "${FIX_TARGET_REPO}/.git" ]; then
    echo "ERROR: MR source checkout is missing — refusing to run the fix agent against the dispatch ref" >&2
    exit 1
  fi
  _FS_TARGET_REPO="${FIX_TARGET_REPO}"
fi
mkdir -p "${CI_PROJECT_DIR}/output"
set +e
# --status-repo uses the pinned project path (from the CI_JOB_TOKEN
# job record), not the overridable CI_PROJECT_PATH pipeline variable
# — same outrankable class as CI_PROJECT_ID (ADR 0125). `fullsend run`
# has no --gitlab-url flag; its GitLab client instead resolves the API
# host from FULLSEND_GITLAB_URL, then GITLAB_API_URL, then CI_SERVER_URL
# (newGitLabClientFromEnv in internal/cli/reconcilestatus.go) — all
# overridable pipeline variables in the same outrankable class. Export
# the pin-validated API root so that resolution never falls through to
# one of those instead.
export FULLSEND_GITLAB_URL="${FULLSEND_PINNED_GITLAB_URL}"
fullsend run "${STAGE}" \
  --fullsend-dir .fullsend \
  --target-repo "${_FS_TARGET_REPO}" \
  --output-dir "${CI_PROJECT_DIR}/output" \
  --forge gitlab \
  --run-url "${CI_PIPELINE_URL}" \
  --status-repo "${FULLSEND_PINNED_PROJECT_PATH}" \
  --status-number "${MR_NUMBER:-${STATUS_IID:-0}}"
RUN_STATUS=$?
set -e

MEASURE_ARGS=(--agent "${STAGE}" --output-dir "${CI_PROJECT_DIR}/output")
if [ -n "${DEFAULT_BRANCH_SHA}" ]; then
  if MEASURE_YAML=$(git show "${DEFAULT_BRANCH_SHA}:.fullsend/eval/measurements/${STAGE}.yaml" 2>/dev/null); then
    MEASURE_FILE="${CI_PROJECT_DIR}/output/.fullsend-measure-${STAGE}.yaml"
    printf '%s\n' "${MEASURE_YAML}" > "${MEASURE_FILE}"
    MEASURE_ARGS+=(--registry "${MEASURE_FILE}")
  fi
fi
fullsend eval-measure "${MEASURE_ARGS[@]}" || true

exit "${RUN_STATUS}"
