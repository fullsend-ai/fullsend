#!/usr/bin/env bash
# ci-local_test.sh — Tests for hack/ci-local/ci-local against a stub container
# engine: target validation, fail-closed targets, engine argv (isolation,
# user mapping, SELinux labels), git worktree mounts, and reading tool versions
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
case "$1" in
  image) exit "${STUB_IMAGE_RC:-0}" ;;
  build) exit 0 ;;
  run)
    for a in "$@"; do [ "$a" = 0 ] && [ "${prev:-}" = --user ] && exit 0; prev=$a; done
    [ "${STUB_EXEC:-0}" = 1 ] && exec bash -c "${*: -1}"
    exit "${STUB_RC:-0}" ;;
esac
exit 0
STUB
  chmod +x "${dir}/$1"
  echo "${dir}/$1"
}
PODMAN=$(write_engine podman)
DOCKER=$(write_engine docker)

# Shims for tools the container command calls when STUB_EXEC=1.
SHIMS="${WORK}/shims"
mkdir -p "${SHIMS}"

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
  : > "${ENGINE_LOG}"
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
exit 0
SHIM
chmod +x "${SHIMS}/make"
PATH="${SHIMS}:${PATH}" STUB_EXEC=1 run_ci_local "${PODMAN}" script-test
if [ "${RC}" -ne 0 ] && grep -q 'suite c ok' <<< "${OUT}" &&
  grep -q 'script-test: 3 suite(s) failed' <<< "${OUT}"; then
  pass "script-test runs every suite and counts only make's (ignored) error lines"
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
PTR="${HOME}/.cache/ci-local/gitfile-$(printf '%s' "${REPO}" | { sha256sum 2>/dev/null || shasum -a 256; } | cut -c1-12)"
if grep -qx "${COMMON}:/gitcommon" <<< "${ARGS}" && grep -qx "${PTR}:/src/.git:ro" <<< "${ARGS}" &&
  [ "$(cat "${PTR}")" = "gitdir: /gitcommon/worktrees/wt" ]; then
  pass "worktree: git common dir at /gitcommon and .git overlaid with a pointer"
else
  fail "worktree mounts: ${ARGS}"
fi

if [ "${FAILURES}" -ne 0 ]; then
  echo "${FAILURES} case(s) failed" >&2
  exit 1
fi
echo "all cases passed"
