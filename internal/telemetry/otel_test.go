package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

func TestInitTracer_Disabled(t *testing.T) {
	tp, err := InitTracer(context.Background(), Config{Enabled: false})
	require.NoError(t, err)
	require.NotNil(t, tp)
	require.NotNil(t, tp.Tracer)

	// Noop tracer should not produce valid span contexts.
	ctx, span := tp.Tracer.Start(context.Background(), "test")
	defer span.End()
	sc := trace.SpanContextFromContext(ctx)
	assert.False(t, sc.IsValid())

	assert.NoError(t, tp.Shutdown(context.Background()))
}

func TestInitTracer_EnabledNoEndpoint(t *testing.T) {
	// Enabled without an endpoint — should still create a real tracer
	// that produces valid trace IDs for the events file.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	tp, err := InitTracer(context.Background(), Config{
		Enabled:        true,
		ServiceVersion: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, tp)

	ctx, span := tp.Tracer.Start(context.Background(), "test")
	defer span.End()
	sc := trace.SpanContextFromContext(ctx)
	assert.True(t, sc.IsValid())

	assert.NoError(t, tp.Shutdown(context.Background()))
}

func TestConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("FULLSEND_TELEMETRY", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	cfg := ConfigFromEnv()
	assert.False(t, cfg.Enabled)
}

func TestConfigFromEnv_TelemetryFlag(t *testing.T) {
	t.Setenv("FULLSEND_TELEMETRY", "1")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	cfg := ConfigFromEnv()
	assert.True(t, cfg.Enabled)
}

func TestConfigFromEnv_OTLPEndpoint(t *testing.T) {
	t.Setenv("FULLSEND_TELEMETRY", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://localhost:4318")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	cfg := ConfigFromEnv()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, "http://localhost:4318", cfg.OTLPEndpoint)
}

func TestConfigFromEnv_TracesEndpoint(t *testing.T) {
	t.Setenv("FULLSEND_TELEMETRY", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "https://mlflow.example.com/v1/traces")
	cfg := ConfigFromEnv()
	assert.True(t, cfg.Enabled, "traces-specific endpoint should enable telemetry")
}

func TestTracerProvider_ShutdownNil(t *testing.T) {
	var tp *TracerProvider
	assert.NoError(t, tp.Shutdown(context.Background()))
}

func TestInitTracer_UnreachableEndpoint_StillWorks(t *testing.T) {
	// Simulates a misconfigured endpoint. The exporter will fail to
	// connect but the SDK should still produce valid trace/span IDs
	// so local files remain useful.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://192.0.2.1:4318") // RFC 5737 TEST-NET
	tp, err := InitTracer(context.Background(), Config{
		Enabled:        true,
		OTLPEndpoint:   "http://192.0.2.1:4318",
		ServiceVersion: "test",
	})
	require.NoError(t, err)
	require.NotNil(t, tp)

	ctx, span := tp.Tracer.Start(context.Background(), "test-op")
	sc := trace.SpanContextFromContext(ctx)
	assert.True(t, sc.IsValid(), "should produce valid span context even with unreachable endpoint")
	span.End()

	// Shutdown may return a timeout error when the endpoint is unreachable —
	// the important thing is it doesn't panic or block indefinitely.
	_ = tp.Shutdown(context.Background())
}
