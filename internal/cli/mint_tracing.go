package cli

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/fullsend-ai/fullsend/internal/dispatch/cf"
	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// maxOTLPHeadersFileSize bounds the collector header file read by
// `mint deploy --otlp-headers-file`.
const maxOTLPHeadersFileSize = 64 << 10

// mintTracingConfig is the validated tracing configuration for
// `fullsend mint deploy`.
type mintTracingConfig struct {
	// endpoint is the resolved OTLP/HTTP traces URL stamped into the
	// build artifact. Empty disables tracing.
	endpoint string
	// headers is the collector header file content
	// (OTEL_EXPORTER_OTLP_HEADERS syntax), stored as a platform secret.
	// Nil when no header file was given.
	headers []byte
	// noHeaders is set by --otlp-no-headers: the collector takes no
	// authentication headers, so no collector headers secret is bound to
	// the new deployment.
	noHeaders bool
	// headerCount is the number of parsed headers, for reporting.
	headerCount int
}

// enabled reports whether a traces endpoint is configured.
func (c mintTracingConfig) enabled() bool { return c.endpoint != "" }

// endpointOrigin returns scheme://host of the endpoint for display.
func (c mintTracingConfig) endpointOrigin() string {
	u, err := url.Parse(c.endpoint)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// resolveMintTracing validates the tracing flags of `mint deploy`. The
// endpoint flags follow OTEL_EXPORTER_OTLP_TRACES_ENDPOINT (used
// verbatim) and OTEL_EXPORTER_OTLP_ENDPOINT (base URL, "/v1/traces"
// appended) precedence. Header values are read from a protected file so
// they never appear in command-line arguments. noHeaders is the
// --otlp-no-headers flag; it is mutually exclusive with headersFile and
// requires an endpoint.
func resolveMintTracing(tracesEndpoint, baseEndpoint, headersFile string, noHeaders bool) (mintTracingConfig, error) {
	if noHeaders && headersFile != "" {
		return mintTracingConfig{}, errors.New("--otlp-no-headers and --otlp-headers-file are mutually exclusive")
	}
	endpoint := mintcore.ResolveOTLPTracesEndpoint(tracesEndpoint, baseEndpoint)
	if endpoint == "" {
		if headersFile != "" {
			return mintTracingConfig{}, errors.New("--otlp-headers-file requires --otlp-traces-endpoint or --otlp-endpoint")
		}
		if noHeaders {
			return mintTracingConfig{}, errors.New("--otlp-no-headers requires --otlp-traces-endpoint or --otlp-endpoint")
		}
		return mintTracingConfig{}, nil
	}
	if err := cf.ValidateTraceEndpoint(endpoint); err != nil {
		return mintTracingConfig{}, err
	}
	cfg := mintTracingConfig{endpoint: endpoint, noHeaders: noHeaders}
	if headersFile != "" {
		data, count, err := readOTLPHeadersFile(headersFile)
		if err != nil {
			return mintTracingConfig{}, err
		}
		cfg.headers = data
		cfg.headerCount = count
	}
	return cfg, nil
}

// readOTLPHeadersFile reads and validates a collector header file. The
// file must be a regular file that is not accessible by group or others.
// Errors never include header values.
func readOTLPHeadersFile(path string) ([]byte, int, error) {
	// Reject non-regular paths before opening: os.Open on a FIFO with no
	// writer blocks indefinitely, so the descriptor check below would
	// never be reached. That check stays to cover a path that changes
	// between the stat and the open.
	pre, err := os.Stat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("reading --otlp-headers-file: %w", err)
	}
	if !pre.Mode().IsRegular() {
		return nil, 0, errors.New("--otlp-headers-file must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("reading --otlp-headers-file: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("reading --otlp-headers-file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errors.New("--otlp-headers-file must be a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, 0, fmt.Errorf("--otlp-headers-file must not be accessible by group or others (mode %04o; use chmod 600)", info.Mode().Perm())
	}
	data, err := io.ReadAll(io.LimitReader(f, maxOTLPHeadersFileSize+1))
	if err != nil {
		return nil, 0, fmt.Errorf("reading --otlp-headers-file: %w", err)
	}
	if len(data) > maxOTLPHeadersFileSize {
		return nil, 0, fmt.Errorf("--otlp-headers-file is larger than %d bytes", maxOTLPHeadersFileSize)
	}
	trimmed := strings.TrimSpace(string(data))
	headers, err := mintcore.ParseOTLPHeaders(trimmed)
	if err != nil {
		return nil, 0, fmt.Errorf("parsing --otlp-headers-file: %w", err)
	}
	if len(headers) == 0 {
		return nil, 0, errors.New("--otlp-headers-file contains no headers")
	}
	for k, v := range headers {
		if strings.ContainsAny(v, "\r\n\x00") {
			return nil, 0, fmt.Errorf("--otlp-headers-file: invalid value for header %q", k)
		}
	}
	return []byte(trimmed), len(headers), nil
}
