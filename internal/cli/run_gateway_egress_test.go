package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/ui"
)

type renderedGatewayProfile struct {
	ID          string `yaml:"id"`
	Credentials []struct {
		EnvVars   []string `yaml:"env_vars"`
		AuthStyle string   `yaml:"auth_style"`
		Refresh   struct {
			Strategy string `yaml:"strategy"`
		} `yaml:"refresh"`
	} `yaml:"credentials"`
	Endpoints []struct {
		Host                        string `yaml:"host"`
		Port                        int    `yaml:"port"`
		Path                        string `yaml:"path"`
		Protocol                    string `yaml:"protocol"`
		AllowUninspectedCredentials bool   `yaml:"allow_uninspected_credentials"`
		Rules                       []struct {
			Allow struct {
				Method string `yaml:"method"`
				Path   string `yaml:"path"`
			} `yaml:"allow"`
		} `yaml:"rules"`
	} `yaml:"endpoints"`
	Binaries []string `yaml:"binaries"`
}

func TestRenderGatewayProfile(t *testing.T) {
	data, id, err := renderGatewayProfile("Gateway.Example.com")
	require.NoError(t, err)
	assert.Regexp(t, `^fullsend-inference-gateway-[0-9a-f]{12}$`, id)
	assert.NotContains(t, string(data), gatewayProfileHostPlaceholder)

	var p renderedGatewayProfile
	require.NoError(t, yaml.Unmarshal(data, &p))
	assert.Equal(t, id, p.ID)
	require.Len(t, p.Credentials, 1)
	assert.Equal(t, []string{gatewayCredentialKey}, p.Credentials[0].EnvVars)
	assert.Equal(t, "bearer", p.Credentials[0].AuthStyle)
	assert.Equal(t, "external", p.Credentials[0].Refresh.Strategy)
	require.Len(t, p.Endpoints, 1)
	ep := p.Endpoints[0]
	assert.Equal(t, "gateway.example.com", ep.Host, "the host is lower-cased")
	assert.Equal(t, 443, ep.Port)
	assert.Equal(t, "/v1/**", ep.Path, "the credential binding is limited to the model API prefix")
	assert.Equal(t, "rest", ep.Protocol)
	assert.True(t, ep.AllowUninspectedCredentials)
	var rules []string
	for _, r := range ep.Rules {
		rules = append(rules, r.Allow.Method+" "+r.Allow.Path)
	}
	assert.Equal(t, []string{"POST /v1/responses", "POST /v1/messages", "POST /v1/chat/completions"}, rules)
	assert.Equal(t, []string{"**/node"}, p.Binaries)

	// Deterministic per host, case-insensitive, distinct across hosts.
	_, again, err := renderGatewayProfile("gateway.example.com")
	require.NoError(t, err)
	assert.Equal(t, id, again)
	data2, other, err := renderGatewayProfile("other-gateway.example.net")
	require.NoError(t, err)
	assert.NotEqual(t, id, other, "two gateways must not share a profile id")
	assert.Contains(t, string(data2), "host: other-gateway.example.net\n")
	assert.NotContains(t, string(data2), "gateway.example.com")
}

func TestValidateGatewayHost(t *testing.T) {
	for _, ok := range []string{"gateway.example.com", "GW-1.Example.COM", "localhost", "10.0.0.1"} {
		_, err := validateGatewayHost(ok)
		assert.NoError(t, err, ok)
	}
	for _, bad := range []string{
		"", "   ", " gateway.example.com", "https://gateway.example.com",
		"gateway.example.com/v1", "gateway.example.com:8443", "user@gateway.example.com",
		"*.example.com", "gateway.example.com.", ".example.com", "gate..way.com",
		"-gw.example.com", "gw-.example.com", "gw_1.example.com", "gw.example.com?x=1",
		"[::1]", "gw\n.example.com", strings.Repeat("a", 64) + ".com",
	} {
		_, err := validateGatewayHost(bad)
		assert.Error(t, err, "%q", bad)
	}
	_, _, err := renderGatewayProfile("https://gateway.example.com")
	assert.Error(t, err, "the renderer validates the host")
}

func TestUninspectedEndpointRules_GatewayHost(t *testing.T) {
	policy := []byte(`network_policies:
  tunnel:
    endpoints:
    - host: gateway.example.com
      port: 443
  skip:
    endpoints:
    - host: "*.example.com"
      port: 443
      protocol: rest
      tls: skip
  inspected:
    endpoints:
    - host: gateway.example.com
      port: 443
      protocol: rest
  openai:
    endpoints:
    - host: api.openai.com
      port: 443
`)
	rules, err := uninspectedEndpointRules(policy, "gateway.example.com", 443)
	require.NoError(t, err)
	assert.Equal(t, []string{"skip", "tunnel"}, rules)

	rules, err = uninspectedEndpointRules(policy, openAIAPIHost, 443)
	require.NoError(t, err)
	assert.Equal(t, []string{"openai"}, rules, "the OpenAI host is checked independently")
}

func TestCheckGatewayEgressInspected(t *testing.T) {
	t.Run("uninspected rule fails with the gateway host named", func(t *testing.T) {
		stubOpenshell(t, "case \"$1 $2\" in 'policy get') printf -- '---\\nnetwork_policies:\\n  raw:\\n    endpoints:\\n    - host: gateway.example.com\\n      port: 443\\n'; exit 0 ;; esac; exit 1")
		err := checkGatewayEgressInspected(context.Background(), "fs-x", "gateway.example.com")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "rule raw allows gateway.example.com:443 without L7 inspection")
		assert.Contains(t, err.Error(), "inference gateway credential")
		// The OpenAI check on the same policy is unaffected.
		require.NoError(t, checkOpenAIEgressInspected(context.Background(), "fs-x"))
	})
	t.Run("inspected policy passes", func(t *testing.T) {
		stubOpenshell(t, "case \"$1 $2\" in 'policy get') printf -- '---\\nnetwork_policies:\\n  gw:\\n    endpoints:\\n    - host: gateway.example.com\\n      port: 443\\n      protocol: rest\\n'; exit 0 ;; esac; exit 1")
		require.NoError(t, checkGatewayEgressInspected(context.Background(), "fs-x", "gateway.example.com"))
	})
	t.Run("invalid host fails before reading the policy", func(t *testing.T) {
		stubOpenshell(t, "exit 1")
		err := checkGatewayEgressInspected(context.Background(), "fs-x", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})
}

func TestGatewayRunScopedProviderName(t *testing.T) {
	assert.Equal(t, "inference-gateway-0123456789ab", gatewayRunScopedProviderName("fs-tri-0123456789abcdef"))
}

func TestEnsureGatewayProfile_ImportsPerHostID(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	id := gatewayProfileID("gateway.example.com")
	argsLog := profileListingStub(t, "Available Provider Profiles:\n    "+id+"  Fullsend inference gateway  endpoints: 1")
	got, err := ensureGatewayProfile(context.Background(), "gateway.example.com", ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, id, got)
	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 3, "%q", lines)
	assert.Equal(t, "provider profile delete "+id, lines[0])
	assert.Regexp(t, `^provider profile import --file \S+`+id+`-\S+\.yaml$`, lines[1])
}

func TestEnsureGatewayProvider(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "")
	id := gatewayProfileID("gateway.example.com")
	argsLog := profileListingStub(t, "Available Provider Profiles:\n    "+id+"  Fullsend inference gateway  endpoints: 1")
	expires := time.Now().Add(5 * time.Minute).UTC()
	name, gotID, err := ensureGatewayProvider(context.Background(), "gateway.example.com", "fs-tri-0123456789abcdef", "eyJhbGciOiJSUzI1NiJ9.gateway-test-token.sig", expires, ui.New(io.Discard))
	require.NoError(t, err)
	assert.Equal(t, "inference-gateway-0123456789ab", name)
	assert.Equal(t, id, gotID)
	lines := readArgLines(t, argsLog)
	require.Len(t, lines, 5, "profile delete/import/list, provider create, update: %q", lines)
	assert.Equal(t, "provider create --name inference-gateway-0123456789ab --type "+id+" --credential "+gatewayCredentialKey, lines[3])
	assert.True(t, strings.HasPrefix(lines[4], "provider update inference-gateway-0123456789ab --credential "+gatewayCredentialKey+" --credential-expires-at "), lines[4])

	_, _, err = ensureGatewayProvider(context.Background(), "gateway.example.com", "fs-x-1", "a.b.c", expires, ui.New(io.Discard))
	require.Error(t, err, "a token too short to redact is refused")
}

func TestValidateGatewayAssertion(t *testing.T) {
	for _, ok := range []string{
		"eyJhbGciOiJSUzI1NiJ9.eyJpYXQiOjF9.c2ln",
		"eyJhbGciOiJub25lIn0.eyJpYXQiOjF9.",
		"a_b-c.d_e-f.g_h-i",
	} {
		assert.NoError(t, validateGatewayAssertion(ok), ok)
	}
	for name, bad := range map[string]string{
		"empty":            "",
		"two segments":     "eyJhbGciOiJSUzI1NiJ9.eyJpYXQiOjF9",
		"four segments":    "a.b.c.d",
		"empty payload":    "a..c",
		"trailing newline": "eyJh.eyJp.c2ln\n",
		"workflow command": "eyJh.eyJp.c2ln\n::error::pwned",
		"carriage return":  "eyJh.eyJp.c2ln\r::warning::x",
		"padding":          "eyJh.eyJp.c2ln==",
		"space":            "eyJh.eyJp. c2ln",
		"opaque":           "not-a-jwt-at-all",
	} {
		err := validateGatewayAssertion(bad)
		require.Error(t, err, name)
		if bad != "" {
			assert.NotContains(t, err.Error(), bad, "%s: the error never carries the value", name)
		}
	}
}

// TestEnsureGatewayProvider_RejectsNonJWT: a value that is not a compact
// JWT is refused before anything interpolates it — no ::add-mask:: line
// (whose newline would start a second workflow command) and no provider.
func TestEnsureGatewayProvider_RejectsNonJWT(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("GITHUB_ACTIONS", "true")
	argsLog := profileListingStub(t, "")
	malicious := "eyJhbGciOiJSUzI1NiJ9.eyJpYXQiOjF9.c2lnbmF0dXJl\n::error::pwned"
	var err error
	stderr := captureStderr(t, func() {
		_, _, err = ensureGatewayProvider(context.Background(), "gateway.example.com", "fs-x-1", malicious, time.Now().Add(5*time.Minute), ui.New(io.Discard))
	})
	require.ErrorContains(t, err, "not a compact JWT")
	assert.NotContains(t, err.Error(), "pwned")
	assert.NotContains(t, stderr, "::add-mask::")
	assert.NotContains(t, stderr, "::error::")
	assert.NoFileExists(t, argsLog, "no openshell call was made")
}
