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
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel"
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
	return NewOTLPExporter(ctx)
}

// OTLPEnabled reports whether an OTLP traces endpoint is configured via
// OTEL_EXPORTER_OTLP_ENDPOINT or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT.
func OTLPEnabled() bool {
	return strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")) != "" ||
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")) != ""
}

// valueFreeParseErrors lists fixed net/url parse error messages that never
// quote any part of the input.
var valueFreeParseErrors = map[string]bool{
	"missing protocol scheme": true,
	"empty url":               true,
	"first path segment in URL cannot contain colon": true,
	"net/url: invalid control character in URL":      true,
	"net/url: invalid userinfo":                      true,
	"invalid IP-literal":                             true,
	"missing ']' in host":                            true,
}

// sanitizeError strips raw URLs and escape sequences from errors to avoid leaking credentials.
func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		var escErr url.EscapeError
		if errors.As(urlErr.Err, &escErr) {
			return fmt.Sprintf("%s: invalid URL escape", urlErr.Op)
		}
		if urlErr.Err != nil {
			// url.Parse quotes the offending port text, which can hold a
			// secret (e.g. "http://user:pass" with the '@host' missing).
			if strings.HasPrefix(urlErr.Err.Error(), "invalid port ") {
				return fmt.Sprintf("%s: invalid port after host", urlErr.Op)
			}
			// Other url.Parse failures (e.g. "invalid host: ParseAddr(...)"
			// for a bracketed host) can quote part of the input, so only
			// verified value-free messages are kept; the rest are reduced
			// to a generic reason.
			if urlErr.Op == "parse" {
				if valueFreeParseErrors[urlErr.Err.Error()] {
					return fmt.Sprintf("%s: %v", urlErr.Op, urlErr.Err)
				}
				return fmt.Sprintf("%s: invalid URL", urlErr.Op)
			}
			return fmt.Sprintf("%s: %v", urlErr.Op, urlErr.Err)
		}
		return urlErr.Op
	}
	var escErr url.EscapeError
	if errors.As(err, &escErr) {
		return "invalid URL escape"
	}
	return err.Error()
}

// redactingLogSink is a logr.LogSink that drops keysAndValues when logging errors,
// ensuring secret headers and credentials are never emitted to stderr while still
// surfacing real misconfiguration errors like invalid TLS certificates or durations.
type redactingLogSink struct {
	out io.Writer
}

func (s *redactingLogSink) Init(info logr.RuntimeInfo)                       {}
func (s *redactingLogSink) Enabled(level int) bool                           { return true }
func (s *redactingLogSink) Info(level int, msg string, keysAndValues ...any) {}
func (s *redactingLogSink) Error(err error, msg string, keysAndValues ...any) {
	w := s.out
	if w == nil {
		w = os.Stderr
	}
	if err != nil {
		fmt.Fprintf(w, "fullsend: otel: %s: %s\n", msg, sanitizeError(err))
	} else {
		fmt.Fprintf(w, "fullsend: otel: %s\n", msg)
	}
}
func (s *redactingLogSink) WithValues(keysAndValues ...any) logr.LogSink { return s }
func (s *redactingLogSink) WithName(name string) logr.LogSink            { return s }

var installLoggerOnce sync.Once

// InstallOTELRedactingLogger configures the global OpenTelemetry logger with a
// redacting sink that emits error messages without keysAndValues context.
// Safe for concurrent use and runs at most once per process.
func InstallOTELRedactingLogger() {
	installLoggerOnce.Do(func() {
		otel.SetLogger(NewRedactingLogger(nil))
	})
}

// NewRedactingLogger returns a logr.Logger backed by a redactingLogSink writing to w.
func NewRedactingLogger(w io.Writer) logr.Logger {
	return logr.New(&redactingLogSink{out: w})
}

// NewOTLPExporter builds the HTTP OTLP span exporter from OTEL_* env
// (same path Setup uses for agent traces). Callers must validate endpoints
// first with ValidateOTLPEndpoints when they want fail-closed setup.
func NewOTLPExporter(ctx context.Context) (sdktrace.SpanExporter, error) {
	InstallOTELRedactingLogger()
	retryOption := otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
		Enabled:         true,
		InitialInterval: 250 * time.Millisecond,
		MaxInterval:     2 * time.Second,
	})
	return otlptracehttp.New(ctx, retryOption)
}

// NewOTLPExporterBounded is NewOTLPExporter with MaxElapsedTime set so
// post-hoc exporters (eval scores) cannot retry forever on a flaky collector.
func NewOTLPExporterBounded(ctx context.Context, maxElapsed time.Duration) (sdktrace.SpanExporter, error) {
	InstallOTELRedactingLogger()
	retryOption := otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
		Enabled:         true,
		InitialInterval: 250 * time.Millisecond,
		MaxInterval:     2 * time.Second,
		MaxElapsedTime:  maxElapsed,
	})
	return otlptracehttp.New(ctx, retryOption)
}

// BuildResource returns the fullsend OTLP resource (service.name, version,
// plus OTEL_RESOURCE_ATTRIBUTES). Shared by agent Setup and score export so
// backends that group by resource keep both on the same service identity.
func BuildResource(serviceVersion string) *resource.Resource {
	if serviceVersion == "" {
		serviceVersion = "unknown"
	}
	return buildResource(serviceVersion)
}

// ValidateOTLPEndpoints checks all configured OTEL endpoint env vars
// (OTEL_EXPORTER_OTLP_ENDPOINT and OTEL_EXPORTER_OTLP_TRACES_ENDPOINT)
// that the SDK will parse for traces export.
func ValidateOTLPEndpoints() error {
	return validateEndpoints(
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
		strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")),
	)
}

func validateEndpoints(endpoint, tracesEndpoint string) error {
	// The SDK parses both endpoint env vars; validate both to prevent SDK-level leaks.
	if err := validateEndpoint("OTEL_EXPORTER_OTLP_ENDPOINT", endpoint); err != nil {
		return err
	}
	return validateEndpoint("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", tracesEndpoint)
}

func validateEndpoint(envVar, ep string) error {
	ep = strings.TrimSpace(ep)
	if ep == "" {
		return nil
	}

	// Errors never echo the endpoint value: a misconfigured endpoint can carry
	// credentials in userinfo, the query string, or (when the scheme is
	// missing) a userinfo-like prefix that url.Parse reads as the scheme.
	u, err := url.Parse(ep)
	if err != nil {
		return fmt.Errorf("%s: %s", envVar, sanitizeError(err))
	}

	if u.Scheme == "" {
		return fmt.Errorf("%s: endpoint has no scheme, it is required", envVar)
	}

	if u.Scheme != "https" && u.Scheme != "http" {
		if wellKnownSchemes[u.Scheme] {
			return fmt.Errorf("%s: endpoint scheme %q is not supported, use http or https", envVar, u.Scheme)
		}
		return fmt.Errorf("%s: endpoint scheme is not supported, use http or https", envVar)
	}

	if u.Host == "" {
		return fmt.Errorf("%s: endpoint has no host, it is required", envVar)
	}

	return nil
}

// wellKnownSchemes lists unsupported schemes that are safe to name in
// validation errors. Any other parsed scheme may be part of a secret (e.g.
// the "user" in a schemeless "user:pass@host:port"), so it is not echoed.
var wellKnownSchemes = map[string]bool{
	"grpc": true, "grpcs": true,
	"ws": true, "wss": true,
	"tcp": true, "udp": true, "unix": true,
	"ftp": true, "ftps": true, "file": true,
}

// ValidateOTLPHeaders checks the OTEL header env vars (OTEL_EXPORTER_OTLP_HEADERS
// and OTEL_EXPORTER_OTLP_TRACES_HEADERS) for syntax errors like missing '=',
// invalid RFC 7230 token characters in keys, or invalid URL-escapes in values.
func ValidateOTLPHeaders() error {
	if err := validateHeaders("OTEL_EXPORTER_OTLP_HEADERS", os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")); err != nil {
		return err
	}
	return validateHeaders("OTEL_EXPORTER_OTLP_TRACES_HEADERS", os.Getenv("OTEL_EXPORTER_OTLP_TRACES_HEADERS"))
}

func isTokenChar(c rune) bool {
	return c <= unicode.MaxASCII && (unicode.IsLetter(c) ||
		unicode.IsDigit(c) ||
		c == '!' || c == '#' || c == '$' || c == '%' || c == '&' || c == '\'' || c == '*' ||
		c == '+' || c == '-' || c == '.' || c == '^' || c == '_' || c == '`' || c == '|' || c == '~')
}

func isValidHeaderKey(key string) bool {
	if key == "" {
		return false
	}
	for _, c := range key {
		if !isTokenChar(c) {
			return false
		}
	}
	return true
}

func validateHeaders(envVar, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for i, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		key, val, found := strings.Cut(entry, "=")
		if !found {
			if strings.Contains(entry, ":") {
				return fmt.Errorf("%s: entry %d: OTLP headers must use 'key=value' format, found ':' separator", envVar, i)
			}
			return fmt.Errorf("%s: entry %d: missing '=' separator", envVar, i)
		}
		key = strings.TrimSpace(key)
		if !isValidHeaderKey(key) {
			if strings.Contains(key, ":") {
				return fmt.Errorf("%s: entry %d: OTLP headers must use 'key=value' format, found ':' separator", envVar, i)
			}
			return fmt.Errorf("%s: entry %d: invalid header key", envVar, i)
		}
		if _, err := url.PathUnescape(strings.TrimSpace(val)); err != nil {
			return fmt.Errorf("%s: entry %d: invalid URL escape in header value", envVar, i)
		}
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

// SpanLimits returns the SDK span limits used by Setup (default 8KiB
// attribute value length unless OTEL_*_ATTRIBUTE_VALUE_LENGTH_LIMIT is set,
// or unlimited when Level 3 content capture lifts the provider cap).
func SpanLimits() sdktrace.SpanLimits {
	return spanLimits()
}

// FreeTextAttrValueLenLimit is the call-site bound for free-text values the
// SDK does not truncate (event attributes) or that must stay intact when
// Level 3 content capture lifts SpanLimits. Honors an explicit operator
// OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT / OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT
// (including -1 = unlimited); otherwise defaults to MaxSpanAttrValueLen.
// Independent of ContentCaptureEnabled — that gate only lifts the provider
// cap for content JSON span attrs.
func FreeTextAttrValueLenLimit() int {
	if n, ok := operatorAttrValueLimit(); ok {
		return n
	}
	return MaxSpanAttrValueLen
}

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
// (spanLimits), so the SDK will cut any gen_ai.input.messages or
// gen_ai.output.messages value over the limit mid-JSON — in both sinks,
// with no fullsend.content.truncated marker (that marker reflects only
// collector-side cuts) — silently breaking the documented consumer
// contract. Telemetry never fails a run (ADR 0050), so the collision is
// surfaced, not fatal. An explicit -1 (unlimited) cannot cut and is not a
// conflict.
func warnContentCaptureAttrLimit() {
	if !ContentCaptureEnabled() {
		return
	}
	if limit, ok := operatorAttrValueLimit(); ok && limit >= 0 {
		fmt.Fprintf(os.Stderr,
			"fullsend: content capture is enabled but the operator attribute value length limit (%d) is set; "+
				"gen_ai.input.messages and gen_ai.output.messages values over the limit will be cut mid-JSON (unparseable, and "+
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
	InstallOTELRedactingLogger()

	noop := func(context.Context) {}

	if sdkDisable := os.Getenv("OTEL_SDK_DISABLED"); strings.EqualFold(strings.TrimSpace(sdkDisable), "true") {
		return tracenoop.NewTracerProvider().Tracer(""), noop
	}

	f, err := os.OpenFile(filepath.Join(dir, TelemetryFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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
		} else if err := ValidateOTLPHeaders(); err != nil {
			fmt.Fprintf(os.Stderr, "fullsend: OTLP headers validation failed: %v\n", err)
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
