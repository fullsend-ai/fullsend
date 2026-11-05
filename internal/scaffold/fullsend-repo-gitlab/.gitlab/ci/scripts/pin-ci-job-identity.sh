#!/usr/bin/env bash
# pin-ci-job-identity.sh — Pin job identity to the CI_JOB_TOKEN job record.
#
# Source this file (do not execute it) after the CI_DEBUG_TRACE guard and
# before any PAT-bearing call or admit logic. The dispatcher job (#7771)
# reuses the same helper with FULLSEND_ADMIT_SOURCE=trigger.
#
# Required caller-set input:
#   FULLSEND_ADMIT_SOURCE  exactly one of api|schedule|trigger, or the
#                          agent-only set "api pipeline" (#8438): an agent
#                          pipeline is started either by the Poller through
#                          the pipelines API (api) or by a dispatch job
#                          through the CI_JOB_TOKEN trigger API (pipeline).
#                          Per-job allowlists stay disjoint: the poller
#                          admits schedule and the dispatcher trigger only.
#
# Exports on success:
#   FULLSEND_PINNED_PROJECT_ID
#   FULLSEND_PINNED_PIPELINE_ID
#   FULLSEND_PINNED_REF
#   FULLSEND_PINNED_PIPELINE_SOURCE
#   FULLSEND_PINNED_PIPELINE_RESPONSE
#   FULLSEND_PINNED_JOB_USER_ID
#   FULLSEND_PINNED_PROJECT_PATH  (path_with_namespace, e.g. group/project)
#   FULLSEND_PINNED_GITLAB_URL     (validated API root with /api/v4 stripped)
#
# FULLSEND_PINNED_PROJECT_PATH / FULLSEND_PINNED_GITLAB_URL exist so
# callers can pass the fullsend CLI's --project/--status-repo and
# --gitlab-url flags the pinned job-record identity, instead of the
# overridable CI_PROJECT_PATH / FULLSEND_GITLAB_URL / CI_SERVER_URL
# pipeline variables (same outrankable class as CI_PROJECT_ID, ADR
# 0125). See run-poll-job.sh and run-agent-job.sh for that reuse.
#
# Trust model (ADR 0131, superseding ADR 0125's trigger-variable control):
#   The GitLab-side pipeline-variable restriction
#   (ci_pipeline_variables_minimum_override_role=no_one_allowed) is the
#   REQUIRED control that stops a pipeline caller, including a
#   trigger-token holder, from outranking CI_JOB_TOKEN, CI_API_V4_URL,
#   and dispatch secrets with pipeline variables. Dispatch data travels
#   as typed pipeline inputs, which that setting does not govern, so
#   the Poller stays a Developer-role identity and no runtime
#   credential (including the webhook trigger token's owner) is above
#   Developer. Install/converge
#   (internal/repos/gitlab_pipeline_var_restriction.go) verifies
#   no_one_allowed before delivering runnable typed jobs and fails
#   closed otherwise; automatic changes to the setting stay behind an
#   opt-in environment variable (see that file). See
#   docs/contributing/gitlab-role-credentials.md ("How a job selects
#   its credential") and the ADR 0125 security-gate status table in
#   docs/normative/normalized-event/v1/README.md.
#   Historical: ADR 0125 first proposed an `owner` threshold with an
#   Owner-role poller/dispatcher PAT and a sub-Owner trigger identity;
#   ADR 0131 replaced that design (#7769).
#
#   This helper is defense-in-depth: it
#   takes project, pipeline, and ref from GET /api/v4/job (the job
#   that owns the supplied token) rather than from the overridable
#   CI_PROJECT_ID / CI_PIPELINE_ID / CI_COMMIT_REF_PROTECTED env
#   vars, and it fails closed unless the server-side .source equals
#   FULLSEND_ADMIT_SOURCE and the job ref is the project's
#   protected default branch.
#
#   GET /api/v4/job still needs a bootstrap URL (CI_API_V4_URL).
#   That URL is in the same overridable class; the restriction is
#   what makes the bootstrap trustworthy. An invalid CI_JOB_TOKEN
#   override is expected to fail closed here (the lookup 401s/404s).
#   A valid foreign job token would mis-resolve the pin to that
#   foreign job — residual, closed by the restriction rather than by
#   this script. Neither override case has been live-tested on a
#   target GitLab version yet (see the security-gate status table).
#
# Transport: gate curls ignore trigger-influenced proxy settings
# (--noproxy '*', proxy env stripped), disable curlrc (-q, CURLRC/
# CURL_HOME stripped), disable URL brace/bracket-range globbing
# (--globoff, plus a CI_API_V4_URL character check below) so a
# crafted API root cannot fan a single call out to an attacker host,
# and pin CA from trust-ci-server-ca.sh (runner-provisioned
# CI_SERVER_TLS_CA_FILE), after unsetting SSL_CERT_FILE/GIT_SSL_CAINFO/
# CURL_CA_BUNDLE/REQUESTS_CA_BUNDLE/NODE_EXTRA_CA_CERTS/SSL_CERT_DIR/
# SSLKEYLOGFILE (also stripped per-call by fullsend_gate_curl) so a
# trigger-supplied CA/keylog override cannot stick from job init.
# JOB-TOKEN is sent on every identity-pin call this script makes below;
# the project-detail and branch-protection calls additionally retry with
# the already-provisioned Poller-role PAT (FULLSEND_GITLAB_POLLER_TOKEN,
# injected into every protected-branch job regardless of which role a
# given job ultimately selects — see run-poll-job.sh) when the JOB-TOKEN
# attempt fails. Some GitLab versions (observed: self-hosted EE 19.2.7,
# #7965) do not mark `GET /projects/:id` job_token_allowed in their own
# route config, so JOB-TOKEN alone cannot be relied on for that lookup.
# This fallback does not weaken the pin: CI_JOB_TOKEN is already proven
# valid by the GET /job call above (that is the sole identity claim —
# which project/pipeline/ref to trust), so the PAT here only supplies an
# alternate, equally-authoritative source for data the checks below
# (pipeline source, protected default branch) still fully enforce. A
# caller's `-H "JOB-TOKEN: ..."` or
# `-H "PRIVATE-TOKEN: ..."` argument is rewritten to `-H @tempfile`
# before exec'ing curl, so the token value itself never appears in
# curl's argv (/proc/<pid>/cmdline, `ps`, execve audit logs).
# fullsend_gate_curl itself is transport-only (it does not choose
# which auth header to send): once this file is sourced, the function
# stays defined in the caller's shell, and callers reuse it for their
# own PAT-bearing calls after the pin succeeds so those calls get the
# same proxy-stripping, CA-pinning, and argv-hygiene protections. See
# run-agent-job.sh and run-poll-job.sh for that reuse pattern.

fullsend_gate_curl() {
  # Diagnostic output must stay on stderr: callers capture stdout.
  #
  # -q (--disable) must be curl's first argument: it makes curl ignore
  # curlrc, so a trigger-influenced CURLRC/CURL_HOME cannot inject
  # extra options (e.g. a proxy or a different CA) into these calls.
  # --globoff disables curl's URL brace/bracket-range globbing so a
  # CI_API_V4_URL such as https://{attacker.example,gitlab.example}/api/v4
  # cannot make a single call fan out to an attacker-chosen host with
  # the JOB-TOKEN/PRIVATE-TOKEN header attached.
  _fs_curl_args=(
    -q
    --silent --show-error --fail
    --retry 3 --retry-delay 2 --retry-all-errors
    --noproxy '*' --proto '=https' --max-redirs 0 --globoff
  )
  if [ -n "${CURL_CA_BUNDLE:-}" ] && [ -f "${CURL_CA_BUNDLE}" ]; then
    _fs_curl_args+=(--cacert "${CURL_CA_BUNDLE}")
  fi

  # Rewrite a caller's `-H "JOB-TOKEN: ..."` / `-H "PRIVATE-TOKEN: ..."`
  # into `-H @tempfile` (curl >=7.55 loads headers from a file with this
  # syntax) so the token itself never appears in curl's argv — visible
  # via /proc/<pid>/cmdline, `ps`, or execve audit logs otherwise. Every
  # caller keeps passing the header the same way; this rewrite is
  # transparent so the auth-header hygiene lives in one place instead of
  # needing to be repeated at each call site.
  local _fs_header_file=""
  local -a _fs_call_args=()
  local _fs_arg _fs_next
  while [ "$#" -gt 0 ]; do
    _fs_arg=$1
    if [ "${_fs_arg}" = "-H" ] && [ "$#" -gt 1 ]; then
      _fs_next=$2
      case "${_fs_next}" in
        'JOB-TOKEN: '*|'PRIVATE-TOKEN: '*)
          _fs_header_file=$(mktemp)
          chmod 600 "${_fs_header_file}"
          printf '%s\n' "${_fs_next}" > "${_fs_header_file}"
          _fs_call_args+=(-H "@${_fs_header_file}")
          shift 2
          continue
          ;;
      esac
    fi
    _fs_call_args+=("${_fs_arg}")
    shift
  done

  local _fs_gate_curl_status
  env -u HTTP_PROXY -u HTTPS_PROXY -u ALL_PROXY \
    -u http_proxy -u https_proxy -u all_proxy \
    -u CURLRC -u CURL_HOME \
    -u SSL_CERT_DIR -u SSLKEYLOGFILE \
    curl "${_fs_curl_args[@]}" "${_fs_call_args[@]}"
  _fs_gate_curl_status=$?
  [ -n "${_fs_header_file}" ] && rm -f "${_fs_header_file}"
  unset _fs_curl_args _fs_call_args _fs_header_file _fs_arg _fs_next
  return "${_fs_gate_curl_status}"
}

fullsend_pin_ci_job_identity() {
  case "${FULLSEND_ADMIT_SOURCE:-}" in
    api|schedule|trigger|"api pipeline") ;;
    *)
      echo "ERROR: FULLSEND_ADMIT_SOURCE must be 'api', 'schedule', 'trigger', or 'api pipeline' (disjoint per-job allowlist), got '${FULLSEND_ADMIT_SOURCE:-<empty>}'" >&2
      return 1
      ;;
  esac

  if [ -z "${CI_JOB_TOKEN:-}" ]; then
    echo "ERROR: CI_JOB_TOKEN is empty — cannot pin job identity (fail-closed)" >&2
    return 1
  fi

  _fs_api="${CI_API_V4_URL:-}"
  _fs_api="${_fs_api%/}"
  case "${_fs_api}" in
    https://*/api/v4) ;;
    *)
      echo "ERROR: CI_API_V4_URL is not an https GitLab API root — refusing to bootstrap the identity pin (fail-closed)" >&2
      unset _fs_api
      return 1
      ;;
  esac
  # The case glob above only requires an https://*/api/v4 shape: bash's
  # `*` matches any characters, including brace/bracket-range syntax
  # (e.g. https://{attacker.example,gitlab.example}/api/v4 or
  # https://gitlab[1-2].example/api/v4) that curl expands into multiple
  # requests by default, and userinfo (https://real-host@attacker/api/v4,
  # where curl actually connects to "attacker"). --globoff above closes
  # the expansion path; this check closes it at validation time too and
  # rejects userinfo and stray whitespace/quote/angle-bracket characters
  # that have no legitimate place in a GitLab API root.
  #
  # A bracketed IPv6 literal host (https://[2001:db8::1]/api/v4, or with
  # a :port) legitimately contains '[' and ']', so it is allow-listed
  # here before the general bracket ban below — --globoff already
  # neutralizes curl's own range-expansion behavior for it, so admitting
  # it does not reopen the fan-out vector the general ban closes.
  _fs_ipv6_literal=0
  _fs_host_and_port="${_fs_api#https://}"
  _fs_host_and_port="${_fs_host_and_port%/api/v4}"
  if [[ "${_fs_host_and_port}" =~ ^\[[0-9A-Fa-f:]+\](:[0-9]+)?$ ]]; then
    _fs_ipv6_literal=1
  fi
  unset _fs_host_and_port
  if [ "${_fs_ipv6_literal}" -ne 1 ]; then
    case "${_fs_api}" in
      *[\{\}\[\]\<\>\"\'@]*|*[$' \t\n\r']*)
        echo "ERROR: CI_API_V4_URL contains characters not permitted in a GitLab API root (braces, brackets, angle brackets, quotes, '@', or whitespace) — refusing to bootstrap the identity pin (fail-closed)" >&2
        unset _fs_api _fs_ipv6_literal
        return 1
        ;;
    esac
  fi
  unset _fs_ipv6_literal

  # Force a fresh CA-trust pass so a trigger-supplied CURL_CA_BUNDLE
  # or FULLSEND_CI_SERVER_CA_TRUSTED=1 cannot stick from job init.
  unset _fullsend_trust_ci_server_ca_done
  unset FULLSEND_CI_SERVER_CA_TRUSTED
  unset SSL_CERT_FILE GIT_SSL_CAINFO CURL_CA_BUNDLE REQUESTS_CA_BUNDLE NODE_EXTRA_CA_CERTS
  unset SSL_CERT_DIR SSLKEYLOGFILE
  _fs_trust="${CI_PROJECT_DIR:-.}/.gitlab/ci/scripts/trust-ci-server-ca.sh"
  if [ ! -f "${_fs_trust}" ]; then
    echo "ERROR: ${_fs_trust} is missing — cannot pin transport for the identity gate (fail-closed)" >&2
    unset _fs_api _fs_trust
    return 1
  fi
  # shellcheck disable=SC1090
  . "${_fs_trust}"

  _fs_job_json=""
  if ! _fs_job_json=$(fullsend_gate_curl \
    -H "JOB-TOKEN: ${CI_JOB_TOKEN}" \
    "${_fs_api}/job"); then
    echo "ERROR: CI_JOB_TOKEN job lookup failed — aborting (fail-closed)" >&2
    unset _fs_api _fs_trust _fs_job_json
    return 1
  fi

  FULLSEND_PINNED_PROJECT_ID=$(printf '%s' "${_fs_job_json}" | jq -r '.pipeline.project_id // empty')
  FULLSEND_PINNED_PIPELINE_ID=$(printf '%s' "${_fs_job_json}" | jq -r '.pipeline.id // empty')
  FULLSEND_PINNED_REF=$(printf '%s' "${_fs_job_json}" | jq -r '.ref // .pipeline.ref // empty')
  # The user the running job (and so CI_JOB_TOKEN) authenticates as. A retry
  # keeps the pipeline creator but runs as the retrier, so route selection
  # compares this, not the pipeline creator, with the trigger account.
  FULLSEND_PINNED_JOB_USER_ID=$(printf '%s' "${_fs_job_json}" | jq -r '.user.id // empty' 2>/dev/null || true)
  unset _fs_job_json

  case "${FULLSEND_PINNED_PROJECT_ID}" in
    ''|*[!0-9]*)
      echo "ERROR: CI_JOB_TOKEN job record is missing a numeric pipeline.project_id — aborting (fail-closed)" >&2
      unset _fs_api _fs_trust FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF
      return 1
      ;;
  esac
  case "${FULLSEND_PINNED_PIPELINE_ID}" in
    ''|*[!0-9]*)
      echo "ERROR: CI_JOB_TOKEN job record is missing a numeric pipeline.id — aborting (fail-closed)" >&2
      unset _fs_api _fs_trust FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF
      return 1
      ;;
  esac
  case "${FULLSEND_PINNED_REF}" in
    ''|*[$'\n\r']*|*'..'*|*' '*|*$'\t'*)
      echo "ERROR: CI_JOB_TOKEN job record ref is empty or unsafe — aborting (fail-closed)" >&2
      unset _fs_api _fs_trust FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF
      return 1
      ;;
  esac
  FULLSEND_PINNED_REF="${FULLSEND_PINNED_REF#refs/heads/}"

  _fs_project_json=""
  if ! _fs_project_json=$(fullsend_gate_curl \
    -H "JOB-TOKEN: ${CI_JOB_TOKEN}" \
    "${_fs_api}/projects/${FULLSEND_PINNED_PROJECT_ID}"); then
    # Some GitLab versions (observed: self-hosted EE 19.2.7, #7965) 404 on
    # this call with JOB-TOKEN — GitLab's own lib/api/projects.rb does not
    # mark GET /projects/:id job_token_allowed, unlike the /job call above
    # and the /pipelines/:id call below, both of which work with JOB-TOKEN.
    # Fall back to the already-provisioned Poller-role PAT for this same
    # read: FULLSEND_PINNED_PROJECT_ID is already pinned from the trusted
    # GET /job JOB-TOKEN response, so this only supplies an alternate
    # source for project detail the checks below still fully enforce.
    _fs_project_json=""
    _fs_fallback_ok=1
    if [ -n "${FULLSEND_GITLAB_POLLER_TOKEN:-}" ]; then
      if _fs_project_json=$(fullsend_gate_curl \
        -H "PRIVATE-TOKEN: ${FULLSEND_GITLAB_POLLER_TOKEN}" \
        "${_fs_api}/projects/${FULLSEND_PINNED_PROJECT_ID}"); then
        _fs_fallback_ok=0
      fi
    fi
    if [ "${_fs_fallback_ok}" -ne 0 ]; then
      echo "ERROR: cannot read pinned project ${FULLSEND_PINNED_PROJECT_ID} via JOB-TOKEN or the Poller-role PAT fallback — aborting (fail-closed)" >&2
      unset _fs_api _fs_trust _fs_project_json _fs_fallback_ok FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF
      return 1
    fi
    unset _fs_fallback_ok
  fi
  _fs_default_branch=$(printf '%s' "${_fs_project_json}" | jq -r '.default_branch // empty')
  FULLSEND_PINNED_PROJECT_PATH=$(printf '%s' "${_fs_project_json}" | jq -r '.path_with_namespace // empty')
  unset _fs_project_json
  _fs_default_branch="${_fs_default_branch#refs/heads/}"
  if [ -z "${_fs_default_branch}" ]; then
    echo "ERROR: pinned project has no default_branch — aborting (fail-closed)" >&2
    unset _fs_api _fs_trust _fs_default_branch FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF FULLSEND_PINNED_PROJECT_PATH
    return 1
  fi
  if [ -z "${FULLSEND_PINNED_PROJECT_PATH}" ]; then
    echo "ERROR: pinned project has no path_with_namespace — aborting (fail-closed)" >&2
    unset _fs_api _fs_trust _fs_default_branch FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF FULLSEND_PINNED_PROJECT_PATH
    return 1
  fi
  if [ "${FULLSEND_PINNED_REF}" != "${_fs_default_branch}" ]; then
    echo "ERROR: job ref '${FULLSEND_PINNED_REF}' is not the enrolled default branch '${_fs_default_branch}' — refusing a protected-but-non-default ref (fail-closed)" >&2
    unset _fs_api _fs_trust _fs_default_branch FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF FULLSEND_PINNED_PROJECT_PATH
    return 1
  fi

  # GET /projects/:id/repository/branches/:branch is a same-project call
  # (FULLSEND_PINNED_PROJECT_ID is the project_id of the pipeline that owns
  # the supplied CI_JOB_TOKEN, from GET /job above) rather than a
  # cross-project one. GitLab's CI/CD job token *scope* allowlist restricts
  # only cross-project access; it has never gated a token's calls into its
  # own project. GitLab's fine-grained job-token permissions documentation
  # (introduced for cross-project grants, GA in 18.3) separately lists
  # "GET /projects/:id/repository/branches" under the read_repository
  # policy, the same family as this single-branch lookup. Both facts
  # support CI_JOB_TOKEN authenticating this same-project read on current
  # GitLab; however, on self-hosted EE 19.2.7 (#7965) this call 404s with
  # JOB-TOKEN too — "Project Not Found", the same response as the
  # project-detail call above, not a branch-specific rejection — so the
  # same Poller-role PAT fallback applies here.
  _fs_ref_enc=$(printf '%s' "${FULLSEND_PINNED_REF}" | jq -sRr @uri)
  _fs_branch_json=""
  if ! _fs_branch_json=$(fullsend_gate_curl \
    -H "JOB-TOKEN: ${CI_JOB_TOKEN}" \
    "${_fs_api}/projects/${FULLSEND_PINNED_PROJECT_ID}/repository/branches/${_fs_ref_enc}"); then
    _fs_branch_json=""
    _fs_fallback_ok=1
    if [ -n "${FULLSEND_GITLAB_POLLER_TOKEN:-}" ]; then
      if _fs_branch_json=$(fullsend_gate_curl \
        -H "PRIVATE-TOKEN: ${FULLSEND_GITLAB_POLLER_TOKEN}" \
        "${_fs_api}/projects/${FULLSEND_PINNED_PROJECT_ID}/repository/branches/${_fs_ref_enc}"); then
        _fs_fallback_ok=0
      fi
    fi
    if [ "${_fs_fallback_ok}" -ne 0 ]; then
      echo "ERROR: cannot read pinned branch '${FULLSEND_PINNED_REF}' via JOB-TOKEN or the Poller-role PAT fallback — aborting (fail-closed)" >&2
      unset _fs_api _fs_trust _fs_default_branch _fs_ref_enc _fs_branch_json _fs_fallback_ok FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF FULLSEND_PINNED_PROJECT_PATH
      return 1
    fi
    unset _fs_fallback_ok
  fi
  _fs_protected=$(printf '%s' "${_fs_branch_json}" | jq -r '.protected // false')
  unset _fs_branch_json _fs_ref_enc
  if [ "${_fs_protected}" != "true" ]; then
    echo "ERROR: enrolled default branch '${FULLSEND_PINNED_REF}' is not protected — aborting (fail-closed)" >&2
    unset _fs_api _fs_trust _fs_default_branch _fs_protected FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF FULLSEND_PINNED_PROJECT_PATH
    return 1
  fi
  unset _fs_default_branch _fs_protected

  FULLSEND_PINNED_PIPELINE_RESPONSE=""
  if ! FULLSEND_PINNED_PIPELINE_RESPONSE=$(fullsend_gate_curl \
    -H "JOB-TOKEN: ${CI_JOB_TOKEN}" \
    "${_fs_api}/projects/${FULLSEND_PINNED_PROJECT_ID}/pipelines/${FULLSEND_PINNED_PIPELINE_ID}"); then
    echo "ERROR: cannot read pinned pipeline ${FULLSEND_PINNED_PIPELINE_ID} — aborting (fail-closed)" >&2
    unset _fs_api _fs_trust FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF FULLSEND_PINNED_PIPELINE_RESPONSE FULLSEND_PINNED_PROJECT_PATH
    return 1
  fi
  FULLSEND_PINNED_PIPELINE_SOURCE=$(printf '%s' "${FULLSEND_PINNED_PIPELINE_RESPONSE}" | jq -r '.source // empty')
  _fs_source_admitted=false
  case "${FULLSEND_ADMIT_SOURCE}:${FULLSEND_PINNED_PIPELINE_SOURCE}" in
    "api pipeline:api"|"api pipeline:pipeline") _fs_source_admitted=true ;;
  esac
  if [ "${FULLSEND_PINNED_PIPELINE_SOURCE}" = "${FULLSEND_ADMIT_SOURCE}" ]; then
    _fs_source_admitted=true
  fi
  if [ "${_fs_source_admitted}" != "true" ]; then
    unset _fs_source_admitted
    echo "ERROR: pinned pipeline source '${FULLSEND_PINNED_PIPELINE_SOURCE:-<empty>}' is not '${FULLSEND_ADMIT_SOURCE}' — disjoint allowlist deny (fail-closed)" >&2
    unset _fs_api _fs_trust FULLSEND_PINNED_PROJECT_ID FULLSEND_PINNED_PIPELINE_ID FULLSEND_PINNED_JOB_USER_ID FULLSEND_PINNED_REF FULLSEND_PINNED_PIPELINE_SOURCE FULLSEND_PINNED_PIPELINE_RESPONSE FULLSEND_PINNED_PROJECT_PATH
    return 1
  fi
  unset _fs_source_admitted

  export FULLSEND_PINNED_PROJECT_ID
  export FULLSEND_PINNED_PIPELINE_ID
  export FULLSEND_PINNED_REF
  export FULLSEND_PINNED_JOB_USER_ID
  export FULLSEND_PINNED_PIPELINE_SOURCE
  export FULLSEND_PINNED_PIPELINE_RESPONSE
  export FULLSEND_PINNED_PROJECT_PATH
  FULLSEND_PINNED_GITLAB_URL="${_fs_api%/api/v4}"
  export FULLSEND_PINNED_GITLAB_URL
  unset _fs_api _fs_trust

  # fullsend_gate_curl only strips proxy env vars for its own curl
  # invocations (env -u ...); it does not remove them from this shell.
  # The subsequent fullsend poll/run Go processes inherit this shell's
  # exported environment and build their http.Client with a nil
  # Transport (ProxyFromEnvironment), so a trigger-influenced
  # HTTP_PROXY/HTTPS_PROXY left in the parent shell could still
  # CONNECT-proxy PAT-bearing API traffic after the pin succeeds. Unset
  # them here (the names Go's ProxyFromEnvironment honors) so later
  # CLI calls in this job cannot be steered by a trigger-supplied proxy.
  # ALL_PROXY/all_proxy is included too: git (used for post-pin,
  # credential-bearing fetches elsewhere in the scaffold) honors it as
  # a fallback, matching the set fullsend_gate_curl already strips.
  unset HTTP_PROXY HTTPS_PROXY http_proxy https_proxy ALL_PROXY all_proxy
}

# fullsend_gitlab_trigger_account_id prints the numeric user ID of the
# trigger-only service account (#8437) recorded in the protected
# FULLSEND_GITLAB_TRIGGER_ACCOUNT ledger, or nothing. Only a ledger in phase
# "ready" names a usable account: while an account is bootstrapping,
# rotating, or being removed, no pipeline it creates is trusted. It also
# requires the FULLSEND_GITLAB_DISPATCH_ACTIVATION record, which
# `fullsend repos install` writes only after the full activation gate
# (schedules migrated, override role no_one_allowed, job-token push disabled,
# no unrelated job in trigger or agent child pipelines, account ready) passes
# and deletes on any later lifecycle run, so a ready ledger alone never selects
# or admits the child-pipeline route (#8438). The ledger
# is a protected CI/CD variable written only by `fullsend repos install`. The
# trigger account holds a live trigger credential only while
# ci_pipeline_variables_minimum_override_role is no_one_allowed (the install
# lifecycle defers otherwise and contains a published token when the setting
# weakens), so a pipeline it creates cannot carry a variable that overrides
# this ledger.
fullsend_gitlab_trigger_account_id() {
  # The ledger and activation are passed as raw strings and parsed with
  # `fromjson` (try/catch to null): a value that is not exactly one JSON
  # document fails to parse, yields null, and prints no account ID, matching
  # the Go decoder (which rejects trailing garbage and multiple documents).
  #
  # The activation is a separate record (FULLSEND_GITLAB_DISPATCH_ACTIVATION),
  # not a ledger field: older CLIs reject unknown ledger fields before they
  # read its version. It is honored only while it names exactly the ledger's
  # current generation (nonce, account, deploy key, grant, trigger token), so
  # an older CLI that rotates, repairs or removes the account leaves it stale
  # and the Poller route stays selected.
  jq -nr \
    --arg ledger "${FULLSEND_GITLAB_TRIGGER_ACCOUNT:-}" \
    --arg activation "${FULLSEND_GITLAB_DISPATCH_ACTIVATION:-}" '
    ($ledger | try fromjson catch null) as $l
    | ($activation | try fromjson catch null) as $a
    | if ($l | type) == "object" and ($a | type) == "object"
        and $l.version == 1
        and $l.phase == "ready"
        and ($l.account_id | type) == "number" and $l.account_id > 0
        and ($l.trigger_token_id | type) == "number" and $l.trigger_token_id > 0
        and $l.trigger_published == true
        and ($l.nonce | type) == "string" and ($l.nonce | length) > 0
        and $a.version == 1
        and $a.nonce == $l.nonce
        and $a.account_id == $l.account_id
        and ($a.deploy_key_id // 0) == ($l.deploy_key_id // 0)
        and ($a.grant_entry_id // 0) == ($l.grant_entry_id // 0)
        and $a.trigger_token_id == $l.trigger_token_id
      then $l.account_id | tostring else empty end' 2>/dev/null \
    || true
}

# fullsend_cli_supports_dispatch_route succeeds only when the installed
# fullsend CLI reports the given dispatch route. A CLI that predates
# FULLSEND_GITLAB_DISPATCH_ROUTE rejects --print-dispatch-routes as an unknown
# flag, so the probe fails closed for it (#8438).
fullsend_cli_supports_dispatch_route() {
  local _fs_routes
  _fs_routes=$(fullsend poll --forge gitlab --fullsend-dir .fullsend --print-dispatch-routes 2>/dev/null) || return 1
  printf '%s\n' "${_fs_routes}" | grep -qx -- "$1"
}

# fullsend_pinned_pipeline_creator_id prints the user ID that created the
# pinned pipeline, or nothing. Some GitLab versions omit .user on the
# job-token pipeline GET; it is then re-read with FULLSEND_JOB_TOKEN when the
# caller already selected one, using the pinned IDs (never the overridable
# CI_PROJECT_ID / CI_PIPELINE_ID).
fullsend_pinned_pipeline_creator_id() {
  local _fs_creator _fs_response
  _fs_creator=$(printf '%s' "${FULLSEND_PINNED_PIPELINE_RESPONSE:-}" | jq -r '.user.id // empty' 2>/dev/null || true)
  if [ -z "${_fs_creator}" ] && [ -n "${FULLSEND_JOB_TOKEN:-}" ] && [ -n "${FULLSEND_PINNED_GITLAB_URL:-}" ]; then
    if _fs_response=$(fullsend_gate_curl \
      -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}" \
      "${FULLSEND_PINNED_GITLAB_URL}/api/v4/projects/${FULLSEND_PINNED_PROJECT_ID}/pipelines/${FULLSEND_PINNED_PIPELINE_ID}"); then
      _fs_creator=$(printf '%s' "${_fs_response}" | jq -r '.user.id // empty' 2>/dev/null || true)
    fi
  fi
  case "${_fs_creator}" in
    ''|*[!0-9]*) ;;
    *) printf '%s' "${_fs_creator}" ;;
  esac
}

# fullsend_select_gitlab_dispatch_route exports FULLSEND_GITLAB_DISPATCH_ROUTE
# for `fullsend poll` (#8438). A schedule or webhook pipeline created by the
# trigger-only account runs on the protected default branch through that
# account's deploy-key grant, and the job can then start the agent pipeline
# with its own CI_JOB_TOKEN ("child-pipeline", source=pipeline, attributed to
# the same account). Any other creator, an unreadable creator, or no ready
# trigger account keeps the Poller credential and the pipelines API ("api"),
# so existing polling continues unchanged while activation is deferred.
# Call it after select-gitlab-role-token.sh, so the creator can be re-read
# when the job-token pipeline GET omits it.
fullsend_select_gitlab_dispatch_route() {
  local _fs_account _fs_creator _fs_job_user
  FULLSEND_GITLAB_DISPATCH_ROUTE=api
  _fs_account=$(fullsend_gitlab_trigger_account_id)
  if [ -n "${_fs_account}" ]; then
    _fs_creator=$(fullsend_pinned_pipeline_creator_id)
    # CI_JOB_TOKEN acts as the running job's user, which differs from the
    # pipeline creator when the job was retried by someone else. Both must be
    # the trigger account, or the child pipeline would be created as another
    # user and rejected by the agent job after the dispatch is lost.
    case "${FULLSEND_PINNED_JOB_USER_ID:-}" in
      ''|*[!0-9]*) _fs_job_user="" ;;
      *) _fs_job_user="${FULLSEND_PINNED_JOB_USER_ID}" ;;
    esac
    if [ -n "${_fs_creator}" ] && [ "${_fs_creator}" = "${_fs_account}" ] &&
      [ "${_fs_job_user}" = "${_fs_account}" ]; then
      # This pipeline can only dispatch through CI_JOB_TOKEN: the trigger
      # account holds no Poller credential. A CLI that predates the route
      # selector would ignore it and fail late on the Poller route, after
      # the event was consumed, so refuse explicitly before any dispatch.
      if ! fullsend_cli_supports_dispatch_route child-pipeline; then
        echo "ERROR: the installed fullsend CLI does not support the FULLSEND_GITLAB_DISPATCH_ROUTE child-pipeline route required by this trigger-account pipeline; upgrade the CLI pinned in .gitlab/ci/scripts/install-fullsend-cli.sh (re-run 'fullsend repos install') before using this scaffold. Refusing to dispatch." >&2
        exit 1
      fi
      FULLSEND_GITLAB_DISPATCH_ROUTE=child-pipeline
    elif [ -n "${_fs_creator}" ] && [ "${_fs_creator}" = "${_fs_account}" ]; then
      echo "WARNING: Pipeline creator is the trigger account but the running job is not; keeping the api route" >&2
    fi
  fi
  export FULLSEND_GITLAB_DISPATCH_ROUTE
  echo "Agent dispatch route: ${FULLSEND_GITLAB_DISPATCH_ROUTE}"
}

# fullsend_resolve_gitlab_analyst_user_id exports
# FULLSEND_GITLAB_ANALYST_USER_ID, the numeric GitLab user ID of the
# registered Analyst identity, for `fullsend poll` in the poll and
# dispatcher jobs (#8807). The review stage posts its changes-requested
# marker as the Analyst, which in distinct-role installations is not the
# Poller whose credential those jobs hold, so the poller needs the Analyst
# ID to retain that marker for the fix stage. Only the numeric ID is
# exported: the Analyst credential is selected inside a subshell for a
# single GET /user call through fullsend_gate_curl and never replaces
# FULLSEND_JOB_TOKEN. Call it after select-gitlab-role-token.sh (so
# fullsend_select_gitlab_role_token is defined) and before the caller
# blanks sibling role secrets. Any inherited value is discarded first, and
# a failed lookup only warns: the poller then retains no marker, which is
# the safe outcome, instead of stopping unrelated dispatch.
fullsend_resolve_gitlab_analyst_user_id() {
  local _fs_resp _fs_id
  unset FULLSEND_GITLAB_ANALYST_USER_ID
  if ! _fs_resp=$(
    # shellcheck disable=SC2034  # consumed by fullsend_select_gitlab_role_token
    FULLSEND_JOB_KIND=agent
    # shellcheck disable=SC2034  # consumed by fullsend_select_gitlab_role_token
    FULLSEND_JOB_AGENT=analyst
    fullsend_select_gitlab_role_token || exit 1
    fullsend_gate_curl "${FULLSEND_PINNED_GITLAB_URL}/api/v4/user" \
      -H "PRIVATE-TOKEN: ${FULLSEND_JOB_TOKEN}"
  ); then
    echo "WARNING: could not resolve the Analyst identity; changes-requested markers will not trigger the fix stage in this job" >&2
    return 0
  fi
  _fs_id=$(printf '%s' "${_fs_resp}" | jq -r '.id // empty' 2>/dev/null || true)
  case "${_fs_id}" in
    ''|0|*[!0-9]*)
      echo "WARNING: GitLab returned no valid user ID for the Analyst identity; changes-requested markers will not trigger the fix stage in this job" >&2
      return 0
      ;;
  esac
  export FULLSEND_GITLAB_ANALYST_USER_ID="${_fs_id}"
}

fullsend_pin_ci_job_identity
