package mintclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// sensitiveUpstreamText matches no token-shaped pattern, so only keeping
// upstream response text out of spans can prevent its export.
const sensitiveUpstreamText = "internal-customer-detail-4711"

type mintTraceEnv struct {
	mintURL string

	mu           sync.Mutex
	traceparents []string
	tracestates  []string
}

// newMintTraceEnv runs a stub mint that records the inbound trace context
// headers and answers 403 with sensitiveUpstreamText in the body, plus a
// fake OIDC endpoint. Server-side trace handling is covered by the
// internal/mintcore tests.
func newMintTraceEnv(t *testing.T) *mintTraceEnv {
	t.Helper()
	env := &mintTraceEnv{}

	mint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.mu.Lock()
		env.traceparents = append(env.traceparents, r.Header.Get("traceparent"))
		env.tracestates = append(env.tracestates, r.Header.Get("tracestate"))
		env.mu.Unlock()
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"` + sensitiveUpstreamText + `"}`))
	}))
	t.Cleanup(mint.Close)
	env.mintURL = mint.URL

	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("traceparent") != "" {
			t.Errorf("trace context must not be sent to the OIDC token endpoint")
		}
		json.NewEncoder(w).Encode(oidcTokenResponse{Value: "oidc-jwt-value"})
	}))
	t.Cleanup(oidc.Close)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	return env
}

func callerContext(t *testing.T, flags trace.TraceFlags) (context.Context, *tracetest.SpanRecorder, trace.Span) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithSpanProcessor(recorder),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	state, err := trace.ParseTraceState("vendor=abc,other=xyz")
	if err != nil {
		t.Fatal(err)
	}
	traceID, _ := trace.TraceIDFromHex("4bf92f3577b34da6a3ce929d0e0e4736")
	spanID, _ := trace.SpanIDFromHex("00f067aa0ba902b7")
	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: flags, TraceState: state, Remote: true,
	})
	ctx, caller := tp.Tracer("test").Start(trace.ContextWithRemoteSpanContext(context.Background(), remote), "caller")
	return ctx, recorder, caller
}

func TestMintToken_TraceContextPropagatedToMint(t *testing.T) {
	env := newMintTraceEnv(t)
	ctx, recorder, caller := callerContext(t, trace.FlagsSampled)

	_, err := MintToken(ctx, MintRequest{MintURL: env.mintURL, Role: "triage", Repos: []string{"test-repo"}})
	caller.End()
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected mint 403, got %v", err)
	}

	var client sdktrace.ReadOnlySpan
	for _, s := range recorder.Ended() {
		if s.Name() == "fullsend.mint.token" {
			client = s
		}
	}
	if client == nil {
		t.Fatal("client span not recorded")
	}
	if client.SpanKind() != trace.SpanKindClient {
		t.Fatalf("client span kind = %v", client.SpanKind())
	}
	if client.Parent().SpanID() != caller.SpanContext().SpanID() {
		t.Fatal("client span is not a child of the caller span")
	}
	if client.Status().Description == "" {
		t.Fatal("client span did not record the mint failure")
	}

	env.mu.Lock()
	defer env.mu.Unlock()
	if len(env.traceparents) != 1 {
		t.Fatalf("expected 1 mint request (403 is not retried), got %d", len(env.traceparents))
	}
	csc := client.SpanContext()
	wantTP := "00-" + csc.TraceID().String() + "-" + csc.SpanID().String() + "-01"
	if env.traceparents[0] != wantTP {
		t.Fatalf("traceparent = %q, want %q", env.traceparents[0], wantTP)
	}
	if env.tracestates[0] != "vendor=abc,other=xyz" {
		t.Fatalf("tracestate = %q", env.tracestates[0])
	}
}

func TestMintToken_SpanErrorOmitsUpstreamResponse(t *testing.T) {
	env := newMintTraceEnv(t)
	ctx, recorder, caller := callerContext(t, trace.FlagsSampled)

	_, err := MintToken(ctx, MintRequest{MintURL: env.mintURL, Role: "triage", Repos: []string{"test-repo"}})
	caller.End()
	if err == nil || !strings.Contains(err.Error(), sensitiveUpstreamText) {
		t.Fatalf("expected the returned error to keep the upstream excerpt, got %v", err)
	}

	for _, s := range recorder.Ended() {
		if s.Name() != "fullsend.mint.token" {
			continue
		}
		if strings.Contains(s.Status().Description, sensitiveUpstreamText) {
			t.Fatalf("span status leaked upstream response: %q", s.Status().Description)
		}
		for _, ev := range s.Events() {
			for _, kv := range ev.Attributes {
				if strings.Contains(kv.Value.Emit(), sensitiveUpstreamText) {
					t.Fatalf("span event %s attribute %s leaked upstream response", ev.Name, kv.Key)
				}
			}
		}
		return
	}
	t.Fatal("client span not recorded")
}

func TestMintToken_OIDCFailureSpanOmitsUpstreamResponse(t *testing.T) {
	oidc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(sensitiveUpstreamText))
	}))
	t.Cleanup(oidc.Close)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", oidc.URL)
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "request-token")
	ctx, recorder, caller := callerContext(t, trace.FlagsSampled)

	_, err := MintToken(ctx, MintRequest{MintURL: "http://127.0.0.1:1", Role: "triage", Repos: []string{"test-repo"}})
	caller.End()
	if err == nil {
		t.Fatal("expected OIDC failure")
	}
	for _, s := range recorder.Ended() {
		if s.Name() != "fullsend.mint.token" {
			continue
		}
		if s.Status().Description == "" {
			t.Fatal("client span did not record the OIDC failure")
		}
		if strings.Contains(s.Status().Description, sensitiveUpstreamText) {
			t.Fatalf("span status leaked upstream response: %q", s.Status().Description)
		}
		for _, ev := range s.Events() {
			for _, kv := range ev.Attributes {
				if strings.Contains(kv.Value.Emit(), sensitiveUpstreamText) {
					t.Fatalf("span event attribute %s leaked upstream response", kv.Key)
				}
			}
		}
		return
	}
	t.Fatal("client span not recorded")
}

func TestMintToken_UnsampledParentPropagatesDecision(t *testing.T) {
	env := newMintTraceEnv(t)
	ctx, recorder, caller := callerContext(t, 0)

	_, _ = MintToken(ctx, MintRequest{MintURL: env.mintURL, Role: "triage", Repos: []string{"test-repo"}})
	caller.End()

	if n := len(recorder.Ended()); n != 0 {
		t.Fatalf("unsampled caller recorded %d spans", n)
	}
	env.mu.Lock()
	defer env.mu.Unlock()
	if len(env.traceparents) != 1 {
		t.Fatalf("expected 1 mint request, got %d", len(env.traceparents))
	}
	tp := env.traceparents[0]
	if !strings.HasPrefix(tp, "00-4bf92f3577b34da6a3ce929d0e0e4736-") || !strings.HasSuffix(tp, "-00") {
		t.Fatalf("unsampled traceparent = %q", tp)
	}
	if env.tracestates[0] != "vendor=abc,other=xyz" {
		t.Fatalf("tracestate = %q", env.tracestates[0])
	}
}

func TestMintToken_NoActiveTraceSendsNoHeaders(t *testing.T) {
	env := newMintTraceEnv(t)
	_, _ = MintToken(context.Background(), MintRequest{MintURL: env.mintURL, Role: "triage", Repos: []string{"test-repo"}})

	env.mu.Lock()
	defer env.mu.Unlock()
	if len(env.traceparents) != 1 || env.traceparents[0] != "" || env.tracestates[0] != "" {
		t.Fatalf("trace headers sent without an active trace: %q %q", env.traceparents, env.tracestates)
	}
}

func TestCallStatus_InjectsTraceContext(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("traceparent")
		json.NewEncoder(w).Encode(StatusResult{Org: "o"})
	}))
	defer srv.Close()
	ctx, _, caller := callerContext(t, trace.FlagsSampled)
	defer caller.End()
	if _, err := callStatus(ctx, srv.URL, "token"); err != nil {
		t.Fatal(err)
	}
	sc := caller.SpanContext()
	if want := "00-" + sc.TraceID().String() + "-" + sc.SpanID().String() + "-01"; got != want {
		t.Fatalf("traceparent = %q, want %q", got, want)
	}
}
