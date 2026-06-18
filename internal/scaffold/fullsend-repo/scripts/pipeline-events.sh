#!/usr/bin/env bash
# pipeline-events.sh — Emit structured pipeline events for full-trace observability.
#
# Source this file in pre/post scripts to record timing and metadata for each
# pipeline phase. Events are appended to a JSONL file that send-trace.py reads
# to build a hierarchical trace spanning the entire workflow — not just the
# Claude sandbox.
#
# When otel-trace-context.sh is also sourced and initialized, events are
# enriched with trace_id, span_id, and parent_span_id fields for proper
# distributed trace correlation.
#
# Usage:
#   source "$(dirname "${BASH_SOURCE[0]}")/pipeline-events.sh"
#   pe_start "pre-explore" "fetch-issue"
#   ... do work ...
#   pe_end "pre-explore" "fetch-issue" '{"children": 5, "comments": 3}'
#
# The events file path defaults to /tmp/workspace/pipeline-events.jsonl and
# is also written to $GITHUB_WORKSPACE/output/pipeline-events.jsonl for
# artifact upload.

PIPELINE_EVENTS_DIR="/tmp/workspace"
PIPELINE_EVENTS_FILE="${PIPELINE_EVENTS_DIR}/pipeline-events.jsonl"
mkdir -p "$PIPELINE_EVENTS_DIR"

# Auto-source OTEL trace context if available and not already loaded
_PE_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -z "${_OTEL_INITIALIZED:-}" && -f "${_PE_SCRIPT_DIR}/otel-trace-context.sh" ]]; then
  source "${_PE_SCRIPT_DIR}/otel-trace-context.sh"
fi

_pe_now_ms() {
  python3 -c "import time; print(int(time.time() * 1000))" 2>/dev/null \
    || date +%s%3N 2>/dev/null \
    || echo "$(date +%s)000"
}

_pe_now_iso() {
  date -u +"%Y-%m-%dT%H:%M:%S.%3NZ" 2>/dev/null || date -u +"%Y-%m-%dT%H:%M:%SZ"
}

pe_start() {
  local phase="$1" step="$2"
  local ts_ms; ts_ms=$(_pe_now_ms)
  local ts_iso; ts_iso=$(_pe_now_iso)

  # Start an OTEL span if trace context is initialized
  if [[ -n "${_OTEL_INITIALIZED:-}" ]]; then
    otel_start_span "${phase}:${step}"
  fi

  jq -nc \
    --arg phase "$phase" \
    --arg step "$step" \
    --arg event "start" \
    --arg ts "$ts_iso" \
    --argjson ts_ms "$ts_ms" \
    --arg trace_id "${_OTEL_TRACE_ID:-}" \
    '{phase: $phase, step: $step, event: $event, timestamp: $ts, timestamp_ms: $ts_ms}
     + (if $trace_id != "" then {trace_id: $trace_id} else {} end)' \
    >> "$PIPELINE_EVENTS_FILE"
}

pe_end() {
  local phase="$1" step="$2"
  local metadata="$3"
  [[ -z "$metadata" ]] && metadata='{}'
  local ts_ms; ts_ms=$(_pe_now_ms)
  local ts_iso; ts_iso=$(_pe_now_iso)

  if ! echo "$metadata" | jq empty 2>/dev/null; then
    metadata="{}"
  fi

  # End the OTEL span if trace context is initialized
  if [[ -n "${_OTEL_INITIALIZED:-}" ]]; then
    otel_end_span "ok" "$metadata"
  fi

  jq -nc \
    --arg phase "$phase" \
    --arg step "$step" \
    --arg event "end" \
    --arg ts "$ts_iso" \
    --argjson ts_ms "$ts_ms" \
    --argjson meta "$metadata" \
    --arg trace_id "${_OTEL_TRACE_ID:-}" \
    '{phase: $phase, step: $step, event: $event, timestamp: $ts, timestamp_ms: $ts_ms, metadata: $meta}
     + (if $trace_id != "" then {trace_id: $trace_id} else {} end)' \
    >> "$PIPELINE_EVENTS_FILE"
}

pe_error() {
  local phase="$1" step="$2" error_msg="$3"
  local ts_ms; ts_ms=$(_pe_now_ms)
  local ts_iso; ts_iso=$(_pe_now_iso)

  # End the OTEL span with error status
  if [[ -n "${_OTEL_INITIALIZED:-}" ]]; then
    otel_end_span "error" "$(jq -nc --arg err "$error_msg" '{error: $err}')"
  fi

  jq -nc \
    --arg phase "$phase" \
    --arg step "$step" \
    --arg event "error" \
    --arg ts "$ts_iso" \
    --argjson ts_ms "$ts_ms" \
    --arg error "$error_msg" \
    --arg trace_id "${_OTEL_TRACE_ID:-}" \
    '{phase: $phase, step: $step, event: $event, timestamp: $ts, timestamp_ms: $ts_ms, error: $error}
     + (if $trace_id != "" then {trace_id: $trace_id} else {} end)' \
    >> "$PIPELINE_EVENTS_FILE"
}

pe_copy_to_output() {
  local output_dir="${1:-${GITHUB_WORKSPACE:-$(pwd)}/output}"
  if [[ -f "$PIPELINE_EVENTS_FILE" ]]; then
    mkdir -p "$output_dir"
    cp "$PIPELINE_EVENTS_FILE" "$output_dir/pipeline-events.jsonl"
  fi
  # Also copy OTEL trace files if they exist
  for f in otel-spans.jsonl otel-trace-context.json; do
    if [[ -f "${OTEL_SPANS_DIR:-/tmp/workspace}/$f" ]]; then
      cp "${OTEL_SPANS_DIR:-/tmp/workspace}/$f" "$output_dir/"
    fi
  done
}
