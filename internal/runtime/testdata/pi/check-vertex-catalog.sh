#!/usr/bin/env bash
# Diff piGoogleVertexModels against the google-vertex catalog the pinned pi
# actually bundles.
#
# piGoogleVertexModels (internal/runtime/pi_bootstrap.go) is that catalog
# copied verbatim, and it is what the sub-agent Agent tool accepts as a
# Gemini id -- so an id pi serves but the table lacks is rejected by us, and
# an id the table has but pi dropped reaches Vertex as an unknown model.
#
# Check the DATA FILE, not the generated wrapper: the wrapper was unchanged
# between 0.84.4 and 0.85.0 while the data file gained gemini-3.8-flash, so
# reading the wrapper says "unchanged" and is wrong (#7018).
#
# The pi version defaults to the sandbox image pin (ARG PI_VERSION in
# images/sandbox/Containerfile); override with PI_VERSION=x.y.z.
#
# Usage (from repo root or this directory):
#   internal/runtime/testdata/pi/check-vertex-catalog.sh
set -euo pipefail

DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "${DIR}/../../../.." && pwd)"
CONTAINERFILE="${ROOT}/images/sandbox/Containerfile"
BOOTSTRAP="${ROOT}/internal/runtime/pi_bootstrap.go"

IMAGE_PIN="$(sed -n 's/^ARG PI_VERSION=//p' "${CONTAINERFILE}" | head -n1)"
PINNED="${PI_VERSION:-${IMAGE_PIN}}"
if [[ -z "${PINNED}" ]]; then
	echo "check-vertex-catalog.sh: could not read ARG PI_VERSION from ${CONTAINERFILE}; set PI_VERSION" >&2
	exit 1
fi
command -v npm >/dev/null 2>&1 || { echo "check-vertex-catalog.sh: npm is required" >&2; exit 1; }

TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

echo "check-vertex-catalog.sh: fetching @earendil-works/pi-ai@${PINNED}" >&2
npm install --prefix "${TMP}" --no-audit --no-fund --ignore-scripts --silent \
	"@earendil-works/pi-ai@${PINNED}" >&2

CATALOG="${TMP}/node_modules/@earendil-works/pi-ai/dist/providers/data/google-vertex.json"
[[ -f "${CATALOG}" ]] || { echo "check-vertex-catalog.sh: no catalog at ${CATALOG}" >&2; exit 1; }

# Upstream ids, sorted.
node -e '
  const d = require(process.argv[1]);
  const inner = d[Object.keys(d)[0]];
  console.log(Object.keys(inner).sort().join("\n"));
' "${CATALOG}" >"${TMP}/upstream.txt"

# The Go slice literal, sorted.
sed -n '/^var piGoogleVertexModels/,/^}/p' "${BOOTSTRAP}" \
	| grep -oE '"[^"]+"' | tr -d '"' | sort >"${TMP}/table.txt"

if diff -u "${TMP}/upstream.txt" "${TMP}/table.txt" >"${TMP}/delta.txt"; then
	echo "OK: piGoogleVertexModels matches pi ${PINNED} ($(wc -l <"${TMP}/upstream.txt" | tr -d ' ') ids)"
	exit 0
fi

echo "MISMATCH: piGoogleVertexModels vs pi ${PINNED} google-vertex catalog" >&2
echo "  (-) upstream only -> add it to piGoogleVertexModels" >&2
echo "  (+) table only    -> pi dropped it" >&2
sed -n '3,$p' "${TMP}/delta.txt" >&2
exit 1
