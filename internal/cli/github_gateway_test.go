package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

const testGatewayModelsFile = `{"providers": {"gateway": {"models": {"gpt-6-luna": {"api": "openai-responses", "contextWindow": 400000}}}}}`

func gatewayFlags(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func TestValidateGatewaySetupFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  githubSetupConfig
		want string // empty means valid
	}{
		{"no flags", githubSetupConfig{}, ""},
		{"complete inline", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAudience: "aud",
			gatewayModels: []string{"gpt-6-luna=openai-responses", " claude = anthropic-messages "},
			changedFlags:  gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-model"),
		}, ""},
		{"url and audience only", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAudience: "aud",
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience"),
		}, ""},
		{"clear with both empty", githubSetupConfig{
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience"),
		}, ""},
		{"single empty flag is not a clear", githubSetupConfig{
			changedFlags: gatewayFlags("inference-gateway-url"),
		}, "pass both empty to remove the block"},
		{"missing audience", githubSetupConfig{
			gatewayURL:   "https://gw.example.com",
			changedFlags: gatewayFlags("inference-gateway-url"),
		}, "missing audience"},
		{"models without url and audience", githubSetupConfig{
			gatewayModels: []string{"m=openai-responses"},
			changedFlags:  gatewayFlags("inference-gateway-model"),
		}, "missing url, audience"},
		{"http url", githubSetupConfig{
			gatewayURL: "http://gw.example.com", gatewayAudience: "aud",
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience"),
		}, "must use https"},
		{"model without api", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAudience: "aud",
			gatewayModels: []string{"m"},
			changedFlags:  gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-model"),
		}, "want id=api"},
		{"model bad api", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAudience: "aud",
			gatewayModels: []string{"m=openai"},
			changedFlags:  gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-model"),
		}, "api must be one of"},
		{"duplicate model", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAudience: "aud",
			gatewayModels: []string{"m=openai-responses", "m=openai-completions"},
			changedFlags:  gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-model"),
		}, "duplicate"},
		{"model and models file", githubSetupConfig{
			gatewayURL: "https://gw.example.com", gatewayAudience: "aud",
			gatewayModels: []string{"m=openai-responses"}, gatewayModelsFile: "x.json",
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-model", "inference-gateway-models-file"),
		}, "mutually exclusive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateGatewaySetupFlags(tc.cfg)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestApplySetupFlagsToConfig_Gateway(t *testing.T) {
	t.Parallel()
	cfg := githubSetupConfig{
		gatewayURL: " https://gw.example.com ", gatewayAudience: "aud",
		gatewayModels: []string{"gpt-6-luna=openai-responses", "claude=anthropic-messages"},
		changedFlags:  gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-model"),
	}
	require.True(t, setupConfigFlagsChanged(cfg), "the gateway flags turn a re-run into a config change")
	o := buildPresetOverlay(cfg, nil)
	require.NotNil(t, o)
	g := o.ConfigInferenceGateway()
	assert.Equal(t, "https://gw.example.com", g.URL)
	assert.Equal(t, "aud", g.Audience)
	assert.Equal(t, []string{"claude", "gpt-6-luna"}, g.ModelIDs())
	assert.Equal(t, config.GatewayAPIAnthropicMessages, g.Models["claude"].API)

	// Round-trip through YAML.
	data, err := o.Marshal()
	require.NoError(t, err)
	parsed, err := config.ParsePerRepoConfigWriter(data)
	require.NoError(t, err)
	require.NoError(t, parsed.Validate())
	assert.Equal(t, g, parsed.ConfigInferenceGateway())

	// Both flags passed empty clear the block.
	clearCfg := githubSetupConfig{changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience")}
	changed := applySetupFlagsToConfig(clearCfg, parsed, nil)
	assert.Equal(t, []string{"inference.gateway"}, changed)
	assert.True(t, parsed.ConfigInferenceGateway().IsZero())

	// A models file sets models_file to the committed path.
	fileCfg := githubSetupConfig{
		gatewayURL: "https://gw.example.com", gatewayAudience: "aud", gatewayModelsFile: "/tmp/local.json",
		changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-models-file"),
	}
	applySetupFlagsToConfig(fileCfg, parsed, nil)
	assert.Equal(t, gatewayModelsFileRepoPath, parsed.ConfigInferenceGateway().ModelsFile)
}

func TestPinnedSetupFlags_Gateway(t *testing.T) {
	t.Parallel()
	base := []byte("version: \"1\"\ninference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n")
	inherited := inheritedSetupReader(base)
	cfg := githubSetupConfig{
		gatewayURL: "https://gw.example.com", gatewayAudience: "other",
		changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience"),
	}
	warnings := pinnedSetupFlags(cfg, inherited, nil)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "inference.gateway.url")

	cfg.gatewayAudience = "aud"
	warnings = pinnedSetupFlags(cfg, inherited, nil)
	require.Len(t, warnings, 2)
	assert.Contains(t, warnings[1], "inference.gateway.audience")
}

func TestLoadGatewayModelsFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	require.NoError(t, os.WriteFile(good, []byte(testGatewayModelsFile), 0o600))
	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte(`{"providers": {"gateway": {"apiKeyEnv": "K", "models": {"m": {}}}}}`), 0o600))

	data, err := loadGatewayModelsFile(githubSetupConfig{})
	require.NoError(t, err)
	assert.Nil(t, data)

	data, err = loadGatewayModelsFile(githubSetupConfig{gatewayModelsFile: good})
	require.NoError(t, err)
	assert.Equal(t, testGatewayModelsFile, string(data))

	_, err = loadGatewayModelsFile(githubSetupConfig{gatewayModelsFile: bad})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "runner-owned")

	_, err = loadGatewayModelsFile(githubSetupConfig{gatewayModelsFile: filepath.Join(dir, "missing.json")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading --inference-gateway-models-file")
}

func TestRunGitHubSetupPerRepo_GatewayModelsFileCommitted(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	local := filepath.Join(t.TempDir(), "gw.json")
	require.NoError(t, os.WriteFile(local, []byte(testGatewayModelsFile), 0o600))

	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	printer := ui.New(&discardWriter{})

	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:            "acme/widget",
		mintURL:           "https://mint-test-abc123.run.app",
		agents:            strings.Join(config.PerRepoDefaultRoles(), ","),
		gatewayURL:        "https://gw.example.com",
		gatewayAudience:   "aud",
		gatewayModelsFile: local,
		changedFlags:      gatewayFlags("mint-url", "inference-gateway-url", "inference-gateway-audience", "inference-gateway-models-file"),
	})
	require.NoError(t, err)

	var modelsFile, cfgYAML []byte
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			switch f.Path {
			case gatewayModelsFileRepoPath:
				modelsFile = f.Content
			case ".fullsend/config.yaml":
				cfgYAML = f.Content
			}
		}
	}
	assert.Equal(t, testGatewayModelsFile, string(modelsFile))
	require.NotEmpty(t, cfgYAML)
	parsed, err := config.ParsePerRepoConfig(cfgYAML)
	require.NoError(t, err)
	g := parsed.ConfigInferenceGateway()
	assert.Equal(t, "https://gw.example.com", g.URL)
	assert.Equal(t, "aud", g.Audience)
	assert.Equal(t, gatewayModelsFileRepoPath, g.ModelsFile)
}

func TestRunGitHubSetupPerRepo_GatewayModelsFileRejected(t *testing.T) {
	local := filepath.Join(t.TempDir(), "gw.json")
	require.NoError(t, os.WriteFile(local, []byte(`{"providers": {"gateway": {"baseUrl": "https://evil.example.com", "models": {"m": {}}}}}`), 0o600))
	client := forge.NewFakeClient()
	printer := ui.New(&discardWriter{})
	err := runGitHubSetupPerRepo(context.Background(), client, printer, githubSetupConfig{
		target:            "acme/widget",
		agents:            strings.Join(config.PerRepoDefaultRoles(), ","),
		gatewayURL:        "https://gw.example.com",
		gatewayAudience:   "aud",
		gatewayModelsFile: local,
		changedFlags:      gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-models-file"),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "baseUrl is runner-owned")
	assert.Empty(t, client.CommittedFilesToBranch)
}

func TestGitHubSetupCmd_GatewayFlagsAllOrNone(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	cmd := newRootCmd()
	cmd.SetArgs([]string{"github", "setup", "acme/widget",
		"--mint-url", "https://mint-test-abc123.run.app",
		"--inference-gateway-url", "https://gw.example.com",
		"--inference-gateway-model", "m=openai-responses",
		"--dry-run"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be set together (missing audience)")
}
