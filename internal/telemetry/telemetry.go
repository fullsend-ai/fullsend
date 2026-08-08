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
	})
	return otlptracehttp.New(ctx, retryOption)
}

func validateEndpoints(endpoint, tracesEndpoint string) error {
	// The SDK uses TRACES_ENDPOINT when set, falling back to ENDPOINT.
	// Validate only the value that will actually be used.
	ep := tracesEndpoint
	if ep == "" {
		ep = endpoint
	}
	if ep == "" {
		return nil
	}

	u, err := url.Parse(ep)
	if err != nil {
		return err
	}

	if u.Scheme == "" {
		return fmt.Errorf("endpoint %q has no scheme, it is required", ep)
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("endpoint %q uses the %q scheme which is not supported", ep, u.Scheme)
	}

	if u.Host == "" {
		return fmt.Errorf("endpoint %q has no host, it is required", ep)
	}

	return nil
}

// maxSpanAttrValueLen bounds every span attribute value recorded through
// this provider. The SDK applies the limit (with UTF-8-safe truncation)
// to span attributes only — event messages are bounded at their call
// site — and free-text attributes such as a transcript-derived model
// name or a pre-script skip reason are otherwise unbounded. 8192 matches
// the exception-event bound in internal/cli; an operator's
// OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT / OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT
// setting takes precedence.
const maxSpanAttrValueLen = 8192

// spanLimits returns the SDK span limits (env vars included), with the
// attribute value-length limit defaulted to maxSpanAttrValueLen when the
// operator has not set one.
func spanLimits() sdktrace.SpanLimits {
	limits := sdktrace.NewSpanLimits()
	if limits.AttributeValueLengthLimit < 0 {
		limits.AttributeValueLengthLimit = maxSpanAttrValueLen
	}
	return limits
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
		sdktrace.WithRawSpanLimits(spanLimits()),
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(newFileExporter(f))),
	}

	endpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	tracesEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"))
	if endpoint != "" || tracesEndpoint != "" {
		if err := validateEndpoints(endpoint, tracesEndpoint); err != nil {
			fmt.Fprintf(os.Stderr, "fullsend: OTLP endpoints validation failed: %v\n", err)
		} else {
			exp, err := newOTLPExporter(context.Background())
			if err != nil {
				fmt.Fprintf(os.Stderr, "fullsend: OTLP export setup failed: %v\n", err)
			} else {
				opts = append(opts, sdktrace.WithSpanProcessor(&parentSampledProcessor{base: sdktrace.NewBatchSpanProcessor(exp)}))
			}
		}
	}

	tp := sdktrace.NewTracerProvider(opts...)
	tracer := tp.Tracer(scopeName, trace.WithInstrumentationVersion(serviceVersion))

	cleanup := func(ctx context.Context) {
		if err := tp.Shutdown(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "fullsend: telemetry flush incomplete: %v\n", err)
		}
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
