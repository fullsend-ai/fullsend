package cli

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
)

func TestGatewayRefreshWork_UsesOpenAISettle(t *testing.T) {
	// ADR 0137: the gateway route reuses the OpenAI placeholder settle, and
	// the default budget still fits a 300 s token's 150 s lead.
	assert.Equal(t, 90*time.Second, openAIPlaceholderSettle)
	assert.Equal(t, 130*time.Second, gatewayRefreshWork())
	assert.LessOrEqual(t, gatewayRefreshWork()+gatewayRefreshSafety, 150*time.Second)
}

func TestGatewayRefreshDelay_TokenLifetimes(t *testing.T) {
	iat := time.Unix(1_000_000, 0)
	need := gatewayRefreshWork() + gatewayRefreshSafety
	for _, lifetime := range []time.Duration{300 * time.Second, 10 * time.Minute, time.Hour} {
		exp := iat.Add(lifetime)
		for _, frac := range []float64{0, 0.5, 0.999} {
			d, ok := gatewayRefreshDelay(iat, exp, iat, frac)
			if !ok {
				t.Fatalf("lifetime %v frac %v: refresh work does not fit", lifetime, frac)
			}
			if d < gatewayRefreshMinDelay {
				t.Fatalf("lifetime %v: delay %v below minimum", lifetime, d)
			}
			if left := lifetime - d; left < need {
				t.Fatalf("lifetime %v: only %v left after refresh fires, need %v", lifetime, left, need)
			}
		}
	}
}

func TestGatewayRefreshDelay_300sTokenWindow(t *testing.T) {
	iat := time.Unix(1_000_000, 0)
	exp := iat.Add(300 * time.Second)
	early, _ := gatewayRefreshDelay(iat, exp, iat, 0.999)
	late, _ := gatewayRefreshDelay(iat, exp, iat, 0)
	if late != 150*time.Second {
		t.Fatalf("no-jitter delay = %v, want 150s (half the lifetime)", late)
	}
	if early < 120*time.Second || early >= late {
		t.Fatalf("jittered delay = %v, want in [120s,150s)", early)
	}
}

func TestGatewayRefreshDelay_LateStartAndTooShort(t *testing.T) {
	iat := time.Unix(1_000_000, 0)
	exp := iat.Add(300 * time.Second)
	// Fetched late: only the work budget is left, refresh at once.
	now := exp.Add(-(gatewayRefreshWork() + gatewayRefreshSafety + gatewayRefreshMinDelay))
	d, ok := gatewayRefreshDelay(iat, exp, now, 0.5)
	if d != gatewayRefreshMinDelay || !ok {
		t.Fatalf("late start: d=%v ok=%v", d, ok)
	}
	// A token shorter than the refresh work cannot be refreshed in time;
	// with the 90 s settle that includes a 120 s token.
	for _, lifetime := range []time.Duration{30 * time.Second, 120 * time.Second} {
		d, ok = gatewayRefreshDelay(iat, iat.Add(lifetime), iat, 0)
		if d != gatewayRefreshMinDelay || ok {
			t.Fatalf("too short (%v): d=%v ok=%v", lifetime, d, ok)
		}
	}
	// An out-of-range jitter fraction is ignored.
	if d, _ := gatewayRefreshDelay(iat, exp, iat, 7); d != 150*time.Second {
		t.Fatalf("bad jitter fraction: d=%v", d)
	}
}

func TestIsGatewayModel(t *testing.T) {
	for m, want := range map[string]bool{
		"gateway/gpt-5":       true,
		"Gateway/claude":      true,
		" GATEWAY/x ":         true,
		"openai/gpt-5":        false,
		"anthropic-vertex/c":  false,
		"gateway":             false,
		"gatewayx/foo":        false,
		"claude-haiku-latest": false,
	} {
		if got := isGatewayModel(m); got != want {
			t.Errorf("isGatewayModel(%q) = %v, want %v", m, got, want)
		}
	}
	if !anyGatewayModel([]string{"openai/a", "gateway/b"}) || anyGatewayModel([]string{"openai/a"}) {
		t.Fatal("anyGatewayModel")
	}
}

func TestValidateGatewayRuntime(t *testing.T) {
	if err := validateGatewayRuntime("pi", []string{"gateway/a", "openai/b"}); err != nil {
		t.Fatal(err)
	}
	if err := validateGatewayRuntime("claude", []string{"opus"}); err != nil {
		t.Fatal(err)
	}
	for _, rt := range []string{"claude", "codex"} {
		err := validateGatewayRuntime(rt, []string{"gateway/a"})
		if err == nil || !strings.Contains(err.Error(), rt) {
			t.Fatalf("%s: err = %v", rt, err)
		}
	}
}

func stubGatewayOIDC(t *testing.T, reqURL, reqToken string) {
	t.Helper()
	orig := gatewayOIDCEnv
	gatewayOIDCEnv = func() (string, string) { return reqURL, reqToken }
	t.Cleanup(func() { gatewayOIDCEnv = orig })
}

func TestGatewayBlockApplies(t *testing.T) {
	full := config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud"}

	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "tok")
	if ok, err := gatewayBlockApplies(config.InferenceGatewayConfig{}); ok || err != nil {
		t.Fatalf("no block: ok=%v err=%v", ok, err)
	}
	if ok, err := gatewayBlockApplies(full); !ok || err != nil {
		t.Fatalf("full block: ok=%v err=%v", ok, err)
	}
	if _, err := gatewayBlockApplies(config.InferenceGatewayConfig{URL: "https://gw.example.com"}); err == nil || !strings.Contains(err.Error(), "audience") {
		t.Fatalf("partial block: err=%v", err)
	}
	if _, err := gatewayBlockApplies(config.InferenceGatewayConfig{URL: "http://gw.example.com", Audience: "a"}); err == nil {
		t.Fatal("http url accepted")
	}

	// A local run (no OIDC endpoint) leaves the route to the harness.
	stubGatewayOIDC(t, "", "")
	if ok, err := gatewayBlockApplies(full); ok || err != nil {
		t.Fatalf("local run: ok=%v err=%v", ok, err)
	}
}

func TestFetchGatewayToken(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	orig := fetchGatewayAssertion
	t.Cleanup(func() { fetchGatewayAssertion = orig })

	var got openaiwif.AssertionConfig
	fetchGatewayAssertion = func(_ context.Context, cfg openaiwif.AssertionConfig) (*openaiwif.Assertion, error) {
		got = cfg
		return &openaiwif.Assertion{Value: "jwt"}, nil
	}
	a, err := fetchGatewayToken(context.Background(), config.InferenceGatewayConfig{URL: "https://gw", Audience: " aud "})
	if err != nil || a.Value != "jwt" {
		t.Fatalf("a=%v err=%v", a, err)
	}
	if got.Audience != "aud" || got.OIDCRequestToken != "req" {
		t.Fatalf("cfg = %+v", got)
	}

	fetchGatewayAssertion = func(context.Context, openaiwif.AssertionConfig) (*openaiwif.Assertion, error) {
		return nil, errors.New("refused")
	}
	if _, err := fetchGatewayToken(context.Background(), config.InferenceGatewayConfig{Audience: "a"}); err == nil || !strings.Contains(err.Error(), "inference gateway") {
		t.Fatalf("err = %v", err)
	}
}
