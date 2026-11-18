package mintcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testParentTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
	testParentSpanID  = "00f067aa0ba902b7"
	testCollectorAuth = "collector-secret-value"
)

// testCollector is an OTLP/HTTP JSON sink that records export requests.
type testCollector struct {
	server   *httptest.Server
	mu       sync.Mutex
	payloads []otlpTracesPayload
	headers  []http.Header
	raw      []string
}

func newTestCollector(t *testing.T, status int, delay time.Duration) *testCollector {
	t.Helper()
	c := &testCollector{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		body, _ := io.ReadAll(r.Body)
		var p otlpTracesPayload
		if err := json.Unmarshal(body, &p); err != nil {
			t.Errorf("collector: invalid OTLP JSON: %v", err)
		}
		c.mu.Lock()
		c.payloads = append(c.payloads, p)
		c.headers = append(c.headers, r.Header.Clone())
		c.raw = append(c.raw, string(body))
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *testCollector) spans() []otlpSpan {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []otlpSpan
	for _, p := range c.payloads {
		for _, rs := range p.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				out = append(out, ss.Spans...)
			}
		}
	}
	return out
}

func (c *testCollector) requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.payloads)
}

func spanByName(spans []otlpSpan, name string) *otlpSpan {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}

func attr(s *otlpSpan, key string) (string, bool) {
	for _, kv := range s.Attributes {
		if kv.Key != key {
			continue
		}
		switch {
		case kv.Value.StringValue != nil:
			return *kv.Value.StringValue, true
		case kv.Value.IntValue != nil:
			return *kv.Value.IntValue, true
		case kv.Value.BoolValue != nil:
			if *kv.Value.BoolValue {
				return "true", true
			}
			return "false", true
		}
	}
	return "", false
}

func mustNewTracer(t *testing.T, endpoint string, timeout time.Duration) *Tracer {
	t.Helper()
	tr, err := NewTracer(TracingConfig{
		Endpoint:      endpoint,
		Headers:       map[string]string{"Authorization": "Bearer " + testCollectorAuth},
		Backend:       TraceBackendStandalone,
		Deployment:    "unit-test",
		ExportTimeout: timeout,
	})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	return tr
}

// newTracedFullFlowHandler returns a handler whose token path succeeds
// against a fake GitHub API.
func newTracedFullFlowHandler(t *testing.T, tracer *Tracer) *Handler {
	t.Helper()
	t.Setenv("ROLE_APP_IDS", `{"coder":"200"}`)
	pemData, err := generateTestRSAKey()
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	verifier := &fakeOIDCVerifier{claims: &Claims{
		Repository:      "test-org/test-repo",
		RepositoryOwner: "test-org",
		JobWorkflowRef:  "fullsend-ai/fullsend/.github/workflows/code.yml@refs/heads/main",
	}}
	h := mustNewHandler(t, &fakePEMAccessor{pems: map[string][]byte{"coder": pemData}}, verifier)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens"):
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(installationTokenResponse{
				Token:               "ghs_secret_installation_token",
				ExpiresAt:           "2026-05-06T12:00:00Z",
				Permissions:         map[string]string{"contents": "write", "pull_requests": "write", "issues": "write", "checks": "read", "metadata": "read"},
				Repositories:        []installationTokenRepository{{FullName: "test-org/test-repo"}},
				RepositorySelection: "selected",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(github.Close)
	h.githubBaseURL = github.URL
	h.SetTracer(tracer)
	return h
}

// testCallerJWT returns a JWT-shaped bearer value built at runtime so the
// test can assert it never reaches exported spans.
func testCallerJWT() string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"RS256"}`)) + "." + enc([]byte(`{"sub":"x"}`)) + "." + enc([]byte("signature"))
}

func serveToken(h *Handler, traceparent, tracestate string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+testCallerJWT())
	if traceparent != "" {
		req.Header.Set("traceparent", traceparent)
	}
	if tracestate != "" {
		req.Header.Set("tracestate", tracestate)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestTracing_SampledParentJoinsTraceAndExports(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	h := newTracedFullFlowHandler(t, mustNewTracer(t, collector.server.URL+"/v1/traces", 0))

	rec := serveToken(h, "00-"+testParentTraceID+"-"+testParentSpanID+"-01", "vendor=abc,other=xyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if tr := rec.Header().Get("traceresponse"); !strings.HasPrefix(tr, "00-"+testParentTraceID+"-") || !strings.HasSuffix(tr, "-01") {
		t.Fatalf("unexpected traceresponse %q", tr)
	}

	if got := collector.requests(); got != 1 {
		t.Fatalf("expected 1 export request, got %d", got)
	}
	if got := collector.headers[0].Get("Authorization"); got != "Bearer "+testCollectorAuth {
		t.Fatalf("collector auth header not sent")
	}
	if ct := collector.headers[0].Get("Content-Type"); ct != "application/json" {
		t.Fatalf("unexpected content type %q", ct)
	}

	spans := collector.spans()
	root := spanByName(spans, "POST /v1/token")
	if root == nil {
		t.Fatalf("server span missing; got %d spans", len(spans))
	}
	if root.TraceID != testParentTraceID || root.ParentSpanID != testParentSpanID {
		t.Fatalf("server span not joined to caller trace: trace=%s parent=%s", root.TraceID, root.ParentSpanID)
	}
	if root.TraceState != "vendor=abc,other=xyz" {
		t.Fatalf("tracestate not preserved: %q", root.TraceState)
	}
	if root.Kind != spanKindServer {
		t.Fatalf("expected server span kind, got %d", root.Kind)
	}
	if !strings.Contains(rec.Header().Get("traceresponse"), root.SpanID) {
		t.Fatalf("traceresponse does not identify the server span")
	}
	for key, want := range map[string]string{
		"http.response.status_code":     "200",
		"fullsend.mint.outcome":         "success",
		"fullsend.mint.backend":         TraceBackendStandalone,
		"fullsend.mint.deployment":      "unit-test",
		"fullsend.mint.role":            "coder",
		"fullsend.mint.caller_org":      "test-org",
		"fullsend.mint.installation_id": "12345",
		"fullsend.mint.cross_org":       "false",
	} {
		if got, _ := attr(root, key); got != want {
			t.Errorf("root attribute %s = %q, want %q", key, got, want)
		}
	}
	if _, ok := attr(root, "fullsend.mint.failure_stage"); ok {
		t.Errorf("successful request must not record a failure stage")
	}

	for _, name := range []string{"mint.oidc_verify", "mint.authorize", "mint.pem_access", "mint.github_app_auth", "mint.installation_lookup", "mint.create_installation_token"} {
		s := spanByName(spans, name)
		if s == nil {
			t.Errorf("stage span %s missing", name)
			continue
		}
		if s.TraceID != testParentTraceID || s.ParentSpanID != root.SpanID {
			t.Errorf("stage span %s not a child of the server span", name)
		}
		if s.Status.Code == spanStatusError {
			t.Errorf("stage span %s unexpectedly failed: %s", name, s.Status.Message)
		}
	}

	raw := collector.raw[0]
	for _, secret := range []string{"ghs_secret_installation_token", testCallerJWT(), testCollectorAuth, "PRIVATE KEY"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("exported spans contain sensitive value %q", secret)
		}
	}
	if !strings.Contains(raw, `"service.name"`) || !strings.Contains(raw, `"fullsend-mint"`) {
		t.Fatalf("resource service.name missing from export")
	}
}

func TestTracing_UnsampledParentIsPreservedAndNotExported(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	h := newTracedFullFlowHandler(t, mustNewTracer(t, collector.server.URL+"/v1/traces", 0))

	rec := serveToken(h, "00-"+testParentTraceID+"-"+testParentSpanID+"-00", "vendor=abc")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	tr := rec.Header().Get("traceresponse")
	tc, ok := parseTraceparent(tr)
	if !ok {
		t.Fatalf("invalid traceresponse %q", tr)
	}
	if got := tr[3:35]; got != testParentTraceID {
		t.Fatalf("unsampled request left the caller trace: %s", got)
	}
	if tc.flags&0x01 != 0 {
		t.Fatalf("upstream unsampled decision was overridden: %q", tr)
	}
	if tr[36:52] == testParentSpanID {
		t.Fatalf("server span must have its own span ID")
	}
	if got := collector.requests(); got != 0 {
		t.Fatalf("unsampled trace must not be exported, got %d requests", got)
	}
}

func TestTracing_WrapTracesWholeChainWithSingleServerSpan(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	tracer := mustNewTracer(t, collector.server.URL+"/v1/traces", 0)
	// The inner Handler also has a tracer, as at load sites that wrap the
	// whole chain; it must reuse the outer server span.
	h := newTracedFullFlowHandler(t, tracer)

	var gotParent, gotState string
	chain := tracer.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fallback" {
			gotParent, gotState = TraceHeaders(r.Context())
			RecordSpanError(r.Context(), "fallback_proxy", errors.New("upstream failed Authorization: Bearer opaqueWRAP"))
			writeError(w, http.StatusBadGateway, "fallback mint unreachable")
			return
		}
		h.ServeHTTP(w, r)
	}))

	// Request routed through the inner handler: one server span.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	req.Header.Set("Authorization", "Bearer "+testCallerJWT())
	req.Header.Set("traceparent", "00-"+testParentTraceID+"-"+testParentSpanID+"-01")
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	servers := 0
	for _, s := range collector.spans() {
		if s.Kind == spanKindServer {
			servers++
		}
	}
	if servers != 1 || collector.requests() != 1 {
		t.Fatalf("expected one server span in one export, got %d spans in %d exports", servers, collector.requests())
	}

	// Request that never reaches the inner handler (fallback routing).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/fallback", nil)
	req.Header.Set("traceparent", "00-"+testParentTraceID+"-"+testParentSpanID+"-01")
	req.Header.Set("tracestate", "vendor=abc")
	chain.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", rec.Code)
	}
	if collector.requests() != 2 {
		t.Fatalf("expected the fallback request to be exported, got %d exports", collector.requests())
	}
	tr := rec.Header().Get("traceresponse")
	if tr == "" || gotParent != tr {
		t.Fatalf("TraceHeaders = %q, want traceresponse %q", gotParent, tr)
	}
	if gotState != "vendor=abc" {
		t.Fatalf("tracestate not propagated: %q", gotState)
	}
	var fb *otlpSpan
	for _, s := range collector.spans() {
		if s.Kind == spanKindServer && s.Status.Code == spanStatusError {
			s := s
			fb = &s
		}
	}
	if fb == nil {
		t.Fatalf("fallback server span with error status missing")
	}
	if got, _ := attr(fb, "fullsend.mint.failure_stage"); got != "fallback_proxy" {
		t.Errorf("failure stage = %q, want fallback_proxy", got)
	}
	if strings.Contains(collector.raw[1], "opaqueWRAP") {
		t.Errorf("exported fallback span leaked a credential")
	}

	// /health is not traced.
	before := collector.requests()
	rec = httptest.NewRecorder()
	chain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if collector.requests() != before || rec.Header().Get("traceresponse") != "" {
		t.Fatalf("/health must not be traced")
	}

	// A nil tracer returns the handler unchanged.
	var nilTracer *Tracer
	if nilTracer.Wrap(h) != http.Handler(h) {
		t.Fatalf("nil tracer must return the handler unchanged")
	}
	if tp, ts := TraceHeaders(context.Background()); tp != "" || ts != "" {
		t.Fatalf("TraceHeaders without a span = %q, %q", tp, ts)
	}
	RecordSpanError(context.Background(), "x", errors.New("boom"))
}

func TestTracing_NoParentStartsNewSampledTrace(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	h := newTracedFullFlowHandler(t, mustNewTracer(t, collector.server.URL+"/v1/traces", 0))

	rec := serveToken(h, "not-a-traceparent", "vendor=ignored")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	root := spanByName(collector.spans(), "POST /v1/token")
	if root == nil {
		t.Fatal("server span missing")
	}
	if root.ParentSpanID != "" || root.TraceID == testParentTraceID || root.TraceState != "" {
		t.Fatalf("invalid inbound context must start a fresh root trace: %+v", root)
	}
}

func TestTracing_DisabledMakesNoExportAttempt(t *testing.T) {
	tr, err := NewTracer(TracingConfig{Endpoint: "  "})
	if err != nil || tr != nil {
		t.Fatalf("empty endpoint should disable tracing, got tracer=%v err=%v", tr, err)
	}
	h := newTracedFullFlowHandler(t, nil)
	var calls int
	var mu sync.Mutex
	githubURL := h.githubBaseURL
	SetMintHTTPForTest(t, func(req *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(req.URL.String(), githubURL) {
			mu.Lock()
			calls++
			mu.Unlock()
		}
		return http.DefaultClient.Do(req)
	})
	rec := serveToken(h, "00-"+testParentTraceID+"-"+testParentSpanID+"-01", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Header().Get("traceresponse") != "" {
		t.Fatalf("disabled tracing must not emit traceresponse")
	}
	if calls != 0 {
		t.Fatalf("disabled tracing made %d non-GitHub requests", calls)
	}
}

func TestTracing_CollectorFailuresDoNotAffectMint(t *testing.T) {
	failing := newTestCollector(t, http.StatusInternalServerError, 0)
	slow := newTestCollector(t, http.StatusOK, 2*time.Second)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	for name, endpoint := range map[string]string{
		"collector 500":  failing.server.URL + "/v1/traces",
		"collector slow": slow.server.URL + "/v1/traces",
		"collector down": closedURL + "/v1/traces",
	} {
		t.Run(name, func(t *testing.T) {
			h := newTracedFullFlowHandler(t, mustNewTracer(t, endpoint, 100*time.Millisecond))
			start := time.Now()
			rec := serveToken(h, "", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var resp mintResponse
			if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil || resp.Token != "ghs_secret_installation_token" {
				t.Fatalf("mint result changed: token=%q err=%v", resp.Token, err)
			}
			if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
				t.Fatalf("collector failure not bounded by export timeout: %v", elapsed)
			}
		})
	}
}

func TestTracing_FailureStages(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	tracer := mustNewTracer(t, collector.server.URL, 0)

	t.Run("oidc verification", func(t *testing.T) {
		h := newTracedFullFlowHandler(t, tracer)
		h.oidcVerifier = &fakeOIDCVerifier{err: errors.New("bad signature for token eyJabc.eyJdef.sig")}
		before := len(collector.spans())
		rec := serveToken(h, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		spans := collector.spans()[before:]
		root := spanByName(spans, "POST /v1/token")
		if got, _ := attr(root, "fullsend.mint.failure_stage"); got != stageOIDCVerification {
			t.Fatalf("failure stage = %q", got)
		}
		if got, _ := attr(root, "fullsend.mint.outcome"); got != "denied" {
			t.Fatalf("outcome = %q", got)
		}
		oidc := spanByName(spans, "mint.oidc_verify")
		if oidc == nil || oidc.Status.Code != spanStatusError || len(oidc.Events) != 1 {
			t.Fatalf("oidc span did not record the error: %+v", oidc)
		}
		if strings.Contains(oidc.Status.Message, "eyJabc") {
			t.Fatalf("JWT not redacted from span error: %q", oidc.Status.Message)
		}
	})

	t.Run("authorization policy", func(t *testing.T) {
		h := newTracedFullFlowHandler(t, tracer)
		h.oidcVerifier = &fakeOIDCVerifier{claims: &Claims{
			Repository:      "test-org/test-repo",
			RepositoryOwner: "test-org",
			JobWorkflowRef:  "evil/repo/.github/workflows/x.yml@refs/heads/main",
		}}
		before := len(collector.spans())
		rec := serveToken(h, "", "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d", rec.Code)
		}
		root := spanByName(collector.spans()[before:], "POST /v1/token")
		if got, _ := attr(root, "fullsend.mint.failure_stage"); got != stageAuthorization {
			t.Fatalf("failure stage = %q", got)
		}
	})

	t.Run("pem access", func(t *testing.T) {
		h := newTracedFullFlowHandler(t, tracer)
		h.pemAccessor = &fakePEMAccessor{err: errors.New("secret manager unavailable")}
		before := len(collector.spans())
		rec := serveToken(h, "", "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403, got %d", rec.Code)
		}
		root := spanByName(collector.spans()[before:], "POST /v1/token")
		if got, _ := attr(root, "fullsend.mint.failure_stage"); got != stagePEMAccess {
			t.Fatalf("failure stage = %q", got)
		}
	})

	t.Run("request validation", func(t *testing.T) {
		h := newTracedFullFlowHandler(t, tracer)
		before := len(collector.spans())
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/token", strings.NewReader(`{`))
		req.Header.Set("Authorization", "Bearer x")
		h.ServeHTTP(rec, req)
		root := spanByName(collector.spans()[before:], "POST /v1/token")
		if root == nil {
			t.Fatal("server span missing")
		}
		if got, _ := attr(root, "fullsend.mint.failure_stage"); got != stageRequestValidation {
			t.Fatalf("failure stage = %q", got)
		}
	})

	t.Run("upstream 5xx", func(t *testing.T) {
		h := newTracedFullFlowHandler(t, tracer)
		gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		t.Cleanup(gh.Close)
		h.githubBaseURL = gh.URL
		before := len(collector.spans())
		rec := serveToken(h, "", "")
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("expected 502, got %d", rec.Code)
		}
		root := spanByName(collector.spans()[before:], "POST /v1/token")
		if got, _ := attr(root, "fullsend.mint.failure_stage"); got != stageInstallationLookup {
			t.Fatalf("failure stage = %q", got)
		}
		if root.Status.Code != spanStatusError {
			t.Fatalf("5xx server span must have error status")
		}
	})
}

func TestTracing_StatusAndUnknownRoutes(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	h := newTracedFullFlowHandler(t, mustNewTracer(t, collector.server.URL, 0))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("BREW", "/some/attacker/controlled/path", nil))

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Header().Get("traceresponse") != "" {
		t.Fatalf("health checks must not be traced")
	}

	spans := collector.spans()
	if s := spanByName(spans, "mint.status_auth"); s == nil || s.Status.Code != spanStatusError {
		t.Fatalf("status auth span missing or not failed")
	}
	other := spanByName(spans, "_OTHER other")
	if other == nil {
		t.Fatalf("unknown route span missing")
	}
	if got, _ := attr(other, "fullsend.mint.failure_stage"); got != stageRouting {
		t.Fatalf("failure stage = %q", got)
	}
	for _, s := range spans {
		if strings.Contains(s.Name, "attacker") {
			t.Fatalf("caller-controlled path recorded in span name")
		}
	}
	if spanByName(spans, "GET /health") != nil {
		t.Fatalf("health check exported a span")
	}
}

func TestNewTracer_Validation(t *testing.T) {
	cases := map[string]TracingConfig{
		"relative URL":     {Endpoint: "/v1/traces"},
		"bad scheme":       {Endpoint: "ftp://collector/v1/traces"},
		"userinfo":         {Endpoint: "https://user:pass@collector/v1/traces"},
		"bad header name":  {Endpoint: "https://collector/v1/traces", Headers: map[string]string{"bad name": "v"}},
		"header injection": {Endpoint: "https://collector/v1/traces", Headers: map[string]string{"X-Auth": "v\r\nX-Evil: 1"}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTracer(cfg); err == nil {
				t.Fatal("expected error")
			} else if strings.Contains(err.Error(), "pass") || strings.Contains(err.Error(), "X-Evil") {
				t.Fatalf("error leaks secret material: %v", err)
			}
		})
	}
	tr, err := NewTracer(TracingConfig{Endpoint: "https://collector/v1/traces"})
	if err != nil || tr == nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if tr.timeout != defaultTraceExportTimeout {
		t.Fatalf("default timeout not applied")
	}
}

func TestNewStampedTracer(t *testing.T) {
	t.Run("empty endpoint disables tracing", func(t *testing.T) {
		tr, err := NewStampedTracer(TraceBackendCloudflare, "w", " ", "Authorization=x")
		if err != nil || tr != nil {
			t.Fatalf("expected nil tracer and no error, got %v, %v", tr, err)
		}
	})
	t.Run("invalid headers are rejected without leaking values", func(t *testing.T) {
		_, err := NewStampedTracer(TraceBackendCloudflare, "w", "https://collector/v1/traces", "no-equals-"+testCollectorAuth)
		if err == nil {
			t.Fatal("expected error")
		}
		if strings.Contains(err.Error(), testCollectorAuth) {
			t.Fatalf("error leaks header material: %v", err)
		}
	})
	t.Run("invalid endpoint is rejected", func(t *testing.T) {
		if _, err := NewStampedTracer(TraceBackendCloudflare, "w", "ftp://collector", ""); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("exports synchronously with secret headers", func(t *testing.T) {
		collector := newTestCollector(t, http.StatusOK, 0)
		tr, err := NewStampedTracer(TraceBackendCloudflare, "bt-mint@bt-1234",
			collector.server.URL+"/v1/traces", "Authorization=Bearer%20"+testCollectorAuth)
		if err != nil || tr == nil {
			t.Fatalf("NewStampedTracer: %v", err)
		}
		if tr.async {
			t.Fatal("stamped tracer must export synchronously")
		}
		h := newTracedFullFlowHandler(t, tr)
		if rec := serveToken(h, "", ""); rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		// Synchronous export: the spans have arrived when ServeHTTP returns.
		if got := collector.requests(); got != 1 {
			t.Fatalf("expected 1 export request, got %d", got)
		}
		if got := collector.headers[0].Get("Authorization"); got != "Bearer "+testCollectorAuth {
			t.Fatal("collector auth header not sent")
		}
		raw := collector.raw[0]
		for _, want := range []string{`"` + TraceBackendCloudflare + `"`, `"bt-mint@bt-1234"`} {
			if !strings.Contains(raw, want) {
				t.Errorf("export missing resource attribute value %s", want)
			}
		}
		if strings.Contains(raw, testCollectorAuth) {
			t.Fatal("exported spans contain the collector header value")
		}
	})
}

func TestResolveOTLPTracesEndpoint(t *testing.T) {
	cases := []struct{ traces, base, want string }{
		{"https://a/custom", "https://b", "https://a/custom"},
		{"", "https://b/", "https://b/v1/traces"},
		{"", "https://b", "https://b/v1/traces"},
		{" ", " ", ""},
	}
	for _, c := range cases {
		if got := ResolveOTLPTracesEndpoint(c.traces, c.base); got != c.want {
			t.Errorf("ResolveOTLPTracesEndpoint(%q, %q) = %q, want %q", c.traces, c.base, got, c.want)
		}
	}
}

func TestParseOTLPHeaders(t *testing.T) {
	got, err := ParseOTLPHeaders("Authorization=Bearer%20abc, x-team = blue\n# comment\r\nX-Other=a=b\n\n")
	if err != nil {
		t.Fatalf("ParseOTLPHeaders: %v", err)
	}
	want := map[string]string{"Authorization": "Bearer abc", "x-team": "blue", "X-Other": "a=b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("header %s = %q, want %q", k, got[k], v)
		}
	}

	for name, raw := range map[string]string{
		"missing equals": "Authorization",
		"empty key":      "=secretvalue",
		"bad name":       "bad name=secretvalue",
		"bad escape":     "X-Auth=secret%zz",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseOTLPHeaders(raw)
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaks header value: %v", err)
			}
		})
	}
}

func TestParseTraceparent(t *testing.T) {
	valid := "00-" + testParentTraceID + "-" + testParentSpanID + "-01"
	if tc, ok := parseTraceparent(valid); !ok || tc.flags != 0x01 {
		t.Fatalf("valid traceparent rejected")
	}
	if _, ok := parseTraceparent("01-" + testParentTraceID + "-" + testParentSpanID + "-01-future"); !ok {
		t.Fatalf("future version with extra fields should be accepted")
	}
	for _, bad := range []string{
		"",
		"ff-" + testParentTraceID + "-" + testParentSpanID + "-01",
		"00-" + testParentTraceID + "-" + testParentSpanID + "-01-extra",
		"01-" + testParentTraceID + "-" + testParentSpanID + "-01x",
		"00-" + strings.ToUpper(testParentTraceID) + "-" + testParentSpanID + "-01",
		"00-00000000000000000000000000000000-" + testParentSpanID + "-01",
		"00-" + testParentTraceID + "-0000000000000000-01",
		"00-" + testParentTraceID + "-" + testParentSpanID + "-zz",
		"0x-" + testParentTraceID + "-" + testParentSpanID + "-01",
	} {
		if _, ok := parseTraceparent(bad); ok {
			t.Errorf("invalid traceparent accepted: %q", bad)
		}
	}
}

func TestSafeErrorMessage(t *testing.T) {
	// Built at runtime so private-key detectors do not flag this file.
	pemHeader := "-----BEGIN " + "RSA " + "PRIVATE KEY-----"
	in := "upstream said Bearer abc.def token: ghs_AAAA github_pat_11BBB_ccc jwt eyJa.eyJb.c " + pemHeader + "\nMIIE"
	got := safeErrorMessage(in)
	for _, leaked := range []string{"abc.def", "ghs_AAAA", "github_pat_11BBB", "eyJa.eyJb", "MIIE", "PRIVATE KEY"} {
		if strings.Contains(got, leaked) {
			t.Errorf("safeErrorMessage leaked %q: %q", leaked, got)
		}
	}
	for _, tc := range []struct{ in, secret string }{
		{"failed token=opaque123", "opaque123"},
		{"failed authorization:opaque456", "opaque456"},
		{"failed token = opaque789", "opaque789"},
		{"failed authorization: opaque012", "opaque012"},
		{"failed bearer opaque345", "opaque345"},
		{"upstream Authorization: Bearer opaqueAAA rejected", "opaqueAAA"},
		{"upstream Authorization: Basic opaqueBBB rejected", "opaqueBBB"},
		{"upstream authorization: bearer opaqueCCC", "opaqueCCC"},
		{`headers {"Authorization": "Bearer opaqueDDD"}`, "opaqueDDD"},
		{`headers Authorization: "Basic opaqueEEE"`, "opaqueEEE"},
		{"dial https://svc-user:opaqueFFF@example.com/v1 failed", "opaqueFFF"},
		{"GET https://example.com/cb?state=ok&access_token=opaqueGGG&x=1 failed", "opaqueGGG"},
		{"GET https://example.com/cb?api_key=opaqueHHH failed", "opaqueHHH"},
	} {
		if got := safeErrorMessage(tc.in); strings.Contains(got, tc.secret) || !strings.Contains(got, "[REDACTED]") {
			t.Errorf("safeErrorMessage(%q) = %q; want credential redacted", tc.in, got)
		}
	}
	long := strings.Repeat("é", 400)
	if got := safeErrorMessage(long); len(got) > maxSpanErrorLen+len("…") || !strings.HasSuffix(got, "…") {
		t.Fatalf("long message not bounded: len=%d", len(got))
	}
	if got := boundString("ok\xff", 10); got != "ok�" {
		t.Fatalf("invalid UTF-8 not repaired: %q", got)
	}
}

func TestNilTracerAndSpanAreNoOps(t *testing.T) {
	var tr *Tracer
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	ctx, s := tr.startRequest(req)
	if s != nil || ctx != req.Context() {
		t.Fatal("nil tracer must not start spans")
	}
	tr.finishRequest(nil, 200)
	ctx, child := startSpan(context.Background(), "x", "y")
	if child != nil || ctx == nil {
		t.Fatal("startSpan without parent must be a no-op")
	}
	child.setStr("a", "b")
	child.setInt("a", 1)
	child.setBool("a", true)
	child.recordError(errors.New("boom"))
	child.end()
	if child.traceresponse() != "" {
		t.Fatal("nil span traceresponse must be empty")
	}
	tr.export(nil)
}

// newAsyncTracer is mustNewTracer with AsyncExport set.
func newAsyncTracer(t *testing.T, endpoint string, timeout time.Duration) *Tracer {
	t.Helper()
	tr, err := NewTracer(TracingConfig{
		Endpoint:      endpoint,
		Backend:       TraceBackendStandalone,
		ExportTimeout: timeout,
		AsyncExport:   true,
	})
	if err != nil {
		t.Fatalf("NewTracer: %v", err)
	}
	return tr
}

// postToken sends a mint request over the wire and returns the status,
// body and elapsed time.
func postToken(t *testing.T, baseURL string) (int, string, time.Duration) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/token", strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testCallerJWT())
	start := time.Now()
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("mint request failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading mint response: %v", err)
	}
	return resp.StatusCode, string(body), time.Since(start)
}

// A slow collector must not hold the buffered mint response past the
// server's WriteTimeout when export is asynchronous (the standalone
// backend). Synchronous export on the same server loses the response.
func TestTracing_AsyncExportDoesNotDelayWireResponse(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 1500*time.Millisecond)
	endpoint := collector.server.URL + "/v1/traces"

	serve := func(tracer *Tracer) *httptest.Server {
		h := newTracedFullFlowHandler(t, tracer)
		srv := httptest.NewUnstartedServer(tracer.Wrap(h))
		srv.Config.WriteTimeout = 500 * time.Millisecond
		srv.Start()
		t.Cleanup(srv.Close)
		return srv
	}

	t.Run("async", func(t *testing.T) {
		tracer := newAsyncTracer(t, endpoint, 5*time.Second)
		srv := serve(tracer)
		status, body, elapsed := postToken(t, srv.URL)
		var resp mintResponse
		if status != http.StatusOK || json.Unmarshal([]byte(body), &resp) != nil || resp.Token == "" {
			t.Fatalf("expected complete successful mint response, got status %d", status)
		}
		if elapsed > 700*time.Millisecond {
			t.Fatalf("response delayed by export: %v", elapsed)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracer.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown did not drain: %v", err)
		}
		if collector.requests() == 0 {
			t.Fatal("pending export was not drained to the collector")
		}
	})

	t.Run("sync loses the response", func(t *testing.T) {
		tracer := mustNewTracer(t, endpoint, 5*time.Second)
		srv := serve(tracer)
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/token", strings.NewReader(`{"role":"coder","repos":["test-repo"]}`))
		req.Header.Set("Authorization", "Bearer "+testCallerJWT())
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err == nil {
			resp.Body.Close()
			t.Fatal("expected synchronous export to cross the WriteTimeout and drop the response")
		}
	})
}

func TestTracer_ShutdownBoundedByContext(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 2*time.Second)
	tracer := newAsyncTracer(t, collector.server.URL+"/v1/traces", 5*time.Second)
	h := newTracedFullFlowHandler(t, tracer)
	if rec := serveToken(h, "", ""); rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := tracer.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded while export pending, got %v", err)
	}
	var nilTracer *Tracer
	if err := nilTracer.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil tracer Shutdown: %v", err)
	}
}

func TestStatusRecorder_DefaultsToOK(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	if rec.statusCode() != http.StatusOK {
		t.Fatal("unwritten response should report 200")
	}
	_, _ = rec.Write([]byte("x"))
	rec.WriteHeader(http.StatusTeapot)
	if rec.statusCode() != http.StatusOK {
		t.Fatal("implicit 200 from Write must win over a later WriteHeader")
	}
}
