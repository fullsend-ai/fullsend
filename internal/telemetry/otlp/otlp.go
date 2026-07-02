// Package otlp implements ADR 0050 Level 2: best-effort OTLP/HTTP export of
// the Level 1 telemetry artifacts. Export is a pure function of the run
// directory — run-telemetry.jsonl and run-summary.json are replayed into
// OpenTelemetry span snapshots and sent through the OTel Go SDK's OTLP/HTTP
// exporter — so the exported trace is, by construction, the same trace the
// local files record.
//
// The package never affects the run: it is inert unless a standard
// OTEL_EXPORTER_OTLP_(TRACES_)ENDPOINT is configured, every network
// operation is bounded by a hard wall-clock deadline, and all failures are
// returned as an error the caller may surface as a warning — the exit code
// and the Level 1 artifacts are never touched. "Non-blocking flush" is
// implemented as a bounded flush: a fire-and-forget goroutine would be
// killed at process exit and silently lose every span.
//
// All endpoint, header, TLS, timeout, and compression configuration is
// delegated to the exporter's standard env-var handling, which matches the
// contract published in docs/guides/infrastructure/distributed-tracing.md.
package otlp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// exportTimeout is the hard wall-clock budget for the entire export
// (exporter construction, send incl. retries, shutdown). It intentionally
// derives from context.Background(), not the run context: at Finalize time
// after a cancellation the run context is already dead, and the traces of
// failed runs are the most valuable ones. Package variable as a test seam.
var exportTimeout = 5 * time.Second

// newExporter is a seam over exporter construction. Retries are capped well
// below exportTimeout — the SDK defaults (5s initial backoff, 1min max
// elapsed) are tuned for long-lived services, not a CLI at exit.
var newExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
	return otlptracehttp.New(ctx, otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
		Enabled:         true,
		InitialInterval: 1 * time.Second,
		MaxInterval:     2 * time.Second,
		MaxElapsedTime:  4 * time.Second,
	}))
}

// endpointFromEnv returns the configured OTLP traces endpoint, honoring the
// standard precedence: the signal-specific variable wins over the generic
// one. Whitespace-only values count as unset per the OTel spec.
func endpointFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
}

// Enabled reports whether an OTLP traces endpoint is configured.
func Enabled() bool {
	return endpointFromEnv() != ""
}

// ExportRunDir exports the completed spans recorded in dir's Level 1
// artifacts. It is a no-op (nil) when no endpoint is configured, when the
// standard kill switches (OTEL_SDK_DISABLED, OTEL_TRACES_EXPORTER=none) are
// set, when the run's trace is unsampled (W3C sampled bit unset — an
// upstream sampling decision must be respected), or when the artifacts are
// missing or contain no completed spans. It never blocks longer than
// exportTimeout and never modifies anything in dir.
func ExportRunDir(dir, serviceVersion string) error {
	endpoint := endpointFromEnv()
	if endpoint == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true") {
		return nil
	}
	if strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")) == "none" {
		return nil
	}
	// Pre-validate: on a malformed endpoint value the SDK reports to its
	// global error handler and silently falls back to localhost:4318 — a
	// typo would spray spans at localhost. Refuse instead.
	if u, err := url.Parse(endpoint); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("OTLP endpoint %q is not an http(s) URL; export skipped", endpoint)
	}
	// Only OTLP over HTTP/protobuf is implemented (matches MLflow and the
	// published guide). Silently posting protobuf at a gRPC-only endpoint
	// fails cryptically, so refuse loudly.
	if p := protocolFromEnv(); p != "" && p != "http/protobuf" {
		return fmt.Errorf("OTEL_EXPORTER_OTLP_PROTOCOL %q is not supported (only http/protobuf); export skipped", p)
	}

	spans, sampled, err := readRun(dir, serviceVersion)
	if err != nil {
		return err
	}
	if !sampled || len(spans) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()

	exp, err := newExporter(ctx)
	if err != nil {
		return fmt.Errorf("creating OTLP exporter: %w", err)
	}
	expErr := exp.ExportSpans(ctx, spans)
	shutErr := exp.Shutdown(ctx)
	if err := errors.Join(expErr, shutErr); err != nil {
		return fmt.Errorf("exporting %d spans: %w", len(spans), err)
	}
	return nil
}

// protocolFromEnv returns the configured OTLP protocol (signal-specific
// variable wins), or "" when unset.
func protocolFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
}
