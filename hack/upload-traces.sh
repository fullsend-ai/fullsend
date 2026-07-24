#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONFIG="${SCRIPT_DIR}/upload-traces-otelcol-config.yaml"

die() { echo "error: $*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: $(basename "$0") <file-or-dir-or-glob> --endpoint <otlp-http-url>

Replay OTLP JSON traces into an OTLP HTTP endpoint.

Reads run-telemetry.jsonl files (OTLP JSON, one TracesData per line) and
pushes them to any OTLP-speaking backend via otelcol-contrib.

Arguments:
  <file-or-dir-or-glob>  A .jsonl file, directory, or glob pattern
  --endpoint             OTLP HTTP endpoint (e.g. http://localhost:4318)

To send headers (auth tokens, experiment IDs, etc.), edit the config file
directly — otelcol-contrib reads headers from its YAML config, not from
env vars or CLI flags:

  $CONFIG

If yq is installed and no headers are configured, the script prompts for
confirmation before sending traces without authentication.

Examples:
  $(basename "$0") run/agent-run-123/run-telemetry.jsonl --endpoint http://localhost:4318
  $(basename "$0") run/ --endpoint http://localhost:4318
  $(basename "$0") 'run/*/run-telemetry.jsonl' --endpoint http://localhost:4318

The collector runs continuously and watches for new data. This is useful
for glob patterns or files still being written to. Press Ctrl+C to stop
once all traces have been uploaded.

Prerequisites:
  otelcol-contrib >=0.120.0 on PATH
  Install: https://github.com/open-telemetry/opentelemetry-collector-releases/releases
EOF
  exit 1
}

# --- parse args ---
SOURCE=""
ENDPOINT=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --endpoint)
      [[ $# -ge 2 ]] || die "--endpoint requires a value"
      ENDPOINT="$2"; shift 2 ;;
    --help|-h)
      usage ;;
    -*)
      die "unknown flag: $1" ;;
    *)
      [[ -z "$SOURCE" ]] || die "unexpected argument: $1"
      SOURCE="$1"; shift ;;
  esac
done

[[ -n "$SOURCE" ]]   || die "missing <file-or-dir> argument"
[[ -n "$ENDPOINT" ]] || die "missing --endpoint flag"

# --- validate prerequisites ---
command -v otelcol-contrib >/dev/null 2>&1 \
  || die "otelcol-contrib not found. Install: https://github.com/open-telemetry/opentelemetry-collector-releases/releases"

[[ -f "$CONFIG" ]] \
  || die "config not found: $CONFIG"

# --- resolve to absolute include pattern ---
if [[ -d "$SOURCE" ]]; then
  INCLUDE="$(cd "$SOURCE" && pwd)/**/*.jsonl"
elif [[ -f "$SOURCE" ]]; then
  INCLUDE="$(cd "$(dirname "$SOURCE")" && pwd)/$(basename "$SOURCE")"
else
  if [[ "$SOURCE" == /* ]]; then
    INCLUDE="$SOURCE"
  else
    INCLUDE="$(pwd)/$SOURCE"
  fi
fi
echo "include: $INCLUDE"

# --- run collector ---
# confmap recursively expands ${scheme:...} tokens in resolved values.
# Escape literal $ as $$ so paths/URLs can't pivot into env var lookups.
escape_confmap() { printf '%s' "${1//\$/\$\$}"; }

REPLAY_INCLUDE="$(escape_confmap "$INCLUDE")"
export REPLAY_INCLUDE
REPLAY_ENDPOINT="$(escape_confmap "$ENDPOINT")"
export REPLAY_ENDPOINT

if command -v yq >/dev/null 2>&1; then
  header_count="$(yq '.exporters.otlphttp.headers | length' "$CONFIG" 2>/dev/null || echo 0)"
  if [[ "$header_count" -eq 0 ]]; then
    echo "warning: no headers configured in $CONFIG"
    echo "traces will be sent without authentication headers."
    read -rp "Continue? [y/N] " answer
    [[ "$answer" =~ ^[Yy]$ ]] || exit 0
  fi
fi

echo "endpoint: $ENDPOINT"
echo "starting otelcol-contrib (runs continuously, watching for new data; press Ctrl+C to stop)..."
otelcol-contrib --config "$CONFIG"
