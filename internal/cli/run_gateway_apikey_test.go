package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/actionsoidc"
	"github.com/fullsend-ai/fullsend/internal/runtime"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// gatewayTestAPIKey is a non-secret stand-in for a gateway API key, long
// enough for the redactor to register.
const gatewayTestAPIKey = "gw-test-key-not-a-real-secret-0123456789"

func stubGatewayAPIKey(t *testing.T, key string) {
	t.Helper()
	orig := gatewayAPIKeyEnvFn
	gatewayAPIKeyEnvFn = func() string { return key }
	t.Cleanup(func() { gatewayAPIKeyEnvFn = orig })
}

func TestGatewayBlockApplies_APIKey(t *testing.T) {
	apiKey := config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey}

	// The api-key mode applies on a local run too: no OIDC endpoint.
	stubGatewayOIDC(t, "", "")
	ok, err := gatewayBlockApplies(apiKey)
	require.NoError(t, err)
	assert.True(t, ok, "api-key applies without an OIDC endpoint")

	// It needs no audience, but still needs url.
	_, err = gatewayBlockApplies(config.InferenceGatewayConfig{Auth: config.GatewayAuthAPIKey, Models: map[string]config.InferenceGatewayModel{"m": {API: config.GatewayAPIOpenAIResponses}}})
	require.ErrorContains(t, err, "missing url")
	assert.NotContains(t, err.Error(), "audience")

	// An explicit oidc block still needs the OIDC endpoint and audience.
	ok, err = gatewayBlockApplies(config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud", Auth: config.GatewayAuthOIDC})
	require.NoError(t, err)
	assert.False(t, ok, "oidc on a local run leaves the route to the harness")
	_, err = gatewayBlockApplies(config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthOIDC})
	assert.ErrorContains(t, err, "audience")

	// An unknown mode is refused, not treated as either mode.
	_, err = gatewayBlockApplies(config.InferenceGatewayConfig{URL: "https://gw.example.com", Audience: "aud", Auth: "token"})
	assert.ErrorContains(t, err, "inference.gateway.auth")
}

func TestGatewayAPIKey(t *testing.T) {
	stubGatewayAPIKey(t, "  "+gatewayTestAPIKey+"\t")
	key, err := gatewayAPIKey()
	require.NoError(t, err)
	assert.Equal(t, gatewayTestAPIKey, key)

	stubGatewayAPIKey(t, "")
	_, err = gatewayAPIKey()
	assert.ErrorContains(t, err, gatewayAPIKeyEnv)

	stubGatewayAPIKey(t, "abc\n::error::pwned-key-value")
	_, err = gatewayAPIKey()
	require.ErrorContains(t, err, "control character")
	assert.NotContains(t, err.Error(), "pwned")
}

func TestStartGatewayRoute_APIKey(t *testing.T) {
	// The api-key mode never fetches an assertion.
	stubGatewayOIDC(t, "", "")
	stubGatewayAssertion(t, func(context.Context, actionsoidc.AssertionConfig) (*actionsoidc.Assertion, error) {
		t.Fatal("the api-key mode must not fetch an OIDC assertion")
		return nil, nil
	})
	stubGatewayAPIKey(t, gatewayTestAPIKey)
	var gotHost, gotKey string
	var gotExpiry time.Time
	orig := ensureGatewayAPIKeyProviderFn
	t.Cleanup(func() { ensureGatewayAPIKeyProviderFn = orig })
	ensureGatewayAPIKeyProviderFn = func(_ context.Context, profile gatewayProfile, _ string, key string, expiresAt time.Time, _ *ui.Printer) (string, string, error) {
		gotHost, gotKey, gotExpiry = profile.host, key, expiresAt
		return "inference-gateway-k", "fullsend-inference-gateway-abc", nil
	}
	plan := &gatewayRoutePlan{
		block: config.InferenceGatewayConfig{URL: "https://gw.example.com", Auth: config.GatewayAuthAPIKey},
		host:  "gw.example.com",
		seed:  runtime.PiRuntime{}.GatewayCredentialSeed(),
	}
	var out strings.Builder
	before := time.Now()
	h, err := startGatewayRoute(context.Background(), plan, "fs-key", ui.New(&out))
	require.NoError(t, err)
	assert.Equal(t, "inference-gateway-k", h.name)
	assert.True(t, h.apiKey)
	assert.Equal(t, "gw.example.com", gotHost)
	assert.Equal(t, gatewayTestAPIKey, gotKey)
	assert.False(t, gotExpiry.Before(before.Add(gatewayAPIKeyLifetime)), "the instance is bounded by gatewayAPIKeyLifetime")
	assert.Contains(t, out.String(), "long-lived")
	assert.Contains(t, out.String(), "bounded at 24h0m0s")
	assert.NotContains(t, out.String(), gatewayTestAPIKey, "the key is never printed")

	// A plan sized for a longer run bounds the instance by it.
	plan.runLifetime = 30 * time.Hour
	before = time.Now()
	_, err = startGatewayRoute(context.Background(), plan, "fs-key", ui.New(io.Discard))
	require.NoError(t, err)
	assert.False(t, gotExpiry.Before(before.Add(30*time.Hour)), "the run's own bound is used")
	plan.runLifetime = 0

	// The refresher does nothing in this mode: it returns at once.
	done := make(chan struct{})
	go func() {
		runGatewayRefresh(context.Background(), h, ui.New(io.Discard))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the api-key refresher did not return")
	}

	// A missing key fails the run: no fallback to the oidc mode.
	stubGatewayAPIKey(t, "")
	_, err = startGatewayRoute(context.Background(), plan, "fs-key", ui.New(io.Discard))
	assert.ErrorContains(t, err, gatewayAPIKeyEnv)

	// A provider failure fails the run too.
	stubGatewayAPIKey(t, gatewayTestAPIKey)
	ensureGatewayAPIKeyProviderFn = func(context.Context, gatewayProfile, string, string, time.Time, *ui.Printer) (string, string, error) {
		return "", "", errors.New("gateway down")
	}
	_, err = startGatewayRoute(context.Background(), plan, "fs-key", ui.New(io.Discard))
	assert.ErrorContains(t, err, "gateway down")
}

func TestGatewayAPIKeyIsNotExpandable(t *testing.T) {
	t.Setenv(gatewayAPIKeyEnv, gatewayTestAPIKey)
	assert.True(t, oidcDenyKeys[gatewayAPIKeyEnv])
	assert.True(t, harnessExpansionDenied(gatewayAPIKeyEnv))
	assert.NotContains(t, safeExpandEnv("prefix-${"+gatewayAPIKeyEnv+"}-suffix"), gatewayTestAPIKey, "a harness expansion must not yield the key")
	assert.NotContains(t, shellSafeExpandEnv("prefix-${"+gatewayAPIKeyEnv+"}-suffix"), gatewayTestAPIKey)
	// The runner itself still reads it.
	key, err := gatewayAPIKey()
	require.NoError(t, err)
	assert.Equal(t, gatewayTestAPIKey, key)
}

func TestEnsureGatewayAPIKeyProvider(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "")
	id := gatewayProfileID("gateway.example.com")
	argsLog := profileListingStub(t, "Available Provider Profiles:\n    "+id+"  Fullsend inference gateway  endpoints: 1")
	expires := time.Now().Add(gatewayAPIKeyLifetime).UTC()
	name, gotID, err := ensureGatewayAPIKeyProvider(context.Background(), gatewayProfile{host: "gateway.example.com"}, "fs-tri-0123456789abcdef", gatewayTestAPIKey, expires, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, "inference-gateway-0123456789ab", name)
	assert.Equal(t, id, gotID)
	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 5, "profile delete/import/list, provider create, update: %q", lines)
	assert.Equal(t, "provider create --name inference-gateway-0123456789ab --type "+id+" --credential "+gatewayCredentialKey, lines[3])
	for _, l := range lines {
		assert.NotContains(t, l, gatewayTestAPIKey, "the key never reaches argv")
	}
}

// The ::add-mask:: line is percent-encoded, so the runner decodes it back to
// the exact key even when the key holds a literal "%25" or "%0A".
func TestEnsureGatewayAPIKeyProvider_MaskIsPercentEncoded(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "true")
	id := gatewayProfileID("gateway.example.com")
	profileListingStub(t, "Available Provider Profiles:\n    "+id+"  Fullsend inference gateway  endpoints: 1")
	key := gatewayTestAPIKey + "%25x%0Ay"
	var err error
	stderr := captureStderr(t, func() {
		_, _, err = ensureGatewayAPIKeyProvider(context.Background(), gatewayProfile{host: "gateway.example.com"}, "fs-tri-0123456789abcdef", key, time.Now().Add(time.Hour), ui.New(io.Discard))
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, "::add-mask::"+gatewayTestAPIKey+"%2525x%250Ay\n")
}

func TestEnsureGatewayAPIKeyProvider_RejectsControlCharacters(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "true")
	argsLog := profileListingStub(t, "")
	for _, key := range []string{"", "   ", gatewayTestAPIKey + "\n::error::pwned"} {
		var err error
		stderr := captureStderr(t, func() {
			_, _, err = ensureGatewayAPIKeyProvider(context.Background(), gatewayProfile{host: "gateway.example.com"}, "fs-x-1", key, time.Now().Add(time.Hour), ui.New(io.Discard))
		})
		require.Error(t, err, "%q", key)
		assert.NotContains(t, err.Error(), "pwned")
		assert.NotContains(t, stderr, "::add-mask::")
	}
	assert.NoFileExists(t, argsLog, "no openshell call was made")
}

// The api-key bound covers the run's own agent budget plus slack, and
// never drops below gatewayAPIKeyLifetime.
func TestGatewayAPIKeyLifetimeFor(t *testing.T) {
	assert.Equal(t, gatewayAPIKeyLifetime, gatewayAPIKeyLifetimeFor(1, 30*time.Minute))
	assert.Equal(t, gatewayAPIKeyLifetime, gatewayAPIKeyLifetimeFor(0, 30*time.Minute), "no iterations count as one")
	assert.Equal(t, 10*24*time.Hour+gatewayAPIKeyRunSlack, gatewayAPIKeyLifetimeFor(10, 24*time.Hour))
}
