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
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinOTELEnv clears ambient OTEL variables so tests are hermetic in CI
// (where OTEL_EXPORTER_OTLP_TRACES_ENDPOINT may be set by org vars). The
// span-limit variables are load-bearing for spanLimits, so every caller
// gets them cleared too.
func pinOTELEnv(t *testing.T) {
	t.Helper()
	t.Setenv("OTEL_SDK_DISABLED", "")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "")
	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "")
	t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", "")
	t.Setenv(ContentCaptureEnvVar, "")
}

func TestSetup_FileExporter(t *testing.T) {
	pinOTELEnv(t)
	orig := newOTLPExporter
	t.Cleanup(func() { newOTLPExporter = orig })
	var exporterCreated bool
	newOTLPExporter = func(_ context.Context) (sdktrace.SpanExporter, error) {
		exporterCreated = true
		return orig(context.Background())
	}

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0-test")

	_, span := tracer.Start(context.Background(), "test-span")
	span.End()

	cleanup(context.Background())

	assert.False(t, exporterCreated,
		"OTLP exporter must not be created when no endpoint is configured")

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	require.NotEmpty(t, data, "file exporter must have written span data")

	var td otlpTracesData
	require.NoError(t, json.Unmarshal(data, &td), "output must be valid OTLP JSON")
	require.NotEmpty(t, td.ResourceSpans)
	require.NotEmpty(t, td.ResourceSpans[0].ScopeSpans)
	assert.Equal(t, "test-span", td.ResourceSpans[0].ScopeSpans[0].Spans[0].Name)
}

// TestSetup_SpanAttributeValueLengthLimit pins the provider-level bound on
// attribute values: a free-text attribute (model name, skip reason) cannot
// ride an export at arbitrary size.
func TestSetup_SpanAttributeValueLengthLimit(t *testing.T) {
	pinOTELEnv(t)
	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0-test")

	_, span := tracer.Start(context.Background(), "attr-span")
	span.SetAttributes(attribute.String("fullsend.test_attr", strings.Repeat("a", 100_000)))
	span.End()
	cleanup(context.Background())

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	spans := doc["resourceSpans"].([]any)[0].(map[string]any)["scopeSpans"].([]any)[0].(map[string]any)["spans"].([]any)
	attrs := spans[0].(map[string]any)["attributes"].([]any)
	var got string
	for _, a := range attrs {
		kv := a.(map[string]any)
		if kv["key"] == "fullsend.test_attr" {
			got = kv["value"].(map[string]any)["stringValue"].(string)
		}
	}
	require.NotEmpty(t, got, "test attribute must be exported")
	assert.Len(t, got, MaxSpanAttrValueLen, "attribute value must be truncated to the provider limit")
}

// TestAttrLimit_SDKBehaviorCanary pins two SDK properties this package's
// callers depend on, so an SDK upgrade that changes either fails here
// instead of silently shifting the export contract: (1) the attribute
// limit counts characters, not bytes — a multibyte value truncates to
// MaxSpanAttrValueLen runes, which is more than MaxSpanAttrValueLen
// bytes on the wire; (2) an under-limit value is returned unchanged,
// invalid UTF-8 included — which is why free-text attribute values are
// repaired at their call sites (internal/cli stringAttr).
func TestAttrLimit_SDKBehaviorCanary(t *testing.T) {
	pinOTELEnv(t)
	record := func(val string) string {
		rec := tracetest.NewSpanRecorder()
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithRawSpanLimits(spanLimits()),
			sdktrace.WithSpanProcessor(rec),
		)
		_, span := tp.Tracer("t").Start(context.Background(), "s")
		span.SetAttributes(attribute.String("k", val))
		span.End()
		ended := rec.Ended()
		require.Len(t, ended, 1)
		for _, kv := range ended[0].Attributes() {
			if kv.Key == "k" {
				return kv.Value.AsString()
			}
		}
		t.Fatal("attribute not recorded")
		return ""
	}

	t.Run("limit counts characters, not bytes", func(t *testing.T) {
		got := record(strings.Repeat("é", MaxSpanAttrValueLen+100))
		assert.Equal(t, MaxSpanAttrValueLen, utf8.RuneCountInString(got))
		assert.Equal(t, 2*MaxSpanAttrValueLen, len(got),
			"two-byte runes make the byte size twice the limit")
	})

	t.Run("under-limit invalid UTF-8 passes through unrepaired", func(t *testing.T) {
		got := record("bad\xff\xfebytes")
		assert.False(t, utf8.ValidString(got),
			"the SDK repairs only when truncating — call sites own the repair")
	})
}

// TestSpanLimits pins the default and the operator-env precedence,
// including the explicit "-1" (unlimited) sentinel, which the SDK
// collapses to the same struct value as "unset".
func TestSpanLimits(t *testing.T) {
	pinOTELEnv(t)
	assert.Equal(t, MaxSpanAttrValueLen, spanLimits().AttributeValueLengthLimit,
		"unset env defaults to MaxSpanAttrValueLen")

	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "512")
	assert.Equal(t, 512, spanLimits().AttributeValueLengthLimit,
		"operator env setting wins over the default")

	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "-1")
	assert.Equal(t, -1, spanLimits().AttributeValueLengthLimit,
		"an explicit -1 (unlimited) is honored, not capped")

	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "")
	t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", "-1")
	assert.Equal(t, -1, spanLimits().AttributeValueLengthLimit,
		"the generic variable's explicit -1 is honored too")

	t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", "banana")
	assert.Equal(t, MaxSpanAttrValueLen, spanLimits().AttributeValueLengthLimit,
		"an unparseable value is not an operator setting; the default applies")

	// The SDK's firstInt short-circuits on the first non-empty key: an
	// unparseable specific variable falls back to the SDK default without
	// ever consulting the generic one. The helper must mirror that, or a
	// mixed valid/invalid override leaves attribute values unbounded.
	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "garbage")
	t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", "500")
	require.Equal(t, -1, sdktrace.NewSpanLimits().AttributeValueLengthLimit,
		"SDK ground truth: invalid specific var short-circuits, generic var ignored")
	assert.Equal(t, MaxSpanAttrValueLen, spanLimits().AttributeValueLengthLimit,
		"an override the SDK discarded is not an operator setting; the default applies")

	t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", "512")
	t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", "garbage")
	require.Equal(t, 512, sdktrace.NewSpanLimits().AttributeValueLengthLimit)
	assert.Equal(t, 512, spanLimits().AttributeValueLengthLimit,
		"a valid specific var wins regardless of the generic one")
}

// TestSetup_ContentCaptureOperatorLimitWarning pins the collision warning:
// when the Level 3 gate is on but an operator's finite attribute value
// length limit is configured, the SDK will cut gen_ai.output.messages
// mid-JSON (no fullsend.content.truncated marker reflects an SDK cut), so
// Setup must say so on stderr instead of letting the contract break
// silently. An explicit -1 (unlimited) is not a conflict.
func TestSetup_ContentCaptureOperatorLimitWarning(t *testing.T) {
	cases := []struct {
		name     string
		gate     string
		specific string // OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT
		generic  string // OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT
		want     bool
	}{
		{"gate on with finite specific limit warns", "true", "512", "", true},
		{"gate on with finite generic limit warns", "true", "", "8192", true},
		{"gate on with explicit unlimited does not warn", "true", "-1", "", false},
		{"gate on with no limit does not warn", "true", "", "", false},
		{"gate off with finite limit does not warn", "", "512", "", false},
		{"gate on with unparseable limit does not warn", "true", "garbage", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pinOTELEnv(t)
			t.Setenv(ContentCaptureEnvVar, tc.gate)
			t.Setenv("OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT", tc.specific)
			t.Setenv("OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT", tc.generic)

			pr, pw, err := os.Pipe()
			require.NoError(t, err)
			oldStderr := os.Stderr
			os.Stderr = pw
			defer func() { os.Stderr = oldStderr }()

			var captured []byte
			done := make(chan struct{})
			go func() {
				captured, _ = io.ReadAll(pr)
				close(done)
			}()

			dir := t.TempDir()
			_, cleanup := Setup(dir, "1.0.0")
			cleanup(context.Background())

			os.Stderr = oldStderr
			pw.Close()
			<-done

			if tc.want {
				assert.Contains(t, string(captured), "fullsend: content capture is enabled but the operator attribute value length limit",
					"Setup must warn when the operator limit will cut content JSON mid-value")
			} else {
				assert.NotContains(t, string(captured), "content capture",
					"Setup must not warn when the configuration cannot cut content JSON")
			}
		})
	}
}

func TestSetup_NoopOnBadDir(t *testing.T) {
	pinOTELEnv(t)
	tracer, cleanup := Setup("/nonexistent/path/that/should/fail", "1.0.0")
	defer cleanup(context.Background())

	_, span := tracer.Start(context.Background(), "noop-span")
	assert.False(t, span.SpanContext().IsValid(), "noop tracer produces invalid span context")
	span.End()
}

func TestSetup_SDKDisabled(t *testing.T) {
	for _, tt := range []struct {
		name          string
		disabledValue string
	}{
		{
			name:          "is disabled lowercase",
			disabledValue: "true",
		},
		{
			name:          "is disabled uppercase",
			disabledValue: "TRUE",
		},
		{
			name:          "is disabled title case",
			disabledValue: "True",
		},
		{
			name:          "is disabled mixed case",
			disabledValue: "truE",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pinOTELEnv(t)
			sink := newOTLPSink(t)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)
			t.Setenv("OTEL_SDK_DISABLED", tt.disabledValue)

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
	pinOTELEnv(t)
	orig := newOTLPExporter
	t.Cleanup(func() { newOTLPExporter = orig })
	var exporterCreated bool
	newOTLPExporter = func(_ context.Context) (sdktrace.SpanExporter, error) {
		exporterCreated = true
		return orig(context.Background())
	}

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "file-only-span")
	assert.True(t, span.SpanContext().IsValid(), "tracer is active without OTLP endpoint")
	span.End()
	cleanup(context.Background())

	assert.False(t, exporterCreated,
		"OTLP exporter must not be created when no endpoint is configured")

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file exporter writes spans when no OTLP endpoint is set")
}

func TestSetup_WhitespaceOnlyEndpoint_NoOTLP(t *testing.T) {
	pinOTELEnv(t)
	orig := newOTLPExporter
	t.Cleanup(func() { newOTLPExporter = orig })
	var exporterCreated bool
	newOTLPExporter = func(_ context.Context) (sdktrace.SpanExporter, error) {
		exporterCreated = true
		return orig(context.Background())
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "  \t ")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "  ")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "whitespace-span")
	assert.True(t, span.SpanContext().IsValid(), "tracer is active with file exporter")
	span.End()
	cleanup(context.Background())

	assert.False(t, exporterCreated,
		"OTLP exporter must not be created when endpoints are whitespace-only")

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file exporter writes spans")
}

func TestSetup_TracesEndpointAlone(t *testing.T) {
	pinOTELEnv(t)
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL+"/v1/traces")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "traces-only-span")
	span.End()
	cleanup(context.Background())

	assert.Contains(t, sink.spanNames(), "traces-only-span",
		"OTLP exporter activates on OTEL_EXPORTER_OTLP_TRACES_ENDPOINT alone")

	sink.mu.Lock()
	defer sink.mu.Unlock()
	assert.Equal(t, "/v1/traces", sink.paths[0],
		"signal-specific endpoint must be used verbatim, no path appended")

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file exporter still writes")
}

func TestSetup_TracesEndpointUsedVerbatim(t *testing.T) {
	pinOTELEnv(t)
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL+"/otlp")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "verbatim-span")
	span.End()
	cleanup(context.Background())

	assert.Contains(t, sink.spanNames(), "verbatim-span")

	sink.mu.Lock()
	defer sink.mu.Unlock()
	assert.Equal(t, "/otlp", sink.paths[0],
		"signal-specific endpoint path must be used verbatim, not have /v1/traces appended")
}

func TestSetup_GeneralEndpointAlone(t *testing.T) {
	pinOTELEnv(t)
	sink := newOTLPSink(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", sink.srv.URL)

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "traces-only-span")
	span.End()
	cleanup(context.Background())

	assert.Contains(t, sink.spanNames(), "traces-only-span",
		"OTLP exporter activates on OTEL_EXPORTER_OTLP_ENDPOINT alone")

	data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
	require.NoError(t, err)
	assert.NotEmpty(t, data, "file exporter still writes")
}

func TestSetup_ExporterCreationFails(t *testing.T) {
	pinOTELEnv(t)
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

func TestSetup_OTLPWirePath(t *testing.T) {
	t.Run("delivery_and_path", func(t *testing.T) {
		pinOTELEnv(t)
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
		pinOTELEnv(t)
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
		pinOTELEnv(t)
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
		pinOTELEnv(t)
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

		primary.mu.Lock()
		defer primary.mu.Unlock()
		assert.Equal(t, "/", primary.paths[0],
			"signal-specific endpoint must be used verbatim, no path appended")
	})

	t.Run("custom_headers", func(t *testing.T) {
		pinOTELEnv(t)
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
		pinOTELEnv(t)
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
		defer func() { srv.CloseClientConnections(); srv.Close() }()

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

	t.Run("persistent_503_emits_stderr_warning", func(t *testing.T) {
		pinOTELEnv(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer func() { srv.CloseClientConnections(); srv.Close() }()

		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "doomed-span")
		span.End()

		pr, pw, err := os.Pipe()
		require.NoError(t, err)
		oldStderr := os.Stderr
		os.Stderr = pw
		defer func() { os.Stderr = oldStderr }()

		// Drain the pipe concurrently so cleanup can't deadlock
		// filling the pipe buffer.
		var captured []byte
		done := make(chan struct{})
		go func() {
			captured, _ = io.ReadAll(pr)
			close(done)
		}()

		ctx, cancel := context.WithTimeout(context.Background(), FlushTimeout)
		defer cancel()
		cleanup(ctx)

		os.Stderr = oldStderr
		pw.Close()
		<-done

		assert.Contains(t, string(captured), "fullsend: telemetry flush incomplete:",
			"cleanup must warn on stderr when OTLP export fails persistently")
	})

	t.Run("bare_ip_port_rejected_by_validation", func(t *testing.T) {
		pinOTELEnv(t)
		// url.Parse("127.0.0.1:PORT") returns a parse error ("first path
		// segment in URL cannot contain colon"), so validateEndpoints
		// rejects it before the SDK is ever invoked.
		sink := newOTLPSink(t)

		addr := strings.TrimPrefix(sink.srv.URL, "http://")
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", addr)

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "schemeless-span")
		span.End()

		ctx, cancel := context.WithTimeout(context.Background(), FlushTimeout)
		defer cancel()
		cleanup(ctx)

		assert.Equal(t, 0, sink.requestCount(),
			"bare IP:port fails url.Parse and must be rejected by validation")

		data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
		require.NoError(t, err)
		assert.NotEmpty(t, data, "file exporter still writes when OTLP export fails")
	})

	t.Run("shutdown_does_not_hang_on_hanging_endpoint", func(t *testing.T) {
		pinOTELEnv(t)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer ln.Close()

		// Accept connections but never respond; close each in its own
		// goroutine when the listener shuts down (via defer above).
		var conns []net.Conn
		var connsMu sync.Mutex
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				connsMu.Lock()
				conns = append(conns, conn)
				connsMu.Unlock()
			}
		}()
		t.Cleanup(func() {
			connsMu.Lock()
			defer connsMu.Unlock()
			for _, c := range conns {
				c.Close()
			}
		})

		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://"+ln.Addr().String())

		dir := t.TempDir()
		tracer, cleanup := Setup(dir, "1.0.0-wire")

		_, span := tracer.Start(context.Background(), "blackhole-span")
		span.End()

		ctx, cancel := context.WithTimeout(context.Background(), FlushTimeout)
		defer cancel()

		start := time.Now()
		cleanup(ctx)
		elapsed := time.Since(start)

		assert.Less(t, elapsed, FlushTimeout+time.Second,
			"cleanup must return within the flush budget even when the endpoint hangs")

		data, err := os.ReadFile(filepath.Join(dir, TelemetryFile))
		require.NoError(t, err)
		assert.NotEmpty(t, data, "file exporter must still write when OTLP endpoint hangs")
	})
}

func TestValidateEndpoints(t *testing.T) {
	tests := []struct {
		name           string
		endpoint       string
		tracesEndpoint string
		wantErr        string // substring; empty means no error
	}{
		{
			name:           "both empty",
			endpoint:       "",
			tracesEndpoint: "",
		},
		{
			name:           "valid endpoint alone",
			endpoint:       "http://localhost:4318",
			tracesEndpoint: "",
		},
		{
			name:           "valid traces endpoint alone",
			endpoint:       "",
			tracesEndpoint: "https://backend:4318/v1/traces",
		},
		{
			name:           "traces endpoint takes precedence over endpoint",
			endpoint:       "not-a-url",
			tracesEndpoint: "https://backend:4318/v1/traces",
		},
		{
			name:           "endpoint used when traces endpoint empty",
			endpoint:       "not-a-url",
			tracesEndpoint: "",
			wantErr:        "no scheme",
		},
		{
			name:           "parse error on bare ip:port",
			endpoint:       "127.0.0.1:4318",
			tracesEndpoint: "",
			wantErr:        "cannot contain colon",
		},
		{
			name:           "no scheme rejected",
			endpoint:       "localhost",
			tracesEndpoint: "",
			wantErr:        "no scheme",
		},
		{
			name:           "unsupported scheme rejected",
			endpoint:       "ftp://localhost:4318",
			tracesEndpoint: "",
			wantErr:        "not supported",
		},
		{
			name:           "no host rejected",
			endpoint:       "http://",
			tracesEndpoint: "",
			wantErr:        "no host",
		},
		{
			name:           "http scheme accepted",
			endpoint:       "http://collector:4318",
			tracesEndpoint: "",
		},
		{
			name:           "https scheme accepted",
			endpoint:       "https://collector:4318",
			tracesEndpoint: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEndpoints(tt.endpoint, tt.tracesEndpoint)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestSetup_SchemelessEndpointFailed(t *testing.T) {
	pinOTELEnv(t)
	orig := newOTLPExporter
	t.Cleanup(func() { newOTLPExporter = orig })

	var called bool
	newOTLPExporter = func(_ context.Context) (sdktrace.SpanExporter, error) {
		called = true
		return nil, fmt.Errorf("expected: SDK will reject this")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "not-a-url")

	dir := t.TempDir()
	_, cleanup := Setup(dir, "1.0.0")
	cleanup(context.Background())

	assert.False(t, called, "schemeless string fails validation")
}

func TestSetup_SchemelessTracesEndpointFailed(t *testing.T) {
	pinOTELEnv(t)
	orig := newOTLPExporter
	t.Cleanup(func() { newOTLPExporter = orig })

	var called bool
	newOTLPExporter = func(_ context.Context) (sdktrace.SpanExporter, error) {
		called = true
		return nil, fmt.Errorf("expected: SDK will reject this")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "yes-a-url")

	dir := t.TempDir()
	_, cleanup := Setup(dir, "1.0.0")
	cleanup(context.Background())

	assert.False(t, called, "schemeless string fails validation")
}

func TestSetup_SchemelessEndpointsFailed(t *testing.T) {
	pinOTELEnv(t)
	orig := newOTLPExporter
	t.Cleanup(func() { newOTLPExporter = orig })

	var called bool
	newOTLPExporter = func(_ context.Context) (sdktrace.SpanExporter, error) {
		called = true
		return nil, fmt.Errorf("expected: SDK will reject this")
	}

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "not-a-url")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "yes-a-url")

	dir := t.TempDir()
	_, cleanup := Setup(dir, "1.0.0")
	cleanup(context.Background())

	assert.False(t, called, "schemeless string fails validation")
}

func TestSetup_InvalidEndpointValidTracesEndpoint(t *testing.T) {
	pinOTELEnv(t)
	sink := newOTLPSink(t)

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "not-a-url")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", sink.srv.URL+"/v1/traces")

	dir := t.TempDir()
	tracer, cleanup := Setup(dir, "1.0.0")

	_, span := tracer.Start(context.Background(), "precedence-bypass-span")
	span.End()
	cleanup(context.Background())

	assert.Contains(t, sink.spanNames(), "precedence-bypass-span",
		"valid TRACES_ENDPOINT must not be blocked by an invalid generic ENDPOINT")
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
