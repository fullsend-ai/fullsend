package telemetry

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

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
	for _, test := range []struct {
		Name          string
		DisabledValue string
	}{
		{
			Name:          "is disabled lowercase",
			DisabledValue: "true",
		},
		{
			Name:          "is disabled uppercase",
			DisabledValue: "TRUE",
		},
		{
			Name:          "is disabled title case",
			DisabledValue: "True",
		},
		{
			Name:          "is disabled mixed case",
			DisabledValue: "truE",
		},
	} {
		t.Run(test.Name, func(t *testing.T) {
			sink := newOTLPSink(t)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
			t.Setenv("OTEL_SDK_DISABLED", test.DisabledValue)

			dir := t.TempDir()
			tracer, cleanup := Setup(dir, "1.0.0")

			_, span := tracer.Start(context.Background(), "disabled-span")
			span.End()
			cleanup(context.Background())

			assert.False(t, span.SpanContext().IsValid(), "noop tracer when SDK disabled")

			_, err := os.Stat(filepath.Join(dir, TelemetryFile))
			assert.True(t, os.IsNotExist(err), "no telemetry file when SDK disabled")

			assert.Equal(t, 0, sink.requestCount(), "no OTLP export when SDK disabled")
		})
	}
}

func TestSetup_NoEndpoint_FileOnly(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "file-only-span")
	assert.True(t, span.SpanContext().IsValid(), "tracer is active without OTLP endpoint")
	span.End()
	cleanup(context.Background())

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file exporter writes spans when no OTLP endpoint is set")
}

func TestSetup_TracesEndpointAlone(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL+"/v1/traces")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "traces-only-span")
	span.End()
	cleanup(context.Background())

	assert.Contains(t, sink.spanNames(), "traces-only-span",
		"OTLP exporter activates on OTEL_EXPORTER_OTLP_TRACES_ENDPOINT alone")

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file exporter still writes")
}

func TestSetup_GeneralEndpointAlone(t *testing.T) {
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "traces-only-span")
	span.End()
	cleanup(context.Background())

	assert.Contains(t, sink.spanNames(), "traces-only-span",
		"OTLP exporter activates on OTEL_EXPORTER_OTLP_TRACES_ENDPOINT alone")

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file exporter still writes")
}

func TestSetup_ExporterCreationFails(t *testing.T) {
	orig := newOTLPExporter
	t.Cleanup(func() { newOTLPExporter = orig })
	newOTLPExporter = func(_ context.Context) (sdktrace.SpanExporter, error) {
		return nil, fmt.Errorf("bad endpoint")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://bad-host:4318")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "span")
	assert.True(t, span.SpanContext().IsValid(), "if the OTLP fails file still has traces")
	span.End()

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file spans written when OTLP exporter creation fails")
}

func TestSetup_ExporterCreationFailsBadProtocol(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "httpsda1://bad-host:4318")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "span")
	assert.True(t, span.SpanContext().IsValid(), "if the OTLP fails file still has traces")
	span.End()

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file spans written when OTLP exporter creation fails")
}

func TestSetup_OTLPWirePath(t *testing.T) {
	t.Run("delivery_and_path", func(t *testing.T) {
		sink := newOTLPSink(t)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "wire-span")
		span.End()
		cleanup(context.Background())

		// File exporter wrote the span.
		data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
		require.NoError(t, err)
		require.NotEmpty(t, data)

		// OTLP exporter delivered the span as valid protobuf.
		require.NotEmpty(t, sink.spanNames(), "span must arrive at the OTLP collector")
		assert.Contains(t, sink.spanNames(), "wire-span")

		sink.mu.Lock()
		defer sink.mu.Unlock()
		assert.Equal(t, "/v1/traces", sink.paths[0])
	})

	t.Run("gzip_compression", func(t *testing.T) {
		sink := newOTLPSink(t)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
		t.Setenv("OTEL_EXPORTER_OTLP_COMPRESSION", "gzip")

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "gzip-span")
		span.End()
		cleanup(context.Background())

		require.NotEmpty(t, sink.spanNames())
		assert.Contains(t, sink.spanNames(), "gzip-span")

		sink.mu.Lock()
		defer sink.mu.Unlock()
		assert.Equal(t, "gzip", sink.headers[0].Get("Content-Encoding"))
	})

	t.Run("base_endpoint_appends_v1_traces", func(t *testing.T) {
		sink := newOTLPSink(t)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL+"/otlp")

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "path-span")
		span.End()
		cleanup(context.Background())

		require.NotEmpty(t, sink.spanNames())
		assert.Contains(t, sink.spanNames(), "path-span")

		sink.mu.Lock()
		defer sink.mu.Unlock()
		assert.Equal(t, "/otlp/v1/traces", sink.paths[0],
			"base endpoint must have /v1/traces appended per OTLP spec")
	})

	t.Run("traces_endpoint_precedence", func(t *testing.T) {
		primary := newOTLPSink(t)
		decoy := newOTLPSink(t)

		t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", primary.srv.URL)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", decoy.srv.URL)

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "precedence-span")
		span.End()
		cleanup(context.Background())

		assert.Contains(t, primary.spanNames(), "precedence-span")
		assert.Equal(t, 0, decoy.requestCount(), "generic endpoint must not receive spans when traces endpoint is set")
	})

	t.Run("custom_headers", func(t *testing.T) {
		sink := newOTLPSink(t)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
		t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "x-test-key=test-value")

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "header-span")
		span.End()
		cleanup(context.Background())

		require.NotEmpty(t, sink.spanNames())
		sink.mu.Lock()
		defer sink.mu.Unlock()
		assert.Equal(t, "test-value", sink.headers[0].Get("X-Test-Key"))
	})

	t.Run("retry_delivers_within_cli_flush_budget", func(t *testing.T) {
		var (
			mu        sync.Mutex
			attempts  int
			delivered bool
		)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()

			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}

			raw, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if r.Header.Get("Content-Encoding") == "gzip" {
				zr, err := gzip.NewReader(bytes.NewReader(raw))
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				raw, err = io.ReadAll(zr)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
			}
			var req coltracepb.ExportTraceServiceRequest
			if err := proto.Unmarshal(raw, &req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			mu.Lock()
			delivered = true
			mu.Unlock()
			resp, _ := proto.Marshal(&coltracepb.ExportTraceServiceResponse{})
			w.Header().Set("Content-Type", "application/x-protobuf")
			w.Write(resp)
		}))
		defer srv.Close()

		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "flush-budget-span")
		span.End()

		ctx, cancel := context.WithTimeout(context.Background(), FlushTimeout)
		defer cancel()
		cleanup(ctx)

		mu.Lock()
		defer mu.Unlock()
		assert.True(t, delivered,
			"retry after 503 must complete within the CLI flush budget")
	})

	t.Run("shutdown_does_not_hang_on_unreachable_endpoint", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().String()
		ln.Close()

		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+addr)

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "blackhole-span")
		span.End()

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		done := make(chan struct{})
		go func() {
			cleanup(ctx)
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("cleanup hung on unreachable endpoint")
		}

		data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
		require.NoError(t, err)
		assert.NotEmpty(t, data, "file exporter must still write when OTLP endpoint is unreachable")
	})
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
