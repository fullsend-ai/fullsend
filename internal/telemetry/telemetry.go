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
	"strconv"
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

// MaxSpanAttrValueLen bounds span attribute values recorded through this
// provider in metadata-only mode. When the Level 3 content gate is on,
// spanLimits lifts the provider-wide cap (a capped cut would corrupt the
// content JSON mid-value), so free-text values that relied on this cap
// are bounded at their call sites instead (internal/cli boundedStringAttr). The SDK applies the limit to span attributes only —
// event messages are bounded at their call site — counting characters,
// not bytes (a multibyte value can reach four bytes per character on the
// wire), and it repairs invalid UTF-8 only when it truncates: values at
// or under the limit pass through unrepaired, so free-text attribute
// values are repaired at their call sites (internal/cli stringAttr).
// Both properties are pinned by TestAttrLimit_SDKBehaviorCanary. The
// exception-event bound in internal/cli (maxSpanEventMsgLen, bytes) is
// defined from this constant so the shared numeric default cannot drift,
// each side applying it in its own unit. It applies only when the
// SDK took no operator override — the first non-empty of
// OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT and
// OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT decides alone, and a parseable value
// there, including -1 (unlimited), is honored as-is.
const MaxSpanAttrValueLen = 8192

// spanLimits returns the SDK span limits. NewSpanLimits collapses "env
// unset" and an explicit "-1" (the OTel sentinel for unlimited) to the
// same struct value, so the env vars are consulted directly: the
// MaxSpanAttrValueLen default applies only when the deciding variable —
// the first non-empty one — holds no parseable integer.
func spanLimits() sdktrace.SpanLimits {
	limits := sdktrace.NewSpanLimits()
	if limits.AttributeValueLengthLimit < 0 && !attrValueLenConfigured() {
		if ContentCaptureEnabled() {
			// Level 3 puts JSON-string content attributes on spans. The
			// default cap would cut such a value mid-string and corrupt
			// the JSON; the content collector's byte budget is the size
			// bound, so the SDK cap stays unlimited. An operator's
			// explicit limit env var still wins above.
			return limits
		}
		limits.AttributeValueLengthLimit = MaxSpanAttrValueLen
	}
	return limits
}

// attrValueLenConfigured reports whether the SDK honored an operator's
// attribute value-length limit.
func attrValueLenConfigured() bool {
	_, ok := operatorAttrValueLimit()
	return ok
}

// operatorAttrValueLimit resolves the operator's attribute value-length
// limit env vars to the value the SDK honored, reporting ok=false when no
// operator setting took effect. It mirrors the SDK's firstInt resolution
// exactly (sdk/trace/internal/env, v1.44.0): the first non-empty variable
// decides alone — if its value fails strconv.Atoi, the SDK falls back to
// its default without consulting the second variable, so a discarded
// override is not a setting here either.
func operatorAttrValueLimit() (int, bool) {
	for _, key := range []string{"OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT"} {
		v := os.Getenv(key)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	return 0, false
}

// warnContentCaptureAttrLimit warns on stderr when the Level 3 content
// gate is on but an operator's finite attribute value-length limit is
// configured. The operator limit wins over the gate's cap lift
// (spanLimits), so the SDK will cut any gen_ai.output.messages value over
// the limit mid-JSON — in both sinks, with no fullsend.content.truncated
// marker (that marker reflects only collector-side cuts) — silently
// breaking the documented consumer contract. Telemetry never fails a run
// (ADR 0050), so the collision is surfaced, not fatal. An explicit -1
// (unlimited) cannot cut and is not a conflict.
func warnContentCaptureAttrLimit() {
	if !ContentCaptureEnabled() {
		return
	}
	if limit, ok := operatorAttrValueLimit(); ok && limit >= 0 {
		fmt.Fprintf(os.Stderr,
			"fullsend: content capture is enabled but the operator attribute value length limit (%d) is set; "+
				"gen_ai.output.messages values over the limit will be cut mid-JSON (unparseable, and "+
				"fullsend.content.truncated will not flag the cut) — raise the limit or unset it to keep content parseable\n",
			limit)
	}
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

	warnContentCaptureAttrLimit()

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
