package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func clearTracingEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS",
		headersFileEnv, "MINT_DEPLOYMENT_NAME",
	} {
		t.Setenv(k, "")
	}
}

func writeHeadersFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "otlp-headers")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuildTracer_DisabledWithoutEndpoint(t *testing.T) {
	clearTracingEnv(t)
	t.Setenv(headersFileEnv, "/nonexistent")
	tr, err := buildTracer()
	if err != nil || tr != nil {
		t.Fatalf("expected disabled tracer, got %v, %v", tr, err)
	}
}

func TestBuildTracer_EnabledWithHeadersFile(t *testing.T) {
	clearTracingEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "ignored=1")
	t.Setenv("MINT_DEPLOYMENT_NAME", "mint-a")
	t.Setenv(headersFileEnv, writeHeadersFile(t, "Authorization=Bearer%20abc\nX-Team=blue\n", 0o600))
	tr, err := buildTracer()
	if err != nil || tr == nil {
		t.Fatalf("expected tracer, got %v, %v", tr, err)
	}
}

func TestBuildTracer_TracesEndpointWithoutHeaders(t *testing.T) {
	clearTracingEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://otel-collector:4318/v1/traces")
	tr, err := buildTracer()
	if err != nil || tr == nil {
		t.Fatalf("expected tracer, got %v, %v", tr, err)
	}
}

func TestBuildTracer_Errors(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"invalid endpoint": func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "not a url")
		},
		"missing headers file": func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example")
			t.Setenv(headersFileEnv, filepath.Join(t.TempDir(), "missing"))
		},
		"world-readable headers file": func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example")
			t.Setenv(headersFileEnv, writeHeadersFile(t, "Authorization=secretvalue", 0o644))
		},
		"directory headers file": func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example")
			t.Setenv(headersFileEnv, t.TempDir())
		},
		"malformed headers file": func(t *testing.T) {
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example")
			t.Setenv(headersFileEnv, writeHeadersFile(t, "secretvalue", 0o600))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			clearTracingEnv(t)
			setup(t)
			tr, err := buildTracer()
			if err == nil || tr != nil {
				t.Fatalf("expected error, got tracer=%v err=%v", tr, err)
			}
			if strings.Contains(err.Error(), "secretvalue") {
				t.Fatalf("error leaks header value: %v", err)
			}
		})
	}
}

func TestBuildHandler_TracingConfigError(t *testing.T) {
	clearTracingEnv(t)
	t.Setenv("PEM_DIR", setupTestPEMDir(t))
	t.Setenv("ROLE_APP_IDS", `{"coder":"1"}`)
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "ftp://collector")
	if _, err := buildHandler(); err == nil {
		t.Fatal("expected buildHandler to reject an invalid tracing config")
	}
}
