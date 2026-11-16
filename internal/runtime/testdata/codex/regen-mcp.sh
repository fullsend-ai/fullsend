#!/usr/bin/env bash
# Capture a live `codex exec --json` mcp_tool_call.ndjson for parseCodexStream
# tests: three calls to the stub MCP server in mcp_stub.mjs — one that
# succeeds, one whose result is flagged isError, and one answered with a
# JSON-RPC error — so the fixture pins the mcp_tool_call item as codex
# really emits it (#7414).
#
# Like regen.sh, the codex version defaults to the sandbox image pin
# (ARG CODEX_VERSION in images/sandbox/Containerfile); override with
# CODEX_VERSION=x.y.z. The run gets a throwaway CODEX_HOME, so the host's
# own config.toml (and any MCP servers in it) never reaches the capture;
# that means it authenticates with OPENAI_API_KEY, not a host login.
#
# Requires npx, node, python3 and OPENAI_API_KEY.
#
# Usage (from repo root or this directory):
#   internal/runtime/testdata/codex/regen-mcp.sh
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
CONTAINERFILE="${DIR}/../../../../images/sandbox/Containerfile"
IMAGE_PIN=""
if [[ -f "${CONTAINERFILE}" ]]; then
	IMAGE_PIN="$(sed -n 's/^ARG CODEX_VERSION=//p' "${CONTAINERFILE}" | head -n1)"
fi
PINNED="${CODEX_VERSION:-${IMAGE_PIN}}"
if [[ -z "${PINNED}" ]]; then
	echo "regen-mcp.sh: could not read ARG CODEX_VERSION from ${CONTAINERFILE}; set CODEX_VERSION" >&2
	exit 1
fi
PKG="@openai/codex@${PINNED}"

PYTHON="$(command -v python3 2>/dev/null || true)"
if [[ -z "${PYTHON}" ]] || ! "${PYTHON}" -c '' 2>/dev/null; then
	PYTHON="$(command -v python 2>/dev/null || true)"
fi
if [[ -z "${PYTHON}" ]]; then
	echo "regen-mcp.sh: python3 is required" >&2
	exit 1
fi
for tool in npx node; do
	if ! command -v "${tool}" >/dev/null 2>&1; then
		echo "regen-mcp.sh: ${tool} is required" >&2
		exit 1
	fi
done
if [[ -z "${OPENAI_API_KEY:-}" ]]; then
	echo "regen-mcp.sh: OPENAI_API_KEY is required (the throwaway CODEX_HOME has no login)" >&2
	exit 1
fi

# A scratch directory outside any git repo, as in regen.sh.
WORKDIR="$(cd "$(mktemp -d "${TMPDIR:-/tmp}/fs-codex-mcp.XXXXXX")" && pwd)"
export CODEX_HOME="${WORKDIR}/codex-home"
# The API key saved by `login` goes first and on its own: codex can leave a
# background plugin clone holding files under CODEX_HOME, which makes the
# directory removal fail, and that must neither keep the key on disk nor
# fail a capture that was already written.
cleanup() {
	rm -f "${CODEX_HOME}/auth.json"
	rm -rf "${WORKDIR}" 2>/dev/null ||
		echo "regen-mcp.sh: could not remove all of ${WORKDIR} (codex may still be writing to it); the saved API key was removed" >&2
}
trap cleanup EXIT
mkdir -p "${CODEX_HOME}" "${WORKDIR}/repo"
printf '%s' "${OPENAI_API_KEY}" | npx -y --ignore-scripts "${PKG}" login --with-api-key >/dev/null

# The stub's path is handed to codex as a TOML string; forward slashes keep
# it readable on Windows too. JSON string escaping is valid in a TOML basic
# string, so a quote or backslash in the checkout path cannot break the
# config.
STUB="${DIR}/mcp_stub.mjs"
if command -v cygpath >/dev/null 2>&1; then
	STUB="$(cygpath -m "${STUB}")"
fi
STUB_TOML="$("${PYTHON}" -c 'import json, sys; sys.stdout.write(json.dumps(sys.argv[1]))' "${STUB}")"

echo "regen-mcp.sh: capturing with ${PKG} in ${WORKDIR}" >&2

RAW="${WORKDIR}/raw.ndjson"
# Flags as in regen.sh, except a read-only sandbox: the capture runs no
# command and writes no file. stdin is closed for the same reason.
npx -y --ignore-scripts "${PKG}" exec --json \
	--skip-git-repo-check \
	--sandbox read-only \
	-c approval_policy=never \
	-c model_reasoning_effort=low \
	-c 'mcp_servers.stub.command="node"' \
	-c "mcp_servers.stub.args=[${STUB_TOML}]" \
	-c 'mcp_servers.stub.default_tools_approval_mode="approve"' \
	-C "${WORKDIR}/repo" \
	--model "${CODEX_MODEL:-gpt-5.6-luna}" \
	'Using only the stub MCP server tools, call lookup with key alpha, then fail with key beta, then broken with key gamma, one at a time. Do not run shell commands. Then say done.' \
	</dev/null >"${RAW}"

# Keep the capture free of host paths: the working directory and the stub's
# location are the only ones codex could echo. The result is checked in the
# scratch directory and replaces the committed fixture only once it passes,
# so a failed run never leaves a partial or host-path capture in the tree.
CLEAN="${WORKDIR}/clean.ndjson"
WORKDIR="${WORKDIR}" STUB="${STUB}" "${PYTHON}" -c '
import os, sys
s = sys.stdin.read()
s = s.replace(os.environ["STUB"], "/sandbox/mcp_stub.mjs")
s = s.replace(os.environ["WORKDIR"], "/sandbox/workspace")
sys.stdout.write(s)
' <"${RAW}" >"${CLEAN}"

if grep -qF -e "${WORKDIR}" -e "${STUB}" "${CLEAN}"; then
	echo "regen-mcp.sh: refusing to keep a capture that still contains a host path" >&2
	exit 1
fi

# codex can exit 0 without following the prompt. Keep the capture only if it
# holds a completed call to each stub tool and a completed turn.
if ! "${PYTHON}" -c '
import json, sys
tools, turn = set(), False
for line in open(sys.argv[1], encoding="utf-8"):
    try:
        e = json.loads(line)
    except ValueError:
        continue
    item = e.get("item") or {}
    if e.get("type") == "item.completed" and item.get("type") == "mcp_tool_call" and item.get("server") == "stub":
        tools.add(item.get("tool"))
    turn = turn or e.get("type") == "turn.completed"
missing = sorted({"lookup", "fail", "broken"} - tools)
if missing or not turn:
    sys.exit("missing: " + ", ".join(missing + ([] if turn else ["turn.completed"])))
' "${CLEAN}"; then
	echo "regen-mcp.sh: the capture is incomplete; ${DIR}/mcp_tool_call.ndjson was left as it was" >&2
	exit 1
fi
mv -f "${CLEAN}" "${DIR}/mcp_tool_call.ndjson"

echo "wrote ${DIR}/mcp_tool_call.ndjson ($(wc -l <"${DIR}/mcp_tool_call.ndjson") lines) with ${PKG}"
echo "update README.md with the model and version, and re-check the expected events in codex_progress_test.go" >&2
