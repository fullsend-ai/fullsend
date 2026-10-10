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
	t.Run("Claude Code on a gateway model prepares the runtime", func(t *testing.T) {
		const sb = "fs-plan-claude"
		plan, err := planGatewayRoute(rc, claude, sb, []string{"gateway/claude-haiku-5-5"}, true)
		require.NoError(t, err)
		require.NotNil(t, plan)
		t.Cleanup(func() { plan.prepared.ClearGatewayRun(sb) })
		require.NotNil(t, plan.prepared)
		assert.Equal(t, runtime.ClaudeRuntime{}.GatewayCredentialSeed(), plan.seed)
		assert.Equal(t, gatewayProfile{host: "gw.example.com", claude: true}, plan.profileSpec())
	})
	t.Run("Claude Code with no gateway model keeps its route", func(t *testing.T) {
		plan, err := planGatewayRoute(rc, claude, "fs-plan-claude-vertex", []string{"opus"}, false)
		require.NoError(t, err)
		assert.Nil(t, plan)
	})
	t.Run("a gateway model on Claude Code without a block is an error", func(t *testing.T) {
		_, err := planGatewayRoute(runConfig{}, claude, "fs-plan-claude-nocfg", []string{"gateway/m1"}, true)
		assert.ErrorContains(t, err, "need an inference.gateway block")
		none := gatewayTestRunConfig(t, "version: \"1\"\n", nil)
		_, err = planGatewayRoute(none, claude, "fs-plan-claude-noblock", []string{"gateway/m1"}, true)
		assert.ErrorContains(t, err, "need an inference.gateway block")
	})
	t.Run("a gateway model on Codex is an error", func(t *testing.T) {
		_, err := planGatewayRoute(rc, runtime.Backend{Runtime: runtime.CodexRuntime{}}, "fs-plan-codex", []string{"gateway/m1"}, true)
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
	t.Run("a local oidc run is an error on Claude Code", func(t *testing.T) {
		stubGatewayOIDC(t, "", "")
		_, err := planGatewayRoute(rc, claude, "fs-plan-claude-local", []string{"gateway/m1"}, true)
		assert.ErrorContains(t, err, "auth: api-key for a local run")
	})
	t.Run("a local api-key run on Claude Code uses the api-key profile", func(t *testing.T) {
		stubGatewayOIDC(t, "", "")
		keyed := gatewayTestRunConfig(t, "version: \"1\"\ninference:\n  gateway:\n    url: https://gw.example.com\n    auth: api-key\n", nil)
		const sb = "fs-plan-claude-key"
		plan, err := planGatewayRoute(keyed, claude, sb, []string{"gateway/m1"}, true)
		require.NoError(t, err)
		require.NotNil(t, plan)
		t.Cleanup(func() { plan.prepared.ClearGatewayRun(sb) })
		assert.Equal(t, gatewayProfile{host: "gw.example.com", claude: true, apiKey: true}, plan.profileSpec())
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
	ensureGatewayProviderFn = func(_ context.Context, profile gatewayProfile, sandboxName, token string, _ time.Time, _ *ui.Printer) (string, string, error) {
		gotHost, gotToken = profile.host, token
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
	ensureGatewayProviderFn = func(context.Context, gatewayProfile, string, string, time.Time, *ui.Printer) (string, string, error) {
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

}

// The cleanup keys the runner passes for each route must be the env keys
// the runtime seeds from, or provider cleanup and re-seed drift apart.
func TestRouteCredentialKeysMatchRuntimeSeeds(t *testing.T) {
	assert.Equal(t, gatewayCredentialKey, runtime.PiRuntime{}.GatewayCredentialSeed().PlaceholderEnv)
	assert.Equal(t, openAIDefaultCredentialKey, runtime.OpenAIRouteSeed(runtime.PiRuntime{}).PlaceholderEnv)
}

// gatewayRecoveryStub puts an openshell on PATH whose placeholder read
// answers nothing until dir/base exists, then the old generation, and the
// new one once the provider was updated and dir/flip exists; a read after
// the update touches dir/settling (and fails while dir/readfail exists).
// Seed execs fail while dir/seedfail
// exists and verify execs succeed; a provider update touches dir/updated.
func gatewayRecoveryStub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	q := func(name string) string { return shellQuoteForTest(filepath.Join(dir, name)) }
	stubOpenshell(t, "case \"$*\" in\n"+
		"  *grep*) exit 0 ;;\n"+
		"  *inference-gateway.token*) if test -f "+q("seedfail")+"; then exit 1; fi; exit 0 ;;\n"+
		"  *INFERENCE_GATEWAY_API_KEY:-*) if test -f "+q("updated")+" && test -f "+q("readfail")+"; then exit 1; fi; if test -f "+q("updated")+"; then touch "+q("settling")+"; fi; if test -f "+q("updated")+" && test -f "+q("flip")+"; then printf '"+ph("v222_INFERENCE_GATEWAY_API_KEY")+"'; elif test -f "+q("base")+"; then printf '"+ph("v111_INFERENCE_GATEWAY_API_KEY")+"'; fi; exit 0 ;;\n"+
		"  *'provider update'*) touch "+q("updated")+"; exit 0 ;;\n"+
		"esac\nexit 0")
	return dir
}

// shrinkGatewayRefreshTimers makes the refresher's waits test-sized.
func shrinkGatewayRefreshTimers(t *testing.T) {
	t.Helper()
	oldPoll, oldSettle, oldMin, oldBackoff := openAIPlaceholderPoll, openAIPlaceholderSettle, gatewayRefreshMinDelay, gatewayRefreshBackoff
	openAIPlaceholderPoll, openAIPlaceholderSettle, gatewayRefreshMinDelay, gatewayRefreshBackoff = time.Millisecond, 20*time.Millisecond, time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		openAIPlaceholderPoll, openAIPlaceholderSettle, gatewayRefreshMinDelay, gatewayRefreshBackoff = oldPoll, oldSettle, oldMin, oldBackoff
	})
}

// gatewayDueHandle is a handle whose token is inside its refresh lead, on
// a sandbox that is up.
func gatewayDueHandle(expiresIn time.Duration) gatewayProviderHandle {
	up := &atomic.Bool{}
	up.Store(true)
	now := time.Now()
	return gatewayProviderHandle{name: "inference-gateway-x", block: config.InferenceGatewayConfig{Audience: "aud"}, sandbox: "fs-x",
		seed: runtime.PiRuntime{}.GatewayCredentialSeed(), sandboxUp: up, issuedAt: now.Add(expiresIn - 5*time.Minute), expiresAt: now.Add(expiresIn)}
}

// countFiveMinuteAssertions stubs the assertion fetch with 5 minute tokens
// and counts the fetches.
func countFiveMinuteAssertions(t *testing.T) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		calls.Add(1)
		now := time.Now()
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute)}, nil
	})
	return &calls
}

// A hand-off whose seed fails is retried on its own, without fetching or
// rotating again, and the agent gets the new placeholder once the seed
// succeeds — well before the next rotation is due.
func TestRunGatewayRefresh_RetriesTheHandOffAlone(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	dir := gatewayRecoveryStub(t)
	for _, f := range []string{"base", "flip", "seedfail"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o644))
	}
	shrinkGatewayRefreshTimers(t)
	calls := countFiveMinuteAssertions(t)
	h := gatewayDueHandle(time.Minute)
	var out syncBuffer
	stops := startGatewayRefreshers([]gatewayProviderHandle{h}, ui.New(&out))
	t.Cleanup(stops[0])

	require.Eventually(t, func() bool { return strings.Contains(out.String(), "retrying the hand-off") }, 10*time.Second, 5*time.Millisecond, out.String())
	assert.Never(t, func() bool { return calls.Load() > 1 }, 200*time.Millisecond, 5*time.Millisecond, "the hand-off is retried without another fetch")
	assert.NotContains(t, out.String(), "still failing", "no warning per retry while the agent's token is valid")
	require.NoError(t, os.Remove(filepath.Join(dir, "seedfail")))
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "handed off") }, 10*time.Second, 5*time.Millisecond, out.String())
	assert.Equal(t, int32(1), calls.Load())
}

// A placeholder that cannot be read rotates nothing; the refresh is
// retried and rotates once the read succeeds. Once the provider's token
// has expired, the refresher gives up.
func TestRunGatewayRefresh_BaselineFailureIsRetried(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	dir := gatewayRecoveryStub(t)
	shrinkGatewayRefreshTimers(t)
	calls := countFiveMinuteAssertions(t)
	var out syncBuffer
	// Expiring inside even the shrunk refresh budget: a late refresh.
	stops := startGatewayRefreshers([]gatewayProviderHandle{gatewayDueHandle(20 * time.Second)}, ui.New(&out))
	t.Cleanup(stops[0])

	require.Eventually(t, func() bool { return strings.Count(out.String(), "deferred") >= 2 }, 10*time.Second, 5*time.Millisecond, out.String())
	assert.Equal(t, int32(0), calls.Load(), "nothing rotated without the baseline")
	assert.Contains(t, out.String(), "running late", "a late refresh is not blamed on the token lifetime")
	assert.NotContains(t, out.String(), "lifetime")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base"), nil, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "flip"), nil, 0o644))
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "token refreshed for") }, 10*time.Second, 5*time.Millisecond, out.String())
	assert.Equal(t, int32(1), calls.Load())

	// An expired provider token with no readable placeholder: give up.
	gatewayRecoveryStub(t)
	var expired syncBuffer
	done := make(chan struct{})
	go func() {
		runGatewayRefresh(context.Background(), gatewayDueHandle(-time.Second), ui.New(&expired))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("refresher did not give up on an expired token")
	}
	assert.Contains(t, expired.String(), "gave up")
}

// A refresher restarted around a remint resumes from the token the
// provider holds now, not the one minted when the route started.
func TestStartGatewayRefreshers_RestartResumesState(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	stubOpenshell(t, "exit 0")
	shrinkGatewayRefreshTimers(t)
	calls := countFiveMinuteAssertions(t)
	h := gatewayDueHandle(time.Minute)
	h.sandboxUp = &atomic.Bool{} // before the sandbox exists
	handles := []gatewayProviderHandle{h}
	var out syncBuffer
	stops := startGatewayRefreshers(handles, ui.New(&out))
	t.Cleanup(stops[0])
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "token refreshed for") }, 10*time.Second, 5*time.Millisecond)
	stops[0]()
	require.Equal(t, int32(1), calls.Load())

	stops = startGatewayRefreshers(handles, ui.New(&out))
	t.Cleanup(stops[0])
	assert.Never(t, func() bool { return calls.Load() > 1 }, 200*time.Millisecond, 5*time.Millisecond, "no refresh: the provider's token is fresh")
	assert.NotContains(t, out.String(), "refresh budget", "no false lifetime warning")
}

// A stop that interrupts a hand-off records the landed rotation as a
// pending hand-off without warning, and the restarted refresher finishes
// the hand-off without fetching again.
func TestRunGatewayRefresh_StopDuringHandOff(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	dir := gatewayRecoveryStub(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base"), nil, 0o644))
	shrinkGatewayRefreshTimers(t)
	openAIPlaceholderSettle = time.Minute // the hand-off waits until stopped
	calls := countFiveMinuteAssertions(t)
	handles := []gatewayProviderHandle{gatewayDueHandle(time.Minute)}
	var out syncBuffer
	stops := startGatewayRefreshers(handles, ui.New(&out))
	t.Cleanup(stops[0])
	require.Eventually(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, "settling"))
		return err == nil
	}, 10*time.Second, 5*time.Millisecond, "the hand-off is waiting for the new generation")
	stops[0]()

	st := handles[0].state
	assert.True(t, st.handOffPending)
	assert.True(t, st.expiresAt.After(time.Now().Add(4*time.Minute)), "the new token is recorded")
	assert.NotContains(t, out.String(), "retrying the hand-off")

	openAIPlaceholderSettle = 20 * time.Millisecond
	require.NoError(t, os.WriteFile(filepath.Join(dir, "flip"), nil, 0o644))
	stops = startGatewayRefreshers(handles, ui.New(&out))
	t.Cleanup(stops[0])
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "handed off") }, 10*time.Second, 5*time.Millisecond, out.String())
	assert.Equal(t, int32(1), calls.Load())
}

// When a pending hand-off reaches the next rotation, the rotation hands
// the agent the generation it creates, not the stale one in between: the
// sandbox hands out the previous generation for one read after each
// update, as a propagating provider does.
func TestRunGatewayRefresh_PendingHandOffSeedsTheNewestGeneration(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	dir := t.TempDir()
	q := func(name string) string { return shellQuoteForTest(filepath.Join(dir, name)) }
	require.NoError(t, os.WriteFile(filepath.Join(dir, "count"), []byte("0\n"), 0o644))
	stubOpenshell(t, "case \"$*\" in\n"+
		"  *grep*) exit 0 ;;\n"+
		// The first hand-off fails at the seed; later ones succeed.
		"  *inference-gateway.token*) test \"$(cat "+q("count")+")\" -ge 2 ;;\n"+
		"  *INFERENCE_GATEWAY_API_KEY:-*) n=$(cat "+q("count")+"); if test -f "+q("lag")+"; then rm -f "+q("lag")+"; n=$((n-1)); fi; printf 'openshell:resolve:env:v%s_INFERENCE_GATEWAY_API_KEY' \"$n\" ;;\n"+
		"  *'provider update'*) n=$(cat "+q("count")+"); echo $((n+1)) > "+q("count")+"; touch "+q("lag")+" ;;\n"+
		"esac")
	shrinkGatewayRefreshTimers(t)
	var calls atomic.Int32
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		// The first token's lifetime is inside the refresh budget, so the
		// next rotation is due at once and meets the pending hand-off.
		lifetime := 5 * time.Minute
		if calls.Add(1) == 1 {
			lifetime = 40 * time.Second
		}
		now := time.Now()
		return &actionsoidc.Assertion{Value: gatewayTestJWT, IssuedAt: now, ExpiresAt: now.Add(lifetime)}, nil
	})
	handles := []gatewayProviderHandle{gatewayDueHandle(20 * time.Second)}
	var out syncBuffer
	stops := startGatewayRefreshers(handles, ui.New(&out))
	t.Cleanup(stops[0])
	require.Eventually(t, func() bool { return strings.Contains(out.String(), "token refreshed for inference-gateway-x (next") }, 10*time.Second, 5*time.Millisecond, out.String())
	stops[0]()

	st := handles[0].state
	assert.Equal(t, int32(2), calls.Load())
	assert.False(t, st.handOffPending)
	assert.Equal(t, ph("v2_INFERENCE_GATEWAY_API_KEY"), st.placeholder, "the agent holds the newest generation")
	assert.Equal(t, st.expiresAt, st.heldExpiresAt)
}

// A new generation that never reaches the sandbox within the settle wait
// fails the route closed: no second rotation, the provider's expiry moves
// back to the token the agent holds, and the refresher stops.
func TestRunGatewayRefresh_SettleTimeoutFailsClosed(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	dir := gatewayRecoveryStub(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "base"), nil, 0o644)) // no flip: the old generation stays
	shrinkGatewayRefreshTimers(t)
	calls := countFiveMinuteAssertions(t)
	var expiries []time.Time
	orig := setProviderCredentialExpiryFn
	setProviderCredentialExpiryFn = func(_ context.Context, name, key string, at time.Time) error {
		assert.Equal(t, "inference-gateway-x", name)
		assert.Equal(t, gatewayCredentialKey, key)
		expiries = append(expiries, at)
		return nil
	}
	t.Cleanup(func() { setProviderCredentialExpiryFn = orig })
	h := gatewayDueHandle(time.Minute)
	h.refreshState() // shared with the refresher's copy of h
	held := h.expiresAt
	var out syncBuffer
	done := make(chan struct{})
	go func() {
		runGatewayRefresh(context.Background(), h, ui.New(&out))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("refresher did not stop after the settle wait ran out")
	}
	assert.Equal(t, int32(1), calls.Load(), "no second rotation")
	assert.Equal(t, []time.Time{held}, expiries, "the provider fails closed at the held token's expiry")
	assert.Contains(t, out.String(), "stopped")
	assert.NotContains(t, out.String(), "retrying the hand-off")
	assert.Equal(t, held, h.state.expiresAt)
}

func TestHandOffTimeout_CoversEveryAttempt(t *testing.T) {
	want := openAIPlaceholderSettle + openAIPlaceholderPoll + time.Duration(1+2*reseedSeedAttempts)*openAIPlaceholderExecTimeout
	assert.Equal(t, want, handOffTimeout())
}

// A placeholder read that fails after the rotation is no evidence of the
// new generation either: the route fails closed instead of rotating again.
func TestRunGatewayRefresh_ReadFailureAfterRotationFailsClosed(t *testing.T) {
	stubGatewayOIDC(t, "https://x.actions.githubusercontent.com/t", "req")
	dir := gatewayRecoveryStub(t)
	for _, f := range []string{"base", "readfail"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), nil, 0o644))
	}
	shrinkGatewayRefreshTimers(t)
	calls := countFiveMinuteAssertions(t)
	var expiries []time.Time
	orig := setProviderCredentialExpiryFn
	setProviderCredentialExpiryFn = func(_ context.Context, _, _ string, at time.Time) error {
		expiries = append(expiries, at)
		return nil
	}
	t.Cleanup(func() { setProviderCredentialExpiryFn = orig })
	h := gatewayDueHandle(time.Minute)
	h.refreshState()
	held := h.expiresAt
	var out syncBuffer
	done := make(chan struct{})
	go func() {
		runGatewayRefresh(context.Background(), h, ui.New(&out))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("refresher did not stop after the failed read")
	}
	assert.Equal(t, int32(1), calls.Load(), "no second rotation")
	assert.Equal(t, []time.Time{held}, expiries)
	assert.Contains(t, out.String(), "not observed in the sandbox")
}
