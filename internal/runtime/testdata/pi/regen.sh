#!/usr/bin/env bash
# Capture a live pi --mode json basic_run.ndjson for parsePiStream tests.
#
# Other fixtures (error/malformed/multi-step/reasoning/truncated) remain
# hand-authored to packages/coding-agent/docs/json.md v0.84.2. Run this
# when you have a configured provider to replace basic_run.ndjson.
#
# The pi version defaults to the sandbox image pin (ARG PI_VERSION in
# images/sandbox/Containerfile) so a renovate bump is picked up here too;
# override with PI_VERSION=x.y.z in the environment.
#
# Usage (from repo root or this directory):
#   internal/runtime/testdata/pi/regen.sh
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
CONTAINERFILE="${DIR}/../../../../images/sandbox/Containerfile"
IMAGE_PIN="$(sed -n 's/^ARG PI_VERSION=//p' "${CONTAINERFILE}" | head -n1)"
PINNED="${PI_VERSION:-${IMAGE_PIN}}"
if [[ -z "${PINNED}" ]]; then
	echo "regen.sh: could not read ARG PI_VERSION from ${CONTAINERFILE}; set PI_VERSION" >&2
	exit 1
fi
PKG="@earendil-works/pi-coding-agent@${PINNED}"
# --ignore-scripts mirrors the image install (the package needs no install scripts).
PI=(npx -y --ignore-scripts "${PKG}" --print --mode json)

if ! command -v npx >/dev/null 2>&1; then
	echo "regen.sh: npx is required" >&2
	exit 1
fi

echo "regen.sh: capturing fixtures with ${PKG}" >&2
echo "regen.sh: writing into ${DIR}" >&2

# --no-session keeps the capture out of the operator's session store. Probe
# the flag via --help; an npx failure is a hard error, not "flag unsupported",
# so a broken install never silently falls back to writing a real session.
if ! help_out="$(npx -y --ignore-scripts "${PKG}" --help 2>&1)"; then
	echo "regen.sh: '${PKG} --help' failed:" >&2
	printf '%s\n' "${help_out}" >&2
	exit 1
fi
NO_SESSION=()
if grep -q -- '--no-session' <<<"${help_out}"; then
	NO_SESSION=(--no-session)
else
	echo "regen.sh: --no-session not supported by ${PKG}; capture will be stored in your session dir" >&2
fi

capture() {
	local out="$1"
	shift
	"${PI[@]}" "${NO_SESSION[@]}" "$@" >"${out}"
}

capture "${DIR}/basic_run.ndjson" "List files in one sentence, then stop."
echo "wrote ${DIR}/basic_run.ndjson ($(wc -l <"${DIR}/basic_run.ndjson") lines)"
