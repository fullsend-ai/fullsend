#!/usr/bin/env bash
# ci-local_test.sh — Tests for hack/ci-local/ci-local against a stub container
# engine: target validation, fail-closed targets, engine argv (isolation,
# user mapping, SELinux labels), git worktree and submodule mounts, and reading tool versions
# from the checkout's go.mod and lint.yml (#8479).
#
# Run from the repo root:
#   bash hack/ci-local/ci-local_test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CI_LOCAL="${SCRIPT_DIR}/ci-local"
FAILURES=0

pass() { echo "ok       $*"; }
fail() {
  echo "FAIL     $*" >&2
  FAILURES=$((FAILURES + 1))
}

WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT
export HOME="${WORK}/home" TMPDIR="${WORK}/tmp"
mkdir -p "${HOME}" "${TMPDIR}"
# A host secret that must never reach the engine's argv.
export CI_LOCAL_TEST_SECRET="s3cr3t-must-not-leak"
unset CI_LOCAL_ENGINE CI_LOCAL_DNS

ENGINE_LOG="${WORK}/engine.log"
export ENGINE_LOG

# Stub engine: logs each call (one arg per line after a CALL marker).
# `image inspect` exits STUB_IMAGE_RC (non-zero = image missing, forcing a
# build). `run` executes the container command on the host when STUB_EXEC=1,
# so the target logic itself is tested; otherwise it exits with STUB_RC.
write_engine() {
  local dir="${WORK}/bin-$1"
  mkdir -p "${dir}"
  cat > "${dir}/$1" <<'STUB'
#!/usr/bin/env bash
{ echo CALL; printf 'ARG %s\n' "$@"; } >> "${ENGINE_LOG}"
# Podman's override file per call, as "<subcommand> <path>".
while [ "${1#--}" != "$1" ]; do shift; done
echo "$1 ${CONTAINERS_CONF_OVERRIDE:-}" >> "${ENGINE_LOG}.env"
case "$1" in
  image) exit "${STUB_IMAGE_RC:-0}" ;;
  # STUB_REMOTE/STUB_ROOTLESS: what `podman info` reports as Host.ServiceIsRemote
  # and Host.Security.Rootless; STUB_INFO_RC fails that query.
  info)
    case "$*" in *ServiceIsRemote*) echo "${STUB_REMOTE:-false} ${STUB_ROOTLESS:-true}"; exit "${STUB_INFO_RC:-0}" ;; esac
    echo "${STUB_OS:-}"; exit 0 ;;
  # STUB_PODMAN_VERSION: what `podman version` reports as Client.Version.
  version) echo "${STUB_PODMAN_VERSION:-5.0.0}"; exit 0 ;;
  # clean: STUB_IMAGES/STUB_VOLUMES are the listings; STUB_BUSY is refused as in use.
  # STUB_LIST_RC fails both listings, as a stopped engine does.
  images) printf '%s\n' "${STUB_IMAGES:-}"; exit "${STUB_LIST_RC:-0}" ;;
  rmi) [ "$2" != "${STUB_BUSY:-}" ]; exit ;;
  volume)
    [ "$2" = ls ] && { printf '%s\n' "${STUB_VOLUMES:-}"; exit "${STUB_LIST_RC:-0}"; }
    # inspect: STUB_VOLUME_MISSING=1 says no volume exists; --format prints the
    # checkout label from STUB_VOLUME_LABELS ("<volume> <path>" lines).
    if [ "$2" = inspect ]; then
      [ "${STUB_VOLUME_MISSING:-0}" = 1 ] && exit 1
      [ "$3" != --format ] || awk -v v="${*: -1}" '$1 == v {sub(/^[^ ]+ /, ""); print}' <<< "${STUB_VOLUME_LABELS:-}"
      exit 0
    fi
    [ "$2" != rm ] || [ "$3" != "${STUB_BUSY:-}" ]; exit ;;
  build) exit 0 ;;
  run)
    # Docker's root volume helper (--user 0) exits STUB_HELPER_RC.
    for a in "$@"; do [ "$a" = 0 ] && [ "${prev:-}" = --user ] && exit "${STUB_HELPER_RC:-0}"; prev=$a; done
    [ "${STUB_EXEC:-0}" = 1 ] && exec bash -c "${*: -1}"
    exit "${STUB_RC:-0}" ;;
esac
exit 0
STUB
  chmod +x "${dir}/$1"
  echo "${dir}/$1"
}
PODMAN=$(write_engine podman)
PODMAN_REMOTE=$(write_engine podman-remote)
DOCKER=$(write_engine docker)

# Shims for tools the container command calls when STUB_EXEC=1.
SHIMS="${WORK}/shims"
mkdir -p "${SHIMS}"

# Stub scutil: prints STUB_SCUTIL_OUT (empty = no resolvers), so DNS argv
# never depends on the machine running the suite. On PATH everywhere, so
# Linux runs take the macOS parsing path too.
mkdir -p "${WORK}/bin-scutil"
cat > "${WORK}/bin-scutil/scutil" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "${STUB_SCUTIL_OUT:-}"
STUB
chmod +x "${WORK}/bin-scutil/scutil"
export PATH="${WORK}/bin-scutil:${PATH}"

# Stub uname: `uname -s` prints STUB_UNAME_S when set (remote Podman is
# refused on Linux); everything else goes to the real uname.
mkdir -p "${WORK}/bin-uname"
printf '#!/usr/bin/env bash
[ "$1" = -s ] && [ -n "${STUB_UNAME_S:-}" ] && { echo "$STUB_UNAME_S"; exit; }
exec %q "$@"
' \
  "$(command -v uname)" > "${WORK}/bin-uname/uname"
chmod +x "${WORK}/bin-uname/uname"
export PATH="${WORK}/bin-uname:${PATH}"

# Args of the last `run` call that is not Docker's root volume chmod.
last_run_args() {
  awk '/^CALL$/{n++} {a[n]=a[n] $0 "\n"} END{for(i=n;i>0;i--) if (a[i] ~ /\nARG run\n/ && a[i] !~ /ARG --user\nARG 0\n/) {printf "%s", a[i]; exit}}' "${ENGINE_LOG}" |
    sed -n 's/^ARG //p'
}

# Args of the last `build` call.
last_build_args() {
  awk '/^CALL$/{n++} {a[n]=a[n] $0 "\n"} END{for(i=n;i>0;i--) if (a[i] ~ /\nARG build\n/) {printf "%s", a[i]; exit}}' "${ENGINE_LOG}" |
    sed -n 's/^ARG //p'
}

# Host side of the last run's mount at $1 (e.g. /src/.git:ro).
mounted() { last_run_args | sed -n "s|:$1\$||p"; }
# Cache file prefix for checkout $2: mount_args names them <prefix>-<content hash>.
cache_prefix() { echo "${HOME}/.cache/ci-local/$1-$(printf '%s' "$2" | { sha256sum 2>/dev/null || shasum -a 256; } | cut -c1-12)-"; }

# Test checkouts carry the real CI config, so a lint.yml change that ci-local
# can no longer parse fails this suite in the same PR.
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
add_ci_config() {
  mkdir -p "$1/.github/workflows"
  cp "${ROOT}/.github/workflows/lint.yml" "$1/.github/workflows/lint.yml"
  cp "${ROOT}/go.mod" "$1/go.mod"
}

REPO="${WORK}/repo"
mkdir -p "${REPO}/.git"
add_ci_config "${REPO}"

run_ci_local() { # engine, then ci-local args; sets RC and OUT
  local engine=$1
  shift
  : > "${ENGINE_LOG}"; : > "${ENGINE_LOG}.env"
  set +e
  OUT=$(CI_LOCAL_ENGINE="${engine}" "${CI_LOCAL}" -C "${REPO}" "$@" 2>&1)
  RC=$?
  set -e
}

# --- target validation ---
STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" no-such-target
if [ "${RC}" -eq 2 ] && ! grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then
  pass "unknown target exits 2 without building or starting a container"
else
  fail "unknown target: rc=${RC}, engine log: $(tr '\n' ' ' < "${ENGINE_LOG}")"
fi

run_ci_local "${PODMAN}" go-vet no-such-target
if [ "${RC}" -eq 2 ] && ! grep -qx 'ARG run' "${ENGINE_LOG}"; then
  pass "an unknown target anywhere in the list stops before any target runs"
else
  fail "mixed valid/unknown targets: rc=${RC}"
fi

# build-image, shell and clean must run alone: extra targets were silently dropped.
bad_alone=""
for args in "build-image go-test" "build-image no-such-target" "go-test clean" "shell lint" "clean clean"; do
  # shellcheck disable=SC2086 # split the case into arguments
  STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" ${args}
  if [ "${RC}" -ne 2 ] || grep -qxE 'ARG (run|build|images|volume)' "${ENGINE_LOG}" ||
    ! grep -q 'must be the only target' <<< "${OUT}"; then
    bad_alone="${bad_alone} [${args}: rc=${RC}]"
  fi
done
if [ -z "${bad_alone}" ]; then
  pass "build-image/shell/clean with other targets exit 2 before touching the engine"
else
  fail "maintenance targets not alone:${bad_alone}"
fi

# -C without a directory is a usage error (exit 2), not bash's unbound-variable abort.
set +e
OUT=$(CI_LOCAL_ENGINE="${PODMAN}" "${CI_LOCAL}" -C 2>&1)
RC=$?
set -e
if [ "${RC}" -eq 2 ] && grep -q -- '-C needs a directory' <<< "${OUT}" && ! grep -q 'unbound variable' <<< "${OUT}"; then
  pass "-C without a directory exits 2 with a usage error"
else
  fail "-C without a directory: rc=${RC} out=${OUT}"
fi

# Targets that look like echo options must still be rejected, not swallowed.
bad_dash=""
for a in -n -e -E; do
  STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" "${a}"
  if [ "${RC}" -ne 2 ] || grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then bad_dash="${bad_dash} [${a}: rc=${RC}]"; fi
done
if [ -z "${bad_dash}" ]; then
  pass "-n/-e/-E are rejected as unknown targets (exit 2, no build or run)"
else
  fail "echo-option-shaped targets:${bad_dash}"
fi

# --- versions come from the checkout's CI config ---
STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" go-vet
BUILD=$(last_build_args)
arg() { grep -A1 -x -- '--build-arg' <<< "${BUILD}" | sed -n "s/^$1=//p"; }
want_go=$(awk '$1 == "go" {print $2; exit}' "${REPO}/go.mod")
missing=""
for v in GO_VERSION PYTHON_VERSION NODE_VERSION PIP_PACKAGES LYCHEE_VERSION LYCHEE_SHA256_AMD64 PINACT_VERSION PINACT_SHA256_AMD64; do
  [ -n "$(arg "$v")" ] || missing="${missing} $v"
done
if [ -z "${missing}" ] && [ "$(arg GO_VERSION)" = "${want_go}" ] &&
  grep -q "python-version: \"$(arg PYTHON_VERSION)\"" "${REPO}/.github/workflows/lint.yml" &&
  grep -q "$(arg LYCHEE_SHA256_AMD64)  /tmp/lychee.tar.gz" "${REPO}/.github/workflows/lint.yml" &&
  [[ "$(arg PINACT_SHA256_AMD64)" =~ ^[0-9a-f]{64}$ ]]; then
  pass "all build args are read from the real go.mod and lint.yml"
else
  fail "build args from CI config: missing=[${missing}] args=$(tr '\n' ' ' <<< "${BUILD}")"
fi
TAG1=$(grep -A1 -x -- '-t' <<< "${BUILD}" | tail -1)

case "$(uname -m)" in arm64 | aarch64) native=linux/arm64 ;; *) native=linux/amd64 ;; esac
if grep -A1 -x -- '--platform' <<< "${BUILD}" | grep -qx "${native}" &&
  grep -A1 -x -- '--platform' <<< "$(last_run_args)" | grep -qx "${native}" &&
  [[ "${TAG1}" == fullsend-ci-local:* ]]; then
  pass "a native build and run name the host platform (${native}) explicitly"
else
  fail "native platform not passed: build=$(tr '\n' ' ' <<< "${BUILD}")"
fi

sed -i.bak -E 's/(python-version: )"[0-9.]+"/\1"3.99"/' "${REPO}/.github/workflows/lint.yml"
STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" go-vet
TAG2=$(grep -A1 -x -- '-t' <<< "$(last_build_args)" | tail -1)
if [ -n "${TAG1}" ] && [ "${TAG1}" != "${TAG2}" ] && grep -qx 'PYTHON_VERSION=3.99' <<< "$(last_build_args)"; then
  pass "a version bump in lint.yml changes the image tag, forcing a rebuild"
else
  fail "image tag did not follow the version bump: ${TAG1} -> ${TAG2}"
fi

sed -i.bak -E '/python-version:/d' "${REPO}/.github/workflows/lint.yml"
STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'cannot read PYTHON_VERSION from lint.yml' <<< "${OUT}" &&
  ! grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then
  pass "an unreadable version fails before any build or run"
else
  fail "missing python-version must fail closed: rc=${RC} out=${OUT}"
fi

rm "${REPO}/.github/workflows/lint.yml"
run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'missing .*lint.yml' <<< "${OUT}"; then
  pass "a checkout without lint.yml fails closed"
else
  fail "missing lint.yml must fail: rc=${RC} out=${OUT}"
fi
add_ci_config "${REPO}"

run_ci_local "${PODMAN}" build-image
if grep -qx -- '--no-cache' <<< "$(last_build_args)"; then
  pass "build-image rebuilds with --no-cache to refresh floating versions"
else
  fail "build-image did not pass --no-cache"
fi

# Hosts that share an image store must not reuse each other's native image.
tag_for_arch() { # uname -m value -> image tag
  printf '#!/bin/sh\necho %s\n' "$1" > "${SHIMS}/uname"
  chmod +x "${SHIMS}/uname"
  PATH="${SHIMS}:${PATH}" STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" go-vet
  rm -f "${SHIMS}/uname"
  grep -A1 -x -- '-t' <<< "$(last_build_args)" | tail -1
}
ARM_TAG=$(tag_for_arch aarch64)
X86_TAG=$(tag_for_arch x86_64)
if [ -n "${ARM_TAG}" ] && [ "${ARM_TAG}" != "${X86_TAG}" ]; then
  pass "native arm64 and amd64 builds get different image tags"
else
  fail "native arches share an image tag: ${ARM_TAG} vs ${X86_TAG}"
fi

# The fetch/sumof helpers are copied into the image, so changing one must
# change the tag. Run a copy of hack/ci-local so the real files stay untouched.
COPY_DIR="${WORK}/ci-local-copy"
mkdir -p "${COPY_DIR}"
cp "${SCRIPT_DIR}/ci-local" "${SCRIPT_DIR}/Containerfile" "${SCRIPT_DIR}/fetch" "${SCRIPT_DIR}/sumof" "${COPY_DIR}/"
copy_tag() {
  : > "${ENGINE_LOG}"
  CI_LOCAL_ENGINE="${PODMAN}" STUB_IMAGE_RC=1 "${COPY_DIR}/ci-local" -C "${REPO}" go-vet >/dev/null 2>&1 || true
  grep -A1 -x -- '-t' <<< "$(last_build_args)" | tail -1
}
T_BEFORE=$(copy_tag)
echo "# changed" >> "${COPY_DIR}/sumof"
T_SUMOF=$(copy_tag)
echo "# changed" >> "${COPY_DIR}/fetch"
T_FETCH=$(copy_tag)
if [ -n "${T_BEFORE}" ] && [ "${T_BEFORE}" != "${T_SUMOF}" ] && [ "${T_SUMOF}" != "${T_FETCH}" ]; then
  pass "changing fetch or sumof changes the image tag, forcing a rebuild"
else
  fail "helper change did not change the tag: ${T_BEFORE} / ${T_SUMOF} / ${T_FETCH}"
fi

# --- Containerfile: build-time downloads are never piped into a parser ---
# sh has no pipefail, so `curl ... | jq` would turn a failed download into a
# misleading "no release matches".
if ! grep -nE 'curl [^|]*\|' "${SCRIPT_DIR}/Containerfile"; then
  pass "Containerfile: no curl output piped into a parser"
else
  fail "Containerfile pipes curl output (a failed download would be hidden)"
fi

# --- sumof: checksum-file formats (local file:// URLs, no network) ---
SUMS="${WORK}/sums"
mkdir -p "${SUMS}"
printf '%s  other.tar.gz\n%s  tool-linux.tar.gz\n' "$(printf 'a%.0s' {1..64})" "$(printf 'b%.0s' {1..64})" > "${SUMS}/two-space.txt"
printf '%s *tool-linux.tar.gz\n' "$(printf 'c%.0s' {1..64})" > "${SUMS}/binary-mode.txt"
printf '%s\n' "$(printf 'd%.0s' {1..64})" > "${SUMS}/lone.sha256"
sumof_out() { "${SCRIPT_DIR}/sumof" "file://${SUMS}/$1" "$2" 2> /dev/null; }
if [ "$(sumof_out two-space.txt tool-linux.tar.gz)" = "$(printf 'b%.0s' {1..64})" ] &&
  [ "$(sumof_out binary-mode.txt tool-linux.tar.gz)" = "$(printf 'c%.0s' {1..64})" ] &&
  [ "$(sumof_out lone.sha256 tool-linux.tar.gz)" = "$(printf 'd%.0s' {1..64})" ]; then
  pass "sumof reads '<sum>  <name>', '<sum> *<name>' and lone-'<sum>' checksum files"
else
  fail "sumof formats: two-space=$(sumof_out two-space.txt tool-linux.tar.gz) binary=$(sumof_out binary-mode.txt tool-linux.tar.gz) lone=$(sumof_out lone.sha256 tool-linux.tar.gz)"
fi
set +e
"${SCRIPT_DIR}/sumof" "file://${SUMS}/two-space.txt" missing.tar.gz > /dev/null 2>&1; rc_missing=$?
"${SCRIPT_DIR}/sumof" "file://${SUMS}/two-space.txt" > /dev/null 2>&1; rc_usage=$?
set -e
if [ "${rc_missing}" -ne 0 ] && [ "${rc_usage}" -eq 2 ]; then
  pass "sumof fails for an unlisted name and on wrong usage"
else
  fail "sumof must fail: missing-name rc=${rc_missing}, usage rc=${rc_usage}"
fi

# sumof: a malformed checksum in the file, and a failed download, each fail with their own error.
printf 'zz%s  tool-linux.tar.gz\n' "$(printf 'a%.0s' {1..62})" > "${SUMS}/bad-hex.txt"
# Uppercase too: a bracket range like [a-f] matches it under some locales (macOS en_US.UTF-8).
printf '%s  tool-linux.tar.gz\n' "$(printf 'A%.0s' {1..64})" > "${SUMS}/upper-hex.txt"
set +e
bad_out=$("${SCRIPT_DIR}/sumof" "file://${SUMS}/bad-hex.txt" tool-linux.tar.gz 2>&1); rc_bad=$?
upper_out=$("${SCRIPT_DIR}/sumof" "file://${SUMS}/upper-hex.txt" tool-linux.tar.gz 2>&1); rc_upper=$?
dl_out=$("${SCRIPT_DIR}/sumof" "file://${SUMS}/does-not-exist.txt" tool-linux.tar.gz 2>&1); rc_dl=$?
set -e
if [ "${rc_bad}" -ne 0 ] && grep -q 'malformed checksum' <<< "${bad_out}" &&
  [ "${rc_upper}" -ne 0 ] && grep -q 'malformed checksum' <<< "${upper_out}" &&
  [ "${rc_dl}" -ne 0 ] && grep -q '^curl: ' <<< "${dl_out}" && ! grep -q 'no checksum' <<< "${dl_out}"; then
  pass "sumof rejects a malformed (or uppercase) checksum and reports a failed download as such"
else
  fail "sumof malformed/download: bad rc=${rc_bad} [${bad_out}] upper rc=${rc_upper} [${upper_out}] download rc=${rc_dl} [${dl_out}]"
fi

# fetch: a checksum that isn't 64 lowercase hex characters is rejected before any download.
bad_fetch=""
hex64=$(printf 'a%.0s' {1..64})
for d in "" "${hex64:0:63}" "$(printf 'A%.0s' {1..64})" "${hex64}"'\040\040/dev/null' "${hex64}"$'\n'"${hex64}"; do
  set +e
  out=$("${SCRIPT_DIR}/fetch" "file://${SUMS}/does-not-exist" "${d}" 2>&1); rc=$?
  set -e
  if [ "${rc}" -ne 2 ] || ! grep -q 'not a SHA-256 digest' <<< "${out}" || grep -q '^curl: ' <<< "${out}"; then
    bad_fetch="${bad_fetch} [$(printf '%q' "${d}"): rc=${rc}]"
  fi
done
if [ -z "${bad_fetch}" ]; then
  pass "fetch rejects malformed checksums (short, uppercase, escape payload, newline) before downloading"
else
  fail "fetch malformed checksums:${bad_fetch}"
fi

# --- fail-closed targets (command executed on the host via the stub) ---
cat > "${SHIMS}/git" <<'SHIM'
#!/usr/bin/env bash
case "$1" in
  config) exit 0 ;;
  rev-list) echo "fatal: bad revision 'origin/main..HEAD'" >&2; exit 128 ;;
esac
exit 0
SHIM
chmod +x "${SHIMS}/git"
PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" commit-lint
if [ "${RC}" -ne 0 ] && grep -q 'commit-lint *FAIL' <<< "${OUT}"; then
  pass "commit-lint fails when git rev-list fails (missing origin/main)"
else
  fail "commit-lint with failing rev-list must FAIL: rc=${RC} out=${OUT}"
fi

cat > "${SHIMS}/make" <<'SHIM'
#!/usr/bin/env bash
echo "suite a ok"
echo "make: [Makefile:1: script-test] Error 1 (ignored)"
echo "suite b: 2 cases (ignored) by filter"
echo "suite c ok"
echo "make: [Makefile:3: script-test] Error 1 (ignored)"
echo "make[1]: [sub.mk:9: inner] Error 2 (ignored)"
# GNU make 4.3's line when a recipe's own shell is killed by a signal.
echo "make: [Makefile:5: script-test] Killed (ignored)"
# A suite whose output lacks a trailing newline: make's line follows it directly.
printf 'suite d: FAIL'
echo "make: [Makefile:7: script-test] Error 1 (ignored)"
exit 0
SHIM
chmod +x "${SHIMS}/make"
PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" script-test
if [ "${RC}" -ne 0 ] && grep -q 'suite c ok' <<< "${OUT}" &&
  grep -q 'script-test: 5 suite(s) failed' <<< "${OUT}"; then
  pass "script-test runs every suite and counts make's (ignored) error and signal lines"
else
  fail "script-test failure reporting: rc=${RC} out=${OUT}"
fi

printf '#!/usr/bin/env bash\necho "suite a ok"\nexit 0\n' > "${SHIMS}/make"
PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" script-test
if [ "${RC}" -eq 0 ] && grep -q 'script-test: 0 suite(s) failed' <<< "${OUT}"; then
  pass "script-test passes when no suite fails"
else
  fail "script-test clean run: rc=${RC} out=${OUT}"
fi

printf '#!/usr/bin/env bash\necho "make: crashed" >&2\nexit 2\n' > "${SHIMS}/make"
PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" script-test
if [ "${RC}" -ne 0 ] && grep -q 'script-test: make did not run' <<< "${OUT}"; then
  pass "script-test fails when make itself cannot run"
else
  fail "script-test with broken make must FAIL: rc=${RC} out=${OUT}"
fi

printf '#!/usr/bin/env bash\necho "main.go:3:1: expected declaration" >&2\nexit 2\n' > "${SHIMS}/gofmt"
chmod +x "${SHIMS}/gofmt"
PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" go-fmt
if [ "${RC}" -ne 0 ] && grep -q 'gofmt: failed to run' <<< "${OUT}" && ! grep -q 'gofmt: clean' <<< "${OUT}"; then
  pass "go-fmt fails when gofmt itself fails (no stdout), not 'clean'"
else
  fail "go-fmt with failing gofmt must FAIL: rc=${RC} out=${OUT}"
fi

# go-fmt ignores npm packages' .go files in both node_modules volumes; gofmt -l .
# prints the top-level one without a leading slash.
printf '#!/usr/bin/env bash\nprintf "%%s\\n" node_modules/flatted/flatted.go internal/dispatch/cf/workersrc/node_modules/x/y.go ${GOFMT_EXTRA:-}\n' > "${SHIMS}/gofmt"
PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" go-fmt
RC_NM=${RC} OUT_NM=${OUT}
GOFMT_EXTRA=internal/cli/bad.go PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" go-fmt
if [ "${RC_NM}" -eq 0 ] && grep -q 'gofmt: clean' <<< "${OUT_NM}" &&
  [ "${RC}" -ne 0 ] && grep -qx 'internal/cli/bad.go' <<< "${OUT}" && ! grep -q 'node_modules/' <<< "${OUT}"; then
  pass "go-fmt ignores both node_modules directories, and still reports the checkout's files"
else
  fail "go-fmt node_modules filter: nm rc=${RC_NM} [${OUT_NM}] extra rc=${RC} [${OUT}]"
fi

# coverage: make go-test succeeds, but the base-branch diff fails.
printf '#!/usr/bin/env bash\nexit 0\n' > "${SHIMS}/make"
cat > "${SHIMS}/git" <<'SHIM'
#!/usr/bin/env bash
case "$1" in
  config) exit 0 ;;
  diff) echo "fatal: bad revision 'origin/main...HEAD'" >&2; exit 128 ;;
esac
exit 0
SHIM
PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" coverage
if [ "${RC}" -ne 0 ] && grep -q 'coverage *FAIL' <<< "${OUT}"; then
  pass "coverage fails when the origin/main diff fails"
else
  fail "coverage without origin/main must FAIL: rc=${RC} out=${OUT}"
fi
rm -f "${SHIMS}/git" "${SHIMS}/make" "${SHIMS}/gofmt"

# Tests below run the target command (STUB_EXEC=1) in a real git repo, from its directory.
new_git_repo() { # dir: a repo with one commit, which origin/main points at
  mkdir -p "$1"
  git -C "$1" init -q
  git -C "$1" -c user.name=t -c user.email=t@t commit -q --allow-empty -m base
  git -C "$1" update-ref refs/remotes/origin/main HEAD
  add_ci_config "$1"
}
run_in_repo() { # dir, engine, ci-local args: run_ci_local from inside dir
  local dir=$1 saved_repo="${REPO}"
  shift
  REPO="${dir}"
  pushd "${dir}" > /dev/null
  PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "$@"
  popd > /dev/null
  REPO="${saved_repo}"
}

# coverage reports committed, staged, unstaged and new (untracked) Go files, not tests.
COV="${WORK}/cov-repo"
new_git_repo "${COV}"
for f in committed staged unstaged; do echo "package p" > "${COV}/${f}.go"; done
git -C "${COV}" add committed.go unstaged.go
git -C "${COV}" -c user.name=t -c user.email=t@t commit -q -m change
echo "// edit" >> "${COV}/unstaged.go"
git -C "${COV}" add staged.go
echo "package p" > "${COV}/untracked.go"
echo "package p" > "${COV}/untracked_test.go"
cat > "${SHIMS}/make" <<'SHIM'
#!/usr/bin/env bash
for f in committed staged unstaged untracked untracked_test; do
  echo "github.com/fullsend-ai/fullsend/${f}.go:1.1,2.2 1 1"
done > coverage.out
SHIM
chmod +x "${SHIMS}/make"
run_in_repo "${COV}" "${PODMAN}" coverage
reported=$(grep -oE '[a-z_]+\.go \(' <<< "${OUT}" | tr -d ' (' | sort | tr '\n' ' ')
if [ "${RC}" -eq 0 ] && [ "${reported}" = "committed.go staged.go unstaged.go untracked.go " ]; then
  pass "coverage reports committed, staged, unstaged and new Go files (not tests)"
else
  fail "coverage files: rc=${RC} reported=[${reported}] out=[${OUT}]"
fi

# coverage counts each block once, covered if any profile hit it: make go-test
# merges two profiles that both cover internal/mintcore.
cat > "${SHIMS}/make" <<'SHIM'
#!/usr/bin/env bash
{ echo "mode: atomic"
  echo "github.com/fullsend-ai/fullsend/committed.go:1.1,2.2 2 0"
  echo "github.com/fullsend-ai/fullsend/committed.go:3.1,4.2 2 0"
  echo "github.com/fullsend-ai/fullsend/committed.go:1.1,2.2 2 3"
  echo "github.com/fullsend-ai/fullsend/committed.go:3.1,4.2 2 0"; } > coverage.out
SHIM
chmod +x "${SHIMS}/make"
run_in_repo "${COV}" "${PODMAN}" coverage
if [ "${RC}" -eq 0 ] && grep -qE '^ +50\.0%  github.com/fullsend-ai/fullsend/committed.go \(4 stmts\)$' <<< "${OUT}"; then
  pass "coverage counts a block in both merged profiles once"
else
  fail "coverage with merged profiles: rc=${RC} out=[${OUT}]"
fi
rm -f "${SHIMS}/make"

# Without -C, ci-local tests the checkout containing the current directory,
# or the current directory outside a git checkout.
mkdir -p "${COV}/sub/dir"
RC_SUB=0 RC_PLAIN=0
(cd "${COV}/sub/dir" && CI_LOCAL_ENGINE="${PODMAN}" "${CI_LOCAL}" go-vet > /dev/null 2>&1) || RC_SUB=$?
ARGS_SUB=$(last_run_args)
(cd "${WORK}/repo" && CI_LOCAL_ENGINE="${PODMAN}" "${CI_LOCAL}" go-vet > /dev/null 2>&1) || RC_PLAIN=$?
ARGS_PLAIN=$(last_run_args)
RC_C=0
(cd "${WORK}" && CI_LOCAL_ENGINE="${PODMAN}" "${CI_LOCAL}" -C cov-repo/sub/dir go-vet > /dev/null 2>&1) || RC_C=$?
ARGS_C=$(last_run_args)
if [ "${RC_SUB}" -eq 0 ] && grep -qx "${COV}:/src" <<< "${ARGS_SUB}" &&
  [ "${RC_PLAIN}" -eq 0 ] && grep -qx "${WORK}/repo:/src" <<< "${ARGS_PLAIN}" &&
  [ "${RC_C}" -eq 0 ] && grep -qx "${COV}:/src" <<< "${ARGS_C}"; then
  pass "the checkout containing the current directory, or -C's directory, is tested (or the directory itself)"
else
  fail "default checkout: sub rc=${RC_SUB} [$(grep ':/src$' <<< "${ARGS_SUB}")] plain rc=${RC_PLAIN} [$(grep ':/src$' <<< "${ARGS_PLAIN}")] -C rc=${RC_C} [$(grep ':/src$' <<< "${ARGS_C}")]"
fi

# With CDPATH set, cd prints the directory it found there; -C must not capture
# that. -C resolves against the current directory only, as git -C does.
RC=0
# The script itself is started by a relative path too, for its own directory lookup.
ln -sfn "${SCRIPT_DIR}" "${WORK}/ci-local-dir"
(cd "${WORK}" && CDPATH="${WORK}" CI_LOCAL_ENGINE="${PODMAN}" ci-local-dir/ci-local -C cov-repo go-vet > /dev/null 2>&1) || RC=$?
if [ "${RC}" -eq 0 ] && grep -qx "${COV}:/src" <<< "$(last_run_args)"; then
  pass "-C with a relative directory found through CDPATH"
else
  fail "-C through CDPATH: rc=${RC} [$(last_run_args | grep ':/src$')]"
fi

# lint and lint-all install node_modules only when staged docs/.vitepress files need
# lint-staged (which lints staged files, even under --all-files) and it is missing.
printf '#!/usr/bin/env bash\necho "NPM-CALLED $*"\n' > "${SHIMS}/npm"
printf '#!/usr/bin/env bash\necho "MAKE $*"\n' > "${SHIMS}/make"
chmod +x "${SHIMS}/npm" "${SHIMS}/make"
for t in lint lint-all; do
  LNT="${WORK}/${t}-repo"
  new_git_repo "${LNT}"
  mkdir -p "${LNT}/docs/.vitepress" && echo x > "${LNT}/docs/.vitepress/a.ts"
  echo x > "${LNT}/other.md"
  git -C "${LNT}" add other.md
  run_in_repo "${LNT}" "${PODMAN}" "${t}"; LINT_OTHER="${OUT}"
  git -C "${LNT}" add docs/.vitepress/a.ts
  run_in_repo "${LNT}" "${PODMAN}" "${t}"; LINT_VP="${OUT}"
  mkdir -p "${LNT}/node_modules/.bin" && printf '#!/bin/sh\n' > "${LNT}/node_modules/.bin/lint-staged"
  chmod +x "${LNT}/node_modules/.bin/lint-staged"
  run_in_repo "${LNT}" "${PODMAN}" "${t}"; LINT_HAVE="${OUT}"
  if grep -q 'NPM-CALLED ci' <<< "${LINT_VP}" && grep -qx "MAKE ${t}" <<< "${LINT_VP}" &&
    ! grep -q 'NPM-CALLED' <<< "${LINT_OTHER}" && grep -qx "MAKE ${t}" <<< "${LINT_OTHER}" &&
    ! grep -q 'NPM-CALLED' <<< "${LINT_HAVE}"; then
    pass "${t} runs npm ci only for staged docs/.vitepress files without lint-staged installed"
  else
    fail "${t} npm ci: vitepress=[${LINT_VP}] other=[${LINT_OTHER}] installed=[${LINT_HAVE}]"
  fi
done
rm -f "${SHIMS}/npm" "${SHIMS}/make"

# --- engine argv: isolation ---
run_ci_local "${PODMAN}" go-vet
ARGS=$(last_run_args)
envs=$(grep -A1 -x -- '-e' <<< "${ARGS}" | grep -vx -- '-e' | grep -vx -- '--' || true)
if [ "${envs}" = "HOME=/home/runner" ]; then
  pass "only HOME is passed into the container environment"
else
  fail "unexpected -e values: ${envs}"
fi
if ! grep -qE -- '--env-host|--env-file|--env-merge' <<< "${ARGS}" && ! grep -q "${CI_LOCAL_TEST_SECRET}" "${ENGINE_LOG}"; then
  pass "host environment is not forwarded (no --env-host/--env-file, secret absent)"
else
  fail "host environment leaked into engine argv"
fi
mounts=$(grep -A1 -x -- '-v' <<< "${ARGS}" | grep -vx -- '-v' | grep -vx -- '--' || true)
bad=$(grep -vE "^(${REPO}:/src|fullsend-ci-local-[a-z-]+:/home/runner/[^ ]+|fullsend-ci-local-nm-[a-z]+-[0-9a-f]{12}:/src/.*node_modules)$" <<< "${mounts}" || true)
if [ -z "${bad}" ] && ! grep -qE '\.ssh|\.gitconfig|SSH_AUTH_SOCK|docker\.sock|\.config/gh|\.aws|gcloud' <<< "${ARGS}"; then
  pass "mounts are only the checkout and named volumes; no credentials"
else
  fail "unexpected mounts: ${bad}"
fi
if grep -qx -- '--http-proxy=false' <<< "${ARGS}"; then
  pass "podman runs with --http-proxy=false (host proxy vars not copied in)"
else
  fail "podman run is missing --http-proxy=false"
fi
CMD=$(tail -1 <<< "${ARGS}")
if grep -q 'safe.directory /src;' <<< "${CMD}" && grep -q 'safe.directory /gitcommon;' <<< "${CMD}" &&
  ! grep -q 'safe.directory "\*"' <<< "${CMD}"; then
  pass "safe.directory trusts only /src and /gitcommon, not every repo"
else
  fail "safe.directory scope: ${CMD}"
fi
run_ci_local "${PODMAN}" docs
DOCS_CMD=$(tail -1 <<< "$(last_run_args)")
if grep -q 'VPL_MVB_BRANCH="$sha"' <<< "${DOCS_CMD}" && grep -q 'GITHUB_ACTIONS=true' <<< "${DOCS_CMD}" &&
  grep -q 'git submodule update --init; GITHUB_ACTIONS=true' <<< "${DOCS_CMD}" &&
  grep -q 'git submodule update --init' "${ROOT}/.github/workflows/site-build.yml"; then
  pass "docs initializes submodules and passes VPL_MVB_BRANCH/GITHUB_ACTIONS like site-build.yml"
else
  fail "docs target diverges from site-build.yml (submodules, VPL_MVB_BRANCH, GITHUB_ACTIONS)"
fi

# N2: node_modules volumes are per checkout; download caches are shared.
nm_vols() { grep -A1 -x -- '-v' <<< "$(last_run_args)" | grep -E '^fullsend-ci-local-nm-' | sort; }
cache_vols() { grep -A1 -x -- '-v' <<< "$(last_run_args)" | grep -E '^fullsend-ci-local-(go|gobuild|precommit|uv|npm):' | sort; }
OTHER="${WORK}/other-checkout"
mkdir -p "${OTHER}/.git"
add_ci_config "${OTHER}"
run_ci_local "${PODMAN}" go-vet; NM_A=$(nm_vols); CACHE_A=$(cache_vols)
run_ci_local "${PODMAN}" go-vet; NM_A2=$(nm_vols)
SAVED_REPO="${REPO}"; REPO="${OTHER}"
run_ci_local "${PODMAN}" go-vet; NM_B=$(nm_vols); CACHE_B=$(cache_vols)
REPO="${SAVED_REPO}"
if [ "$(wc -l <<< "${NM_A}")" -eq 2 ] && [ "${NM_A}" = "${NM_A2}" ] && [ "${NM_A}" != "${NM_B}" ] &&
  [ -n "${CACHE_A}" ] && [ "${CACHE_A}" = "${CACHE_B}" ]; then
  pass "node_modules volumes are per checkout (stable per path); download caches are shared"
else
  fail "volume scoping: A=[${NM_A}] A2=[${NM_A2}] B=[${NM_B}] cacheA=[${CACHE_A}] cacheB=[${CACHE_B}]"
fi

# N3: podman builds also run with --http-proxy=false; docker builds don't get the podman-only flag.
STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" go-vet
PB=$(last_build_args)
STUB_IMAGE_RC=1 run_ci_local "${DOCKER}" go-vet
DB=$(last_build_args)
if grep -qx -- '--http-proxy=false' <<< "${PB}" && ! grep -q -- '--http-proxy' <<< "${DB}" && [ -n "${DB}" ]; then
  pass "podman builds run with --http-proxy=false; docker builds get no podman-only flag"
else
  fail "build proxy flag: podman=[$(tr '\n' ' ' <<< "${PB}")] docker=[$(tr '\n' ' ' <<< "${DB}")]"
fi
# Docker: every proxy variable is set empty on the build, the volume chmod run
# and the main run, so ~/.docker/config.json proxies never apply. Podman
# (--http-proxy=false) gets none of them.
PROXY_VARS="HTTP_PROXY= http_proxy= HTTPS_PROXY= https_proxy= FTP_PROXY= ftp_proxy= NO_PROXY= no_proxy= ALL_PROXY= all_proxy="
proxy_vals() { grep -A1 -x -- "$1" | grep -vx -- "$1" | grep -vx -- '--' | grep -i proxy | tr '\n' ' ' | sed 's/ $//' || true; }
run_ci_local "${DOCKER}" go-vet
RUN_PROXY=$(last_run_args | proxy_vals -e)
PER_RUN=$(sed -n 's/^ARG //p' "${ENGINE_LOG}" | { grep -ix '[a-z_]*proxy=' || true; } | sort | uniq -c | awk '{print $1}' | sort -u)
if [ "$(proxy_vals --build-arg <<< "${DB}")" = "${PROXY_VARS}" ] && [ "${RUN_PROXY}" = "${PROXY_VARS}" ] &&
  [ "${PER_RUN}" = 2 ] && [ -z "$(proxy_vals --build-arg <<< "${PB}")" ] &&
  ! grep -qiE 'ARG [a-z_]*proxy=.' "${ENGINE_LOG}"; then
  pass "docker: all proxy variables empty on build and both runs; podman builds get none"
else
  fail "docker proxies: build=[$(proxy_vals --build-arg <<< "${DB}")] run=[${RUN_PROXY}] per-run=[${PER_RUN}]"
fi

# Both lint targets skip the hook lint.yml skips (it starts its own container).
LINT_SKIP=$(sed -n 's/^ *SKIP: *//p' "${ROOT}/.github/workflows/lint.yml" | head -1)
run_ci_local "${PODMAN}" lint
LINT_CMD=$(last_run_args | tail -1)
run_ci_local "${PODMAN}" lint-all
LINT_ALL_CMD=$(last_run_args | tail -1)
if [ -n "${LINT_SKIP}" ] && [[ "${LINT_CMD}" == *"; SKIP=${LINT_SKIP} make lint" ]] &&
  [[ "${LINT_ALL_CMD}" == *"; SKIP=${LINT_SKIP} make lint-all" ]]; then
  pass "lint and lint-all both skip ${LINT_SKIP}, as lint.yml does"
else
  fail "lint skip: lint.yml=[${LINT_SKIP}] lint=[${LINT_CMD##*; }] lint-all=[${LINT_ALL_CMD##*; }]"
fi

# Docker builds runner with the host uid:gid it later runs as; podman (keep-id) passes neither.
if grep -qx "RUNNER_UID=$(id -u)" <<< "${DB}" && grep -qx "RUNNER_GID=$(id -g)" <<< "${DB}" &&
  ! grep -q 'RUNNER_' <<< "${PB}"; then
  pass "docker builds pass RUNNER_UID/RUNNER_GID = host uid:gid; podman builds don't"
else
  fail "runner ids: docker=[$(grep RUNNER_ <<< "${DB}" | tr '\n' ' ')] podman=[$(grep RUNNER_ <<< "${PB}" | tr '\n' ' ')]"
fi

# --- engine argv: user mapping and SELinux ---
if grep -qx -- '--userns=keep-id' <<< "${ARGS}" && ! grep -qx -- '--user' <<< "${ARGS}"; then
  pass "podman uses --userns=keep-id"
else
  fail "podman user mapping: ${ARGS}"
fi

run_ci_local "${DOCKER}" go-vet
ARGS=$(last_run_args)
if grep -A1 -x -- '--user' <<< "${ARGS}" | grep -qx "$(id -u):$(id -g)" && ! grep -q 'keep-id' <<< "${ARGS}" &&
  ! grep -q -- '--http-proxy' <<< "${ARGS}"; then
  pass "docker uses --user uid:gid, no keep-id, no podman-only flags"
else
  fail "docker user mapping: ${ARGS}"
fi
if grep -qx 'ARG 0' "${ENGINE_LOG}" && grep -q 'chmod 1777' "${ENGINE_LOG}"; then
  pass "docker opens up its named volumes before the run"
else
  fail "docker volume chmod missing"
fi
# A failed root helper fails the target before the main container starts.
STUB_HELPER_RC=1 run_ci_local "${DOCKER}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'go-vet *FAIL' <<< "${OUT}" &&
  grep -q 'could not open up the docker volumes' <<< "${OUT}" && [ -z "$(last_run_args)" ]; then
  pass "docker: a failed volume helper fails the target without starting the main container"
else
  fail "failed volume helper: rc=${RC} main-run=[$(last_run_args | tr '\n' ' ')] out=[${OUT}]"
fi
run_ci_local "${DOCKER}" go-vet
# The root helper must not reach the checkout or run anything from a volume.
HELPER=$(awk '/^CALL$/{n++} {a[n]=a[n] $0 "\n"} END{for(i=1;i<=n;i++) if (a[i] ~ /\nARG --user\nARG 0\n/) {printf "%s", a[i]; exit}}' "${ENGINE_LOG}" |
  sed -n 's/^ARG //p')
helper_mounts=$(grep -A1 -x -- '-v' <<< "${HELPER}" | grep -vx -- '-v' | grep -vx -- '--' || true)
if [ -n "${helper_mounts}" ] && ! grep -q '^/' <<< "${helper_mounts}" &&
  grep -A1 -x -- '-e' <<< "${HELPER}" | grep -qx 'PATH=/usr/sbin:/usr/bin:/sbin:/bin' &&
  grep -A1 -x -- '--network' <<< "${HELPER}" | grep -qx 'none' &&
  [ "$(tail -3 <<< "${HELPER}" | head -2 | tr '\n' ' ')" = "/bin/sh -c " ] && [[ "$(tail -1 <<< "${HELPER}")" == "/bin/chmod 1777 "* ]]; then
  pass "docker root helper: named volumes only, fixed PATH, no network, absolute /bin/sh and /bin/chmod"
else
  fail "docker root helper: $(tr '\n' ' ' <<< "${HELPER}")"
fi

if ! grep -q ':/src:z' <<< "${ARGS}"; then
  pass "no :z label without SELinux"
else
  fail ":z added without SELinux"
fi
printf '#!/bin/sh\necho Enforcing\n' > "${SHIMS}/getenforce"
chmod +x "${SHIMS}/getenforce"
PATH="${SHIMS}:${PATH}" run_ci_local "${PODMAN}" go-vet
ARGS=$(last_run_args)
if grep -qx "${REPO}:/src:z" <<< "${ARGS}"; then
  pass ":z label added when SELinux is enforcing"
else
  fail "SELinux enforcing: checkout mount not labelled"
fi
rm -f "${SHIMS}/getenforce"

# --- --amd64: platform on build and run, separate image and volumes ---
STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" --amd64 go-vet
BUILD=$(last_build_args)
ARGS=$(last_run_args)
vols=$(grep -A1 -x -- '-v' <<< "${ARGS}" | grep '^fullsend-ci-local' || true)
if grep -A1 -x -- '--platform' <<< "${BUILD}" | grep -qx linux/amd64 &&
  grep -A1 -x -- '--platform' <<< "${ARGS}" | grep -qx linux/amd64 &&
  grep -A1 -x -- '-t' <<< "${BUILD}" | tail -1 | grep -q '^fullsend-ci-local-amd64:' &&
  [ -n "${vols}" ] && ! grep -qv '^fullsend-ci-local-amd64-' <<< "${vols}"; then
  pass "--amd64 builds and runs linux/amd64 with its own image and volumes"
else
  fail "--amd64 plumbing: build=$(tr '\n' ' ' <<< "${BUILD}") run=$(tr '\n' ' ' <<< "${ARGS}")"
fi

# --- git worktree mounts ---
MAIN="${WORK}/main"
git init -q "${MAIN}"
add_ci_config "${MAIN}"
git -C "${MAIN}" add .
git -C "${MAIN}" -c user.name=t -c user.email=t@t commit -q -m init
git -C "${MAIN}" worktree add -q "${WORK}/wt" -b wt
REPO="${WORK}/wt"
run_ci_local "${PODMAN}" go-vet
ARGS=$(last_run_args)
COMMON=$(git -C "${REPO}" rev-parse --path-format=absolute --git-common-dir)
PTR=$(mounted /src/.git:ro)
if grep -qx "${COMMON}:/gitcommon" <<< "${ARGS}" && [[ "${PTR}" == "$(cache_prefix gitfile "${REPO}")"* ]] &&
  [ "$(cat "${PTR}")" = "gitdir: /gitcommon/worktrees/wt" ]; then
  pass "worktree: git common dir at /gitcommon and .git overlaid with a pointer"
else
  fail "worktree mounts: ${ARGS}"
fi

# A submodule's git dir is its common dir, with a host-relative core.worktree;
# --separate-git-dir is the same layout without core.worktree.
git -C "${MAIN}" -c protocol.file.allow=always submodule add -q "${MAIN}" sub
add_ci_config "${MAIN}/sub"
git init -q --separate-git-dir "${WORK}/sep.git" "${WORK}/sep"
add_ci_config "${WORK}/sep"
for REPO in "${MAIN}/sub" "${WORK}/sep"; do
  run_ci_local "${PODMAN}" go-vet
  ARGS=$(last_run_args)
  COMMON=$(git -C "${REPO}" rev-parse --path-format=absolute --git-common-dir)
  PTR=$(mounted /src/.git:ro) CFG=$(mounted /gitcommon/config:ro)
  # The copy keeps the rest of the config; the host config is never changed.
  if [ "${RC}" -eq 0 ] && grep -qx "${COMMON}:/gitcommon" <<< "${ARGS}" &&
    [[ "${PTR}" == "$(cache_prefix gitfile "${REPO}")"* ]] && [[ "${CFG}" == "$(cache_prefix gitconfig "${REPO}")"* ]] &&
    [ "$(cat "${PTR}")" = "gitdir: /gitcommon" ] && ! git config -f "${CFG}" core.worktree > /dev/null &&
    [ "$(git config -f "${CFG}" core.bare)" = false ] &&
    { [ "${REPO}" != "${MAIN}/sub" ] || git config -f "${COMMON}/config" core.worktree > /dev/null; } &&
    ! grep -q GIT_WORK_TREE <<< "${ARGS}"; then
    pass "$(basename "${REPO}"): git dir at /gitcommon, its config mounted without core.worktree"
  else
    fail "$(basename "${REPO}") mounts: rc=${RC} ${ARGS}"
  fi
done

# Another run may have the pointer and config copy bind-mounted, and Docker
# Desktop shows a replaced file as deleted: a rerun reuses the same files, and
# a changed config gets a new one, leaving the old one as it was.
REPO="${MAIN}/sub"
run_ci_local "${PODMAN}" go-vet
PTR1=$(mounted /src/.git:ro) CFG1=$(mounted /gitcommon/config:ro)
# Hard links pin the first run's files, as that run's bind mounts would.
ln -f "${PTR1}" "${WORK}/ptr1"; ln -f "${CFG1}" "${WORK}/cfg1"
CFG1_BODY=$(cat "${CFG1}")
run_ci_local "${PODMAN}" go-vet
PTR2=$(mounted /src/.git:ro) CFG2=$(mounted /gitcommon/config:ro)
git -C "${REPO}" config ci-local.test changed
run_ci_local "${PODMAN}" go-vet
PTR3=$(mounted /src/.git:ro) CFG3=$(mounted /gitcommon/config:ro)
git -C "${REPO}" config --unset ci-local.test
if [ "${RC}" -eq 0 ] && [ "${PTR2}" -ef "${WORK}/ptr1" ] && [ "${CFG2}" -ef "${WORK}/cfg1" ] && [ "${PTR3}" -ef "${WORK}/ptr1" ] &&
  [ "${CFG3}" != "${CFG1}" ] && [ "${CFG1}" -ef "${WORK}/cfg1" ] && [ "$(cat "${CFG1}")" = "${CFG1_BODY}" ] &&
  [ "$(git config -f "${CFG3}" ci-local.test)" = changed ] &&
  ! compgen -G "${HOME}/.cache/ci-local/*.[0-9]*" > /dev/null; then
  pass "a rerun reuses the .git pointer and config copy; a changed config gets a new file"
else
  fail "pointer/config reuse: rc=${RC} ptr=[${PTR1} ${PTR2} ${PTR3}] cfg=[${CFG1} ${CFG2} ${CFG3}] cache=[$(cd "${HOME}/.cache/ci-local" && echo *)]"
fi

# Any other .git file layout fails before a container starts, instead of a bad pointer.
ODD="${WORK}/odd"
mkdir -p "${ODD}"
add_ci_config "${ODD}"
cp -R "${MAIN}/.git/worktrees/wt" "${WORK}/odd-admin"
printf '%s\n' "$(git -C "${MAIN}" rev-parse --path-format=absolute --git-common-dir)" > "${WORK}/odd-admin/commondir"
printf '%s\n' "${ODD}/.git" > "${WORK}/odd-admin/gitdir"
echo "gitdir: ${WORK}/odd-admin" > "${ODD}/.git"
REPO="${ODD}"
run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'unsupported .git layout' <<< "${OUT}" && ! grep -qx 'ARG run' "${ENGINE_LOG}"; then
  pass "an unsupported .git layout fails without starting a container"
else
  fail "unsupported .git layout: rc=${RC} out=[${OUT}]"
fi
REPO="${WORK}/wt"

# A newline in the checkout path stays inside one -v argument, not a new engine option.
NL_REPO="${WORK}/new"$'\n'"line"
mkdir -p "${NL_REPO}/.git"
add_ci_config "${NL_REPO}"
SAVED_REPO="${REPO}"; REPO="${NL_REPO}"
run_ci_local "${PODMAN}" go-vet
REPO="${SAVED_REPO}"
# The stub logs one "ARG <value>" per argument, so a split argument would log "ARG line:/src".
if [ "${RC}" -eq 0 ] && grep -qx 'line:/src' "${ENGINE_LOG}" && ! grep -qx 'ARG line:/src' "${ENGINE_LOG}"; then
  pass "a newline in the checkout path stays inside its -v argument"
else
  fail "newline in checkout path split into engine arguments: rc=${RC}"
fi

# --- DNS: host resolvers in order; CI_LOCAL_DNS overrides; Docker Desktop adds use-vc ---
SCUTIL_SAMPLE='DNS configuration

resolver #1
  search domain[0] : corp.example
  nameserver[0] : 192.0.2.1
  nameserver[1] : 2001:db8::53
  nameserver[2] : 203.0.113.1
  nameserver[3] : 127.0.0.1

resolver #2
  domain   : corp.example
  nameserver[0] : 198.51.100.9

DNS configuration (for scoped queries)

resolver #1
  nameserver[0] : 198.51.100.1'
dns_of() { last_run_args | awk 'p=="--dns"{printf "dns=%s ", $0} p=="--dns-option"{printf "opt=%s ", $0} {p=$0}'; }

STUB_SCUTIL_OUT="${SCUTIL_SAMPLE}" run_ci_local "${PODMAN}" go-vet
if [ "$(dns_of)" = "dns=192.0.2.1 dns=203.0.113.1 " ]; then
  pass "podman: host's default resolvers in order (no IPv6, loopback, per-domain or scoped)"
else
  fail "podman host resolvers: [$(dns_of)]"
fi
STUB_SCUTIL_OUT="${SCUTIL_SAMPLE}" STUB_OS="Docker Desktop" run_ci_local "${DOCKER}" go-vet
if [ "$(dns_of)" = "dns=192.0.2.1 dns=203.0.113.1 opt=use-vc " ]; then
  pass "Docker Desktop: host resolvers plus --dns-option use-vc (DNS over TCP)"
else
  fail "Docker Desktop DNS: [$(dns_of)]"
fi
STUB_SCUTIL_OUT="${SCUTIL_SAMPLE}" STUB_OS="Ubuntu 24.04.1 LTS" run_ci_local "${DOCKER}" go-vet
if [ "$(dns_of)" = "dns=192.0.2.1 dns=203.0.113.1 " ]; then
  pass "Docker Engine (not Desktop): host resolvers, no use-vc"
else
  fail "Docker Engine DNS: [$(dns_of)]"
fi
STUB_SCUTIL_OUT="${SCUTIL_SAMPLE}" CI_LOCAL_DNS="198.51.100.7 198.51.100.8" run_ci_local "${PODMAN}" go-vet
if [ "$(dns_of)" = "dns=198.51.100.7 dns=198.51.100.8 " ]; then
  pass "CI_LOCAL_DNS list overrides the host's resolvers, in order"
else
  fail "CI_LOCAL_DNS override: [$(dns_of)]"
fi
STUB_SCUTIL_OUT="${SCUTIL_SAMPLE}" CI_LOCAL_DNS='' STUB_OS="Docker Desktop" run_ci_local "${DOCKER}" go-vet
if [ -z "$(dns_of)" ]; then
  pass "empty CI_LOCAL_DNS keeps the engine's own DNS (no --dns, no use-vc)"
else
  fail "empty CI_LOCAL_DNS: [$(dns_of)]"
fi
run_ci_local "${PODMAN}" go-vet
if [ -z "$(dns_of)" ]; then
  pass "no usable host resolver: no --dns"
else
  fail "no host resolver: [$(dns_of)]"
fi

# --- node_modules volumes: created up front, labelled with the checkout path ---
STUB_VOLUME_MISSING=1 run_ci_local "${PODMAN}" go-vet
creates=$(awk '/^CALL$/{n++} {a[n]=a[n] $0 " "} END{for(i=1;i<=n;i++) if (a[i] ~ /^CALL ARG volume ARG create /) print a[i]}' "${ENGINE_LOG}")
first_run=$( { grep -nx 'ARG run' "${ENGINE_LOG}" || true; } | head -1 | cut -d: -f1)
last_create=$( { grep -nx 'ARG create' "${ENGINE_LOG}" || true; } | tail -1 | cut -d: -f1)
ID=$(printf '%s' "${REPO}" | { sha256sum 2>/dev/null || shasum -a 256; } | cut -c1-12)
run_ci_local "${PODMAN}" go-vet
if [ "$(wc -l <<< "${creates}" | tr -d ' ')" -eq 2 ] &&
  grep -qF "ARG --label ARG fullsend-ci-local.checkout=${REPO} ARG fullsend-ci-local-nm-root-${ID} " <<< "${creates}" &&
  grep -qF "ARG --label ARG fullsend-ci-local.checkout=${REPO} ARG fullsend-ci-local-nm-workersrc-${ID} " <<< "${creates}" &&
  [ "${last_create:-999999}" -lt "${first_run:-0}" ] && ! grep -qx 'ARG create' "${ENGINE_LOG}"; then
  pass "node_modules volumes are created before the run, labelled with the checkout path, and only when missing"
else
  fail "node_modules volume creation: creates=[${creates}] last_create=${last_create} first_run=${first_run}"
fi

# --- clean: old labelled images and node_modules volumes of gone checkouts only, never forced ---
run_ci_local "${PODMAN}" go-vet
CUR=$(last_run_args | grep -m1 -E '^fullsend-ci-local:')
removed() { awk '/^CALL$/{n++} {a[n]=a[n] $0 " "} END{for(i=1;i<=n;i++) if (a[i] ~ /^CALL ARG (rmi|volume ARG rm) /) print a[i]}' "${ENGINE_LOG}" |
  sed -E 's/^CALL ARG (rmi|volume ARG rm) ARG //; s/ $//' | LC_ALL=C sort | tr '\n' ' '; }
STUB_IMAGES="aaa111 localhost/${CUR}
bbb222 localhost/fullsend-ci-local:0ldtag000000
ccc333 <none>:<none>
ddd444 localhost/fullsend-ci-local-amd64:0ldtag000000
eee555 localhost/fullsend-ci-local:busytag00000" \
STUB_VOLUMES="fullsend-ci-local-nm-root
fullsend-ci-local-nm-root-0123456789ab
fullsend-ci-local-nm-workersrc-0123456789ab
fullsend-ci-local-nm-root-aaaaaaaaaaaa
fullsend-ci-local-nm-workersrc-bbbbbbbbbbbb
fullsend-ci-local-nm-workersrc
fullsend-ci-local-go
fullsend-ci-local-amd64-nm-root
fullsend-ci-local-amd64-nm-root-cccccccccccc" \
STUB_VOLUME_LABELS="fullsend-ci-local-nm-root-0123456789ab ${WORK}
fullsend-ci-local-nm-workersrc-0123456789ab ${WORK}
fullsend-ci-local-nm-root-aaaaaaaaaaaa ${WORK}/gone dir
fullsend-ci-local-amd64-nm-root-cccccccccccc ${WORK}/gone" \
STUB_BUSY="fullsend-ci-local:busytag00000" run_ci_local "${PODMAN}" clean
if [ "${RC}" -eq 0 ] && [ -n "${CUR}" ] &&
  [ "$(removed)" = "ccc333 fullsend-ci-local-nm-root fullsend-ci-local-nm-root-aaaaaaaaaaaa fullsend-ci-local-nm-workersrc fullsend-ci-local-nm-workersrc-bbbbbbbbbbbb fullsend-ci-local:0ldtag000000 fullsend-ci-local:busytag00000 " ] &&
  grep -qF "removed volume fullsend-ci-local-nm-root-aaaaaaaaaaaa (checkout ${WORK}/gone dir is gone)" <<< "${OUT}" &&
  grep -qF "removed volume fullsend-ci-local-nm-workersrc-bbbbbbbbbbbb (no checkout label" <<< "${OUT}" &&
  grep -q "kept image fullsend-ci-local:busytag00000 (in use" <<< "${OUT}" &&
  ! grep -qE 'ARG (-f|--force)$' "${ENGINE_LOG}" && ! grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then
  pass "clean: removes old/untagged images and node_modules volumes of gone, unlabelled or shared checkouts; keeps current, amd64, live checkouts, caches; never forced"
else
  fail "clean: rc=${RC} current=[${CUR}] removed=[$(removed)] out=[${OUT}]"
fi
STUB_IMAGES="ddd444 localhost/fullsend-ci-local-amd64:0ldtag000000
bbb222 localhost/fullsend-ci-local:0ldtag000000" \
STUB_VOLUMES="fullsend-ci-local-nm-root
fullsend-ci-local-nm-root-aaaaaaaaaaaa
fullsend-ci-local-amd64-nm-root
fullsend-ci-local-amd64-nm-root-cccccccccccc" \
STUB_VOLUME_LABELS="fullsend-ci-local-nm-root-aaaaaaaaaaaa ${WORK}/gone
fullsend-ci-local-amd64-nm-root-cccccccccccc ${WORK}/gone" run_ci_local "${PODMAN}" --amd64 clean
if [ "$(removed)" = "fullsend-ci-local-amd64-nm-root fullsend-ci-local-amd64-nm-root-cccccccccccc fullsend-ci-local-amd64:0ldtag000000 " ]; then
  pass "clean --amd64: only the amd64 images and volumes"
else
  fail "clean --amd64: removed=[$(removed)]"
fi
STUB_IMAGES="bbb222 localhost/fullsend-ci-local:0ldtag000000" STUB_VOLUMES="fullsend-ci-local-nm-root" \
STUB_LIST_RC=125 run_ci_local "${PODMAN}" clean
if [ "${RC}" -ne 0 ] && [ -z "$(removed)" ] && grep -q 'cannot list images; is .*podman running?' <<< "${OUT}" &&
  ! grep -q 'kept)' <<< "${OUT}"; then
  pass "clean: a failed listing (engine stopped) exits non-zero and removes nothing"
else
  fail "clean with failed listing: rc=${RC} removed=[$(removed)] out=[${OUT}]"
fi

# #8479: Podman builds and runs with no containers.conf env, env_host,
# volumes, mounts or devices; a local one also gets no mounts.conf default
# mounts. A remote client has no such flag but reads the override; Docker has
# no such configuration.
override_of() { sed -n "s/^$1 //p" "${ENGINE_LOG}.env" | sort -u; }
OVR_WANT=$(printf '[containers]\nenv_host = false\nenv = []\nvolumes = []\nmounts = []\ndevices = []')
STUB_IMAGE_RC=1 run_ci_local "${PODMAN}" go-vet
OVR=$(override_of run)
if [ "${RC}" -eq 0 ] && [ "$(last_build_args | head -1)" = --default-mounts-file=/dev/null ] &&
  [ "$(last_run_args | head -1)" = --default-mounts-file=/dev/null ] &&
  [ -n "${OVR}" ] && [ "$(override_of build)" = "${OVR}" ] &&
  [ "$(cat "${OVR}")" = "${OVR_WANT}" ]; then
  pass "local podman: builds and runs get no default mounts and no containers.conf env, volumes, mounts or devices"
else
  fail "local podman isolation: rc=${RC} override=[${OVR}] build=[$(last_build_args | head -1)] run=[$(last_run_args | head -1)]"
fi
CONTAINERS_CONF_OVERRIDE="${WORK}/user-override.conf" run_ci_local "${PODMAN}" go-vet
if [ "$(override_of run)" = "${OVR}" ]; then
  pass "local podman: a user-set CONTAINERS_CONF_OVERRIDE is replaced"
else
  fail "user override kept: [$(override_of run)]"
fi
STUB_IMAGE_RC=1 STUB_REMOTE=true STUB_UNAME_S=Darwin run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -eq 0 ] && ! grep -q -- '--default-mounts-file' "${ENGINE_LOG}" &&
  [ "$(override_of run)" = "${OVR}" ] && [ "$(override_of build)" = "${OVR}" ]; then
  pass "remote podman: the same override, and no --default-mounts-file (the remote client has none)"
else
  fail "remote podman: rc=${RC} override=[$(override_of run)] log: $(tr '\n' ' ' < "${ENGINE_LOG}")"
fi
STUB_IMAGE_RC=1 run_ci_local "${DOCKER}" go-vet
if [ "${RC}" -eq 0 ] && ! grep -qE -- '--default-mounts-file|ServiceIsRemote' "${ENGINE_LOG}" && [ -z "$(override_of run)" ]; then
  pass "docker: no podman isolation flags or queries"
else
  fail "docker got podman isolation: $(tr '\n' ' ' < "${ENGINE_LOG}")"
fi
STUB_INFO_RC=125 run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'cannot query .*podman; is it running?' <<< "${OUT}" && ! grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then
  pass "podman: a failed remote/local query stops before any build or run"
else
  fail "failed podman info: rc=${RC} out=[${OUT}]"
fi
# Podman < 4.5 ignores CONTAINERS_CONF_OVERRIDE without any error.
STUB_PODMAN_VERSION=4.4.4 run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'Podman 4.4.4 ignores CONTAINERS_CONF_OVERRIDE' <<< "${OUT}" && ! grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then
  pass "podman < 4.5: stops before any build or run"
else
  fail "podman 4.4.4: rc=${RC} out=[${OUT}]"
fi
STUB_PODMAN_VERSION=4.5.0-dev STUB_REMOTE=true STUB_UNAME_S=Darwin run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -eq 0 ]; then
  pass "podman 4.5.0-dev (remote): runs"
else
  fail "podman 4.5.0-dev: rc=${RC} out=[${OUT}]"
fi
# A remote client on Linux reaches a service that applies the host's own
# containers.conf env and mounts.conf, out of the override's reach.
STUB_REMOTE=true STUB_UNAME_S=Linux run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'remote Podman client on Linux' <<< "${OUT}" && ! grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then
  pass "remote podman on Linux: stops before any build or run"
else
  fail "remote podman on Linux: rc=${RC} out=[${OUT}]"
fi
STUB_UNAME_S=Linux run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -eq 0 ]; then
  pass "local podman on Linux: runs"
else
  fail "local podman on Linux: rc=${RC} out=[${OUT}]"
fi

# Rootful Podman maps keep-id to root, so it runs as the host uid:gid like Docker.
STUB_IMAGE_RC=1 STUB_ROOTLESS=false run_ci_local "${PODMAN}" go-vet
ARGS=$(last_run_args) RB=$(last_build_args)
if [ "${RC}" -eq 0 ] && grep -A1 -x -- '--user' <<< "${ARGS}" | grep -qx "$(id -u):$(id -g)" &&
  ! grep -q 'keep-id' <<< "${ARGS}" && grep -qx "RUNNER_UID=$(id -u)" <<< "${RB}" &&
  grep -qx "RUNNER_GID=$(id -g)" <<< "${RB}" && grep -q 'chmod 1777' "${ENGINE_LOG}"; then
  pass "rootful podman: --user uid:gid, RUNNER_UID/RUNNER_GID, volumes opened up, no keep-id"
else
  fail "rootful podman: rc=${RC} args=[$(tr '\n' ' ' <<< "${ARGS}")] out=[${OUT}]"
fi
# Rootful Podman under sudo would run the tests as uid 0.
mkdir -p "${WORK}/bin-root"
printf '#!/bin/sh
[ "$1" = -u ] && { echo 0; exit; }
exec %q "$@"
' "$(command -v id)" > "${WORK}/bin-root/id"
chmod +x "${WORK}/bin-root/id"
PATH="${WORK}/bin-root:${PATH}" STUB_ROOTLESS=false run_ci_local "${PODMAN}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'would run the tests as root' <<< "${OUT}" && ! grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then
  pass "rootful podman as root: stops before any build or run"
else
  fail "rootful podman as root: rc=${RC} out=[${OUT}]"
fi

# podman-remote is Podman: the same isolation, and the same Linux refusal.
STUB_REMOTE=true STUB_UNAME_S=Linux run_ci_local "${PODMAN_REMOTE}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'remote Podman client on Linux' <<< "${OUT}" && ! grep -qxE 'ARG (run|build)' "${ENGINE_LOG}"; then
  pass "podman-remote on Linux: stops before any build or run"
else
  fail "podman-remote on Linux: rc=${RC} out=[${OUT}]"
fi
STUB_IMAGE_RC=1 STUB_REMOTE=true STUB_UNAME_S=Darwin run_ci_local "${PODMAN_REMOTE}" go-vet
if [ "${RC}" -eq 0 ] && [ "$(override_of run)" = "${OVR}" ] && [ "$(override_of build)" = "${OVR}" ] &&
  grep -qx -- '--http-proxy=false' <<< "$(last_run_args)" && grep -qx -- '--userns=keep-id' <<< "$(last_run_args)"; then
  pass "podman-remote on macOS: the Podman override, --http-proxy=false and keep-id"
else
  fail "podman-remote on macOS: rc=${RC} override=[$(override_of run)] args=[$(last_run_args | tr '\n' ' ')]"
fi
# Any other engine would run with none of the isolation.
NERDCTL=$(write_engine nerdctl)
run_ci_local "${NERDCTL}" go-vet
if [ "${RC}" -ne 0 ] && grep -q 'unsupported engine' <<< "${OUT}" && [ ! -s "${ENGINE_LOG}" ]; then
  pass "an unknown engine stops before calling it"
else
  fail "unknown engine: rc=${RC} out=[${OUT}] log=[$(tr '\n' ' ' < "${ENGINE_LOG}")]"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
