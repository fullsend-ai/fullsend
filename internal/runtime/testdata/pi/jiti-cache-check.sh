#!/usr/bin/env bash
# Re-check that pi's module loader cache stays disabled by JITI_FS_CACHE.
#
# pi imports every `-e` extension through jiti (createJiti in
# core/extensions/loader.ts) and passes no fsCache option, so jiti resolves
# it from JITI_FS_CACHE, then JITI_CACHE, then true. A cache entry is
# accepted on a ` /* v9-<hash of the source> */` trailer alone: a body
# rewritten with that trailer left in place runs, while the source file --
# and therefore runtime.piExtensionTreeHash and the hook adapter's SHA-256
# -- is unchanged. jiti probes for a node_modules directory next to the
# module that created it (<pi>/dist/bundle/chunks/ in the published
# package) and falls back to $TMPDIR/jiti; the image ships no such
# directory, so the cache lands in /tmp/jiti, writable by the agent and
# persistent across iterations.
#
# PiRuntime.EnvExports therefore exports JITI_FS_CACHE=false, re-emitted
# after the agent-writable .env is sourced. This script proves both halves
# on the pinned pi: poisoning works with the cache on, and is ignored with
# it off (no cache directory is even created).
#
# The cache is not the only loader lever the environment carries. pi's
# bundled cli.js reaches createJiti on its isBundledNode branch, which
# passes no `alias`, so jiti fills that option from JITI_ALIAS -- a map
# from module specifier to replacement file. An agent-writable .env
# exporting one swaps the file behind an `-e` path while the extension
# source, runtime.piExtensionTreeHash and the hook adapter's SHA-256 all
# stay clean. buildPiRunCommand therefore clears the whole loader family
# (runtime.piLoaderEnvNames) right after `. .env`, on every provider path;
# the second half of this script proves the swap works and that the unset
# stops it, with the name list read out of pi_run.go so the two cannot
# drift.
#
# Run it on a PI_VERSION bump. It needs a working pi provider, because the
# extension is loaded as part of a real one-shot run.
#
# Usage (from repo root or this directory):
#   internal/runtime/testdata/pi/jiti-cache-check.sh
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
CONTAINERFILE="${DIR}/../../../../images/sandbox/Containerfile"
IMAGE_PIN="$(sed -n 's/^ARG PI_VERSION=//p' "${CONTAINERFILE}" | head -n1)"
PINNED="${PI_VERSION:-${IMAGE_PIN}}"
if [[ -z "${PINNED}" ]]; then
	echo "jiti-cache-check.sh: could not read ARG PI_VERSION from ${CONTAINERFILE}; set PI_VERSION" >&2
	exit 1
fi
if ! command -v npx >/dev/null 2>&1; then
	echo "jiti-cache-check.sh: npx is required" >&2
	exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
export TMPDIR="${WORK}/tmp"
mkdir -p "${TMPDIR}" "${WORK}/ext" "${WORK}/evil"
cat >"${WORK}/ext/index.js" <<'EOF'
export default function () {
	console.error("EXT-SOURCE-REAL");
}
EOF
cat >"${WORK}/evil/index.js" <<'EOF'
export default function () {
	console.error("EXT-ALIAS-SWAPPED");
}
EOF

PKG="@earendil-works/pi-coding-agent@${PINNED}"
run_pi() {
	# --ignore-scripts mirrors the image install.
	npx -y --ignore-scripts "${PKG}" \
		--print --mode json --no-approve --no-extensions \
		-e "${WORK}/ext" 'hi' </dev/null 2>&1 | grep -E '^EXT-' | head -n 1 || true
}

# Rewrite the cached body, keeping jiti's trailer byte for byte.
poison() {
	local cache
	cache="$(find "${TMPDIR}/jiti" -type f -name 'ext-index.*' | head -n 1)"
	if [[ -z "${cache}" ]]; then
		echo "jiti-cache-check.sh: no cache entry under ${TMPDIR}/jiti -- did the loader change?" >&2
		exit 1
	fi
	# shellcheck disable=SC2016  # the node program is deliberately unexpanded
	node -e '
		const fs = require("node:fs");
		const f = process.argv[1];
		const m = fs.readFileSync(f, "utf8").match(/ \/\* v[0-9]+-[0-9a-f]+ \*\/\n$/);
		if (!m) { console.error("no jiti trailer in " + f); process.exit(1); }
		fs.writeFileSync(f, `"use strict";Object.defineProperty(exports, "__esModule", { value: true });exports.default = _default;function _default() {\n\tconsole.error("EXT-POISONED-CACHE");\n}${m[0]}`);
	' "${cache}"
}

fail=0
expect() { # $1 = label, $2 = expected marker, $3 = actual
	if [[ "$3" == "$2" ]]; then
		echo "ok   ${1}: ${3}"
	else
		echo "FAIL ${1}: expected ${2}, got '${3}'" >&2
		fail=1
	fi
}

rm -rf "${TMPDIR:?}/jiti"
expect "warm cache runs the source" "EXT-SOURCE-REAL" "$(run_pi)"
poison
expect "cache on: poisoned body wins" "EXT-POISONED-CACHE" "$(run_pi)"

rm -rf "${TMPDIR:?}/jiti"
run_pi >/dev/null
poison
expect "JITI_FS_CACHE=false ignores it" "EXT-SOURCE-REAL" "$(JITI_FS_CACHE=false run_pi)"

rm -rf "${TMPDIR:?}/jiti"
JITI_FS_CACHE=false run_pi >/dev/null
if [[ -d "${TMPDIR}/jiti" ]]; then
	echo "FAIL JITI_FS_CACHE=false still created ${TMPDIR}/jiti" >&2
	fail=1
else
	echo "ok   JITI_FS_CACHE=false creates no cache directory"
fi

# --- JITI_ALIAS: the module-swap half -------------------------------------
#
# The names come from runtime.piLoaderEnvNames, so a name added there is
# cleared here too, and one removed there makes this check fail loudly
# rather than silently pass.
RUN_GO="${DIR}/../../pi_run.go"
LOADER_ENV_NAMES="$(
	sed -n '/^var piLoaderEnvNames = /,/^}/p' "${RUN_GO}" |
		grep -o '"[A-Z0-9_]*"' | tr -d '"' | tr '\n' ' '
)"
case " ${LOADER_ENV_NAMES} " in
*" JITI_ALIAS "*) ;;
*)
	echo "jiti-cache-check.sh: piLoaderEnvNames in ${RUN_GO} no longer clears JITI_ALIAS" >&2
	exit 1
	;;
esac

ALIAS_MAP="{\"${WORK}/ext\":\"${WORK}/evil/index.js\",\"${WORK}/ext/index.js\":\"${WORK}/evil/index.js\"}"

rm -rf "${TMPDIR:?}/jiti"
expect "JITI_ALIAS swaps the module" "EXT-ALIAS-SWAPPED" "$(JITI_ALIAS="${ALIAS_MAP}" JITI_FS_CACHE=false run_pi)"

# What buildPiRunCommand emits right after `. .env`: a bare `unset` of the
# whole family, then the JITI_FS_CACHE pin. `unset` is a special builtin,
# so a function a sourced file defined cannot stand in for it.
rm -rf "${TMPDIR:?}/jiti"
alias_after_unset="$(
	export JITI_ALIAS="${ALIAS_MAP}"
	# shellcheck disable=SC2086  # the name list is deliberately word-split
	unset ${LOADER_ENV_NAMES}
	export JITI_FS_CACHE=false
	run_pi
)"
expect "the runtime's unset restores the source" "EXT-SOURCE-REAL" "${alias_after_unset}"

exit "${fail}"
