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
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// TelemetryFile is the artifact name written to the output dir.
const TelemetryFile = "run-telemetry.jsonl"

const scopeName = "github.com/fullsend-ai/fullsend/internal/telemetry"

// newOTLPExporter is a seam over exporter construction for tests.
var newOTLPExporter = func(ctx context.Context, endpoint string) (sdktrace.SpanExporter, error) {
	return otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(endpoint))
}

// Setup creates a TracerProvider with file and (optionally) OTLP exporters.
// On any failure it returns a noop tracer and an empty cleanup func so the
// run is never affected. The cleanup func shuts down the provider (flushing
// the OTLP batch processor) and closes the file; it should be called with a
// context that has enough budget for the OTLP flush (typically
// context.Background() with a 5s timeout).
func Setup(dir string, serviceVersion string) (trace.Tracer, func(context.Context)) {
	noop := func(context.Context) {}

	if isSDKDisabled() {
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

	if endpoint := endpointFromEnv(); endpoint != "" && !isExporterNone() {
		if err := validateEndpoint(endpoint); err != nil {
			fmt.Fprintf(os.Stderr, "fullsend: OTLP export skipped: %v\n", err)
		} else if exp, err := newOTLPExporter(context.Background(), endpoint); err != nil {
			fmt.Fprintf(os.Stderr, "fullsend: OTLP exporter failed: %v\n", err)
		} else {
			opts = append(opts, sdktrace.WithSpanProcessor(
				&parentSampledProcessor{base: sdktrace.NewBatchSpanProcessor(exp)},
			))
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

func endpointFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
}

func isSDKDisabled() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_SDK_DISABLED")), "true")
}

func isExporterNone() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("OTEL_TRACES_EXPORTER")), "none")
}

func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("OTLP endpoint %q is not an absolute http(s) URL with a host", endpoint)
	}
	if p := protocolFromEnv(); p != "" && p != "http/protobuf" {
		return fmt.Errorf("OTEL_EXPORTER_OTLP_(TRACES_)PROTOCOL %q is not supported (only http/protobuf)", p)
	}
	return nil
}

func protocolFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL")); v != "" {
		return v
	}
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_PROTOCOL"))
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
