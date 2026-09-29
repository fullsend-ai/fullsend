#!/usr/bin/env bash
# checkout-mr-source.sh — Fetch and check out the MR source revision.
#
# Source this file from run-agent-job.sh (do not execute it) so
# FIX_TARGET_REPO is exported for --target-repo. Dispatch pipelines
# run from the repository default branch; the fix sandbox must see
# the exact MR head that the review examined, not origin/main.
#
# The source revision is checked out into $CI_PROJECT_DIR/target-repo
# so the runner's default-branch working tree (including trusted
# .fullsend/ config) stays authoritative. Fetching a branch without
# updating that working tree is not enough.
#
# Fail closed: missing MR identity, missing source branch/SHA, fetch
# failures, and HEAD/SHA mismatches abort the job.
#
# Merge-request identity (source branch, SHA, and project) is resolved
# through `fullsend resolve-mr-source` (internal/cli/resolvemrsource.go),
# not a direct curl call against CI_API_V4_URL: the forge lookup goes
# through the same forge.Client abstraction (internal/forge/gitlab) the
# rest of fullsend uses, instead of adding another hand-rolled forge API
# path to this generated scaffold.
#
# Same-project MRs only: `resolve-mr-source` fails closed when the
# merge request's source lives in a different project (a fork), and
# this script re-checks the resolved project path against
# CI_PROJECT_PATH before fetching. fullsend has no supported way to
# check out — or push a fix commit back to — a fork source today: that
# would need push-capable credentials scoped to a project the
# target-project token cannot write to. run-agent-job.sh's fork gate
# already refuses the fix/code stages whenever IS_FORK is true
# (internal/poll/dispatch.go sets IS_FORK from source-project !=
# target-project), so this script's same-project check is defense in
# depth, not the primary control.

fullsend_validate_mr_source_ref() {
  _fs_kind=$1
  _fs_name=$2
  case "${_fs_name}" in
    ""|-*|/*|*..*|*:|*[[:space:]]*|*"*"*|*"?"*|*"["*|*"@{"*)
      echo "ERROR: invalid MR ${_fs_kind} '${_fs_name}'" >&2
      unset _fs_kind _fs_name
      return 1
      ;;
  esac
  unset _fs_kind _fs_name
  return 0
}

fullsend_validate_mr_source_sha() {
  _fs_sha=$1
  _fs_len=${#_fs_sha}
  case "${_fs_sha}" in
    *[!0-9a-fA-F]*)
      echo "ERROR: invalid MR source SHA '${_fs_sha}'" >&2
      unset _fs_sha _fs_len
      return 1
      ;;
  esac
  # 7 is the shortest usable abbreviation; 64 covers full SHA-256
  # object names (GitLab repositories can be configured to use
  # SHA-256 instead of SHA-1's 40 hex chars) — matching the 7-64
  # range already accepted for head-SHA parsing in run-agent-job.sh.
  if [ "${_fs_len}" -lt 7 ] || [ "${_fs_len}" -gt 64 ]; then
    echo "ERROR: invalid MR source SHA '${_fs_sha}'" >&2
    unset _fs_sha _fs_len
    return 1
  fi
  unset _fs_sha _fs_len
  return 0
}

fullsend_cleanup_mr_source_fetch() {
  if [ -n "${_FS_CRED_HELPER:-}" ]; then
    rm -f "${_FS_CRED_HELPER}"
  fi
  unset _FS_CRED_HELPER _FS_GIT_PASSWORD _FS_CRED_HOST
}

fullsend_git_fetch_mr_source() {
  _fs_refspec=$1
  # Ordinary, overridable CI/CD variables (GIT_CURL_VERBOSE, GIT_TRACE,
  # GIT_TRACE_PACKET, GIT_TRACE2*, GIT_TRACE_SETUP) are the same threat
  # class as CI_MERGE_REQUEST_SOURCE_* elsewhere in this script: with
  # GIT_TRACE_REDACT=false and GIT_CURL_VERBOSE set, git prints the HTTP
  # Basic Authorization header (base64 of oauth2:<token>) to job output,
  # which would not match GitLab's raw-token log masking. Force these
  # off for every fetch this helper performs, credentialed or not,
  # regardless of what the ambient environment sets. GIT_SSL_NO_VERIFY,
  # GIT_ASKPASS, and SSLKEYLOGFILE are the same threat class: an
  # ordinary project/group CI/CD variable setting GIT_SSL_NO_VERIFY=true
  # would disable TLS verification for the credentialed fetch, letting
  # an on-path proxy read that same Authorization header; force those
  # off and pin http.sslVerify=true too. GIT_SSL_CAINFO is left alone —
  # trust-ci-server-ca.sh sets it legitimately for private CAs.
  if [ -z "${_FS_CRED_HELPER:-}" ]; then
    GIT_TERMINAL_PROMPT=0 \
      GIT_CURL_VERBOSE=0 GIT_TRACE=0 GIT_TRACE_PACKET=0 GIT_TRACE2=0 \
      GIT_TRACE2_EVENT=0 GIT_TRACE_SETUP=0 GIT_TRACE_REDACT=true \
      GIT_SSL_NO_VERIFY='' GIT_ASKPASS='' SSLKEYLOGFILE='' \
      git -C "${FIX_TARGET_REPO}" -c http.sslVerify=true fetch --no-tags --prune -- \
      "${_FS_SOURCE_FETCH_URL}" "${_fs_refspec}"
  else
    _FS_GIT_PASSWORD="${FULLSEND_JOB_TOKEN}" _FS_CRED_HOST="${_FS_CRED_HOST}" GIT_TERMINAL_PROMPT=0 \
      GIT_CURL_VERBOSE=0 GIT_TRACE=0 GIT_TRACE_PACKET=0 GIT_TRACE2=0 \
      GIT_TRACE2_EVENT=0 GIT_TRACE_SETUP=0 GIT_TRACE_REDACT=true \
      GIT_SSL_NO_VERIFY='' GIT_ASKPASS='' SSLKEYLOGFILE='' \
      git -C "${FIX_TARGET_REPO}" \
      -c credential.helper= \
      -c "credential.helper=${_FS_CRED_HELPER}" \
      -c http.followRedirects=false \
      -c http.sslVerify=true \
      fetch --no-tags --prune -- "${_FS_SOURCE_FETCH_URL}" "${_fs_refspec}"
  fi
}

fullsend_checkout_mr_source() {
  if [ "${CI_DEBUG_TRACE:-}" = "true" ]; then
    echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets" >&2
    return 1
  fi

  case "${MR_IID:-}" in
    ''|0|*[!0-9]*)
      echo "ERROR: fix stage requires a numeric merge request IID to check out the source revision" >&2
      return 1
      ;;
  esac

  if [ -z "${CI_PROJECT_DIR:-}" ]; then
    echo "ERROR: CI_PROJECT_DIR is required to check out the MR source revision" >&2
    return 1
  fi

  case "${CI_PROJECT_ID:-}" in
    ''|*[!0-9]*)
      echo "ERROR: CI_PROJECT_ID is required to resolve merge request !${MR_IID}" >&2
      return 1
      ;;
  esac
  if [ -z "${CI_PROJECT_PATH:-}" ]; then
    echo "ERROR: CI_PROJECT_PATH is required to resolve merge request !${MR_IID}" >&2
    return 1
  fi
  if [ -z "${FULLSEND_JOB_TOKEN:-}" ]; then
    echo "ERROR: cannot resolve MR source revision — FULLSEND_JOB_TOKEN is required" >&2
    return 1
  fi

  _FS_SERVER_URL="${CI_SERVER_URL:-}"
  _FS_SERVER_URL="${_FS_SERVER_URL%/}"
  if [ -z "${_FS_SERVER_URL}" ]; then
    echo "ERROR: CI_SERVER_URL is required to fetch the MR source revision" >&2
    return 1
  fi

  # CI_MERGE_REQUEST_SOURCE_* are GitLab-predefined variables on native
  # MR pipelines, but they are not part of the HMAC-signed dispatch
  # message: an ordinary project/group CI/CD variable defined with one
  # of these exact names would otherwise be trusted without verification.
  # Treat them as an optional fast path only — `fullsend resolve-mr-source`
  # is always invoked and is the sole source of truth for the branch and
  # SHA; a fast-path value that disagrees with it fails closed instead of
  # being trusted. The two project-identity fast-path variables are
  # cross-checked directly against CI_PROJECT_ID/CI_PROJECT_PATH (both
  # trustworthy, GitLab-set values for this job) rather than against a
  # second API call, because this helper only supports same-project MRs
  # (see the module comment above).
  _FS_FASTPATH_BRANCH="${CI_MERGE_REQUEST_SOURCE_BRANCH_NAME:-}"
  _FS_FASTPATH_SHA="${CI_MERGE_REQUEST_SOURCE_BRANCH_SHA:-}"
  _FS_FASTPATH_PROJECT_ID="${CI_MERGE_REQUEST_SOURCE_PROJECT_ID:-}"
  _FS_FASTPATH_PROJECT_PATH="${CI_MERGE_REQUEST_SOURCE_PROJECT_PATH:-}"

  if [ -n "${_FS_FASTPATH_PROJECT_ID}" ] && [ "${_FS_FASTPATH_PROJECT_ID}" != "${CI_PROJECT_ID}" ]; then
    echo "ERROR: CI_MERGE_REQUEST_SOURCE_PROJECT_ID '${_FS_FASTPATH_PROJECT_ID}' does not match this project's CI_PROJECT_ID '${CI_PROJECT_ID}' — cross-project MR source checkout is not supported" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  if [ -n "${_FS_FASTPATH_PROJECT_PATH}" ] && [ "${_FS_FASTPATH_PROJECT_PATH}" != "${CI_PROJECT_PATH}" ]; then
    echo "ERROR: CI_MERGE_REQUEST_SOURCE_PROJECT_PATH '${_FS_FASTPATH_PROJECT_PATH}' does not match this project's CI_PROJECT_PATH '${CI_PROJECT_PATH}' — cross-project MR source checkout is not supported" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi

  # GITLAB_TOKEN is already exported (from FULLSEND_JOB_TOKEN) before
  # this script is sourced; resolve-mr-source falls back to it via
  # resolveGitLabToken() when --token is omitted. Passing the token as
  # an argv flag would additionally expose it via /proc/<pid>/cmdline,
  # `ps`, and execve audit logs.
  if ! _FS_RESOLVED=$(fullsend resolve-mr-source \
    --project "${CI_PROJECT_PATH}" \
    --mr-iid "${MR_IID}" \
    --gitlab-url "${_FS_SERVER_URL}"); then
    echo "ERROR: cannot resolve merge request !${MR_IID} — refusing to run the fix agent without the source revision" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi

  SOURCE_BRANCH=$(printf '%s' "${_FS_RESOLVED}" | jq -r '.source_branch // empty')
  SOURCE_SHA=$(printf '%s' "${_FS_RESOLVED}" | jq -r '.source_sha // empty')
  SOURCE_PROJECT_PATH=$(printf '%s' "${_FS_RESOLVED}" | jq -r '.source_project_path // empty')
  unset _FS_RESOLVED

  if [ -z "${SOURCE_BRANCH}" ]; then
    echo "ERROR: merge request !${MR_IID} has no source branch — refusing to run the fix agent" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  if [ -z "${SOURCE_SHA}" ]; then
    echo "ERROR: merge request !${MR_IID} has no source SHA — refusing to run the fix agent" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  if [ -z "${SOURCE_PROJECT_PATH}" ]; then
    echo "ERROR: cannot resolve the source project path for merge request !${MR_IID}" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi

  if ! fullsend_validate_mr_source_ref "source branch" "${SOURCE_BRANCH}"; then
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  if ! fullsend_validate_mr_source_sha "${SOURCE_SHA}"; then
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi

  # Same-project MRs only (see module comment above): resolve-mr-source
  # already fails closed when the merge request's source lives in a
  # different project, but re-checking here means a bug in that
  # resolver can never silently widen this script to fetch from — or
  # construct forge credentials for — an unexpected remote.
  if [ "${SOURCE_PROJECT_PATH}" != "${CI_PROJECT_PATH}" ]; then
    echo "ERROR: resolved source project '${SOURCE_PROJECT_PATH}' does not match this project '${CI_PROJECT_PATH}' — cross-project MR source checkout is not supported" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi

  if [ -n "${_FS_FASTPATH_BRANCH}" ] && [ "${_FS_FASTPATH_BRANCH}" != "${SOURCE_BRANCH}" ]; then
    echo "ERROR: CI_MERGE_REQUEST_SOURCE_BRANCH_NAME '${_FS_FASTPATH_BRANCH}' does not match merge request !${MR_IID} source branch '${SOURCE_BRANCH}' — refusing to trust unverified CI variables" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  if [ -n "${_FS_FASTPATH_SHA}" ] && [ "${_FS_FASTPATH_SHA}" != "${SOURCE_SHA}" ]; then
    echo "ERROR: CI_MERGE_REQUEST_SOURCE_BRANCH_SHA '${_FS_FASTPATH_SHA}' does not match merge request !${MR_IID} source SHA '${SOURCE_SHA}' — refusing to trust unverified CI variables" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH

  _FS_SOURCE_FETCH_URL="${_FS_SERVER_URL}/${SOURCE_PROJECT_PATH}.git"
  unset _FS_SERVER_URL

  FIX_TARGET_REPO="${CI_PROJECT_DIR}/target-repo"
  if [ -e "${FIX_TARGET_REPO}" ]; then
    rm -rf "${FIX_TARGET_REPO}"
  fi
  mkdir -p "${FIX_TARGET_REPO}"
  git init --quiet "${FIX_TARGET_REPO}"
  git -C "${FIX_TARGET_REPO}" remote add origin "${_FS_SOURCE_FETCH_URL}"

  _FS_CRED_HELPER=""
  case "${_FS_SOURCE_FETCH_URL}" in
    file://*|/*)
      ;;
    *)
      # Host the credential helper is allowed to answer for. Passed to
      # the helper via the _FS_CRED_HOST environment variable at fetch
      # invocation time — the same way _FS_GIT_PASSWORD is passed —
      # rather than interpolated into the generated script's source, so
      # a shell metacharacter (e.g. a stray quote) in CI_SERVER_URL
      # cannot break out of the helper's source text and execute.
      _FS_CRED_HOST="${_FS_SOURCE_FETCH_URL#*://}"
      _FS_CRED_HOST="${_FS_CRED_HOST#*@}"
      _FS_CRED_HOST="${_FS_CRED_HOST%%/*}"
      _FS_CRED_HELPER=$(mktemp)
      chmod 700 "${_FS_CRED_HELPER}"
      cat > "${_FS_CRED_HELPER}" <<'EOF'
#!/bin/sh
if [ "$1" != "get" ]; then
  exit 0
fi
_fs_protocol=""
_fs_host=""
while IFS='=' read -r _fs_key _fs_value; do
  [ -z "${_fs_key}" ] && break
  case "${_fs_key}" in
    protocol) _fs_protocol=${_fs_value} ;;
    host) _fs_host=${_fs_value} ;;
  esac
done
if [ "${_fs_protocol}" = "https" ] && [ "${_fs_host}" = "${_FS_CRED_HOST}" ]; then
  printf 'username=oauth2\npassword=%s\n' "${_FS_GIT_PASSWORD}"
fi
EOF
      ;;
  esac

  echo "Fetching MR source ${SOURCE_PROJECT_PATH}:${SOURCE_BRANCH} (${SOURCE_SHA}) into ${FIX_TARGET_REPO}"
  if ! fullsend_git_fetch_mr_source "+refs/heads/${SOURCE_BRANCH}:refs/heads/${SOURCE_BRANCH}"; then
    echo "ERROR: failed to fetch MR source branch '${SOURCE_BRANCH}' from ${SOURCE_PROJECT_PATH}" >&2
    fullsend_cleanup_mr_source_fetch
    return 1
  fi
  if ! git -C "${FIX_TARGET_REPO}" cat-file -e "${SOURCE_SHA}^{commit}" 2>/dev/null; then
    if ! fullsend_git_fetch_mr_source "${SOURCE_SHA}"; then
      echo "ERROR: failed to fetch MR source SHA ${SOURCE_SHA} from ${SOURCE_PROJECT_PATH}" >&2
      fullsend_cleanup_mr_source_fetch
      return 1
    fi
  fi
  fullsend_cleanup_mr_source_fetch
  unset _FS_SOURCE_FETCH_URL

  if ! git -C "${FIX_TARGET_REPO}" checkout --force -B "${SOURCE_BRANCH}" "${SOURCE_SHA}"; then
    echo "ERROR: failed to check out MR source SHA ${SOURCE_SHA} on branch '${SOURCE_BRANCH}'" >&2
    return 1
  fi

  _FS_HEAD=$(git -C "${FIX_TARGET_REPO}" rev-parse HEAD)
  _FS_EXPECTED=$(git -C "${FIX_TARGET_REPO}" rev-parse --verify "${SOURCE_SHA}^{commit}")
  if [ "${_FS_HEAD}" != "${_FS_EXPECTED}" ]; then
    echo "ERROR: checked-out HEAD ${_FS_HEAD} does not match MR source SHA ${_FS_EXPECTED}" >&2
    unset _FS_HEAD _FS_EXPECTED
    return 1
  fi
  SOURCE_SHA="${_FS_EXPECTED}"
  unset _FS_HEAD _FS_EXPECTED

  echo "Checked out MR source ${SOURCE_SHA} on branch ${SOURCE_BRANCH}"
  export SOURCE_BRANCH SOURCE_SHA SOURCE_PROJECT_PATH FIX_TARGET_REPO
}

fullsend_checkout_mr_source
