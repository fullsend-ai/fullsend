package function

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/fullsend-ai/fullsend/internal/mintcore"
)

// collectorHeaderValue is a fake collector credential; tests assert it is
// sent as a header and never recorded in exported spans.
const collectorHeaderValue = "Bearer collector-secret-value"

// callerTraceID is the W3C trace ID of the simulated caller's span.
var callerTraceID = strings.Repeat("ab", 16)

type capturedExport struct {
	auth string
	body string
}

// fakeCollector records OTLP export requests and answers with status.
func fakeCollector(t *testing.T, status int) (*httptest.Server, func() []capturedExport) {
	t.Helper()
	var mu sync.Mutex
	var got []capturedExport
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, capturedExport{auth: r.Header.Get("Authorization"), body: string(body)})
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []capturedExport {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedExport(nil), got...)
	}
}

// secretStore returns a secretReader serving the given secrets and
// recording which secrets were requested.
func secretStore(secrets map[string]string, requested *[]string) secretReader {
	return func(ctx context.Context, projectNum, secretID string) ([]byte, error) {
		*requested = append(*requested, projectNum+"/"+secretID)
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("secret read without a deadline")
		}
		v, ok := secrets[secretID]
		if !ok {
			return nil, errors.New("secret access returned status 404")
		}
		return []byte(v), nil
	}
}

// newWiredHandler builds the handler the same way init() does.
func newWiredHandler(t *testing.T) *mintcore.Handler {
	t.Helper()
	t.Setenv("ROLE_APP_IDS", `{"coder":"100"}`)
	t.Setenv("ALLOWED_WORKFLOW_FILES", "*")
	verifier, err := mintcore.NewSTSVerifier(mintcore.STSVerifierConfig{
		GCPProjectNum:      "123456",
		WIFPoolName:        "test-pool",
		DefaultWIFProvider: "test-provider",
	})
	if err != nil {
		t.Fatalf("NewSTSVerifier: %v", err)
	}
	h, err := mintcore.NewHandler(mintcore.NewGCPSecretPEMAccessor("123456"), verifier)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return h
}

func TestBuildTracer_NoEndpointDisablesTracing(t *testing.T) {
	var requested []string
	tr := buildTracer(context.Background(), "123456", "", "fullsend-mint/us-central1", "fullsend-mint-otlp-headers",
		secretStore(map[string]string{"fullsend-mint-otlp-headers": "authorization=x"}, &requested))
	if tr != nil {
		t.Fatal("expected nil tracer without an endpoint")
	}
	if len(requested) != 0 {
		t.Fatalf("secret read without an endpoint: %v", requested)
	}
}

func TestBuildTracer_DisabledWhenHeadersSecretUnreadable(t *testing.T) {
	var requested []string
	tr := buildTracer(context.Background(), "123456", "https://collector.example/v1/traces", "fullsend-mint/us-central1", "fullsend-mint-otlp-headers",
		secretStore(nil, &requested))
	if tr != nil {
		t.Fatal("expected tracing disabled when the headers secret cannot be read")
	}
	if len(requested) != 1 || requested[0] != "123456/fullsend-mint-otlp-headers" {
		t.Fatalf("requested = %v", requested)
	}
}

func TestBuildTracer_DisabledOnInvalidHeaders(t *testing.T) {
	var requested []string
	tr := buildTracer(context.Background(), "123456", "https://collector.example/v1/traces", "fullsend-mint/us-central1", "fullsend-mint-otlp-headers",
		secretStore(map[string]string{"fullsend-mint-otlp-headers": "not-a-pair"}, &requested))
	if tr != nil {
		t.Fatal("expected tracing disabled for malformed headers")
	}
}

// TestDeployedGCFPath_ExportsSpans exercises the deployed GCF load-site
// composition: stamped endpoint and deployment, collector headers read
// from the stamped Secret Manager secret, and the handler built as in
// init(). The request's spans reach the collector with the secret's
// headers, and the header value never appears in the exported spans.
func TestDeployedGCFPath_ExportsSpans(t *testing.T) {
	srv, exports := fakeCollector(t, http.StatusOK)
	var requested []string
	tr := buildTracer(context.Background(), "123456", srv.URL+"/v1/traces", "fullsend-mint/us-central1", "fullsend-mint-otlp-headers",
		secretStore(map[string]string{"fullsend-mint-otlp-headers": "authorization=" + collectorHeaderValue}, &requested))
	if tr == nil {
		t.Fatal("expected a tracer")
	}
	h := newWiredHandler(t)
	h.SetTracer(tr)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/token", nil)
	req.Header.Set("traceparent", "00-"+callerTraceID+"-"+strings.Repeat("cd", 8)+"-01")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("traceresponse"), callerTraceID) {
		t.Fatalf("traceresponse %q does not join the caller's trace", rec.Header().Get("traceresponse"))
	}

	got := exports()
	if len(got) != 1 {
		t.Fatalf("exports = %d, want 1", len(got))
	}
	if got[0].auth != collectorHeaderValue {
		t.Fatalf("collector Authorization = %q, want the header from the secret", got[0].auth)
	}
	for _, want := range []string{mintcore.TraceBackendGCF, "fullsend-mint/us-central1", "authentication", callerTraceID} {
		if !strings.Contains(got[0].body, want) {
			t.Errorf("exported spans missing %q", want)
		}
	}
	if strings.Contains(got[0].body, "collector-secret-value") {
		t.Fatal("collector header value leaked into exported spans")
	}
}

func TestDeployedGCFPath_NoHeadersSecret(t *testing.T) {
	srv, exports := fakeCollector(t, http.StatusOK)
	var requested []string
	tr := buildTracer(context.Background(), "123456", srv.URL+"/v1/traces", "fullsend-mint/us-central1", "", secretStore(nil, &requested))
	if tr == nil {
		t.Fatal("expected a tracer")
	}
	if len(requested) != 0 {
		t.Fatalf("secret read without a stamped secret: %v", requested)
	}
	h := newWiredHandler(t)
	h.SetTracer(tr)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/token", nil))
	got := exports()
	if len(got) != 1 || got[0].auth != "" {
		t.Fatalf("exports = %+v, want one unauthenticated export", got)
	}
}

func TestDeployedGCFPath_CollectorFailureDoesNotAffectMint(t *testing.T) {
	srv, exports := fakeCollector(t, http.StatusInternalServerError)
	var requested []string
	tr := buildTracer(context.Background(), "123456", srv.URL+"/v1/traces", "fullsend-mint/us-central1", "", secretStore(nil, &requested))
	untraced := newWiredHandler(t)
	traced := newWiredHandler(t)
	traced.SetTracer(tr)

	for _, path := range []string{"/v1/token", "/v1/status", "/v1/nonexistent"} {
		method := http.MethodPost
		if path != "/v1/token" {
			method = http.MethodGet
		}
		want := httptest.NewRecorder()
		untraced.ServeHTTP(want, httptest.NewRequest(method, path, nil))
		gotRec := httptest.NewRecorder()
		traced.ServeHTTP(gotRec, httptest.NewRequest(method, path, nil))
		if gotRec.Code != want.Code || gotRec.Body.String() != want.Body.String() {
			t.Errorf("%s %s: traced = %d %q, untraced = %d %q", method, path, gotRec.Code, gotRec.Body.String(), want.Code, want.Body.String())
		}
	}
	if len(exports()) == 0 {
		t.Fatal("expected export attempts against the failing collector")
	}
}
