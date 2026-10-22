package mintcore

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const sensitiveUpstreamBody = "internal-customer-detail-4711"

func sampledTraceparent() string {
	return "00-" + testParentTraceID + "-" + testParentSpanID + "-01"
}

func TestTracing_LocalSamplingBudgetOverridesInboundSampledFlag(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	tracer, err := NewTracer(TracingConfig{
		Endpoint:            collector.server.URL + "/v1/traces",
		Backend:             TraceBackendStandalone,
		MaxSampledPerSecond: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newTracedFullFlowHandler(t, tracer)

	var unsampled int
	for i := 0; i < 8; i++ {
		rec := serveToken(h, sampledTraceparent(), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, rec.Code)
		}
		if strings.HasSuffix(rec.Header().Get("traceresponse"), "-00") {
			unsampled++
		}
	}
	if got := collector.requests(); got < 2 || got > 3 {
		t.Fatalf("expected the 2-request budget to bound exports, got %d export requests", got)
	}
	if unsampled < 5 {
		t.Fatalf("requests beyond the budget must be treated as unsampled, got %d", unsampled)
	}
}

func TestTracing_ExportDroppedWhenConcurrencyCapReached(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	tracer, err := NewTracer(TracingConfig{
		Endpoint:             collector.server.URL + "/v1/traces",
		MaxConcurrentExports: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	tracer.exportSem <- struct{}{} // an export is already in flight
	h := newTracedFullFlowHandler(t, tracer)

	rec := serveToken(h, sampledTraceparent(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := collector.requests(); got != 0 {
		t.Fatalf("export must be dropped when the cap is reached, got %d requests", got)
	}

	<-tracer.exportSem
	serveToken(h, sampledTraceparent(), "")
	if got := collector.requests(); got != 1 {
		t.Fatalf("export should resume once capacity frees, got %d requests", got)
	}
}

func TestTracing_ExportDoesNotFollowRedirects(t *testing.T) {
	var mu sync.Mutex
	var redirected []http.Header
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		redirected = append(redirected, r.Header.Clone())
		mu.Unlock()
	}))
	t.Cleanup(target.Close)
	var origin int
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		origin++
		mu.Unlock()
		http.Redirect(w, r, target.URL+"/v1/traces", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	tracer, err := NewTracer(TracingConfig{
		Endpoint:      redirector.URL + "/v1/traces",
		Headers:       map[string]string{"X-API-Key": testCollectorAuth},
		ExportTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newTracedFullFlowHandler(t, tracer)
	if rec := serveToken(h, sampledTraceparent(), ""); rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	mu.Lock()
	defer mu.Unlock()
	if origin != 1 {
		t.Fatalf("expected 1 request to the configured collector, got %d", origin)
	}
	if len(redirected) != 0 {
		t.Fatalf("export followed a redirect and forwarded credentials: %d requests", len(redirected))
	}
}

func TestTracing_TokenIssuanceSpanOmitsUpstreamBody(t *testing.T) {
	collector := newTestCollector(t, http.StatusOK, 0)
	h := newTracedFullFlowHandler(t, mustNewTracer(t, collector.server.URL+"/v1/traces", 0))
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/test-org/test-repo/installation":
			json.NewEncoder(w).Encode(installationResponse{
				ID: 12345, Account: struct {
					Login string `json:"login"`
				}{Login: "test-org"},
			})
		case strings.HasPrefix(r.URL.Path, "/app/installations/12345/access_tokens"):
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"message":"` + sensitiveUpstreamBody + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(github.Close)
	h.githubBaseURL = github.URL

	rec := serveToken(h, sampledTraceparent(), "")
	if rec.Code == http.StatusOK {
		t.Fatalf("expected token issuance failure, got 200")
	}

	issue := spanByName(collector.spans(), "mint.create_installation_token")
	if issue == nil {
		t.Fatal("token issuance span missing")
	}
	if !strings.Contains(issue.Status.Message, "422") {
		t.Fatalf("span should record the upstream status, got %q", issue.Status.Message)
	}
	for _, raw := range collector.raw {
		if strings.Contains(raw, sensitiveUpstreamBody) {
			t.Fatalf("exported spans contain the upstream response body")
		}
	}
}
