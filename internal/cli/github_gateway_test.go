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
	"github.com/fullsend-ai/fullsend/internal/runtime"
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
		}, "both empty (and no model flag) to remove the block"},
		{"empty audience beside a url", githubSetupConfig{
			gatewayURL:   "https://gw.example.com",
			changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience"),
		}, "--inference-gateway-audience is empty"},
		{"url alone (audience may be inherited)", githubSetupConfig{
			gatewayURL:   "https://gw.example.com",
			changedFlags: gatewayFlags("inference-gateway-url"),
		}, ""},
		{"models alone (url and audience may be inherited)", githubSetupConfig{
			gatewayModels: []string{"m=openai-responses"},
			changedFlags:  gatewayFlags("inference-gateway-model"),
		}, ""},
		{"empty flags beside a model are not a clear", githubSetupConfig{
			gatewayModels: []string{"m=openai-responses"},
			changedFlags:  gatewayFlags("inference-gateway-url", "inference-gateway-audience", "inference-gateway-model"),
		}, "--inference-gateway-url is empty"},
		{"model id with a control character", githubSetupConfig{
			gatewayModels: []string{"a\x7fb=openai-responses"},
			changedFlags:  gatewayFlags("inference-gateway-model"),
		}, "control characters"},
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

func TestApplySetupFlagsToConfig_GatewayMergesIntoExistingBlock(t *testing.T) {
	t.Parallel()
	existing, err := config.ParsePerRepoConfigWriter([]byte(`version: "1"
inference:
  gateway:
    url: https://gw.example.com
    audience: aud
    models:
      gpt-6-luna:
        api: openai-responses
`))
	require.NoError(t, err)

	// Rotating the audience keeps the url and the model list.
	applySetupFlagsToConfig(githubSetupConfig{
		gatewayAudience: "aud-2",
		changedFlags:    gatewayFlags("inference-gateway-audience"),
	}, existing, nil)
	g := existing.ConfigInferenceGateway()
	assert.Equal(t, "https://gw.example.com", g.URL)
	assert.Equal(t, "aud-2", g.Audience)
	assert.Equal(t, []string{"gpt-6-luna"}, g.ModelIDs())

	// A models file replaces the inline list as one unit.
	applySetupFlagsToConfig(githubSetupConfig{
		gatewayModelsFile: "/tmp/local.json",
		changedFlags:      gatewayFlags("inference-gateway-models-file"),
	}, existing, nil)
	g = existing.ConfigInferenceGateway()
	assert.Empty(t, g.Models)
	assert.Equal(t, gatewayModelsFileRepoPath, g.ModelsFile)
	assert.Equal(t, "aud-2", g.Audience)
	require.NoError(t, existing.Validate())
}

func TestValidateEffectiveGateway(t *testing.T) {
	t.Parallel()
	modelsOnly := githubSetupConfig{
		gatewayModels: []string{"m=openai-responses"},
		changedFlags:  gatewayFlags("inference-gateway-model"),
	}

	// The org preset carries url and audience; the repository adds models.
	base := []byte("version: \"1\"\ninference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n")
	overlay := buildPresetOverlay(modelsOnly, nil)
	require.NotNil(t, overlay)
	effective, err := composeSetupLayers(nil, overlay, base)
	require.NoError(t, err)
	require.NoError(t, validateEffectiveGateway(modelsOnly, effective))
	g := effective.ConfigInferenceGateway()
	assert.Equal(t, "https://gw.example.com", g.URL)
	assert.Equal(t, []string{"m"}, g.ModelIDs())

	// Nothing to inherit: models alone leave the block without url and audience.
	effective, err = composeSetupLayers(nil, overlay, nil)
	require.NoError(t, err)
	err = validateEffectiveGateway(modelsOnly, effective)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would have no url or audience")

	// Without gateway flags or a preset the check does not run.
	require.NoError(t, validateEffectiveGateway(githubSetupConfig{}, effective))

	// An inherited http url is refused even though only models were passed.
	httpBase := []byte("version: \"1\"\ninference:\n  gateway:\n    url: http://gw.example.com\n    audience: aud\n")
	effective, err = composeSetupLayers(nil, overlay, httpBase)
	require.NoError(t, err)
	err = validateEffectiveGateway(modelsOnly, effective)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must use https")

	// A --config preset that carries only half the pair is refused.
	partial := []byte("version: \"1\"\ninference:\n  gateway:\n    url: https://gw.example.com\n")
	effective, err = composeSetupLayers(nil, nil, partial)
	require.NoError(t, err)
	err = validateEffectiveGateway(githubSetupConfig{configPreset: "https://presets.example.com/p.yaml"}, effective)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would have no audience")
}

func TestLoadGatewayModelsFile_SizeBoundary(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, size int) string {
		prefix := `{"providers": {"gateway": {"models": {"m": {"api": "openai-responses", "name": "`
		suffix := `"}}}}}`
		body := prefix + strings.Repeat("x", size-len(prefix)-len(suffix)) + suffix
		require.Len(t, body, size)
		p := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
		return p
	}
	// The name is longer than the extension accepts, but the size check is
	// what is under test: at the limit it reads in full and fails later on
	// content; one byte over is refused for size.
	_, err := loadGatewayModelsFile(githubSetupConfig{gatewayModelsFile: write("at.json", runtime.MaxPiGatewayModelsFileBytes)})
	if err != nil {
		assert.NotContains(t, err.Error(), "exceeds")
	}
	_, err = loadGatewayModelsFile(githubSetupConfig{gatewayModelsFile: write("over.json", runtime.MaxPiGatewayModelsFileBytes+1)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds")
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
	assert.Contains(t, err.Error(), "inference.gateway would have no audience")
}

func gatewayClearSetupConfig() githubSetupConfig {
	return githubSetupConfig{
		target:       "acme/widget",
		mintURL:      "https://mint-test-abc123.run.app",
		agents:       strings.Join(config.PerRepoDefaultRoles(), ","),
		changedFlags: gatewayFlags("mint-url", "inference-gateway-url", "inference-gateway-audience"),
	}
}

func newGatewayClearClient() *forge.FakeClient {
	client := forge.NewFakeClient()
	client.AuthenticatedUser = "acme"
	client.Repos = []forge.Repository{{FullName: "acme/widget", DefaultBranch: "main"}}
	client.TokenScopes = []string{"repo", "workflow"}
	return client
}

func committedTreeFile(client *forge.FakeClient, path string) (forge.TreeFile, bool) {
	for _, batch := range client.CommittedFilesToBranch {
		for _, f := range batch.Files {
			if f.Path == path {
				return f, true
			}
		}
	}
	return forge.TreeFile{}, false
}

func TestRunGitHubSetupPerRepo_GatewayClearRemovesModelsFile(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newGatewayClearClient()
	client.FileContents["acme/widget/.fullsend/config.yaml"] = []byte("inference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n    models_file: " + gatewayModelsFileRepoPath + "\n")
	client.FileContents["acme/widget/"+gatewayModelsFileRepoPath] = []byte(testGatewayModelsFile)

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), gatewayClearSetupConfig())
	require.NoError(t, err)

	f, ok := committedTreeFile(client, gatewayModelsFileRepoPath)
	require.True(t, ok, "the models file should be part of the setup commit")
	assert.True(t, f.Delete)

	cfgFile, ok := committedTreeFile(client, ".fullsend/config.yaml")
	require.True(t, ok)
	parsed, err := config.ParsePerRepoConfig(cfgFile.Content)
	require.NoError(t, err)
	assert.True(t, parsed.ConfigInferenceGateway().IsZero())
}

func TestRunGitHubSetupPerRepo_GatewayClearWithoutModelsFile(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newGatewayClearClient()

	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&discardWriter{}), gatewayClearSetupConfig())
	require.NoError(t, err)
	_, ok := committedTreeFile(client, gatewayModelsFileRepoPath)
	assert.False(t, ok, "nothing to remove when the file is absent")
}

func TestGatewayModelsFileRemoval(t *testing.T) {
	ctx := context.Background()
	key := "acme/widget/" + gatewayModelsFileRepoPath

	client := forge.NewFakeClient()
	client.FileContents[key] = []byte(testGatewayModelsFile)

	// Not a clear: the file stays.
	keep := githubSetupConfig{
		gatewayURL:      "https://gw.example.com",
		gatewayAudience: "aud",
		changedFlags:    gatewayFlags("inference-gateway-url", "inference-gateway-audience"),
	}
	remove, kept, err := gatewayModelsFileRemoval(ctx, client, "acme", "widget", keep, nil)
	require.NoError(t, err)
	assert.False(t, remove)
	assert.False(t, kept)

	// No gateway flags at all.
	remove, _, err = gatewayModelsFileRemoval(ctx, client, "acme", "widget", githubSetupConfig{}, nil)
	require.NoError(t, err)
	assert.False(t, remove)

	clear := githubSetupConfig{changedFlags: gatewayFlags("inference-gateway-url", "inference-gateway-audience")}
	remove, kept, err = gatewayModelsFileRemoval(ctx, client, "acme", "widget", clear, config.NewEmptyPerRepoOverlay())
	require.NoError(t, err)
	assert.True(t, remove)
	assert.False(t, kept)

	// The composed config still points at the file (an inherited block):
	// it stays.
	inherited, err := config.ParsePerRepoConfigWriterLayered([]byte("version: \"1\"\n"),
		[]byte("inference:\n  gateway:\n    url: https://org-gw.example.com\n    audience: org-aud\n    models_file: "+gatewayModelsFileRepoPath+"\n"))
	require.NoError(t, err)
	remove, kept, err = gatewayModelsFileRemoval(ctx, client, "acme", "widget", clear, inherited)
	require.NoError(t, err)
	assert.False(t, remove)
	assert.True(t, kept)

	// An inherited block with its own (other) model list does not keep it.
	other, err := config.ParsePerRepoConfigWriterLayered([]byte("version: \"1\"\n"),
		[]byte("inference:\n  gateway:\n    url: https://org-gw.example.com\n    audience: org-aud\n    models_file: .fullsend/org-models.json\n"))
	require.NoError(t, err)
	remove, kept, err = gatewayModelsFileRemoval(ctx, client, "acme", "widget", clear, other)
	require.NoError(t, err)
	assert.True(t, remove)
	assert.False(t, kept)

	// A read failure other than not-found is reported, not ignored.
	client.GetFileContentErrors = map[string]error{key: assert.AnError}
	_, _, err = gatewayModelsFileRemoval(ctx, client, "acme", "widget", clear, nil)
	require.Error(t, err)
}

func TestRunGitHubSetupPerRepo_GatewayClearKeepsInheritedModelsFile(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newGatewayClearClient()
	client.FileContents["acme/widget/.fullsend/config.yaml"] = []byte("inference:\n  gateway:\n    url: https://gw.example.com\n    audience: aud\n")
	client.FileContents["acme/widget/.fullsend/config.base.yaml"] = []byte("inference:\n  gateway:\n    url: https://org-gw.example.com\n    audience: org-aud\n    models_file: " + gatewayModelsFileRepoPath + "\n")
	client.FileContents["acme/widget/"+gatewayModelsFileRepoPath] = []byte(testGatewayModelsFile)

	var buf strings.Builder
	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&buf), gatewayClearSetupConfig())
	require.NoError(t, err)
	_, ok := committedTreeFile(client, gatewayModelsFileRepoPath)
	assert.False(t, ok, "the inherited block still references the models file")
	assert.Contains(t, buf.String(), "Keeping "+gatewayModelsFileRepoPath)
}

func TestRunGitHubSetupPerRepo_GatewayClearDryRun(t *testing.T) {
	t.Setenv("GH_TOKEN", "test-token")
	client := newGatewayClearClient()
	client.FileContents["acme/widget/"+gatewayModelsFileRepoPath] = []byte(testGatewayModelsFile)
	var buf strings.Builder
	cfg := gatewayClearSetupConfig()
	cfg.dryRun = true
	err := runGitHubSetupPerRepo(context.Background(), client, ui.New(&buf), cfg)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "Would delete: "+gatewayModelsFileRepoPath)
	assert.Empty(t, client.CommittedFilesToBranch)
}
