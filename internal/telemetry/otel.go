package telemetry

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	serviceName     = "fullsend-cli"
	shutdownTimeout = 5 * time.Second
)

// Config controls telemetry initialization.
type Config struct {
	// Enabled turns telemetry on. When false, a noop tracer is returned
	// and no events file is written to disk.
	Enabled bool

	// OTLPEndpoint is the OTLP HTTP endpoint (e.g. "localhost:4318").
	// When empty, the SDK reads OTEL_EXPORTER_OTLP_ENDPOINT from the
	// environment. When neither is set, spans are exported only to the
	// run-events.jsonl file (no network export).
	OTLPEndpoint string

	// ServiceVersion is the fullsend CLI version string.
	ServiceVersion string
}

// ConfigFromEnv builds a Config from environment variables.
// Telemetry is enabled when FULLSEND_TELEMETRY=1 or when
// OTEL_EXPORTER_OTLP_ENDPOINT is set (opt-in via either mechanism).
func ConfigFromEnv() Config {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	explicit := os.Getenv("FULLSEND_TELEMETRY")
	return Config{
		Enabled:      explicit == "1" || explicit == "true" || endpoint != "",
		OTLPEndpoint: endpoint,
	}
}

// TracerProvider holds the initialized OTEL provider and its shutdown function.
type TracerProvider struct {
	provider *sdktrace.TracerProvider
	Tracer   trace.Tracer
}

// Shutdown flushes remaining spans and releases resources.
func (tp *TracerProvider) Shutdown(ctx context.Context) error {
	if tp == nil || tp.provider == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	return tp.provider.Shutdown(shutdownCtx)
}

// NoopProvider returns a TracerProvider with a noop tracer. Used as a safe
// fallback when initialization fails.
func NoopProvider() *TracerProvider {
	return &TracerProvider{Tracer: trace.NewNoopTracerProvider().Tracer(serviceName)}
}

// InitTracer sets up an OTEL TracerProvider. When the config has an OTLP
// endpoint (explicit or via env), spans are exported over HTTP. When no
// endpoint is configured, the tracer still produces valid trace/span IDs
// for the events file — consumers get structured telemetry without
// running a collector.
func InitTracer(ctx context.Context, cfg Config) (*TracerProvider, error) {
	if !cfg.Enabled {
		return &TracerProvider{Tracer: trace.NewNoopTracerProvider().Tracer(serviceName)}, nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTEL resource: %w", err)
	}

	var opts []sdktrace.TracerProviderOption
	opts = append(opts, sdktrace.WithResource(res))

	// When an OTLP endpoint is available, configure the HTTP exporter.
	// If the endpoint comes from OTEL_EXPORTER_OTLP_ENDPOINT (the standard
	// env var), let the SDK read it directly — it expects a full URL
	// (e.g. "http://localhost:4318") and handles scheme/path parsing.
	// WithEndpoint() expects bare host:port and would break with a scheme.
	if cfg.OTLPEndpoint != "" || os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exporter, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("creating OTLP exporter: %w", err)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	} else {
		// No collector configured. Use a simple syncer that drops spans.
		// The structured events file (run-events.jsonl) is the primary
		// output in this mode — it captures trace/span IDs from the SDK
		// so consumers can correlate without running a collector.
		opts = append(opts, sdktrace.WithSampler(sdktrace.AlwaysSample()))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return &TracerProvider{
		provider: tp,
		Tracer:   tp.Tracer(serviceName),
	}, nil
}
