// Command telemetry-replay replays a run directory's Level 1 telemetry
// artifacts (run-telemetry.jsonl + run-summary.json) through the production
// OTLP export path (internal/telemetry/otlp). It exists for validating the
// Level 2 export against a real backend without running an agent: point the
// standard OTEL_EXPORTER_OTLP_* env vars at the backend and replay a
// captured artifact directory.
//
//	OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
//	  go run ./hack/telemetry-replay --input /path/to/run-dir
//
// For MLflow (>= 3.6):
//
//	OTEL_EXPORTER_OTLP_TRACES_ENDPOINT={server}/v1/traces \
//	OTEL_EXPORTER_OTLP_TRACES_HEADERS="x-mlflow-experiment-id={id}" \
//	  go run ./hack/telemetry-replay --input /path/to/run-dir
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fullsend-ai/fullsend/internal/telemetry"
	"github.com/fullsend-ai/fullsend/internal/telemetry/otlp"
)

func main() {
	dir := flag.String("input", "", "run directory containing "+telemetry.TelemetryFile+" and "+telemetry.SummaryFile)
	version := flag.String("service-version", "telemetry-replay-dev", "service.version resource attribute")
	flag.Parse()

	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: telemetry-replay --input <run-dir>")
		os.Exit(2)
	}
	if !otlp.Enabled() {
		fmt.Fprintln(os.Stderr, "no OTLP endpoint configured: set OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")
		os.Exit(2)
	}

	if err := otlp.ExportRunDir(*dir, *version); err != nil {
		fmt.Fprintln(os.Stderr, "export failed:", err)
		os.Exit(1)
	}

	// Echo the trace id so the operator can find the trace in the backend.
	var s struct {
		TraceID string `json:"trace_id"`
	}
	if data, err := os.ReadFile(filepath.Join(*dir, telemetry.SummaryFile)); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	fmt.Printf("exported %s (trace_id %s)\n", *dir, s.TraceID)
}
