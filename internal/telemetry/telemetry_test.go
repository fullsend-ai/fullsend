package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetup_FileExporter(t *testing.T) {
	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0-test")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "test-span")
	span.End()

	cleanup(context.Background())

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	require.NotEmpty(t, data, "file exporter must have written span data")

	var td otlpTracesData
	require.NoError(t, json.Unmarshal(data, &td), "output must be valid OTLP JSON")
	require.NotEmpty(t, td.ResourceSpans)
	require.NotEmpty(t, td.ResourceSpans[0].ScopeSpans)
	assert.Equal(t, "test-span", td.ResourceSpans[0].ScopeSpans[0].Spans[0].Name)
}

func TestSetup_NoopOnBadDir(t *testing.T) {
	tracer, cleanup := Setup("/nonexistent/path/that/should/fail", "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "noop-span")
	assert.False(t, span.SpanContext().IsValid(), "noop tracer produces invalid span context")
	span.End()
}

func TestSetup_SDKDisabled(t *testing.T) {
	t.Setenv("OTEL_SDK_DISABLED", "true")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "test")
	assert.False(t, span.SpanContext().IsValid(), "SDK disabled returns noop tracer")
	span.End()

	_, err := os.Stat(filepath.Join(dir, TelemetryFile))
	assert.True(t, os.IsNotExist(err), "no telemetry file when SDK disabled")
}

func TestSetup_OTLPExporterNone(t *testing.T) {
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "test")
	assert.True(t, span.SpanContext().IsValid())
	span.End()
}

func TestSetup_OTLPExporterSeam(t *testing.T) {
	orig := newOTLPExporter
	defer func() { newOTLPExporter = orig }()

	var called bool
	newOTLPExporter = func(_ context.Context, _ string) (sdktrace.SpanExporter, error) {
		called = true
		return orig(context.Background(), "http://localhost:4318")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "")

	dir := t.TempDir()
	_, cleanup := Setup(dir, "1.0.0")
	cleanup(context.Background())

	assert.True(t, called, "OTLP exporter must be created when endpoint is set")
}

func TestSetup_TracesEndpointPreferred(t *testing.T) {
	orig := newOTLPExporter
	defer func() { newOTLPExporter = orig }()

	var called bool
	newOTLPExporter = func(_ context.Context, _ string) (sdktrace.SpanExporter, error) {
		called = true
		return orig(context.Background(), "http://localhost:4318")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://traces.local:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://generic.local:4318")
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "")

	dir := t.TempDir()
	_, cleanup := Setup(dir, "1.0.0")
	cleanup(context.Background())

	assert.True(t, called, "OTLP exporter created when traces-specific endpoint set")
}

func TestSetup_InvalidEndpointSkipsOTLP(t *testing.T) {
	orig := newOTLPExporter
	defer func() { newOTLPExporter = orig }()

	newOTLPExporter = func(_ context.Context, _ string) (sdktrace.SpanExporter, error) {
		t.Fatal("should not be called for invalid endpoint")
		return nil, nil
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "not-a-url")
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "test")
	assert.True(t, span.SpanContext().IsValid(), "file exporter still works")
	span.End()
}

func TestSetup_UnsupportedProtocolSkipsOTLP(t *testing.T) {
	orig := newOTLPExporter
	defer func() { newOTLPExporter = orig }()

	newOTLPExporter = func(_ context.Context, _ string) (sdktrace.SpanExporter, error) {
		t.Fatal("should not be called for unsupported protocol")
		return nil, nil
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "test")
	assert.True(t, span.SpanContext().IsValid(), "file exporter still works")
	span.End()
}

func TestSetup_TracesProtocolPreferred(t *testing.T) {
	orig := newOTLPExporter
	defer func() { newOTLPExporter = orig }()

	newOTLPExporter = func(_ context.Context, _ string) (sdktrace.SpanExporter, error) {
		t.Fatal("traces protocol=grpc should block exporter creation")
		return nil, nil
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4317")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_PROTOCOL", "grpc")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/protobuf")
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "")

	dir := t.TempDir()
	_, cleanup := Setup(dir, "1.0.0")
	cleanup(context.Background())
}

// spyProcessor records span names forwarded to OnEnd.
type spyProcessor struct {
	ended []string
}

func (s *spyProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}
func (s *spyProcessor) OnEnd(span sdktrace.ReadOnlySpan)                { s.ended = append(s.ended, span.Name()) }
func (s *spyProcessor) Shutdown(context.Context) error                  { return nil }
func (s *spyProcessor) ForceFlush(context.Context) error                { return nil }

func TestParentSampledProcessor_SuppressesEntireTrace(t *testing.T) {
	spy := &spyProcessor{}
	proc := &parentSampledProcessor{base: spy}

	// Simulate an unsampled remote parent.
	remoteUnsampled := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{1},
		TraceFlags: 0, // not sampled
		Remote:     true,
	})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(proc),
	)
	tracer := tp.Tracer("test")

	// Root span under unsampled remote parent.
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), remoteUnsampled)
	ctx, root := tracer.Start(ctx, "root")
	// Child span — local parent, should also be suppressed.
	_, child := tracer.Start(ctx, "child")
	child.End()
	root.End()

	assert.Empty(t, spy.ended, "no spans should reach OTLP when remote parent is unsampled")
}

func TestParentSampledProcessor_AllowsSampledTrace(t *testing.T) {
	spy := &spyProcessor{}
	proc := &parentSampledProcessor{base: spy}

	remoteSampled := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{2},
		SpanID:     trace.SpanID{2},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(proc),
	)
	tracer := tp.Tracer("test")

	ctx := trace.ContextWithRemoteSpanContext(context.Background(), remoteSampled)
	ctx, root := tracer.Start(ctx, "root")
	_, child := tracer.Start(ctx, "child")
	child.End()
	root.End()

	assert.ElementsMatch(t, []string{"root", "child"}, spy.ended)
}

func TestSetup_OTLPExporterError(t *testing.T) {
	orig := newOTLPExporter
	defer func() { newOTLPExporter = orig }()

	newOTLPExporter = func(_ context.Context, _ string) (sdktrace.SpanExporter, error) {
		return nil, fmt.Errorf("connection refused")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_TRACES_EXPORTER", "")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "test")
	assert.True(t, span.SpanContext().IsValid(), "file exporter works even when OTLP fails")
	span.End()
}
