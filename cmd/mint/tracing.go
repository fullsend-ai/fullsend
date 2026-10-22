package main

import (
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"runtime"
	"syscall"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// headersFileEnv names the file holding collector authentication headers.
// Header values are read from a protected file rather than the
// environment so they are not exposed through process listings or
// shared environment dumps.
const headersFileEnv = "MINT_OTLP_HEADERS_FILE"

// buildTracer configures mint tracing from the standard OpenTelemetry
// endpoint variables. It returns a nil tracer (tracing disabled, no
// export attempts) when no traces endpoint is set.
func buildTracer() (*mintcore.Tracer, error) {
	endpoint := mintcore.ResolveOTLPTracesEndpoint(
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"),
		os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"),
	)
	if endpoint == "" {
		if os.Getenv(headersFileEnv) != "" {
			log.Printf("warning: %s is set but no OTLP traces endpoint is configured; tracing is disabled", headersFileEnv)
		}
		return nil, nil
	}
	if os.Getenv("OTEL_EXPORTER_OTLP_HEADERS") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS") != "" {
		log.Printf("warning: OTEL_EXPORTER_OTLP_HEADERS and OTEL_EXPORTER_OTLP_TRACES_HEADERS are ignored by the standalone mint; put collector headers in %s", headersFileEnv)
	}

	var headers map[string]string
	if path := os.Getenv(headersFileEnv); path != "" {
		var err error
		headers, err = readHeadersFile(path)
		if err != nil {
			return nil, err
		}
	}

	deployment := os.Getenv("MINT_DEPLOYMENT_NAME")
	if deployment == "" {
		deployment, _ = os.Hostname()
	}

	tracer, err := mintcore.NewTracer(mintcore.TracingConfig{
		Endpoint:   endpoint,
		Headers:    headers,
		Backend:    mintcore.TraceBackendStandalone,
		Deployment: deployment,
		// The server outlives requests, so export off the response path.
		AsyncExport: true,
	})
	if err != nil {
		return nil, fmt.Errorf("configuring tracing: %w", err)
	}
	if u, perr := url.Parse(endpoint); perr == nil {
		log.Printf("tracing enabled: exporting spans to %s://%s (%d collector header(s))", u.Scheme, u.Host, len(headers))
	}
	return tracer, nil
}

// readHeadersFile reads collector headers in OTEL_EXPORTER_OTLP_HEADERS
// syntax (comma- or newline-separated key=value pairs). The file must not
// be readable by group or others.
func readHeadersFile(path string) (map[string]string, error) {
	// Open once and check the descriptor, so the type and permission
	// checks apply to the same file that is read. O_NONBLOCK keeps a FIFO
	// from blocking the open before the regular-file check rejects it.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", headersFileEnv, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", headersFileEnv, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", headersFileEnv)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must not be accessible by group or others (mode %04o; use chmod 600)", headersFileEnv, info.Mode().Perm())
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", headersFileEnv, err)
	}
	headers, err := mintcore.ParseOTLPHeaders(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", headersFileEnv, err)
	}
	return headers, nil
}
