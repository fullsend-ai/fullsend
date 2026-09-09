# shellcheck shell=sh
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
targets=$(printf '%s\n' "$listing" | awk -v me="$me" -v keep='sleep infinity' '
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
