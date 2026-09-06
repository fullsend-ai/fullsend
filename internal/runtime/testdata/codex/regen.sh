#!/usr/bin/env bash
# Capture a live `codex exec --json` basic_run.ndjson for parseCodexStream tests.
#
# Only basic_run.ndjson is a live capture. The other fixtures stay hand-authored
# to the event structs in codex-rs/exec/src/exec_events.rs (see README.md), so
# they can cover shapes a happy-path run never produces: turn.failed, a
# top-level error, MCP calls, declined commands, malformed and truncated lines.
#
# The codex version defaults to the sandbox image pin (ARG CODEX_VERSION in
# images/sandbox/Containerfile) so a renovate bump is picked up here too;
# override with CODEX_VERSION=x.y.z in the environment. Until the image pin
# lands (PR A of the codex runtime stack), run:
#
#   CODEX_VERSION=0.152.1 internal/runtime/testdata/codex/regen.sh
#
# Requires a logged-in host codex (~/.codex/auth.json) or OPENAI_API_KEY.
#
# Usage (from repo root or this directory):
#   internal/runtime/testdata/codex/regen.sh
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
CONTAINERFILE="${DIR}/../../../../images/sandbox/Containerfile"
IMAGE_PIN=""
if [[ -f "${CONTAINERFILE}" ]]; then
	IMAGE_PIN="$(sed -n 's/^ARG CODEX_VERSION=//p' "${CONTAINERFILE}" | head -n1)"
fi
PINNED="${CODEX_VERSION:-${IMAGE_PIN}}"
if [[ -z "${PINNED}" ]]; then
	echo "regen.sh: could not read ARG CODEX_VERSION from ${CONTAINERFILE}; set CODEX_VERSION" >&2
	exit 1
fi
PKG="@openai/codex@${PINNED}"

if ! command -v npx >/dev/null 2>&1; then
	echo "regen.sh: npx is required" >&2
	exit 1
fi

# A scratch directory outside any git repo: codex refuses to run in an
# untrusted directory without --skip-git-repo-check, and the capture must not
# see (or edit) the fullsend checkout.
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/fs-codex-regen.XXXXXX")"
trap 'rm -rf "${WORKDIR}"' EXIT

echo "regen.sh: capturing with ${PKG} in ${WORKDIR}" >&2

RAW="${WORKDIR}/raw.ndjson"
# workspace-write + approval_policy=never keeps the capture non-interactive and
# confined to WORKDIR. `codex exec` has no --ask-for-approval flag; the policy
# is a -c override. model_reasoning_summary=detailed is what makes the run emit
# `reasoning` items, which the fixture needs to cover.
npx -y --ignore-scripts "${PKG}" exec --json \
	--skip-git-repo-check \
	--sandbox workspace-write \
	-c approval_policy=never \
	-c model_reasoning_effort=medium \
	-c model_reasoning_summary=detailed \
	-C "${WORKDIR}" \
	--model "${CODEX_MODEL:-gpt-5.6-luna}" \
	'Run `ls .`, then create hello.txt containing hi, then say done' >"${RAW}"

# Redact the throwaway working directory so the fixture reads like a sandbox
# run and carries nothing machine-specific. Thread ids are kept: they are
# random per run, and the test asserts their shape rather than their value.
#
# mktemp -d can hand back a path holding regex metacharacters (a "+" in a
# macOS TMPDIR, for instance), so the replacement is done literally with
# python rather than as a sed pattern, and the verification grep is -F.
WORKDIR="${WORKDIR}" python3 -c '
import os, sys
work = os.environ["WORKDIR"]
sys.stdout.write(sys.stdin.read().replace(work, "/sandbox/workspace/repo"))
' <"${RAW}" >"${DIR}/basic_run.ndjson"

if grep -qF "${WORKDIR}" "${DIR}/basic_run.ndjson"; then
	echo "regen.sh: refusing to keep a capture that still contains ${WORKDIR}" >&2
	exit 1
fi

echo "wrote ${DIR}/basic_run.ndjson ($(wc -l <"${DIR}/basic_run.ndjson") lines) with ${PKG}"
echo "update README.md with the model and version, and re-check the expected events in codex_progress_test.go" >&2
