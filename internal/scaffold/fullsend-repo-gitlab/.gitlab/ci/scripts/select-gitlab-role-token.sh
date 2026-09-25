#!/usr/bin/env bash
# select-gitlab-role-token.sh — Resolve the GitLab PAT for bootstrap API calls.
#
# Source this file (do not execute it) so FULLSEND_JOB_TOKEN is exported
# for later PRIVATE-TOKEN headers, GITLAB_TOKEN, and PUSH_TOKEN.
#
# Job selection:
#   FULLSEND_JOB_KIND=poller  — poller/controller jobs
#   FULLSEND_JOB_KIND=agent   — agent stages; FULLSEND_JOB_AGENT (or STAGE)
#                               maps onto a registered role. Unlike
#                               `fullsend run` (which falls back to the
#                               harness `role:` field when the agent name
#                               itself is unregistered), this script has
#                               no harness input at CI bootstrap time: the
#                               dispatched STAGE name itself must be a
#                               registered role or agent alias.
#
# Gate FULLSEND_GITLAB_ROLE_MIGRATION (case-insensitive):
#   unset/disabled/rollback — shared FULLSEND_FORGE_TOKEN only
#   migrating               — role secret when present, else shared token
#   enforced                — role secret only; shared token is never used
#
# Custom roles come from FULLSEND_GITLAB_ROLE_REGISTRY (install-state JSON).
# Repository files cannot create or elevate a role. Unknown gate values and
# unregistered agents fail closed in role-aware modes.

fullsend_select_gitlab_role_token() {
  if test "${CI_DEBUG_TRACE:-}" = "true"; then
    echo "ERROR: CI_DEBUG_TRACE enabled — aborting to protect secrets" >&2
    return 1
  fi

  _fs_kind="${FULLSEND_JOB_KIND:-}"
  case "${_fs_kind}" in
    poller|agent) ;;
    *)
      echo "ERROR: FULLSEND_JOB_KIND must be 'poller' or 'agent', got '${_fs_kind:-<empty>}'" >&2
      unset _fs_kind
      return 1
      ;;
  esac

  _fs_mode=$(printf '%s' "${FULLSEND_GITLAB_ROLE_MIGRATION:-}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
  case "${_fs_mode}" in
    "") _fs_mode="disabled" ;;
    disabled|migrating|rollback|enforced) ;;
    *)
      echo "ERROR: invalid GitLab role migration mode '${FULLSEND_GITLAB_ROLE_MIGRATION}'" >&2
      unset _fs_kind _fs_mode
      return 1
      ;;
  esac

  _fs_role=""
  _fs_secret=""
  if test "${_fs_kind}" = "poller"; then
    _fs_role="poller"
    _fs_secret="FULLSEND_GITLAB_POLLER_TOKEN"
  else
    _fs_agent="${FULLSEND_JOB_AGENT:-${STAGE:-}}"
    _fs_agent=$(printf '%s' "${_fs_agent}" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')
    case "${_fs_agent}" in
      poller)
        _fs_role="poller"
        _fs_secret="FULLSEND_GITLAB_POLLER_TOKEN"
        ;;
      analyst|review|triage|prioritize|retro|scribe)
        _fs_role="analyst"
        _fs_secret="FULLSEND_GITLAB_ANALYST_TOKEN"
        ;;
      coder|code|fix)
        _fs_role="coder"
        _fs_secret="FULLSEND_GITLAB_CODER_TOKEN"
        ;;
      *)
        if test -n "${FULLSEND_GITLAB_ROLE_REGISTRY:-}"; then
          _fs_custom=$(FULLSEND_JOB_AGENT="${_fs_agent}" python3 -c '
import json, os, re, sys

agent = (os.environ.get("FULLSEND_JOB_AGENT") or "").strip().lower()
raw = os.environ.get("FULLSEND_GITLAB_ROLE_REGISTRY") or ""
if not agent or not raw.strip():
    sys.exit(0)
try:
    doc = json.loads(raw)
except json.JSONDecodeError:
    print("ERROR: invalid GitLab role registry JSON", file=sys.stderr)
    sys.exit(1)
if not isinstance(doc, dict) or "roles" not in doc:
    print("ERROR: invalid GitLab role registry JSON", file=sys.stderr)
    sys.exit(1)
roles = doc.get("roles")
if not isinstance(roles, list):
    print("ERROR: invalid GitLab role registry JSON", file=sys.stderr)
    sys.exit(1)

name_re = re.compile(r"^[a-z][a-z0-9_-]*$")

# Seed the three built-in roles before applying custom entries so a
# custom role reuse can target a builtin (mirrors
# builtinRegistrations()/resolveReuse in internal/gitlabroles/registry.go).
# The builtin case statement above already intercepts builtin agent
# aliases directly; this seeding only matters for reuse resolution below.
by_name = {
    "poller": {"credential": "own", "secret_name": "FULLSEND_GITLAB_POLLER_TOKEN"},
    "analyst": {"credential": "own", "secret_name": "FULLSEND_GITLAB_ANALYST_TOKEN"},
    "coder": {"credential": "own", "secret_name": "FULLSEND_GITLAB_CODER_TOKEN"},
}
by_agent = {
    "poller": "poller",
    "analyst": "analyst",
    "review": "analyst",
    "triage": "analyst",
    "prioritize": "analyst",
    "retro": "analyst",
    "scribe": "analyst",
    "coder": "coder",
    "code": "coder",
    "fix": "coder",
}
for rec in roles:
    if not isinstance(rec, dict):
        print("ERROR: invalid GitLab role registry JSON", file=sys.stderr)
        sys.exit(1)
    name = str(rec.get("name") or "").strip().lower()
    if not name_re.match(name):
        continue
    by_name[name] = rec
    by_agent[name] = name
    agents = rec.get("agents") or []
    if not isinstance(agents, list):
        continue
    for mapped in agents:
        key = str(mapped or "").strip().lower()
        if name_re.match(key):
            by_agent[key] = name

role = by_agent.get(agent)
if not role:
    sys.exit(0)
seen = set()
rec = None
while True:
    if role in seen:
        print("ERROR: GitLab role registry credential reuse cycle", file=sys.stderr)
        sys.exit(1)
    seen.add(role)
    rec = by_name.get(role)
    if rec is None:
        print("ERROR: GitLab role registry reuses unregistered role", file=sys.stderr)
        sys.exit(1)
    kind = str(rec.get("credential") or "own").strip().lower()
    if kind != "reuse":
        break
    reuse = str(rec.get("reuse") or "").strip().lower()
    if not reuse:
        print("ERROR: GitLab role registry reuse missing target", file=sys.stderr)
        sys.exit(1)
    role = reuse
secret = str(rec.get("secret_name") or "").strip()
if not secret:
    env_role = role.upper().replace("-", "_")
    secret = "FULLSEND_GITLAB_ROLE_" + env_role + "_TOKEN"
if not re.match(r"^[A-Z][A-Z0-9_]*$", secret):
    print("ERROR: invalid GitLab role secret name", file=sys.stderr)
    sys.exit(1)
print(role + "\t" + secret)
') || {
            unset _fs_kind _fs_mode _fs_role _fs_secret _fs_agent _fs_custom
            return 1
          }
          if test -n "${_fs_custom}"; then
            _fs_role="${_fs_custom%%	*}"
            _fs_secret="${_fs_custom#*	}"
          fi
        fi
        ;;
    esac
  fi

  _fs_shared_only=0
  case "${_fs_mode}" in
    disabled|rollback) _fs_shared_only=1 ;;
  esac

  if test "${_fs_shared_only}" -eq 0; then
    if test -z "${_fs_role}" || test -z "${_fs_secret}"; then
      if test "${_fs_kind}" = "agent"; then
        echo "ERROR: GitLab role is not registered for agent '${FULLSEND_JOB_AGENT:-${STAGE:-<empty>}}'" >&2
      else
        echo "ERROR: GitLab role is not registered" >&2
      fi
      unset _fs_kind _fs_mode _fs_role _fs_secret _fs_agent _fs_custom _fs_shared_only
      return 1
    fi
  fi

  _fs_token=""
  _fs_name=""
  if test "${_fs_shared_only}" -eq 1; then
    _fs_token="${FULLSEND_FORGE_TOKEN:-}"
    _fs_name="FULLSEND_FORGE_TOKEN"
  else
    case "${_fs_secret}" in
      FULLSEND_GITLAB_POLLER_TOKEN|FULLSEND_GITLAB_ANALYST_TOKEN|FULLSEND_GITLAB_CODER_TOKEN) ;;
      *)
        if ! [[ "${_fs_secret}" =~ ^FULLSEND_GITLAB_ROLE_[A-Z0-9_]+_TOKEN$ ]]; then
          echo "ERROR: invalid GitLab role secret name" >&2
          unset _fs_kind _fs_mode _fs_role _fs_secret _fs_agent _fs_custom _fs_shared_only _fs_token _fs_name
          return 1
        fi
        ;;
    esac
    _fs_role_value="${!_fs_secret:-}"
    if test -n "${_fs_role_value}"; then
      _fs_token="${_fs_role_value}"
      _fs_name="${_fs_secret}"
    else
      if test "${_fs_mode}" = "migrating"; then
        _fs_token="${FULLSEND_FORGE_TOKEN:-}"
        _fs_name="FULLSEND_FORGE_TOKEN"
      fi
    fi
  fi

  if test -z "${_fs_token}"; then
    if test "${_fs_mode}" = "enforced"; then
      echo "ERROR: ${_fs_secret} is not set — ensure the protected CI/CD variable is configured" >&2
    else
      echo "ERROR: FULLSEND_FORGE_TOKEN is not set — ensure the protected CI/CD variable is configured" >&2
    fi
    unset _fs_kind _fs_mode _fs_role _fs_secret _fs_agent _fs_custom _fs_shared_only _fs_token _fs_name _fs_role_value
    return 1
  fi

  export FULLSEND_JOB_TOKEN="${_fs_token}"
  # FULLSEND_JOB_TOKEN_NAME (not FULLSEND_GITLAB_ROLE_SECRET) is
  # intentional: this bootstrap-only diagnostic follows this script's own
  # FULLSEND_JOB_* naming (FULLSEND_JOB_KIND, FULLSEND_JOB_AGENT,
  # FULLSEND_JOB_TOKEN), not the FULLSEND_GITLAB_ROLE_SECRET diagnostic
  # that `fullsend run` publishes later in the job for the same job
  # (internal/cli/gitlab_role.go).
  export FULLSEND_JOB_TOKEN_NAME="${_fs_name}"
  unset _fs_kind _fs_mode _fs_role _fs_secret _fs_agent _fs_custom _fs_shared_only _fs_token _fs_name _fs_role_value
}

fullsend_select_gitlab_role_token
