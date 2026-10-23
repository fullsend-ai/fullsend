package mintcore

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Mint tracing is a deliberately small, dependency-free implementation of
// the subset of OpenTelemetry the mint needs: W3C trace-context
// extraction, span recording, and OTLP/HTTP JSON export through mintHTTP.
// The OpenTelemetry Go SDK is not used because mintcore compiles to the
// Cloudflare Worker WASM binary, whose size budget the SDK and its
// protobuf dependencies would exceed. See ADR 0050 and
// docs/contributing/mintcore.md.

// Trace backend identifiers recorded as fullsend.mint.backend.
const (
	TraceBackendGCF        = "gcp-cloud-function"
	TraceBackendCloudflare = "cloudflare-worker"
	TraceBackendStandalone = "standalone"
)

const (
	defaultTraceServiceName   = "fullsend-mint"
	defaultTraceExportTimeout = 2 * time.Second
	traceScopeName            = "github.com/fullsend-ai/fullsend/internal/mintcore"
	maxSpanErrorLen           = 256
	maxTraceStateLen          = 512
	maxTraceAttrLen           = 256

	// Local export budget. Inbound traceparent sampling flags are advisory:
	// the mint enforces its own sampling rate and export concurrency so
	// unauthenticated callers cannot drive unbounded collector exports.
	defaultMaxSampledPerSecond  = 20
	defaultMaxConcurrentExports = 4
)

// Failure stages recorded as fullsend.mint.failure_stage.
const (
	stageRequestValidation  = "request_validation"
	stageRouting            = "routing"
	stageAuthentication     = "authentication"
	stageOIDCVerification   = "oidc_verification"
	stageAuthorization      = "authorization"
	stagePEMAccess          = "pem_access"
	stageGitHubAppAuth      = "github_app_auth"
	stageInstallationLookup = "installation_lookup"
	stageTokenIssuance      = "token_issuance"
	stageForeignGrant       = "foreign_grant"
	stageBotRoleConfig      = "bot_role_config"
	stageAppsLookup         = "github_apps_lookup"
)

// OTLP span kinds and status codes (opentelemetry-proto trace.proto).
const (
	spanKindInternal = 1
	spanKindServer   = 2

	spanStatusError = 2
)

// TracingConfig configures mint trace export. It holds plain data only so
// it is safe to construct on every platform, including WASM.
type TracingConfig struct {
	// Endpoint is the full OTLP/HTTP traces URL (for example
	// https://collector.example/v1/traces). Empty disables tracing.
	// Use ResolveOTLPTracesEndpoint to derive it from the standard
	// OTEL_EXPORTER_OTLP_* settings.
	Endpoint string
	// Headers are sent with every export request (collector
	// authentication). Values are never logged or recorded on spans.
	Headers map[string]string
	// ServiceName is the service.name resource attribute. Defaults to
	// "fullsend-mint".
	ServiceName string
	// Backend identifies the mint implementation (TraceBackend*).
	Backend string
	// Deployment identifies the specific deployment (function name,
	// Worker name, preview alias, host).
	Deployment string
	// ExportTimeout bounds each export attempt. Defaults to 2s.
	ExportTimeout time.Duration
	// MaxSampledPerSecond caps how many requests per second are recorded
	// and exported, regardless of inbound sampling flags. Defaults to 20.
	MaxSampledPerSecond int
	// MaxConcurrentExports caps in-flight exports; telemetry is dropped
	// when the cap is reached. Defaults to 4.
	MaxConcurrentExports int
	// AsyncExport exports spans from a background goroutine after the
	// response has been returned, so a slow or unreachable collector
	// cannot delay or fail mint responses. Use it where the process
	// outlives the request (the standalone server) and call
	// Tracer.Shutdown on exit to drain pending exports. Leave it off for
	// serverless consumers, which cannot run work after returning.
	AsyncExport bool
}

// Tracer records mint request spans and exports them over OTLP/HTTP.
// A nil *Tracer is a valid no-op tracer: it records nothing and makes no
// export attempt.
type Tracer struct {
	endpoint   string
	headers    map[string]string
	resource   []otlpKeyValue
	backend    string
	deployment string
	timeout    time.Duration

	// sampleMu guards the token bucket that enforces the local sampling
	// budget.
	sampleMu     sync.Mutex
	sampleRate   float64
	sampleBurst  float64
	sampleTokens float64
	sampleLast   time.Time
	// exportSem admits at most cap(exportSem) concurrent exports.
	exportSem chan struct{}
	// async exports from a background goroutine; pending tracks them so
	// Shutdown can drain.
	async   bool
	pending sync.WaitGroup
}

// NewTracer validates cfg and returns a Tracer. It returns (nil, nil)
// when cfg.Endpoint is empty, which disables tracing.
func NewTracer(cfg TracingConfig) (*Tracer, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("invalid OTLP traces endpoint: must be an absolute http(s) URL")
	}
	if u.User != nil {
		return nil, errors.New("invalid OTLP traces endpoint: credentials must be supplied as headers, not in the URL")
	}
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		if !validHeaderName(k) {
			return nil, fmt.Errorf("invalid OTLP header name %q", k)
		}
		if strings.ContainsAny(v, "\r\n\x00") {
			return nil, fmt.Errorf("invalid value for OTLP header %q", k)
		}
		headers[k] = v
	}
	timeout := cfg.ExportTimeout
	if timeout <= 0 {
		timeout = defaultTraceExportTimeout
	}
	serviceName := cfg.ServiceName
	if serviceName == "" {
		serviceName = defaultTraceServiceName
	}
	rate := cfg.MaxSampledPerSecond
	if rate <= 0 {
		rate = defaultMaxSampledPerSecond
	}
	maxExports := cfg.MaxConcurrentExports
	if maxExports <= 0 {
		maxExports = defaultMaxConcurrentExports
	}
	t := &Tracer{
		endpoint:     endpoint,
		headers:      headers,
		backend:      cfg.Backend,
		deployment:   cfg.Deployment,
		timeout:      timeout,
		sampleRate:   float64(rate),
		sampleBurst:  float64(rate),
		sampleTokens: float64(rate),
		sampleLast:   time.Now(),
		exportSem:    make(chan struct{}, maxExports),
		async:        cfg.AsyncExport,
	}
	t.resource = appendStrAttr(nil, "service.name", serviceName)
	if Version != "" {
		t.resource = appendStrAttr(t.resource, "service.version", Version)
	}
	if Commit != "" {
		t.resource = appendStrAttr(t.resource, "fullsend.mint.commit", Commit)
	}
	if cfg.Backend != "" {
		t.resource = appendStrAttr(t.resource, "fullsend.mint.backend", cfg.Backend)
	}
	if cfg.Deployment != "" {
		t.resource = appendStrAttr(t.resource, "fullsend.mint.deployment", cfg.Deployment)
	}
	return t, nil
}

// NewStampedTracer builds the tracer for a CLI-deployed serverless mint.
// The endpoint and deployment identity are stamped into the build
// artifact at deploy time; rawHeaders (OTEL_EXPORTER_OTLP_HEADERS syntax)
// comes from the platform secret store. It returns (nil, nil) when
// endpoint is empty. Export stays synchronous because serverless runtimes
// cannot run work after the response is returned.
func NewStampedTracer(backend, deployment, endpoint, rawHeaders string) (*Tracer, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, nil
	}
	headers, err := ParseOTLPHeaders(rawHeaders)
	if err != nil {
		return nil, err
	}
	return NewTracer(TracingConfig{
		Endpoint:   endpoint,
		Headers:    headers,
		Backend:    backend,
		Deployment: deployment,
	})
}

// ResolveOTLPTracesEndpoint applies the OpenTelemetry exporter precedence:
// a signal-specific OTEL_EXPORTER_OTLP_TRACES_ENDPOINT is used verbatim;
// otherwise OTEL_EXPORTER_OTLP_ENDPOINT is used as a base URL with
// "/v1/traces" appended. Returns "" when neither is set.
func ResolveOTLPTracesEndpoint(tracesEndpoint, baseEndpoint string) string {
	if v := strings.TrimSpace(tracesEndpoint); v != "" {
		return v
	}
	if v := strings.TrimSpace(baseEndpoint); v != "" {
		return strings.TrimRight(v, "/") + "/v1/traces"
	}
	return ""
}

// ParseOTLPHeaders parses the OTEL_EXPORTER_OTLP_HEADERS syntax: a
// comma-separated list of key=value pairs whose values are
// percent-encoded. Newlines are accepted as separators too so the same
// syntax works in a header file with one pair per line. Error messages
// never include header values.
func ParseOTLPHeaders(raw string) (map[string]string, error) {
	out := make(map[string]string)
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	for i, entry := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' }) {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("OTLP header entry %d is not a key=value pair", i+1)
		}
		if !validHeaderName(key) {
			return nil, fmt.Errorf("OTLP header entry %d has an invalid name", i+1)
		}
		decoded, err := url.PathUnescape(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("OTLP header %q has an invalid percent-encoded value", key)
		}
		out[key] = decoded
	}
	return out, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// traceContext is a parsed W3C trace context.
type traceContext struct {
	traceID [16]byte
	spanID  [8]byte
	flags   byte
}

// parseTraceparent parses a W3C traceparent header value. Unknown future
// versions are accepted per the W3C forward-compatibility rules.
func parseTraceparent(v string) (traceContext, bool) {
	var tc traceContext
	v = strings.TrimSpace(v)
	if len(v) < 55 || v[2] != '-' || v[35] != '-' || v[52] != '-' {
		return tc, false
	}
	version := v[0:2]
	if !isLowerHex(version) || version == "ff" {
		return tc, false
	}
	if version == "00" && len(v) != 55 {
		return tc, false
	}
	if len(v) > 55 && v[55] != '-' {
		return tc, false
	}
	traceHex, spanHex, flagsHex := v[3:35], v[36:52], v[53:55]
	if !isLowerHex(traceHex) || !isLowerHex(spanHex) || !isLowerHex(flagsHex) {
		return tc, false
	}
	if _, err := hex.Decode(tc.traceID[:], []byte(traceHex)); err != nil {
		return tc, false
	}
	if _, err := hex.Decode(tc.spanID[:], []byte(spanHex)); err != nil {
		return tc, false
	}
	var flags [1]byte
	if _, err := hex.Decode(flags[:], []byte(flagsHex)); err != nil {
		return tc, false
	}
	tc.flags = flags[0]
	if tc.traceID == ([16]byte{}) || tc.spanID == ([8]byte{}) {
		return tc, false
	}
	return tc, true
}

func isLowerHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// requestTrace holds the spans recorded for one mint request.
type requestTrace struct {
	tracer       *Tracer
	traceID      [16]byte
	flags        byte
	state        string
	mu           sync.Mutex
	spans        []*span
	failureStage string
}

func (rt *requestTrace) sampled() bool { return rt.flags&0x01 != 0 }

// span is a single recorded operation. A nil *span is a no-op so call
// sites do not need to check whether tracing is enabled.
type span struct {
	rt        *requestTrace
	spanID    [8]byte
	parentID  [8]byte
	hasParent bool
	name      string
	kind      int
	stage     string
	startTime time.Time
	endTime   time.Time
	attrs     []otlpKeyValue
	events    []otlpEvent
	status    otlpStatus
	ended     bool
}

type spanContextKey struct{}

func spanFromContext(ctx context.Context) *span {
	s, _ := ctx.Value(spanContextKey{}).(*span)
	return s
}

func newSpanID() [8]byte {
	var id [8]byte
	for id == ([8]byte{}) {
		_, _ = rand.Read(id[:])
	}
	return id
}

func newTraceID() [16]byte {
	var id [16]byte
	for id == ([16]byte{}) {
		_, _ = rand.Read(id[:])
	}
	return id
}

// knownRoutes bounds the url.path values recorded on spans so arbitrary
// caller-supplied paths never reach the collector.
var knownRoutes = map[string]bool{"/": true, "/v1/token": true, "/v1/status": true, "/v1/bot-role": true, "/health": true}

// admitSample reports whether the local sampling budget allows recording
// one more request. It is a token bucket refilled at sampleRate per second.
func (t *Tracer) admitSample() bool {
	t.sampleMu.Lock()
	defer t.sampleMu.Unlock()
	now := time.Now()
	t.sampleTokens += now.Sub(t.sampleLast).Seconds() * t.sampleRate
	if t.sampleTokens > t.sampleBurst {
		t.sampleTokens = t.sampleBurst
	}
	t.sampleLast = now
	if t.sampleTokens < 1 {
		return false
	}
	t.sampleTokens--
	return true
}

// startRequest extracts the inbound W3C trace context from r and starts
// the server span. When the inbound parent is unsampled the span keeps
// the upstream decision: it is still created (so its identity can be
// propagated back) but neither it nor its children are exported. An
// inbound sampled flag is honored only within the local sampling budget;
// beyond it the request is treated as unsampled.
func (t *Tracer) startRequest(r *http.Request) (context.Context, *span) {
	if t == nil {
		return r.Context(), nil
	}
	rt := &requestTrace{tracer: t, flags: 0x01}
	s := &span{rt: rt, kind: spanKindServer, startTime: time.Now(), spanID: newSpanID()}
	if tc, ok := parseTraceparent(r.Header.Get("traceparent")); ok {
		rt.traceID = tc.traceID
		rt.flags = tc.flags
		s.parentID = tc.spanID
		s.hasParent = true
		if state := strings.TrimSpace(strings.Join(r.Header.Values("tracestate"), ",")); len(state) <= maxTraceStateLen {
			rt.state = state
		}
	} else {
		rt.traceID = newTraceID()
	}
	if rt.sampled() && !t.admitSample() {
		rt.flags &^= 0x01
	}
	route := r.URL.Path
	if !knownRoutes[route] {
		route = "other"
	}
	method := r.Method
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodOptions:
	default:
		method = "_OTHER"
	}
	s.name = method + " " + route
	s.setStr("http.request.method", method)
	s.setStr("url.path", route)
	if t.backend != "" {
		s.setStr("fullsend.mint.backend", t.backend)
	}
	if t.deployment != "" {
		s.setStr("fullsend.mint.deployment", t.deployment)
	}
	return context.WithValue(r.Context(), spanContextKey{}, s), s
}

// startSpan starts an internal child span of the span in ctx. stage is
// the failure stage attributed to the request when the span records an
// error. It returns a nil span when ctx has no recording parent.
func startSpan(ctx context.Context, name, stage string) (context.Context, *span) {
	parent := spanFromContext(ctx)
	if parent == nil || !parent.rt.sampled() {
		return ctx, nil
	}
	s := &span{
		rt:        parent.rt,
		spanID:    newSpanID(),
		parentID:  parent.spanID,
		hasParent: true,
		name:      name,
		kind:      spanKindInternal,
		stage:     stage,
		startTime: time.Now(),
	}
	return context.WithValue(ctx, spanContextKey{}, s), s
}

// traceresponse returns the W3C Trace Context Level 2 traceresponse value
// identifying this server span, so callers can locate the mint's side of
// the trace.
func (s *span) traceresponse() string {
	if s == nil {
		return ""
	}
	return "00-" + hex.EncodeToString(s.rt.traceID[:]) + "-" + hex.EncodeToString(s.spanID[:]) + "-" + hex.EncodeToString([]byte{s.rt.flags})
}

// Wrap returns a handler that records a server span for every request
// served by next, sets the traceresponse header, and exports the spans
// when the request finishes. It lets a load site trace a handler chain
// that routes some requests outside Handler (for example to a fallback
// mint); Handler detects the existing server span and does not start a
// second one. GET /health is not traced. A nil Tracer returns next
// unchanged.
func (t *Tracer) Wrap(next http.Handler) http.Handler {
	if t == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, root := t.startRequest(r)
		w.Header().Set("traceresponse", root.traceresponse())
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(ctx))
		t.finishRequest(root, rec.statusCode())
	})
}

// TraceHeaders returns the traceparent and tracestate values that
// propagate the trace of the server span in ctx to a downstream service,
// with the server span as the parent. Both are empty when ctx carries no
// span.
func TraceHeaders(ctx context.Context) (traceparent, tracestate string) {
	s := spanFromContext(ctx)
	if s == nil {
		return "", ""
	}
	return s.traceresponse(), s.rt.state
}

// RecordSpanError records err (redacted and bounded) on the server span
// in ctx and attributes the request failure to stage. It is a no-op when
// ctx carries no span or err is nil.
func RecordSpanError(ctx context.Context, stage string, err error) {
	spanFromContext(ctx).recordErrorAt(stage, err)
}

func (s *span) setStr(key, value string) {
	if s == nil {
		return
	}
	s.attrs = appendStrAttr(s.attrs, key, boundString(value, maxTraceAttrLen))
}

func (s *span) setInt(key string, value int64) {
	if s == nil {
		return
	}
	s.attrs = appendIntAttr(s.attrs, key, value)
}

// addEvent records a named event with attrs on the span. It is a no-op on
// a nil span.
func (s *span) addEvent(name string, attrs []otlpKeyValue) {
	if s == nil {
		return
	}
	s.events = append(s.events, otlpEvent{
		TimeUnixNano: strconv.FormatInt(time.Now().UnixNano(), 10),
		Name:         name,
		Attributes:   attrs,
	})
}

func (s *span) setBool(key string, value bool) {
	if s == nil {
		return
	}
	v := value
	s.attrs = append(s.attrs, otlpKeyValue{Key: key, Value: otlpAnyValue{BoolValue: &v}})
}

// telemetryError is implemented by errors whose Error text may carry raw
// upstream response content. Spans record telemetryMessage instead, which
// must contain only fixed text and structured values such as status codes.
type telemetryError interface {
	telemetryMessage() string
}

// noRedirectKey marks requests whose redirects must not be followed.
type noRedirectKey struct{}

// withNoRedirect returns ctx marked so that mintHTTP does not follow
// redirects. OTLP export uses it: a collector redirect would otherwise
// forward custom credential headers and the payload to another origin.
func withNoRedirect(ctx context.Context) context.Context {
	return context.WithValue(ctx, noRedirectKey{}, true)
}

// recordError marks the span failed and attributes the request failure
// to the span's stage. A nil err is ignored.
func (s *span) recordError(err error) {
	if s == nil {
		return
	}
	s.recordErrorAt(s.stage, err)
}

// recordErrorAt is recordError with an explicit failure stage.
func (s *span) recordErrorAt(stage string, err error) {
	if s == nil || err == nil {
		return
	}
	text := err.Error()
	var te telemetryError
	if errors.As(err, &te) {
		text = te.telemetryMessage()
	}
	msg := safeErrorMessage(text)
	s.status = otlpStatus{Code: spanStatusError, Message: msg}
	var attrs []otlpKeyValue
	attrs = appendStrAttr(attrs, "exception.type", boundString(fmt.Sprintf("%T", err), maxTraceAttrLen))
	attrs = appendStrAttr(attrs, "exception.message", msg)
	s.events = append(s.events, otlpEvent{
		TimeUnixNano: strconv.FormatInt(time.Now().UnixNano(), 10),
		Name:         "exception",
		Attributes:   attrs,
	})
	if stage != "" {
		s.setStr("fullsend.mint.failure_stage", stage)
		s.rt.mu.Lock()
		if s.rt.failureStage == "" {
			s.rt.failureStage = stage
		}
		s.rt.mu.Unlock()
	}
}

// end finishes the span and queues it for export. Calling end more than
// once has no effect.
func (s *span) end() {
	if s == nil || s.ended {
		return
	}
	s.ended = true
	s.endTime = time.Now()
	if !s.rt.sampled() {
		return
	}
	s.rt.mu.Lock()
	s.rt.spans = append(s.rt.spans, s)
	s.rt.mu.Unlock()
}

// finishRequest records the response outcome on the server span, ends
// it, and exports the request's spans. Export failures are logged and
// never affect the mint response. With AsyncExport the export runs in
// the background and the response is not held for it; otherwise it runs
// before returning.
func (t *Tracer) finishRequest(root *span, status int) {
	if t == nil || root == nil {
		return
	}
	root.setInt("http.response.status_code", int64(status))
	root.setStr("fullsend.mint.outcome", outcomeForStatus(status))
	if status >= 400 {
		root.rt.mu.Lock()
		stage := root.rt.failureStage
		root.rt.mu.Unlock()
		if stage == "" {
			stage = stageForStatus(status)
		}
		root.setStr("fullsend.mint.failure_stage", stage)
	}
	if status >= 500 {
		root.status = otlpStatus{Code: spanStatusError, Message: http.StatusText(status)}
	}
	root.end()
	if !root.rt.sampled() {
		return
	}
	root.rt.mu.Lock()
	spans := root.rt.spans
	root.rt.spans = nil
	root.rt.mu.Unlock()
	if !t.async {
		t.export(spans)
		return
	}
	if !t.admitExport() {
		return
	}
	t.pending.Add(1)
	go func() {
		defer t.pending.Done()
		defer t.releaseExport()
		t.send(spans)
	}()
}

// Shutdown waits for pending asynchronous exports to finish or ctx to
// end, whichever comes first. Call it after the server has stopped
// accepting requests. It is a no-op on a nil Tracer or when exports are
// synchronous.
func (t *Tracer) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		t.pending.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func outcomeForStatus(status int) string {
	switch {
	case status < 400:
		return "success"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "denied"
	case status < 500:
		return "rejected"
	default:
		return "error"
	}
}

func stageForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return stageRequestValidation
	case http.StatusUnauthorized:
		return stageAuthentication
	case http.StatusForbidden:
		return stageAuthorization
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return stageRouting
	default:
		return "unknown"
	}
}

// admitExport reserves one of the concurrent export slots without
// blocking. When none is free the telemetry is dropped and false is
// returned; the caller must call releaseExport after a true result.
func (t *Tracer) admitExport() bool {
	select {
	case t.exportSem <- struct{}{}:
		return true
	default:
		log.Printf("trace export dropped: too many concurrent exports")
		return false
	}
}

func (t *Tracer) releaseExport() { <-t.exportSem }

// export admits, then synchronously sends, spans.
func (t *Tracer) export(spans []*span) {
	if len(spans) == 0 || !t.admitExport() {
		return
	}
	defer t.releaseExport()
	t.send(spans)
}

// send posts spans to the collector in a single bounded OTLP/HTTP JSON
// request. It is fail-open: every error is logged without header values
// and swallowed. The caller holds an export slot (admitExport).
func (t *Tracer) send(spans []*span) {
	if len(spans) == 0 {
		return
	}
	body, err := json.Marshal(t.buildPayload(spans))
	if err != nil {
		log.Printf("trace export skipped: encoding spans: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(withNoRedirect(ctx), http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		log.Printf("trace export skipped: creating request failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	resp, err := mintHTTP(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		log.Printf("trace export failed (mint result unaffected): %s", safeErrorMessage(err.Error()))
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		log.Printf("trace export failed (mint result unaffected): collector returned HTTP %d", resp.StatusCode)
	}
}

func (t *Tracer) buildPayload(spans []*span) otlpTracesPayload {
	out := make([]otlpSpan, 0, len(spans))
	for _, s := range spans {
		o := otlpSpan{
			TraceID:           hex.EncodeToString(s.rt.traceID[:]),
			SpanID:            hex.EncodeToString(s.spanID[:]),
			TraceState:        s.rt.state,
			Name:              s.name,
			Kind:              s.kind,
			StartTimeUnixNano: strconv.FormatInt(s.startTime.UnixNano(), 10),
			EndTimeUnixNano:   strconv.FormatInt(s.endTime.UnixNano(), 10),
			Attributes:        s.attrs,
			Events:            s.events,
			Status:            s.status,
		}
		if s.hasParent {
			o.ParentSpanID = hex.EncodeToString(s.parentID[:])
		}
		out = append(out, o)
	}
	return otlpTracesPayload{ResourceSpans: []otlpResourceSpans{{
		Resource:   otlpResource{Attributes: t.resource},
		ScopeSpans: []otlpScopeSpans{{Scope: otlpScope{Name: traceScopeName, Version: Version}, Spans: out}},
	}}}
}

// Redaction patterns for span error text. Mint errors are not expected to
// contain credentials, but upstream error strings are not fully under our
// control, so anything shaped like a token or key is masked before it
// leaves the process.
var (
	reTraceJWT    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*\.[A-Za-z0-9_-]*`)
	reTraceGitHub = regexp.MustCompile(`(gh[pousr]_[A-Za-z0-9]+|github_pat_[A-Za-z0-9_]+)`)
	// An Authorization header value (scheme and credential, in plain or
	// quoted form) is masked through the end of the line so the scheme
	// word is never masked in place of the credential.
	reTraceAuthHeader = regexp.MustCompile(`(?i)\b(authorization["']?)(\s*[:=]\s*)["']?[^\r\n]*`)
	// A bare credential scheme or token keyword followed by its value.
	reTraceBearer = regexp.MustCompile(`(?i)\b(bearer|basic|digest|negotiate|token)(\s*[:=]\s*|\s+)["']?[^\s,;"']+`)
	// Credentials embedded in URLs: userinfo and sensitive query values.
	reTraceURLUserinfo = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.-]*://)[^\s/?#@]+@`)
	reTraceURLQuery    = regexp.MustCompile(`(?i)([?&;](?:access_token|id_token|refresh_token|token|api[_-]?key|key|password|passwd|secret|client_secret|signature|sig|auth|code)=)[^&#\s"']*`)
)

// safeErrorMessage bounds and redacts error text recorded on spans.
func safeErrorMessage(msg string) string {
	if i := strings.Index(msg, "-----BEGIN"); i >= 0 {
		msg = msg[:i] + "[REDACTED PEM]"
	}
	msg = reTraceJWT.ReplaceAllString(msg, "[REDACTED JWT]")
	msg = reTraceGitHub.ReplaceAllString(msg, "[REDACTED TOKEN]")
	msg = reTraceAuthHeader.ReplaceAllString(msg, "$1$2[REDACTED]")
	msg = reTraceBearer.ReplaceAllString(msg, "$1$2[REDACTED]")
	msg = reTraceURLUserinfo.ReplaceAllString(msg, "${1}[REDACTED]@")
	msg = reTraceURLQuery.ReplaceAllString(msg, "${1}[REDACTED]")
	return boundString(msg, maxSpanErrorLen)
}

// boundString truncates s to at most n bytes on a UTF-8 boundary and
// replaces invalid UTF-8 so the OTLP JSON stays well-formed.
func boundString(s string, n int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func appendStrAttr(attrs []otlpKeyValue, key, value string) []otlpKeyValue {
	v := value
	return append(attrs, otlpKeyValue{Key: key, Value: otlpAnyValue{StringValue: &v}})
}

func appendIntAttr(attrs []otlpKeyValue, key string, value int64) []otlpKeyValue {
	v := strconv.FormatInt(value, 10)
	return append(attrs, otlpKeyValue{Key: key, Value: otlpAnyValue{IntValue: &v}})
}

// statusRecorder captures the response status for the server span.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(b)
}

func (r *statusRecorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// OTLP/HTTP JSON encoding (opentelemetry-proto JSON mapping: trace and
// span IDs are hex strings, 64-bit integers are decimal strings).
type otlpTracesPayload struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	TraceState        string         `json:"traceState,omitempty"`
	ParentSpanID      string         `json:"parentSpanId,omitempty"`
	Name              string         `json:"name"`
	Kind              int            `json:"kind"`
	StartTimeUnixNano string         `json:"startTimeUnixNano"`
	EndTimeUnixNano   string         `json:"endTimeUnixNano"`
	Attributes        []otlpKeyValue `json:"attributes,omitempty"`
	Events            []otlpEvent    `json:"events,omitempty"`
	Status            otlpStatus     `json:"status"`
}

type otlpEvent struct {
	TimeUnixNano string         `json:"timeUnixNano"`
	Name         string         `json:"name"`
	Attributes   []otlpKeyValue `json:"attributes,omitempty"`
}

type otlpStatus struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

type otlpAnyValue struct {
	StringValue *string `json:"stringValue,omitempty"`
	IntValue    *string `json:"intValue,omitempty"`
	BoolValue   *bool   `json:"boolValue,omitempty"`
}
