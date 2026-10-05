#!/usr/bin/env bash
# checkout-mr-source.sh — Fetch and check out the MR source revision.
#
# Source this file from run-agent-job.sh (do not execute it) so
# FIX_TARGET_REPO is exported for --target-repo. Dispatch pipelines
# run from the repository default branch; the fix sandbox must see
# the exact MR head that the review examined, not origin/main.
#
# This pre-script owns GitLab source preparation: it resolves the MR
# source project, source branch, and head SHA with runner-side
# credentials, fetches that exact revision, and checks it out before
# the sandbox is created. The sandbox and the model must not fetch
# GitLab sources, discover MR metadata, or handle credentials.
#
# The source revision is checked out into $CI_PROJECT_DIR/target-repo
# so the runner's default-branch working tree (including trusted
# .fullsend/ config) stays authoritative. Fetching a branch without
# updating that working tree is not enough.
#
# Same-project, fork, and cross-project MR sources are fetched from
# the resolved source project path, which may differ from
# FULLSEND_PINNED_PROJECT_PATH. The fetch URL is FULLSEND_PINNED_GITLAB_URL
# plus that path; credentials stay on the runner and are never passed
# into the sandbox.
#
# Fail closed: missing MR identity, missing source project/branch/SHA,
# invalid source identity, fetch failures, HEAD/SHA mismatches, and
# unverified CI variable disagreement abort the job. The target
# project's default branch is never used as a substitute for missing
# source metadata.
#
# Merge-request identity (source branch, SHA, and project) is resolved
# through `fullsend resolve-mr-source` (internal/cli/resolvemrsource.go),
# not a direct curl call against CI_API_V4_URL: the forge lookup goes
# through the same forge.Client abstraction (internal/forge/gitlab) the
# rest of fullsend uses, instead of adding another hand-rolled forge API
# path to this generated scaffold.
#
# The --project and --gitlab-url passed to that resolve call are
# FULLSEND_PINNED_PROJECT_PATH / FULLSEND_PINNED_GITLAB_URL (exported by
# pin-ci-job-identity.sh, verified via the CI_JOB_TOKEN job record), not
# the overridable CI_PROJECT_PATH / CI_SERVER_URL pipeline variables —
# same outrankable class as CI_PROJECT_ID (ADR 0125). An authenticated
# dispatch that could override CI_PROJECT_PATH or CI_SERVER_URL must not
# be able to redirect the MR lookup to a different project or a
# different GitLab endpoint; the pinned project is always the one whose
# IID namespace MR_IID resolves in (a fork/cross-project MR is still
# opened against the target project), and the resolved
# source_project_path is what may legitimately differ for a fork. This
# script always runs after run-agent-job.sh sources
# pin-ci-job-identity.sh, so the pinned variables are always set.
#
# CI_MERGE_REQUEST_SOURCE_* are an optional consistency check only —
# resolve-mr-source is the sole source of truth. A fast-path value
# that disagrees with the resolved source fails closed instead of
# being trusted. This helper is fork-aware and validates the source
# project, branch, and exact SHA for any resolved source, and also
# runs a pre-push safety gate (`fullsend check-protected-branch`,
# below) against that resolved source — but run-agent-job.sh's
# job-level IS_FORK gate (unchanged by this PR) still refuses the fix
# stage outright whenever IS_FORK is true, so a genuine fork or
# cross-project MR never reaches this checkout logic today. That gate
# is expected to be lifted in a follow-up PR (#7814) once post-script
# push-back also ships to the validated source project.
#
# This script also owns the pre-push safety gate: once the source is
# resolved and validated above, `fullsend check-protected-branch` fails
# the job closed if SOURCE_BRANCH is protected in SOURCE_PROJECT_PATH
# (an exact-name rule or a matching wildcard rule), before any fetch or
# agent run — a fix commit must never be attempted against a protected
# branch in the source project, fork or not.

fullsend_validate_mr_source_ref() {
  _fs_kind=$1
  _fs_name=$2
  # A third "extended" argument selects git's own ref-naming rules for
  # branch names instead of the plain allowlist below. A hand-rolled
  # character allowlist (even one that adds "+", "=", and "@") keeps
  # rejecting legal git branch names — e.g. issue-number punctuation
  # like "feature/#123", "!" as in "feature/abc!", or non-ASCII
  # characters as in "fix/naïve" — that `git check-ref-format` accepts.
  # Delegating to `git check-ref-format` with the name quoted as a
  # single argument (so it is never split or glob-expanded) validates
  # against the real git-check-ref-format(1) rules instead of an
  # approximation of them. check-ref-format alone does not reject a
  # leading "-", since that is a shell/argument-injection concern, not
  # a ref-format rule, so a leading "-" is still rejected explicitly.
  # Source project paths (path_with_namespace) are GitLab identifiers,
  # not git refs: GitLab never allows "+", "=", or "@" there, and this
  # validator's caller also uses it to keep the credentialed fetch URL
  # construction narrower than GitLab's own parser — widening it for
  # paths would only reopen URL/userinfo syntax smuggling (see
  # TestCheckoutMRSource_InvalidSourceProjectPathFailsClosed), not fix
  # a real rejection. Only branch validation passes "extended".
  if [ "${3:-}" = "extended" ]; then
    case "${_fs_name}" in
      ""|-*)
        echo "ERROR: invalid MR ${_fs_kind} '${_fs_name}'" >&2
        unset _fs_kind _fs_name
        return 1
        ;;
    esac
    if ! git check-ref-format "refs/heads/${_fs_name}" >/dev/null 2>&1; then
      echo "ERROR: invalid MR ${_fs_kind} '${_fs_name}'" >&2
      unset _fs_kind _fs_name
      return 1
    fi
  else
    case "${_fs_name}" in
      ""|-*|/*|*..*|*[!A-Za-z0-9._/-]*)
        echo "ERROR: invalid MR ${_fs_kind} '${_fs_name}'" >&2
        unset _fs_kind _fs_name
        return 1
        ;;
    esac
  fi
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

fullsend_validate_mr_source_project_path() {
  _fs_path=$1
  if ! fullsend_validate_mr_source_ref "source project path" "${_fs_path}"; then
    unset _fs_path
    return 1
  fi
  case "${_fs_path}" in
    /*|*/|*//*)
      echo "ERROR: invalid MR source project path '${_fs_path}'" >&2
      unset _fs_path
      return 1
      ;;
  esac
  case "${_fs_path}" in
    */*) ;;
    *)
      echo "ERROR: invalid MR source project path '${_fs_path}'" >&2
      unset _fs_path
      return 1
      ;;
  esac
  unset _fs_path
  return 0
}

fullsend_cleanup_mr_source_fetch() {
  if [ -n "${_FS_CRED_HELPER:-}" ]; then
    rm -f "${_FS_CRED_HELPER}"
  fi
  unset _FS_CRED_HELPER _FS_GIT_PASSWORD _FS_CRED_HOST
}

# fullsend_reset_target_repo_git_config overwrites FIX_TARGET_REPO/.git/config
# with a minimal, known-good config immediately after `git init`: a
# CI/CD-supplied GIT_TEMPLATE_DIR (or a runner-level system template) can seed a fresh
# `git init` with a template-provided "config" file — git copies template
# directory contents into .git/ verbatim, "config" included, and the
# resulting repo-local http.proxy/url.*.insteadOf/http.<url>.sslVerify/
# credential.<url>.helper keys take precedence over the "-c" overrides
# already passed to every git invocation below, the same way a poisoned
# repo-local config would. Unsetting GIT_TEMPLATE_DIR and pointing
# `git init --template=` at a known-empty directory (below) closes the
# common case; rewriting .git/config here closes it regardless of the
# template source.
#
# The trusted config written here must match the object format the
# repository was actually `git init`'d with (second argument, "sha1" or
# "sha256" — see the _FS_OBJECT_FORMAT derivation in
# fullsend_checkout_mr_source). GitLab repositories can be configured to
# use SHA-256 object names (fullsend_validate_mr_source_sha already
# accepts the 64-character length), and a SHA-256 source can only be
# fetched into a SHA-256 target: git refuses to fetch across mismatched
# object formats outright ("mismatched algorithms"). Always writing
# repositoryformatversion=0 with no extensions.objectFormat — regardless
# of how `git init` was actually invoked — would silently discard SHA-256
# format metadata and strand every SHA-256 source behind a fetch failure.
fullsend_reset_target_repo_git_config() {
  _fs_repo=$1
  _fs_object_format=$2
  if [ "${_fs_object_format}" = "sha256" ]; then
    if ! cat > "${_fs_repo}/.git/config" <<'EOF'
[core]
	repositoryformatversion = 1
	filemode = true
	bare = false
	logallrefupdates = true
[extensions]
	objectFormat = sha256
EOF
    then
      echo "ERROR: failed to reset untrusted git config in '${_fs_repo}'" >&2
      unset _fs_repo _fs_object_format
      return 1
    fi
  else
    if ! cat > "${_fs_repo}/.git/config" <<'EOF'
[core]
	repositoryformatversion = 0
	filemode = true
	bare = false
	logallrefupdates = true
EOF
    then
      echo "ERROR: failed to reset untrusted git config in '${_fs_repo}'" >&2
      unset _fs_repo _fs_object_format
      return 1
    fi
  fi
  unset _fs_repo _fs_object_format
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
    # Reuse the caller's _FS_GIT_ENV prefix (GIT_CONFIG_NOSYSTEM/
    # GIT_CONFIG_GLOBAL plus the GIT_DIR/GIT_WORK_TREE/
    # GIT_OBJECT_DIRECTORY/GIT_ALTERNATE_OBJECT_DIRECTORIES/
    # GIT_COMMON_DIR unsets) instead of a narrower one-shot prefix, so
    # this fetch gets the same protection as every other git invocation
    # fullsend_checkout_mr_source makes against FIX_TARGET_REPO — not
    # only the credentialed branch below.
    "${_FS_GIT_ENV[@]}" \
      GIT_TERMINAL_PROMPT=0 \
      GIT_CURL_VERBOSE=0 GIT_TRACE=0 GIT_TRACE_PACKET=0 GIT_TRACE2=0 \
      GIT_TRACE2_EVENT=0 GIT_TRACE_SETUP=0 GIT_TRACE_REDACT=true \
      GIT_SSL_NO_VERIFY='' GIT_ASKPASS='' SSLKEYLOGFILE='' \
      git -C "${FIX_TARGET_REPO}" -c http.sslVerify=true "${_FS_GIT_HOOK_ARGS[@]}" fetch --no-tags --prune -- \
      "${_FS_SOURCE_FETCH_URL}" "${_fs_refspec}"
  else
    # Force-empty the proxy variables so a job-environment proxy cannot
    # sit on-path for the tokenized fetch, and unset the
    # GIT_CONFIG_COUNT/GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n and
    # GIT_CONFIG_PARAMETERS env-config layer — which git gives precedence
    # over configuration files and the "-c" overrides below — plus the
    # deprecated GIT_CONFIG file override, so an ordinary project/group
    # CI/CD variable of this kind cannot rewrite the fetch URL via
    # url.*.insteadOf or install a credential.*.helper that inherits
    # FULLSEND_JOB_TOKEN. Also pin GIT_CONFIG_NOSYSTEM/GIT_CONFIG_GLOBAL
    # to /dev/null so the runner's file-based system/global git config
    # (which the env-config unsets above do not touch) cannot supply the
    # same kind of per-URL insteadOf/proxy/credential.helper override.
    # GIT_DIR/GIT_WORK_TREE/GIT_OBJECT_DIRECTORY/
    # GIT_ALTERNATE_OBJECT_DIRECTORIES/GIT_COMMON_DIR are unset for the
    # same reason: git -C is ignored once GIT_DIR is an absolute path, so
    # an ordinary CI/CD variable of one of those names could otherwise
    # redirect this invocation at a different git directory whose
    # repo-local config takes precedence over the "-c" overrides below,
    # while FULLSEND_JOB_TOKEN is present in the environment.
    env -u GIT_CONFIG_COUNT -u GIT_CONFIG_PARAMETERS -u GIT_CONFIG \
      -u GIT_DIR -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY \
      -u GIT_ALTERNATE_OBJECT_DIRECTORIES -u GIT_COMMON_DIR \
      GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null \
      _FS_GIT_PASSWORD="${FULLSEND_JOB_TOKEN}" _FS_CRED_HOST="${_FS_CRED_HOST}" GIT_TERMINAL_PROMPT=0 \
      GIT_CURL_VERBOSE=0 GIT_TRACE=0 GIT_TRACE_PACKET=0 GIT_TRACE2=0 \
      GIT_TRACE2_EVENT=0 GIT_TRACE_SETUP=0 GIT_TRACE_REDACT=true \
      GIT_SSL_NO_VERIFY='' GIT_ASKPASS='' SSLKEYLOGFILE='' \
      http_proxy='' HTTP_PROXY='' https_proxy='' HTTPS_PROXY='' all_proxy='' ALL_PROXY='' GIT_PROXY_COMMAND='' \
      git -C "${FIX_TARGET_REPO}" \
      -c credential.helper= \
      -c "credential.helper=${_FS_CRED_HELPER}" \
      -c http.followRedirects=false \
      -c http.sslVerify=true "${_FS_GIT_HOOK_ARGS[@]}" \
      fetch --no-tags --prune -- "${_FS_SOURCE_FETCH_URL}" "${_fs_refspec}"
  fi
}

fullsend_checkout_mr_source() {
  # Matches the full truthy set gitlab-runner accepts for this variable
  # (Go strconv.ParseBool: "1", "t"/"T", "true"/"TRUE"/"True"), not just
  # an exact "true" — same guard as run-agent-job.sh / run-poll-job.sh.
  # This script only runs sourced from run-agent-job.sh's fix stage,
  # after that broader guard already ran, but keeps its own guard in
  # lockstep so it never becomes a weaker independent check.
  case "${CI_DEBUG_TRACE:-}" in
    1|[tT]|[tT][rR][uU][eE])
      echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets" >&2
      return 1
      ;;
  esac

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

  case "${FULLSEND_PINNED_PROJECT_ID:-}" in
    ''|*[!0-9]*)
      echo "ERROR: FULLSEND_PINNED_PROJECT_ID is required to resolve merge request !${MR_IID}" >&2
      return 1
      ;;
  esac
  if [ -z "${FULLSEND_PINNED_PROJECT_PATH:-}" ]; then
    echo "ERROR: FULLSEND_PINNED_PROJECT_PATH is required to resolve merge request !${MR_IID}" >&2
    return 1
  fi
  if [ -z "${FULLSEND_JOB_TOKEN:-}" ]; then
    echo "ERROR: cannot resolve MR source revision — FULLSEND_JOB_TOKEN is required" >&2
    return 1
  fi

  _FS_SERVER_URL="${FULLSEND_PINNED_GITLAB_URL:-}"
  _FS_SERVER_URL="${_FS_SERVER_URL%/}"
  if [ -z "${_FS_SERVER_URL}" ]; then
    echo "ERROR: FULLSEND_PINNED_GITLAB_URL is required to fetch the MR source revision" >&2
    return 1
  fi

  # CI_MERGE_REQUEST_SOURCE_* are GitLab-predefined variables on native
  # MR pipelines, but they are not part of the HMAC-signed dispatch
  # message: an ordinary project/group CI/CD variable defined with one
  # of these exact names would otherwise be trusted without verification.
  # Treat them as an optional consistency check only — `fullsend
  # resolve-mr-source` is always invoked and is the sole source of
  # truth for the branch, SHA, and source project. A fast-path value
  # that disagrees with the resolved source fails closed instead of
  # being trusted. Project-identity fast-path variables are compared
  # against the resolved source (not assumed to equal
  # FULLSEND_PINNED_PROJECT_ID/FULLSEND_PINNED_PROJECT_PATH), so fork
  # and cross-project MRs can check out their real head.
  _FS_FASTPATH_BRANCH="${CI_MERGE_REQUEST_SOURCE_BRANCH_NAME:-}"
  _FS_FASTPATH_SHA="${CI_MERGE_REQUEST_SOURCE_BRANCH_SHA:-}"
  _FS_FASTPATH_PROJECT_ID="${CI_MERGE_REQUEST_SOURCE_PROJECT_ID:-}"
  _FS_FASTPATH_PROJECT_PATH="${CI_MERGE_REQUEST_SOURCE_PROJECT_PATH:-}"

  # GITLAB_TOKEN is already exported (from FULLSEND_JOB_TOKEN) before
  # this script is sourced; resolve-mr-source falls back to it via
  # resolveGitLabToken() when --token is omitted. Passing the token as
  # an argv flag would additionally expose it via /proc/<pid>/cmdline,
  # `ps`, and execve audit logs. --project and --gitlab-url are the
  # pinned, job-token-verified identity (see module comment above), not
  # the overridable CI_PROJECT_PATH / CI_SERVER_URL.
  if ! _FS_RESOLVED=$(fullsend resolve-mr-source \
    --project "${FULLSEND_PINNED_PROJECT_PATH}" \
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

  if ! fullsend_validate_mr_source_ref "source branch" "${SOURCE_BRANCH}" extended; then
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  if ! fullsend_validate_mr_source_sha "${SOURCE_SHA}"; then
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  if ! fullsend_validate_mr_source_project_path "${SOURCE_PROJECT_PATH}"; then
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
  if [ -n "${_FS_FASTPATH_PROJECT_PATH}" ] && [ "${_FS_FASTPATH_PROJECT_PATH}" != "${SOURCE_PROJECT_PATH}" ]; then
    echo "ERROR: CI_MERGE_REQUEST_SOURCE_PROJECT_PATH '${_FS_FASTPATH_PROJECT_PATH}' does not match merge request !${MR_IID} source project '${SOURCE_PROJECT_PATH}' — refusing to trust unverified CI variables" >&2
    unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
    return 1
  fi
  if [ -n "${_FS_FASTPATH_PROJECT_ID}" ]; then
    if [ "${SOURCE_PROJECT_PATH}" = "${FULLSEND_PINNED_PROJECT_PATH}" ]; then
      if [ "${_FS_FASTPATH_PROJECT_ID}" != "${FULLSEND_PINNED_PROJECT_ID}" ]; then
        echo "ERROR: CI_MERGE_REQUEST_SOURCE_PROJECT_ID '${_FS_FASTPATH_PROJECT_ID}' does not match this project's FULLSEND_PINNED_PROJECT_ID '${FULLSEND_PINNED_PROJECT_ID}' — refusing to trust unverified CI variables" >&2
        unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
        return 1
      fi
    elif [ "${_FS_FASTPATH_PROJECT_ID}" = "${FULLSEND_PINNED_PROJECT_ID}" ]; then
      echo "ERROR: CI_MERGE_REQUEST_SOURCE_PROJECT_ID '${_FS_FASTPATH_PROJECT_ID}' matches this project's FULLSEND_PINNED_PROJECT_ID but the resolved source project is '${SOURCE_PROJECT_PATH}' — refusing to trust unverified CI variables" >&2
      unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH
      return 1
    fi
  fi
  unset _FS_FASTPATH_BRANCH _FS_FASTPATH_SHA _FS_FASTPATH_PROJECT_ID _FS_FASTPATH_PROJECT_PATH

  # Pre-push safety gate — fail closed before fetching or running the
  # fix agent if the resolved source branch is protected in the
  # resolved source project, directly or via a matching wildcard rule
  # (e.g. "release-*"). Runs through forge.Client (internal/forge/gitlab),
  # not a direct curl call, for the same reason resolve-mr-source does:
  # one forge-API path instead of a hand-rolled one in this generated
  # scaffold. Checked against SOURCE_PROJECT_PATH/SOURCE_BRANCH (the
  # resolved source, which may be a fork or cross-project repository),
  # never against FULLSEND_PINNED_PROJECT_PATH — a fix commit must never
  # be attempted against a protected branch regardless of which project
  # it lives in.
  if ! fullsend check-protected-branch \
    --project "${SOURCE_PROJECT_PATH}" \
    --branch "${SOURCE_BRANCH}" \
    --gitlab-url "${_FS_SERVER_URL}"; then
    echo "ERROR: refusing to run the fix agent — merge request !${MR_IID} source branch '${SOURCE_BRANCH}' in '${SOURCE_PROJECT_PATH}' is protected or its protection status could not be determined" >&2
    return 1
  fi

  _FS_SOURCE_FETCH_URL="${_FS_SERVER_URL}/${SOURCE_PROJECT_PATH}.git"
  unset _FS_SERVER_URL

  FIX_TARGET_REPO="${CI_PROJECT_DIR}/target-repo"
  if [ -e "${FIX_TARGET_REPO}" ]; then
    rm -rf "${FIX_TARGET_REPO}"
  fi
  mkdir -p "${FIX_TARGET_REPO}"

  # FULLSEND_JOB_TOKEN is already exported in the job environment by the
  # time this runs. GIT_CONFIG_COUNT/GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n
  # and GIT_CONFIG_PARAMETERS are a config source git gives precedence
  # over configuration files, including the "-c" overrides in
  # _FS_GIT_HOOK_ARGS below; an ordinary project/group CI/CD variable of
  # this kind (the runner/CI-level env-config layer, not the untrusted
  # MR source tree's own config) could declare a filter.<name>.clean/
  # smudge driver or a core.hooksPath, and the MR source's own
  # .gitattributes (checked out below) can select which named filter
  # applies to a tracked file — so `git checkout --force` populating the
  # working tree could run that driver as a subprocess while the job
  # token is present. Unset that env-config layer, and pin
  # GIT_CONFIG_NOSYSTEM/GIT_CONFIG_GLOBAL via the _FS_GIT_ENV
  # per-invocation prefix (below) for every git invocation this function
  # makes against FIX_TARGET_REPO, including `git init` and the checkout
  # below — not only the credentialed fetch, which already neutralizes
  # this layer for itself via fullsend_git_fetch_mr_source. Unlike an
  # `export`, a per-invocation prefix leaves the parent job shell's
  # environment unchanged once this function returns, so a later
  # host-side git operation in the same job that needs runner
  # system/global config (credential helper, insteadOf, extraHeader)
  # still sees it.
  #
  # _FS_GIT_ENV also unsets GIT_DIR/GIT_WORK_TREE/GIT_OBJECT_DIRECTORY/
  # GIT_ALTERNATE_OBJECT_DIRECTORIES/GIT_COMMON_DIR for the same reason
  # fullsend_git_fetch_mr_source's credentialed branch already does:
  # `git -C` is ignored once GIT_DIR is an absolute path, so an ordinary
  # project/group CI/CD variable of one of those names could otherwise
  # redirect `git init`, `remote add`, `cat-file`, `checkout --force`
  # (which populates the working tree from the untrusted MR source), or
  # `rev-parse` at a foreign git directory whose repo-local
  # credential.helper/filter-driver config takes effect, while
  # FULLSEND_JOB_TOKEN remains exported. Folding the unset into this
  # shared prefix (rather than only the credentialed fetch) gives every
  # git invocation this function makes — including the non-credentialed
  # fetch branch below, which now reuses this same prefix — the same
  # protection.
  unset GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS GIT_CONFIG GIT_TEMPLATE_DIR
  _FS_GIT_ENV=(env -u GIT_DIR -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY \
    -u GIT_ALTERNATE_OBJECT_DIRECTORIES -u GIT_COMMON_DIR \
    GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null)
  _FS_EMPTY_HOOKS=$(mktemp -d)
  # The RETURN trap fires on every return from this function — success
  # or failure alike — so it is also where _FS_EMPTY_HOOKS/_FS_GIT_ENV/
  # _FS_GIT_HOOK_ARGS are unset and the trap itself is cleared
  # (`trap - RETURN`). This script is sourced (not executed) into
  # run-agent-job.sh's shell, so leaving these behind would otherwise
  # leak into every later command that shell runs.
  trap 'rmdir "${_FS_EMPTY_HOOKS}" 2>/dev/null || true; unset _FS_EMPTY_HOOKS _FS_GIT_ENV _FS_GIT_HOOK_ARGS _FS_OBJECT_FORMAT; trap - RETURN' RETURN
  _FS_GIT_HOOK_ARGS=(-c "core.hooksPath=${_FS_EMPTY_HOOKS}" -c core.pager=cat -c core.fsmonitor=)

  # The resolved, validated SOURCE_SHA (not an ambient GIT_DEFAULT_HASH
  # CI/CD variable, which --object-format below always overrides anyway)
  # decides which object format the target repository is initialized
  # with. `fullsend resolve-mr-source` returns the source's full object
  # id, so its length reliably distinguishes a SHA-256 source (64 hex
  # characters) from a SHA-1 one (40); fullsend_validate_mr_source_sha's
  # 7-64 range exists for abbreviation tolerance, but resolve-mr-source
  # never abbreviates. A SHA-256 source can only be fetched into a
  # SHA-256 target (git fails fetch outright on a mismatched object
  # format), so defaulting to sha1 here for anything short of the full
  # SHA-256 length is the safe choice.
  _FS_OBJECT_FORMAT=sha1
  if [ "${#SOURCE_SHA}" -eq 64 ]; then
    _FS_OBJECT_FORMAT=sha256
  fi

  # --template points at the just-created, guaranteed-empty _FS_EMPTY_HOOKS
  # directory so `git init` cannot pull in a GIT_TEMPLATE_DIR-supplied (or
  # system-default) template even though GIT_TEMPLATE_DIR is already
  # unset above — and fullsend_reset_target_repo_git_config rewrites
  # .git/config immediately after so no template-supplied config key can
  # survive into the credentialed fetch below regardless of source.
  # --object-format pins the repository to the format derived above
  # (overriding any ambient GIT_DEFAULT_HASH), and the matching trusted
  # format metadata is written into the minimal config below so the
  # rewrite does not strand a SHA-256 init back on SHA-1.
  "${_FS_GIT_ENV[@]}" git "${_FS_GIT_HOOK_ARGS[@]}" init --quiet --template="${_FS_EMPTY_HOOKS}" --object-format="${_FS_OBJECT_FORMAT}" "${FIX_TARGET_REPO}"
  fullsend_reset_target_repo_git_config "${FIX_TARGET_REPO}" "${_FS_OBJECT_FORMAT}" || return 1
  "${_FS_GIT_ENV[@]}" git -C "${FIX_TARGET_REPO}" "${_FS_GIT_HOOK_ARGS[@]}" remote add origin "${_FS_SOURCE_FETCH_URL}"

  _FS_CRED_HELPER=""
  case "${_FS_SOURCE_FETCH_URL}" in
    file://*|/*)
      ;;
    *)
      # Host the credential helper is allowed to answer for. Passed to
      # the helper via the _FS_CRED_HOST environment variable at fetch
      # invocation time — the same way _FS_GIT_PASSWORD is passed —
      # rather than interpolated into the generated script's source, so
      # a shell metacharacter (e.g. a stray quote) in
      # FULLSEND_PINNED_GITLAB_URL cannot break out of the helper's
      # source text and execute.
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
  if ! "${_FS_GIT_ENV[@]}" git -C "${FIX_TARGET_REPO}" "${_FS_GIT_HOOK_ARGS[@]}" cat-file -e "${SOURCE_SHA}^{commit}" 2>/dev/null; then
    if ! fullsend_git_fetch_mr_source "${SOURCE_SHA}"; then
      echo "ERROR: failed to fetch MR source SHA ${SOURCE_SHA} from ${SOURCE_PROJECT_PATH}" >&2
      fullsend_cleanup_mr_source_fetch
      return 1
    fi
  fi
  fullsend_cleanup_mr_source_fetch
  unset _FS_SOURCE_FETCH_URL

  if ! "${_FS_GIT_ENV[@]}" git -C "${FIX_TARGET_REPO}" "${_FS_GIT_HOOK_ARGS[@]}" checkout --force -B "${SOURCE_BRANCH}" "${SOURCE_SHA}"; then
    echo "ERROR: failed to check out MR source SHA ${SOURCE_SHA} on branch '${SOURCE_BRANCH}'" >&2
    return 1
  fi

  _FS_HEAD=$("${_FS_GIT_ENV[@]}" git -C "${FIX_TARGET_REPO}" "${_FS_GIT_HOOK_ARGS[@]}" rev-parse HEAD)
  _FS_EXPECTED=$("${_FS_GIT_ENV[@]}" git -C "${FIX_TARGET_REPO}" "${_FS_GIT_HOOK_ARGS[@]}" rev-parse --verify "${SOURCE_SHA}^{commit}")
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
