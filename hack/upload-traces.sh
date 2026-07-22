#!/usr/bin/env bash
set -euo pipefail

die() { echo "error: $*" >&2; exit 1; }

usage() {
  cat <<EOF
Usage: $(basename "$0") <file-or-dir-or-glob> --endpoint <otlp-http-url> [--header key=value]...

Replay OTLP JSON traces into an OTLP HTTP endpoint.

Reads run-telemetry.jsonl files (OTLP JSON, one TracesData per line) and
pushes them to any OTLP-speaking backend via otelcol-contrib.

Arguments:
  <file-or-dir-or-glob>  A .jsonl file, directory, or glob pattern
  --endpoint             OTLP HTTP endpoint (e.g. http://localhost:4318)
  --header key=value     Header to send with OTLP requests (repeatable)

Headers are injected into the Collector's otlphttp exporter config.
Any key=value pairs already set in the OTEL_EXPORTER_OTLP_HEADERS env
var are also included and merged with --header flags.

Examples:
  $(basename "$0") run/agent-run-123/run-telemetry.jsonl --endpoint http://localhost:4318
  $(basename "$0") run/ --endpoint http://localhost:4318 --header x-mlflow-experiment-id=42
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
HEADERS=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --endpoint)
      [[ $# -ge 2 ]] || die "--endpoint requires a value"
      ENDPOINT="$2"; shift 2 ;;
    --header)
      [[ $# -ge 2 ]] || die "--header requires a key=value"
      [[ "$2" == *=* ]] || die "--header value must be key=value, got: $2"
      if [[ -n "$HEADERS" ]]; then HEADERS="${HEADERS},$2"; else HEADERS="$2"; fi
      shift 2 ;;
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

# --- resolve to absolute include pattern ---
if [[ -d "$SOURCE" ]]; then
  INCLUDE="$(cd "$SOURCE" && pwd)/**/*.jsonl"
elif [[ -f "$SOURCE" ]]; then
  INCLUDE="$(cd "$(dirname "$SOURCE")" && pwd)/$(basename "$SOURCE")"
else
  # Treat as a glob pattern — make it absolute relative to cwd
  if [[ "$SOURCE" == /* ]]; then
    INCLUDE="$SOURCE"
  else
    INCLUDE="$(pwd)/$SOURCE"
  fi
fi
echo "include: $INCLUDE"

# --- merge headers ---
# Combine --header flags with any pre-existing OTEL_EXPORTER_OTLP_HEADERS
# into a single comma-separated string for config generation.
MERGED_HEADERS="${OTEL_EXPORTER_OTLP_HEADERS:-}"
if [[ -n "$HEADERS" ]]; then
  if [[ -n "$MERGED_HEADERS" ]]; then
    MERGED_HEADERS="${MERGED_HEADERS},${HEADERS}"
  else
    MERGED_HEADERS="$HEADERS"
  fi
fi

# --- generate runtime config ---
# The otelcol-contrib Collector reads headers from its YAML config
# (exporters.otlphttp.headers), not from OTEL_EXPORTER_OTLP_HEADERS.
# Generate a runtime config that injects any headers into the YAML.
export REPLAY_INCLUDE="$INCLUDE"
export REPLAY_ENDPOINT="$ENDPOINT"

RUNTIME_CONFIG=$(mktemp "${TMPDIR:-/tmp}/otelcol-config-XXXXXX.yaml")
cleanup() { rm -f "$RUNTIME_CONFIG"; }
trap cleanup EXIT

cat > "$RUNTIME_CONFIG" <<'YAML'
receivers:
  otlpjsonfile:
    include:
      - "${env:REPLAY_INCLUDE}"
    start_at: beginning

exporters:
  otlphttp:
    endpoint: "${env:REPLAY_ENDPOINT}"
YAML

if [[ -n "$MERGED_HEADERS" ]]; then
  echo "    headers:" >> "$RUNTIME_CONFIG"
  IFS=',' read -ra HEADER_ARRAY <<< "$MERGED_HEADERS"
  for h in "${HEADER_ARRAY[@]}"; do
    key="${h%%=*}"
    value="${h#*=}"
    # Validate key contains only safe characters
    [[ "$key" =~ ^[a-zA-Z0-9_-]+$ ]] \
      || die "invalid header key (must match [a-zA-Z0-9_-]+): $key"
    # Strip newlines/carriage returns to prevent YAML injection
    value="${value//$'\n'/}"
    value="${value//$'\r'/}"
    # Use single-quoted YAML scalar (no escape sequences interpreted);
    # the only special case is a literal single quote, represented as ''.
    value="${value//\'/\'\'}"
    printf "      %s: '%s'\n" "$key" "$value" >> "$RUNTIME_CONFIG"
  done
fi

cat >> "$RUNTIME_CONFIG" <<'YAML'

service:
  pipelines:
    traces:
      receivers: [otlpjsonfile]
      exporters: [otlphttp]
YAML

echo "endpoint: $ENDPOINT"
if [[ -n "$MERGED_HEADERS" ]]; then
  echo "headers: $MERGED_HEADERS"
fi

echo ""
echo "Temporal configuration file: $RUNTIME_CONFIG"
echo "---"
cat "$RUNTIME_CONFIG"
echo "---"
echo ""

echo "starting otelcol-contrib (runs continuously, watching for new data; press Ctrl+C to stop)..."
otelcol-contrib --config "$RUNTIME_CONFIG"
