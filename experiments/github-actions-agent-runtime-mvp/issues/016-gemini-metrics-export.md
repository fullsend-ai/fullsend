# 016: Export Gemini CLI Metrics to GCP Cloud Monitoring

## Problem

The agent workflows produce raw telemetry data (LLM API time, tool execution time, token usage, error rates) embedded in gemini-output artifacts, but this data is only accessible by downloading and parsing individual workflow run artifacts. There is no centralized dashboard for observing agent performance trends, detecting regressions, or alerting on anomalies.

Current telemetry is captured per-run but not exported:

| Metric | Source | Current State |
|--------|--------|---------------|
| LLM API time | gemini-output artifact | Raw data in artifact JSON |
| Tool execution time (shell) | gemini-output artifact | Raw data in artifact JSON |
| Token usage (total, cached %) | gemini-output artifact | Raw data in artifact JSON |
| API request count / errors | gemini-output artifact | Raw data in artifact JSON |
| Loop detector time | gemini-output artifact | Raw data in artifact JSON |
| Workflow duration | GitHub Actions UI | Per-run only, no aggregation |

See [001](001-fix-agent-performance.md) for example telemetry data from fix agent runs.

## What Should Happen

Agent telemetry should be exported to GCP Cloud Monitoring so that:

1. **Dashboards** show per-agent metrics over time (review agent mean duration, fix agent cycle time, token spend)
2. **Alerts** fire when metrics exceed thresholds (e.g., fix agent > 30 min, error rate > 5%)
3. **Trend analysis** is possible without downloading artifacts (e.g., "did the last workflow change improve review agent speed?")

## Scope

### Metrics to Export

| Metric | Type | Labels |
|--------|------|--------|
| `agent/llm_api_time_seconds` | Gauge | agent, model, repo, run_id |
| `agent/tool_execution_time_seconds` | Gauge | agent, repo, run_id |
| `agent/total_tokens` | Gauge | agent, model, repo, run_id |
| `agent/cached_token_ratio` | Gauge | agent, model, repo, run_id |
| `agent/api_requests` | Counter | agent, model, repo, run_id, status |
| `agent/workflow_duration_seconds` | Gauge | agent, repo, run_id |

### Export Mechanism

Options:
1. **Post-step in each workflow** — parse gemini-output artifact, write custom metrics via `gcloud monitoring` CLI or Cloud Monitoring API
2. **Dedicated metrics workflow** — triggered by `workflow_run.completed`, fetches artifacts from the completed run, exports metrics
3. **OpenTelemetry Collector** — emit OTLP from a workflow step, route to Cloud Monitoring via GCP's OTLP endpoint

### GCP Requirements

The service account used for metrics export needs:
- `roles/monitoring.metricWriter` — write custom metrics
- `roles/logging.logWriter` — (optional) structured log export
- `roles/cloudtrace.agent` — (optional) trace export

These roles are additive to the existing `roles/aiplatform.user` and `roles/modelarmor.user` on the CI service account.

## Dependencies

- [015](015-gcp-workload-identity-federation.md) — WIF auth for the metrics export step
- [001](001-fix-agent-performance.md) — defines the telemetry data format being exported
