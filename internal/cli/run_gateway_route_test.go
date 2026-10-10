package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// gatewayTestJWT is compact-JWT shaped (three base64url segments) but
// carries no real claims, so it passes validateGatewayAssertion.
const gatewayTestJWT = "eyJhbGciOiJSUzI1NiJ9.gateway-route-test-token.sig"

// gatewayTestRunConfig writes config (and any extra repo files) into a
// repository with a .fullsend/config.yaml and loads it as `fullsend run`
// does.
func gatewayTestRunConfig(t *testing.T, cfg string, files map[string]string) runConfig {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".fullsend"), 0o755))
	path := filepath.Join(root, ".fullsend", "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o644))
	for p, data := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(root, p), []byte(data), 0o644))
	}
	rc, err := loadRunConfig(path)
	require.NoError(t, err)
	return rc
}

const gatewayTestBlock = `version: "1"
inference:
  gateway:
    url: https://gw.example.com
    audience: aud
    models:
      m1:
        api: openai-responses
`

func TestPlanGatewayRoute(t *testing.T) {
	pi := runtime.Backend{Runtime: runtime.PiRuntime{}}
	claude := runtime.Backend{Runtime: runtime.ClaudeRuntime{}}
	dummy := runtime.Backend{Runtime: runtime.DummyRuntime{}}
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	rc := gatewayTestRunConfig(t, gatewayTestBlock, nil)

	t.Run("pi on a gateway model prepares the runtime", func(t *testing.T) {
		const sb = "fs-plan-pi"
		plan, err := planGatewayRoute(rc, pi, sb, []string{"gateway/m1"}, true)
		require.NoError(t, err)
		require.NotNil(t, plan)
		t.Cleanup(func() { plan.prepared.ClearGatewayRun(sb) })
		assert.Equal(t, "gw.example.com", plan.host)
		assert.Equal(t, "aud", plan.block.Audience)
		require.NotNil(t, plan.prepared)
		assert.Equal(t, runtime.PiRuntime{}.GatewayCredentialSeed(), plan.seed)
	})
	t.Run("pi with no gateway model adds nothing", func(t *testing.T) {
		plan, err := planGatewayRoute(rc, pi, "fs-plan-none", []string{"openai/gpt-5"}, false)
		require.NoError(t, err)
		assert.Nil(t, plan)
	})
	t.Run("dummy attaches the provider without a seed", func(t *testing.T) {
		plan, err := planGatewayRoute(rc, dummy, "fs-plan-dummy", []string{""}, false)
		require.NoError(t, err)
		require.NotNil(t, plan)
		assert.Nil(t, plan.prepared)
		assert.True(t, plan.seed.IsZero())
	})
	t.Run("a gateway model on Claude Code is an error", func(t *testing.T) {
		_, err := planGatewayRoute(rc, claude, "fs-plan-claude", []string{"gateway/m1"}, false)
		assert.ErrorContains(t, err, "does not implement")
	})
	t.Run("no config file", func(t *testing.T) {
		plan, err := planGatewayRoute(runConfig{}, pi, "fs-plan-nocfg", []string{"gateway/m1"}, true)
		require.NoError(t, err)
		assert.Nil(t, plan)
	})
	t.Run("a partial block is an error", func(t *testing.T) {
		partial := gatewayTestRunConfig(t, "version: \"1\"\ninference:\n  gateway:\n    url: https://gw.example.com\n", nil)
		_, err := planGatewayRoute(partial, pi, "fs-plan-partial", []string{"openai/gpt-5"}, false)
		assert.ErrorContains(t, err, "partial")
	})
	t.Run("pi with no model list fails before the sandbox", func(t *testing.T) {
		bare := gatewayTestRunConfig(t, "version: \"1\"\ninference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n", nil)
		_, err := planGatewayRoute(bare, pi, "fs-plan-bare", []string{"gateway/m1"}, true)
		assert.ErrorContains(t, err, "model list")
	})
	t.Run("a local run leaves the route to the harness", func(t *testing.T) {
		stubGatewayOIDC(t, "", "")
		plan, err := planGatewayRoute(rc, pi, "fs-plan-local", []string{"gateway/m1"}, true)
		require.NoError(t, err)
		assert.Nil(t, plan)
	})
}

func TestPlanGatewayRoute_ModelsFile(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	pi := runtime.Backend{Runtime: runtime.PiRuntime{}}
	cfg := "version: \"1\"\ninference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n    models_file: .fullsend/inference-gateway.json\n"
	file := `{"providers":{"gateway":{"models":{"from-file":{"api":"openai-responses"}}}}}`
	rc := gatewayTestRunConfig(t, cfg, map[string]string{".fullsend/inference-gateway.json": file})

	const sb = "fs-plan-file"
	plan, err := planGatewayRoute(rc, pi, sb, []string{"gateway/from-file"}, true)
	require.NoError(t, err)
	require.NotNil(t, plan)
	t.Cleanup(func() { plan.prepared.ClearGatewayRun(sb) })

	missing := gatewayTestRunConfig(t, cfg, nil)
	_, err = planGatewayRoute(missing, pi, "fs-plan-file-missing", []string{"gateway/from-file"}, true)
	assert.ErrorContains(t, err, "models_file")
}

func TestReadGatewayModelsFile(t *testing.T) {
	block := config.InferenceGatewayConfig{ModelsFile: ".fullsend/gw.json"}
	data, err := readGatewayModelsFile(runConfig{}, config.InferenceGatewayConfig{})
	require.NoError(t, err)
	assert.Nil(t, data, "no models_file")

	_, err = readGatewayModelsFile(runConfig{}, block)
	assert.ErrorContains(t, err, "no config.yaml location")

	rc := gatewayTestRunConfig(t, "version: \"1\"\n", map[string]string{".fullsend/gw.json": "{}"})
	data, err = readGatewayModelsFile(rc, block)
	require.NoError(t, err)
	assert.Equal(t, "{}", string(data))

	_, err = readGatewayModelsFile(rc, config.InferenceGatewayConfig{ModelsFile: "../outside.json"})
	assert.Error(t, err, "path outside the repository")

	big := strings.Repeat(" ", runtime.MaxPiGatewayModelsFileBytes+1)
	rc = gatewayTestRunConfig(t, "version: \"1\"\n", map[string]string{".fullsend/gw.json": big})
	_, err = readGatewayModelsFile(rc, block)
	assert.ErrorContains(t, err, "exceeds")

	// A symlink out of the repository is refused by os.Root.
	outside := filepath.Join(t.TempDir(), "secret.json")
	require.NoError(t, os.WriteFile(outside, []byte("{}"), 0o644))
	rc = gatewayTestRunConfig(t, "version: \"1\"\n", nil)
	require.NoError(t, os.Symlink(outside, filepath.Join(gatewayRepoRoot(rc.source), ".fullsend", "gw.json")))
	_, err = readGatewayModelsFile(rc, block)
	assert.Error(t, err, "symlink escaping the repository")
}

func TestGatewayRepoRoot(t *testing.T) {
	assert.Equal(t, filepath.FromSlash("/repo"), gatewayRepoRoot(filepath.FromSlash("/repo/.fullsend/config.yaml")))
	assert.Equal(t, filepath.FromSlash("/cfg"), gatewayRepoRoot(filepath.FromSlash("/cfg/config.yaml")))
}

func TestStartGatewayRoute(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	iat := time.Now().Truncate(time.Second)
	stubGatewayAssertion(t, func(_ context.Context, cfg actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		assert.Equal(t, "aud", cfg.Audience)
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: iat, ExpiresAt: iat.Add(5 * time.Minute)}, nil
	})
	var gotHost, gotToken string
	orig := ensureGatewayProviderFn
	t.Cleanup(func() { ensureGatewayProviderFn = orig })
	ensureGatewayProviderFn = func(_ context.Context, host, sandboxName, token string, _ time.Time, _ *ui.Printer) (string, string, error) {
		gotHost, gotToken = host, token
		return "inference-gateway-x", "fullsend-inference-gateway-abc", nil
	}
	plan := &gatewayRoutePlan{block: config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud"}, host: "gw.example.com", seed: runtime.PiRuntime{}.GatewayCredentialSeed()}
	h, err := startGatewayRoute(context.Background(), plan, "fs-start", ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, "inference-gateway-x", h.name)
	assert.Equal(t, "gw.example.com", gotHost)
	assert.Equal(t, gatewayTestJWT, gotToken)
	assert.Equal(t, iat, h.issuedAt)
	assert.Equal(t, iat.Add(5*time.Minute), h.expiresAt)
	assert.False(t, h.sandboxReady(), "not ready until the sandbox is up")
	h.sandboxUp.Store(true)
	assert.True(t, h.sandboxReady())

	// A refused assertion fails the run: no fallback.
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		return nil, errors.New("refused")
	})
	_, err = startGatewayRoute(context.Background(), plan, "fs-start", ui.New(io.Discard))
	assert.ErrorContains(t, err, "refused")

	// A provider failure fails the run too.
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: iat, ExpiresAt: iat.Add(5 * time.Minute)}, nil
	})
	ensureGatewayProviderFn = func(context.Context, string, string, string, time.Time, *ui.Printer) (string, string, error) {
		return "", "", errors.New("gateway down")
	}
	_, err = startGatewayRoute(context.Background(), plan, "fs-start", ui.New(io.Discard))
	assert.ErrorContains(t, err, "gateway down")
}

func TestRefreshGatewayProvider_BeforeSandbox(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	log := filepath.Join(t.TempDir(), "log")
	stubOpenshell(t, "echo \"$*\" >> "+shellQuoteForTest(log)+"; exit 0")
	iat := time.Now().Truncate(time.Second)
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: iat, ExpiresAt: iat.Add(5 * time.Minute)}, nil
	})
	h := gatewayProviderHandle{name: "inference-gateway-x", block: config.InferenceGatewayConfig{Audience: "aud"}, sandbox: "fs-x", seed: runtime.PiRuntime{}.GatewayCredentialSeed(), sandboxUp: &atomic.Bool{}}
	gotIat, gotExp, placeholder, err := refreshGatewayProvider(context.Background(), h, "", ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, iat, gotIat)
	assert.Equal(t, iat.Add(5*time.Minute), gotExp)
	assert.Empty(t, placeholder, "no sandbox yet: nothing re-seeded")
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Contains(t, string(data), "inference-gateway-x", "the provider was updated")
	assert.NotContains(t, string(data), "sandbox exec", "no re-seed before the sandbox is up")

	// A token that is not a compact JWT is refused before it is stored.
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		return &actionsoidc.Assertion{Value: "not a jwt\n::error::x", IssuedAt: iat, ExpiresAt: iat.Add(5 * time.Minute)}, nil
	})
	_, _, _, err = refreshGatewayProvider(context.Background(), h, "", ui.New(io.Discard))
	assert.ErrorContains(t, err, "compact JWT")
}

func TestRefreshGatewayProvider_ReseedsTheTokenFile(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	binDir := t.TempDir()
	counter := filepath.Join(binDir, "count")
	log := filepath.Join(binDir, "log")
	// The placeholder read answers the old generation until the provider
	// is updated, then the new one; the seed and verify execs succeed.
	script := "#!/bin/sh\n" +
		// The seed fragment also names the placeholder variable, so the
		// grep and seed arms come before the placeholder read.
		"case \"$*\" in\n" +
		"  *grep*) exit 0 ;;\n" +
		"  *inference-gateway.token*) echo seeded >> " + shellQuoteForTest(log) + "; exit 0 ;;\n" +
		"  *INFERENCE_GATEWAY_API_KEY:-*) if test -f " + shellQuoteForTest(counter) + "; then printf '" + ph("v222_INFERENCE_GATEWAY_API_KEY") + "'; else printf '" + ph("v111_INFERENCE_GATEWAY_API_KEY") + "'; fi; exit 0 ;;\n" +
		"  *'provider update'*) touch " + shellQuoteForTest(counter) + "; exit 0 ;;\n" +
		"esac\nexit 0\n"
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "openshell"), []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldPoll := openAIPlaceholderPoll
	openAIPlaceholderPoll = 10 * time.Millisecond
	t.Cleanup(func() { openAIPlaceholderPoll = oldPoll })

	iat := time.Now().Truncate(time.Second)
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: iat, ExpiresAt: iat.Add(5 * time.Minute)}, nil
	})
	up := &atomic.Bool{}
	up.Store(true)
	h := gatewayProviderHandle{name: "inference-gateway-x", block: config.InferenceGatewayConfig{Audience: "aud"}, sandbox: "fs-x", seed: runtime.PiRuntime{}.GatewayCredentialSeed(), sandboxUp: up}
	_, _, placeholder, err := refreshGatewayProvider(context.Background(), h, "", ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, ph("v222_INFERENCE_GATEWAY_API_KEY"), placeholder)
	data, err := os.ReadFile(log)
	require.NoError(t, err)
	assert.Equal(t, "seeded\n", string(data))
}

func TestSandboxPlaceholder_PerRouteKey(t *testing.T) {
	_, err := sandboxPlaceholder(context.Background(), "fs-x", "BAD KEY;")
	assert.ErrorContains(t, err, "not an env var name")

	oldPoll := openAIPlaceholderPoll
	openAIPlaceholderPoll = time.Millisecond
	t.Cleanup(func() { openAIPlaceholderPoll = oldPoll })
	stubOpenshell(t, "exit 0")
	_, err = baselinePlaceholder(context.Background(), "fs-x", "INFERENCE_GATEWAY_API_KEY")
	assert.ErrorContains(t, err, "no INFERENCE_GATEWAY_API_KEY placeholder")

	stubOpenshell(t, "printf '"+ph("v1_INFERENCE_GATEWAY_API_KEY")+"'")
	got, err := baselinePlaceholder(context.Background(), "fs-x", "INFERENCE_GATEWAY_API_KEY")
	require.NoError(t, err)
	assert.Equal(t, ph("v1_INFERENCE_GATEWAY_API_KEY"), got)
}

func TestRunGatewayRefresh_RefreshesUntilCancelled(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	stubOpenshell(t, "exit 0")
	oldMin := gatewayRefreshMinDelay
	gatewayRefreshMinDelay = time.Millisecond
	t.Cleanup(func() { gatewayRefreshMinDelay = oldMin })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int32
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		if calls.Add(1) >= 2 {
			cancel()
		}
		now := time.Now()
		// Already inside the refresh window: refresh again at once.
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: now, ExpiresAt: now.Add(time.Second)}, nil
	})
	h := gatewayProviderHandle{name: "inference-gateway-x", block: config.InferenceGatewayConfig{Audience: "aud"}, sandbox: "fs-x", issuedAt: time.Now(), expiresAt: time.Now().Add(time.Second), sandboxUp: &atomic.Bool{}}
	done := make(chan struct{})
	go func() {
		runGatewayRefresh(ctx, h, ui.New(io.Discard))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("refresher did not stop after cancel")
	}
	assert.GreaterOrEqual(t, calls.Load(), int32(2), "refreshed more than once")
}

func TestRunGatewayRefresh_GivesUp(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	oldMin, oldBackoff := gatewayRefreshMinDelay, gatewayRefreshBackoff
	gatewayRefreshMinDelay, gatewayRefreshBackoff = time.Millisecond, time.Millisecond
	t.Cleanup(func() { gatewayRefreshMinDelay, gatewayRefreshBackoff = oldMin, oldBackoff })
	var calls atomic.Int32
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		calls.Add(1)
		return nil, errors.New("refused")
	})
	h := gatewayProviderHandle{name: "inference-gateway-x", block: config.InferenceGatewayConfig{Audience: "aud"}, issuedAt: time.Now(), expiresAt: time.Now().Add(time.Second)}
	stops := startGatewayRefreshers([]gatewayProviderHandle{h}, ui.New(io.Discard))
	require.Len(t, stops, 1)
	require.Eventually(t, func() bool { return calls.Load() == int32(gatewayRefreshRetries+1) }, 5*time.Second, 5*time.Millisecond)
	stops[0]()
	stops[0]() // safe to call twice
	assert.Equal(t, int32(gatewayRefreshRetries+1), calls.Load(), "gave up after the retries")
}

// A hand-off whose settle wait runs out is not retried with another fetch
// (one settle per refresh, as gatewayRefreshWork budgets): the provider
// keeps the new token, the agent's placeholder is unchanged, and the
// refresher keeps going so the next refresh re-seeds again.
func TestRefreshGatewayProvider_HandOffOnce(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	// The sandbox never hands out a new generation.
	stubOpenshell(t, "case \"$*\" in *INFERENCE_GATEWAY_API_KEY:-*) printf '"+ph("v111_INFERENCE_GATEWAY_API_KEY")+"' ;; esac; exit 0")
	oldPoll, oldSettle, oldMin := openAIPlaceholderPoll, openAIPlaceholderSettle, gatewayRefreshMinDelay
	openAIPlaceholderPoll, openAIPlaceholderSettle, gatewayRefreshMinDelay = time.Millisecond, 20*time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		openAIPlaceholderPoll, openAIPlaceholderSettle, gatewayRefreshMinDelay = oldPoll, oldSettle, oldMin
	})
	var calls atomic.Int32
	iat := time.Now().Truncate(time.Second)
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		calls.Add(1)
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: iat, ExpiresAt: iat.Add(5 * time.Minute)}, nil
	})
	up := &atomic.Bool{}
	up.Store(true)
	h := gatewayProviderHandle{name: "inference-gateway-x", block: config.InferenceGatewayConfig{Audience: "aud"}, sandbox: "fs-x", seed: runtime.PiRuntime{}.GatewayCredentialSeed(), sandboxUp: up}

	gotIat, gotExp, held, err := refreshGatewayProvider(context.Background(), h, "", ui.New(io.Discard))
	var reseedErr *gatewayReseedError
	require.ErrorAs(t, err, &reseedErr)
	assert.Equal(t, int32(1), calls.Load(), "one fetch: the hand-off is not retried with a new token")
	assert.Equal(t, iat, gotIat)
	assert.Equal(t, iat.Add(5*time.Minute), gotExp)
	assert.Equal(t, ph("v111_INFERENCE_GATEWAY_API_KEY"), held, "the agent still holds the previous generation")

	// The refresher does not give up on a failed hand-off.
	calls.Store(0)
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		calls.Add(1)
		now := time.Now()
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: now, ExpiresAt: now.Add(time.Second)}, nil
	})
	h.issuedAt, h.expiresAt = time.Now(), time.Now().Add(time.Second)
	stops := startGatewayRefreshers([]gatewayProviderHandle{h}, ui.New(io.Discard))
	// More fetches than one refresh's retries: it went on to later refreshes.
	require.Eventually(t, func() bool { return calls.Load() > int32(gatewayRefreshRetries+1) }, 10*time.Second, 5*time.Millisecond, "refreshed again after a failed hand-off")
	stops[0]()
}

// The cleanup keys the runner passes for each route must be the env keys
// the runtime seeds from, or provider cleanup and re-seed drift apart.
func TestRouteCredentialKeysMatchRuntimeSeeds(t *testing.T) {
	assert.Equal(t, gatewayCredentialKey, runtime.PiRuntime{}.GatewayCredentialSeed().PlaceholderEnv)
	assert.Equal(t, openAIDefaultCredentialKey, runtime.OpenAIRouteSeed(runtime.PiRuntime{}).PlaceholderEnv)
}
