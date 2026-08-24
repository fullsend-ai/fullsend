#!/usr/bin/env bash
# Tests for setup-agent-env.sh: prefix stripping and the FULLSEND_* override
# passthrough from repository variables.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="${here}/setup-agent-env.sh"
fail=0
check() { # name expected actual
  if [[ "$2" == "$3" ]]; then echo "PASS: $1"; else echo "FAIL: $1 — expected [$2], got [$3]"; fail=1; fi
}

run() { # env assignments... ; prints GITHUB_ENV content
  local envfile; envfile="$(mktemp)"
  ( export GITHUB_ENV="${envfile}"; env "$@" bash "${script}" >/dev/null )
  cat "${envfile}"; rm -f "${envfile}"
}

# 1. Prefix stripping still works.
out="$(run AGENT_PREFIX=TRIAGE_ TRIAGE_TARGET_REPO_DIR=target-repo)"
check "prefix stripped" "1" "$(grep -c '^TARGET_REPO_DIR<<' <<<"${out}")"

# 2. No FULLSEND_REPO_VARS: nothing extra exported.
check "no vars no overrides" "0" "$(grep -c '^FULLSEND_' <<<"${out}" || true)"

# 3. Plain and role-prefixed variables; role wins.
vars='{"FULLSEND_MODEL":"opus","TRIAGE_FULLSEND_MODEL":"google-vertex/gemini-2.5-flash","FULLSEND_EFFORT":"medium","OTHER":"x"}'
out="$(run AGENT_PREFIX=TRIAGE_ FULLSEND_REPO_VARS="${vars}")"
check "role-prefixed wins" "FULLSEND_MODEL=google-vertex/gemini-2.5-flash" "$(grep '^FULLSEND_MODEL=' <<<"${out}")"
check "plain applies" "FULLSEND_EFFORT=medium" "$(grep '^FULLSEND_EFFORT=' <<<"${out}")"
check "other vars ignored" "0" "$(grep -c '^OTHER' <<<"${out}" || true)"

# 4. Another role sees the plain value.
out="$(run AGENT_PREFIX=CODE_ FULLSEND_REPO_VARS="${vars}")"
check "other role gets plain" "FULLSEND_MODEL=opus" "$(grep '^FULLSEND_MODEL=' <<<"${out}")"

# 5. Unsafe values are skipped (newline, shell metacharacters).
vars='{"FULLSEND_MODEL":"opus; rm -rf /","FULLSEND_RUNTIME":"pi\nx","FULLSEND_PI_PROVIDER":"anthropic-vertex"}'
out="$(run AGENT_PREFIX=TRIAGE_ FULLSEND_REPO_VARS="${vars}")"
check "metachars skipped" "0" "$(grep -c '^FULLSEND_MODEL=' <<<"${out}" || true)"
check "newline skipped" "0" "$(grep -c '^FULLSEND_RUNTIME=' <<<"${out}" || true)"
check "safe value kept" "FULLSEND_PI_PROVIDER=anthropic-vertex" "$(grep '^FULLSEND_PI_PROVIDER=' <<<"${out}")"

# 6a. The legacy pi-only name is still forwarded (CLI treats it as an alias).
vars='{"FULLSEND_PI_MODEL":"claude-opus-4-8"}'
out="$(run AGENT_PREFIX=TRIAGE_ FULLSEND_REPO_VARS="${vars}")"
check "legacy pi model forwarded" "FULLSEND_PI_MODEL=claude-opus-4-8" "$(grep '^FULLSEND_PI_MODEL=' <<<"${out}")"

# 6. Fallback chain keeps commas.
vars='{"FULLSEND_FALLBACK_MODELS":"sonnet,haiku"}'
out="$(run AGENT_PREFIX=TRIAGE_ FULLSEND_REPO_VARS="${vars}")"
check "fallback chain" "FULLSEND_FALLBACK_MODELS=sonnet,haiku" "$(grep '^FULLSEND_FALLBACK_MODELS=' <<<"${out}")"

exit "${fail}"
