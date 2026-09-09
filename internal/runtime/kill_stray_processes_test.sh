#!/usr/bin/env bash
# kill_stray_processes_test.sh — real-shell test for the stray-process sweep
# that runtime.ClearIterationArtifacts runs inside the sandbox between
# iterations (internal/runtime/stray_processes.go). It executes the golden
# file testdata/kill_stray_processes.sh, which TestKillStrayProcessesScript_Golden
# pins to the production bytes.
#
# The snippet kills every process of the current user it can see, so it is
# never run against the real process table here: a fake `ps` on PATH
# delegates to the real one and keeps only this test's own subtree, and the
# test refuses to run the snippet unless that fake is what `ps` resolves to.
# kill/sleep/awk/id are the real tools.
#
# Run from repo root: bash internal/runtime/kill_stray_processes_test.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SNIPPET="${SCRIPT_DIR}/testdata/kill_stray_processes.sh"
REAL_PS="$(command -v ps)"
FAILURES=0

TMP="$(mktemp -d)"
STRAY_PIDS=()
cleanup() {
  local p
  for p in "${STRAY_PIDS[@]:-}"; do
    if [ -n "$p" ]; then
      kill -9 "$p" 2>/dev/null || true
    fi
  done
  rm -rf "${TMP}"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  FAILURES=$((FAILURES + 1))
}

pass() {
  echo "PASS: $*"
}

# Fake ps. The snippet's enumeration (`-u <uid>`) is delegated to the real
# ps and then narrowed to STRAY_TEST_ROOT and its descendants (pid is column
# 1, ppid column 2). What its liveness probes (`-p <pid,...>`) do is the
# caller's choice: pass through untouched (they are already scoped to
# signalled targets) or fail, for the broken-probe case below. The
# enumeration stays scoped either way — an unscoped fake would let the sweep
# kill this whole session.
make_fake_ps() {
  local dir="$1" probe="$2"
  mkdir -p "${dir}"
  cat > "${dir}/ps" <<EOF
#!/bin/sh
case " \$* " in
  *" -p "*) ${probe} ;;
esac
'${REAL_PS}' "\$@" | awk -v root="\${STRAY_TEST_ROOT}" '
  { line[NR] = \$0; pid[NR] = \$1; parent[\$1] = \$2 }
  END {
    keep[root] = 1
    do {
      changed = 0
      for (i = 1; i <= NR; i++) {
        p = pid[i]
        if (!(p in keep) && (parent[p] in keep)) { keep[p] = 1; changed = 1 }
      }
    } while (changed)
    for (i = 1; i <= NR; i++) if (pid[i] in keep) print line[i]
  }'
EOF
  chmod +x "${dir}/ps"
}

make_fake_ps "${TMP}/bin" "exec '${REAL_PS}' \"\$@\""
# A ps whose liveness probe fails the way a broken procps does: exit 1 with
# nothing on stdout, indistinguishable from "none of those pids exist".
make_fake_ps "${TMP}/probebin" 'exit 1'

# A ps that always fails, for the exit-3 path.
mkdir -p "${TMP}/badbin"
printf '#!/bin/sh\nexit 1\n' > "${TMP}/badbin/ps"
chmod +x "${TMP}/badbin/ps"

# Safety gate: the golden is a live kill script. Refuse to run it unless
# `ps` under the test PATH is one of the scoped fakes.
FAKE_PATH="${TMP}/bin:${PATH}"
PROBE_PATH="${TMP}/probebin:${PATH}"
for dir in bin probebin; do
  resolved="$(PATH="${TMP}/${dir}:${PATH}" command -v ps)"
  if [ "${resolved}" != "${TMP}/${dir}/ps" ]; then
    echo "ABORT: ps resolves to '${resolved}', not the scoped fake; refusing to run the sweep" >&2
    exit 1
  fi
done

# --- Fixture: strays owned by this test shell -------------------------------

# Plain stray: exits on TERM.
sleep 300 &
STRAY_TERM=$!
STRAY_PIDS+=("${STRAY_TERM}")

# Stubborn stray: SIG_IGN survives exec, so this sleep ignores TERM and only
# the KILL pass can remove it.
sh -c 'trap "" TERM; exec sleep 300' &
STRAY_IGN=$!
STRAY_PIDS+=("${STRAY_IGN}")

# Keep-alive stand-ins for the sandbox main process (sandbox.KeepAliveCommand):
# the bare argv and a path-qualified one. BSD sleep rejects "infinity", so
# only exercised on Linux (CI).
KEEP=""
KEEP_PATH=""
if [ "$(uname)" = "Linux" ]; then
  sleep infinity &
  KEEP=$!
  STRAY_PIDS+=("${KEEP}")
  "$(command -v sleep)" infinity &
  KEEP_PATH=$!
  STRAY_PIDS+=("${KEEP_PATH}")
fi

sleep 0.2
kill -0 "${STRAY_TERM}" || fail "fixture: TERM stray did not start"
kill -0 "${STRAY_IGN}" || fail "fixture: TERM-ignoring stray did not start"

# --- Run the snippet in a subshell, scoped to this test's subtree ----------

expected_killed=2
set +e
OUT="$(STRAY_TEST_ROOT=$$ PATH="${FAKE_PATH}" sh "${SNIPPET}" 2>"${TMP}/stderr")"
RC=$?
set -e

# --- Assertions --------------------------------------------------------------

if [ "${RC}" -eq 0 ]; then
  pass "snippet exits 0"
else
  fail "snippet exited ${RC}"
fi

if [ "${OUT}" = "stray processes killed: ${expected_killed}" ]; then
  pass "stdout reports ${expected_killed} killed"
else
  fail "unexpected stdout: '${OUT}' (stderr: $(cat "${TMP}/stderr"))"
fi

# wait reaps the child and returns its termination status: 128+signal.
set +e
wait "${STRAY_TERM}"
rc_term=$?
wait "${STRAY_IGN}"
rc_ign=$?
set -e
if [ "${rc_term}" -eq 143 ]; then
  pass "TERM stray terminated by SIGTERM"
else
  fail "TERM stray status ${rc_term}, expected 143"
fi
if [ "${rc_ign}" -eq 137 ]; then
  pass "TERM-ignoring stray killed by SIGKILL after the grace period"
else
  fail "TERM-ignoring stray status ${rc_ign}, expected 137"
fi

if [ -n "${KEEP}" ]; then
  if kill -0 "${KEEP}" 2>/dev/null; then
    pass "keep-alive main process survives"
  else
    fail "keep-alive main process was killed"
  fi
  if kill -0 "${KEEP_PATH}" 2>/dev/null; then
    pass "path-qualified keep-alive survives"
  else
    fail "path-qualified keep-alive was killed"
  fi
fi

# The exec shell's own ancestry (this test shell) is untouched — trivially
# true if we are still running, but make it explicit.
kill -0 $$ && pass "test shell survives the sweep"

# Idempotent: a second sweep finds nothing.
OUT2="$(STRAY_TEST_ROOT=$$ PATH="${FAKE_PATH}" sh "${SNIPPET}" 2>/dev/null)"
if [ "${OUT2}" = "stray processes killed: 0" ]; then
  pass "second sweep kills nothing"
else
  fail "second sweep stdout: '${OUT2}'"
fi

# A failed listing is distinct from "nothing to kill": exit 3, message on
# stderr, nothing on stdout.
set +e
OUT3="$(PATH="${TMP}/badbin:${PATH}" sh "${SNIPPET}" 2>"${TMP}/stderr3")"
RC3=$?
set -e
if [ "${RC3}" -eq 3 ] && [ -z "${OUT3}" ] && grep -q 'stray processes: ps failed' "${TMP}/stderr3"; then
  pass "ps failure exits 3 with a stderr message and no count"
else
  fail "ps failure: rc=${RC3} stdout='${OUT3}' stderr='$(cat "${TMP}/stderr3")'"
fi

# --- A failed liveness probe must not read as "everything is dead" ----------
# `ps -p` failing prints nothing and exits 1, exactly like "none of these
# pids exist". Trusting that would skip the KILL pass while the snippet
# still reported a clean sweep, so it cross-checks with kill -0: a broken
# probe KILLs everything that took the TERM and exits 3.

sh -c 'trap "" TERM; exec sleep 300' &
PROBE_IGN=$!
STRAY_PIDS+=("${PROBE_IGN}")
sleep 0.2
kill -0 "${PROBE_IGN}" || fail "fixture: probe-case stray did not start"

set +e
OUT4="$(STRAY_TEST_ROOT=$$ PATH="${PROBE_PATH}" sh "${SNIPPET}" 2>"${TMP}/stderr4")"
RC4=$?
set -e
if [ "${RC4}" -eq 3 ] && [ -z "${OUT4}" ] && grep -q 'stray processes: ps -p failed' "${TMP}/stderr4"; then
  pass "a failed liveness probe exits 3 instead of reporting a clean sweep"
else
  fail "probe failure: rc=${RC4} stdout='${OUT4}' stderr='$(cat "${TMP}/stderr4")'"
fi

# Bounded: if the KILL pass was skipped the stray is still running and a
# bare wait would hang the suite instead of failing it. A killed child that
# this shell has not reaped yet is a zombie, which wait returns for
# immediately, so only a live (non-Z) state is worth waiting out.
probe_state() {
  "${REAL_PS}" -o stat= -p "${PROBE_IGN}" 2>/dev/null | awk 'NR == 1 { print substr($1, 1, 1) }'
}
i=0
while [ "${i}" -lt 30 ] && [ -n "$(probe_state)" ] && [ "$(probe_state)" != "Z" ]; do
  sleep 0.1
  i=$((i + 1))
done
if [ -n "$(probe_state)" ] && [ "$(probe_state)" != "Z" ]; then
  fail "probe-case stray survived the sweep"
  kill -9 "${PROBE_IGN}" 2>/dev/null || true
fi

set +e
wait "${PROBE_IGN}"
rc_probe=$?
set -e
if [ "${rc_probe}" -eq 137 ]; then
  pass "the TERM-ignoring stray is KILLed even though the probe failed"
else
  fail "probe-case stray status ${rc_probe}, expected 137"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} failure(s)" >&2
  exit 1
fi
echo "All kill_stray_processes tests passed"
