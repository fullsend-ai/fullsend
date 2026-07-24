// Package telemetry implements fullsend's distributed tracing (ADR 0050).
//
// Setup configures an OpenTelemetry TracerProvider with two exporters:
//   - fileExporter (synchronous) writes every span as OTLP JSON to
//     run-telemetry.jsonl.
//   - otlptracehttp (batched) exports to a remote backend when an
//     OTEL_EXPORTER_OTLP_*ENDPOINT is configured.
//
// When neither exporter can be created, Setup returns a noop tracer so the
// run is never affected.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// TelemetryFile is the artifact name written to the output dir.
const TelemetryFile = "run-telemetry.jsonl"

// FlushTimeout is the budget for tp.Shutdown to flush pending spans at CLI exit.
const FlushTimeout = 5 * time.Second

const scopeName = "github.com/fullsend-ai/fullsend/internal/telemetry"

// newOTLPExporter is a seam over exporter construction for tests.
// The SDK reads OTEL_EXPORTER_OTLP_*ENDPOINT from the environment.
var newOTLPExporter = func(ctx context.Context) (sdktrace.SpanExporter, error) {
	retryOption := otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
		Enabled:         true,
		InitialInterval: 250 * time.Millisecond,
		MaxInterval:     2 * time.Second,
		MaxElapsedTime:  FlushTimeout, // Can't be longer than the flush timeout
	})
	return otlptracehttp.New(ctx, retryOption)
}

// Setup creates a TracerProvider with file and (optionally) OTLP exporters.
// On any failure it returns a noop tracer and an empty cleanup func so the
// run is never affected. The cleanup func shuts down the provider (flushing
// the OTLP batch processor) and closes the file; it should be called with a
// context that has enough budget for the OTLP flush (typically
// context.Background() with a 5s timeout).
func Setup(dir string, serviceVersion string) (trace.Tracer, func(context.Context)) {
	noop := func(context.Context) {}

	if sdkDisable := os.Getenv("OTEL_SDK_DISABLED"); strings.EqualFold(strings.TrimSpace(sdkDisable), "true") {
		return tracenoop.NewTracerProvider().Tracer(""), noop
	}

	f, err := os.OpenFile(filepath.Join(dir, TelemetryFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return tracenoop.NewTracerProvider().Tracer(""), noop
	}

	res := buildResource(serviceVersion)
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(newFileExporter(f))),
	}

	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
		exp, err := newOTLPExporter(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "fullsend: OTLP export setup failed: %v\n", err)
		} else {
			opts = append(opts, sdktrace.WithSpanProcessor(&parentSampledProcessor{base: sdktrace.NewBatchSpanProcessor(exp)}))
		}
	}

	tp := sdktrace.NewTracerProvider(opts...)
	tracer := tp.Tracer(scopeName, trace.WithInstrumentationVersion(serviceVersion))

	cleanup := func(ctx context.Context) {
		_ = tp.Shutdown(ctx)
		_ = f.Close()
	}

	return tracer, cleanup
}

// parentSampledProcessor wraps a SpanProcessor and only forwards spans whose
// trace was not explicitly unsampled by a remote parent. When a root span
// arrives with a remote unsampled parent, the entire trace is suppressed from
// OTLP export — not just the root.
type parentSampledProcessor struct {
	base       sdktrace.SpanProcessor
	suppressed sync.Map // trace.TraceID → struct{}
}

func (p *parentSampledProcessor) OnStart(parent context.Context, s sdktrace.ReadWriteSpan) {
	if psc := s.Parent(); psc.IsRemote() && !psc.IsSampled() {
		p.suppressed.Store(s.SpanContext().TraceID(), struct{}{})
	}
	p.base.OnStart(parent, s)
}

func (p *parentSampledProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if _, ok := p.suppressed.Load(s.SpanContext().TraceID()); ok {
		return
	}
	p.base.OnEnd(s)
}

func (p *parentSampledProcessor) Shutdown(ctx context.Context) error {
	return p.base.Shutdown(ctx)
}

func (p *parentSampledProcessor) ForceFlush(ctx context.Context) error {
	return p.base.ForceFlush(ctx)
}

func buildResource(serviceVersion string) *resource.Resource {
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			attribute.String("service.name", "fullsend"),
			attribute.String("service.version", serviceVersion),
		),
		resource.WithFromEnv(),
	)
	if err != nil || res == nil {
		return resource.NewSchemaless(
			attribute.String("service.name", "fullsend"),
			attribute.String("service.version", serviceVersion),
		)
	}
	return res
}
