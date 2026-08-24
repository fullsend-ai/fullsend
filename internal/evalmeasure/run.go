package evalmeasure

import (
	"context"
	"fmt"
	"path/filepath"
)

type persistHookKey struct{}

// WithPersistHook runs fn after each successful RecordScored. Tests use it
// to fail a later persist; production callers pass a plain context.
func WithPersistHook(ctx context.Context, fn func()) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, persistHookKey{}, fn)
}

// MeasureFile parses telemetry, scores with the manifest, and writes local
// eval-measurements.jsonl. Idempotent per ledger.
func MeasureFile(telemetryPath, registryPath, outDir string) ([]EvaluationResult, error) {
	r, _, err := MeasureAndExport(context.Background(), telemetryPath, registryPath, outDir, "")
	return r, err
}

// MeasureAndExport is MeasureFile with an explicit context used for portable
// OTLP score export (same OTEL_EXPORTER_OTLP_* path as ADR 0050). Local
// JSONL/ledger always win; OTLP failures are recorded on ParseStats and
// never fail the measure. serviceVersion should match telemetry.Setup
// (CLI Version()) so remote score resources share agent-trace identity.
func MeasureAndExport(ctx context.Context, telemetryPath, registryPath, outDir, serviceVersion string) ([]EvaluationResult, ParseStats, error) {
	var stats ParseStats
	if err := ctx.Err(); err != nil {
		return nil, stats, err
	}
	if outDir == "" {
		outDir = filepath.Dir(telemetryPath)
	}
	reg, err := LoadRegistry(registryPath)
	if err != nil {
		return nil, stats, fmt.Errorf("load registry: %w", err)
	}
	traces, stats, parseErr := ParseTelemetryFile(telemetryPath)
	if parseErr != nil && len(traces) == 0 {
		return nil, stats, fmt.Errorf("parse telemetry: %w", parseErr)
	}
	if parseErr != nil {
		// Oversized/corrupt tail after good lines: score what we have and
		// surface the damage via stats (CLI warns; still exit 0).
		stats.Incomplete = parseErr.Error()
	}

	ledgerPath := filepath.Join(outDir, LedgerFile)
	measPath := filepath.Join(outDir, MeasurementsFile)
	var all []EvaluationResult
	hook, _ := ctx.Value(persistHookKey{}).(func())

	// Fail-open OTLP for any rows already persisted+ledgered, including when
	// a later row hits a mid-loop persist error (do not silently drop remote
	// export for the successful prefix).
	exportScored := func() {
		if len(all) == 0 {
			return
		}
		if err := ExportOTLPScores(ctx, all, serviceVersion); err != nil {
			stats.RemoteExportWarning = err.Error()
		}
	}

	for _, tr := range traces {
		results := ScoreTrace(tr, reg)
		for _, r := range results {
			done, err := AlreadyScored(ledgerPath, r.TraceID, r.Name, r.Version)
			if err != nil {
				exportScored()
				return all, stats, fmt.Errorf("check ledger: %w", err)
			}
			if done {
				continue
			}
			if err := AppendMeasurements(measPath, []EvaluationResult{r}); err != nil {
				exportScored()
				return all, stats, fmt.Errorf("append measurements: %w", err)
			}
			// Only count rows that landed in eval-measurements.jsonl so
			// CLI stdout matches disk on a later ledger/write error.
			all = append(all, r)
			if err := RecordScored(ledgerPath, r.TraceID, r.Name, r.Version); err != nil {
				exportScored()
				return all, stats, fmt.Errorf("record scored: %w", err)
			}
			if hook != nil {
				hook()
			}
		}
	}
	exportScored()
	// Partial parse with traces already scored is success: scores are data.
	// stats.Incomplete (if set) lets the CLI warn without failing the job.
	return all, stats, nil
}
