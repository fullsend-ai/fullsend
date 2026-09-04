package runtime

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fullsend-ai/fullsend/internal/sandbox"
)

// killStrayProcessesTimeout bounds the sweep exec. The snippet itself waits
// at most 2s between TERM and KILL, so anything beyond a few seconds is the
// gateway, not the sandbox.
const killStrayProcessesTimeout = 15 * time.Second

// killStrayProcessesTemplate is the POSIX sh snippet ClearIterationArtifacts
// runs through `sandbox exec` before it removes the previous iteration's
// files. __KEEPALIVE__ is replaced with the shell-quoted
// sandbox.KeepAliveCommand by killStrayProcessesScript; the rendered result
// is pinned in testdata/kill_stray_processes.sh, which
// kill_stray_processes_test.sh runs under a real shell.
//
// Why: pi's built-in bash tool spawns commands detached and kills that
// process tree only on abort/timeout (pi 0.84.4, core/tools/bash.ts;
// killProcessTree call sites unchanged at 0.85.0), so a
// backgrounded command such as `nohup python3 -c 'import time;
// time.sleep(300)' &` survives the agent process's normal exit. Claude
// Code's Bash tool is closed source, but the same symptom was observed:
// after the agent exits, the survivors sit reparented to PID 1 in the
// sandbox. fullsend reuses the sandbox for a validation-retry iteration,
// where such a survivor keeps files open, burns CPU/memory and writes into
// the workspace the next iteration reads.
//
// Process-view assumptions, verified against OpenShell 0.0.116 and its
// source: `sandbox exec` runs `sh -c <command>` as the sandbox user after
// the supervisor drops privileges
// (crates/openshell-supervisor-process/src/process.rs). With the podman
// driver the supervisor is PID 1 owned by root
// (crates/openshell-driver-podman/src/container.rs sets `user: "0:0"` and
// the supervisor entrypoint), so it never appears in `ps -u <sandbox user>`;
// on any driver it is an ancestor of this shell, which is what the
// ancestry walk below spares — the root/PID 1 detail is not load-bearing.
// The keep-alive main process (sandbox.KeepAliveCommand) does run as the
// sandbox user with ppid 1, indistinguishable from a reparented stray by
// tree shape, so it is excluded by argv: the command word is compared
// with any leading directory stripped (`/usr/bin/sleep infinity` also
// counts), which means an agent-started literal `sleep infinity` is spared
// as a consequence. Killing the keep-alive would make the sandbox terminal
// on OpenShell 0.0.111+.
//
// Serialization: the runner's background credential refreshers write into
// the sandbox from the host. refreshOIDCToken's `openshell sandbox upload`
// runs a sandbox-user `/bin/bash -c 'mkdir -p … && cat | tar xf - -C …'`
// chain (ppid 1, indistinguishable from a stray) and tar truncates the
// target on open, so a kill mid-write would leave an empty .gcp-oidc-token
// until the next 4-minute tick; reseedOpenAIAuth's execs are the other
// writer. internal/cli/run.go therefore serializes the sweep against both
// through sandboxMu: the iteration loop holds it across
// ClearIterationArtifacts and the refreshers hold it across each upload or
// writing exec. The residual is the OpenAI seed itself, which writes
// atomically (`printf > tmp && mv -f`) and is retried on
// openAIRefreshBackoff.
//
// Only tools the sandbox image ships are used (procps ps, mawk, dash
// builtins, coreutils sleep/id) — no pkill/pgrep. A failed process listing
// exits 3 (distinct from the always-0 sweep result) so the Go side warns
// instead of trusting a silent zero; a failing `ps -p` liveness probe takes
// the same exit, after KILLing everything that was TERMed, because its
// empty answer is otherwise indistinguishable from "they are all gone".
const killStrayProcessesTemplate = `# shellcheck shell=sh
# Sweep processes left behind by the previous iteration. pi's bash tool
# spawns commands detached and kills that tree only on abort/timeout
# (pi 0.84.4, core/tools/bash.ts), so a backgrounded command (nohup ... &)
# outlives the agent; reparented survivors were observed after agent exit
# regardless of runtime, and because fullsend reuses the sandbox for the
# next iteration they keep running: holding files open, eating CPU,
# writing into the workspace the next iteration reads.
#
# Kills every process of the sandbox user except: this shell and its
# ancestors (the exec channel back to the runner), its own helpers,
# zombies, and the sandbox keep-alive main process. The keep-alive is
# matched on argv with any directory stripped from the command word
# (/usr/bin/sleep infinity counts), so an agent-started literal
# "sleep infinity" is spared as well. TERM first, then KILL whatever is
# still alive after 2s. The count goes to stdout; a process listing or a
# liveness probe that fails exits 3 so the runner warns instead of
# trusting a zero. The user is selected by numeric uid: the sandbox user
# need not be resolvable through NSS.
me=$$
listing=$(ps -o pid= -o ppid= -o stat= -o args= -u "$(id -u)" 2>/dev/null) || {
  echo 'stray processes: ps failed' >&2
  exit 3
}
targets=$(printf '%s\n' "$listing" | awk -v me="$me" -v keep=__KEEPALIVE__ '
  NF >= 4 {
    pid = $1
    parent[pid] = $2
    stat[pid] = $3
    line = $0
    sub(/^[ \t]*[0-9]+[ \t]+[0-9]+[ \t]+[^ \t]+[ \t]+/, "", line)
    sub(/^[^ \t]*\//, "", line)
    cmd[pid] = line
    order[++n] = pid
  }
  END {
    # This shell and everything above it: the exec channel to the runner.
    for (p = me; p in parent; p = parent[p]) {
      if (p in own) break
      own[p] = 1
    }
    own[me] = 1
    # Everything below this shell: the ps/awk helpers of this very snippet.
    do {
      changed = 0
      for (i = 1; i <= n; i++) {
        p = order[i]
        if (p in own || p in below) continue
        if (parent[p] == me || parent[p] in below) {
          below[p] = 1
          changed = 1
        }
      }
    } while (changed)
    for (i = 1; i <= n; i++) {
      p = order[i]
      if (p in own || p in below) continue
      if (substr(stat[p], 1, 1) == "Z") continue
      if (cmd[p] == keep) continue
      print p
    }
  }')
count=0
pids=""
signalled=""
for p in $targets; do
  if kill -s TERM "$p" 2>/dev/null; then
    count=$((count + 1))
    pids="$pids,$p"
    signalled="$signalled $p"
  fi
done
if [ "$count" -gt 0 ]; then
  pids=${pids#,}
  # alive: does any signalled target still exist at all (live or zombie)?
  # Only used to tell a broken probe from "they are all gone".
  alive() {
    for a in $signalled; do
      if kill -0 "$a" 2>/dev/null; then
        return 0
      fi
    done
    return 1
  }
  # survivors: signalled targets still present and not zombies (a killed
  # stray stays a zombie until its new parent reaps it); one ps per tick.
  # An empty answer is trusted only when nothing is left: a ps -p that
  # fails also prints nothing and exits 1, and reading that as "all dead"
  # would skip the KILL pass while still reporting a clean sweep. A probe
  # that fails prints "?" instead.
  survivors() {
    out=$(ps -o pid= -o stat= -p "$pids" 2>/dev/null)
    rc=$?
    if [ "$rc" -gt 1 ] || { [ -z "$out" ] && alive; }; then
      echo '?'
      return
    fi
    printf '%s\n' "$out" | awk 'NF && $2 !~ /^Z/ { print $1 }'
  }
  left=$(survivors)
  i=0
  while [ "$i" -lt 20 ] && [ -n "$left" ] && [ "$left" != '?' ]; do
    sleep 0.1
    i=$((i + 1))
    left=$(survivors)
  done
  if [ "$left" = '?' ]; then
    # The probe is broken, so which targets are still alive is unknown:
    # KILL every one that took the TERM rather than skip the pass, and
    # report it so the runner does not read the count as a clean sweep.
    for p in $signalled; do
      kill -s KILL "$p" 2>/dev/null
    done
    echo 'stray processes: ps -p failed' >&2
    exit 3
  fi
  for p in $left; do
    kill -s KILL "$p" 2>/dev/null
  done
fi
echo "stray processes killed: $count"
exit 0
`

// killStrayProcessesScript renders the sweep snippet with the sandbox
// keep-alive argv it must spare.
func killStrayProcessesScript() string {
	return strings.ReplaceAll(killStrayProcessesTemplate, "__KEEPALIVE__", shellQuote(sandbox.KeepAliveCommand))
}

var strayProcessesKilledRe = regexp.MustCompile(`(?m)^stray processes killed: ([0-9]+)$`)

// killStrayProcesses runs the sweep snippet in the sandbox through execFn
// (sandbox.Exec in production) and returns the number of processes it
// signalled. Any failure — a gateway error, a timeout, a non-zero exit
// (exit 3 is the snippet's own "ps failed"), or output the snippet never
// produces — is returned as an error for the caller to downgrade.
func killStrayProcesses(execFn sandboxExecFunc, sandboxName string) (int, error) {
	stdout, stderr, exitCode, err := execFn(sandboxName, killStrayProcessesScript(), killStrayProcessesTimeout)
	if err != nil {
		return 0, err
	}
	if exitCode != 0 {
		return 0, fmt.Errorf("exit %d: %s", exitCode, strings.TrimSpace(stderr))
	}
	m := strayProcessesKilledRe.FindStringSubmatch(stdout)
	if m == nil {
		return 0, fmt.Errorf("unexpected output: %q", strings.TrimSpace(stdout))
	}
	n, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		return 0, fmt.Errorf("unexpected count in output %q: %w", strings.TrimSpace(stdout), convErr)
	}
	return n, nil
}

// clearStrayProcesses is the warning-only front of killStrayProcesses used
// by ClearIterationArtifacts: a failed sweep must never fail the iteration,
// and the file cleanup that follows must still run. Progress (with the
// sweep's duration, which is the TERM grace period when something ignored
// TERM) and warnings go to w (stderr in production).
func clearStrayProcesses(execFn sandboxExecFunc, sandboxName string, w io.Writer) {
	start := time.Now()
	n, err := killStrayProcesses(execFn, sandboxName)
	elapsed := time.Since(start).Round(100 * time.Millisecond)
	if err != nil {
		fmt.Fprintf(w, "  Warning: could not terminate stray sandbox processes from the previous iteration (after %s): %v\n", elapsed, sanitizeOutput(err.Error()))
		return
	}
	if n > 0 {
		fmt.Fprintf(w, "  Terminated %d stray process(es) left running by the previous iteration (%s)\n", n, elapsed)
	}
}
