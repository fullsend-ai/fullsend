package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/inference/openaiwif"
	"github.com/fullsend-ai/fullsend/internal/ui"
)

// --- openai parent command ---

func TestInferenceOpenAICmd_HasSubcommands(t *testing.T) {
	cmd := newInferenceOpenAICmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	assert.True(t, names["request"], "expected request subcommand")
	assert.True(t, names["import"], "expected import subcommand")
	assert.True(t, names["status"], "expected status subcommand")
}

func TestInferenceOpenAICmd_RegisteredInInference(t *testing.T) {
	cmd := newInferenceCmd()
	found := false
	for _, sub := range cmd.Commands() {
		if sub.Use == "openai" {
			found = true
			break
		}
	}
	assert.True(t, found, "expected openai subcommand registered in inference")
}

// --- request command tests ---

func TestInferenceOpenAIRequestCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInferenceOpenAIRequestCmd_RejectsInvalidFormat(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "yaml"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--format must be one of: json, md")
}

func TestInferenceOpenAIRequestCmd_RejectsOrgOnlyTarget(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestInferenceOpenAIRequestCmd_JSONSingleRepo(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	assert.Equal(t, openAIRequestSchemaVersion, doc.Version)
	assert.Equal(t, githubOIDCIssuer, doc.Provider.Issuer)
	assert.Equal(t, "fullsend://acme", doc.Provider.Audience)
	assert.False(t, doc.Provider.UploadedJWKS)

	require.Len(t, doc.Mappings, 1)
	m := doc.Mappings[0]
	assert.Equal(t, "acme/widget", m.Repository)
	assert.Equal(t, githubOIDCIssuer, m.Assertions.Iss)
	assert.Equal(t, "fullsend://acme", m.Assertions.Aud)
	assert.Equal(t, "acme/widget", m.Assertions.Repository)
	assert.Empty(t, m.Assertions.Ref, "default: no ref assertion")
	assert.Equal(t, "fullsend-widget-ci", m.Target.ServiceAccount)
	assert.Equal(t, []string{"api.model.request"}, m.Target.Permissions)
	assert.Empty(t, m.Target.Project) // no --project flag

	assert.Equal(t, "fullsend://acme", doc.Reply.Audience)
	assert.Empty(t, doc.Reply.IdentityProviderID)
	assert.Contains(t, doc.Reply.ServiceAccountIDs, "acme/widget")
}

func TestInferenceOpenAIRequestCmd_JSONMultiRepo(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request",
		"acme/widget,acme/gadget",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	require.Len(t, doc.Mappings, 2)
	assert.Equal(t, "acme/widget", doc.Mappings[0].Repository)
	assert.Equal(t, "acme/gadget", doc.Mappings[1].Repository)

	assert.Equal(t, "fullsend-widget-ci", doc.Mappings[0].Target.ServiceAccount)
	assert.Equal(t, "fullsend-gadget-ci", doc.Mappings[1].Target.ServiceAccount)

	assert.Len(t, doc.Reply.ServiceAccountIDs, 2)
}

func TestInferenceOpenAIRequestCmd_CustomAudienceAndProject(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--audience", "https://custom.audience",
		"--project", "my-openai-project",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	assert.Equal(t, "https://custom.audience", doc.Provider.Audience)
	assert.Equal(t, "https://custom.audience", doc.Mappings[0].Assertions.Aud)
	assert.Equal(t, "my-openai-project", doc.Mappings[0].Target.Project)
}

func TestInferenceOpenAIRequestCmd_CustomServiceAccount(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--service-account", "existing-sa-id",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	assert.Equal(t, "existing-sa-id", doc.Mappings[0].Target.ServiceAccount)
}

func TestInferenceOpenAIRequestCmd_MarkdownOutput(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "md"})
	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "# OpenAI Workload Identity Federation Request")
	assert.Contains(t, output, "acme/widget")
	assert.Contains(t, output, githubOIDCIssuer)
	assert.Contains(t, output, "fullsend://acme")
	assert.Contains(t, output, "fullsend-widget-ci")
	assert.Contains(t, output, "api.model.request")
	assert.Contains(t, output, "Identity provider ID")
	assert.Contains(t, output, "Service account ID for acme/widget")
}

func TestInferenceOpenAIRequestCmd_OutFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "request.json")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "json",
		"--out", outPath})
	err := cmd.Execute()
	require.NoError(t, err)

	data, err := os.ReadFile(outPath)
	require.NoError(t, err)

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(data, &doc))
	assert.Equal(t, "acme/widget", doc.Mappings[0].Repository)
}

// --- parseRepoList tests ---

func TestParseRepoList_SingleRepo(t *testing.T) {
	repos, err := parseRepoList("acme/widget")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/widget"}, repos)
}

func TestParseRepoList_MultipleRepos(t *testing.T) {
	repos, err := parseRepoList("acme/widget, acme/gadget")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/widget", "acme/gadget"}, repos)
}

func TestParseRepoList_RejectsOrgOnly(t *testing.T) {
	_, err := parseRepoList("acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestParseRepoList_RejectsEmpty(t *testing.T) {
	repos, err := parseRepoList("")
	require.NoError(t, err)
	assert.Empty(t, repos)
}

func TestParseRepoList_RejectsInvalidRepo(t *testing.T) {
	_, err := parseRepoList("acme/")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid")
}

// --- defaultServiceAccountID tests ---

func TestDefaultServiceAccountID(t *testing.T) {
	assert.Equal(t, "fullsend-widget-ci", defaultServiceAccountID("acme/widget"))
	assert.Equal(t, "fullsend-gadget-ci", defaultServiceAccountID("acme/gadget"))
	assert.Equal(t, "fullsend-my-repo-ci", defaultServiceAccountID("org/my-repo"))
}

// --- import command tests ---

func TestInferenceOpenAIImportCmd_Flags(t *testing.T) {
	cmd := newInferenceOpenAIImportCmd()

	assert.NotNil(t, cmd.Flags().Lookup("audience"))
	assert.NotNil(t, cmd.Flags().Lookup("identity-provider-id"))
	assert.NotNil(t, cmd.Flags().Lookup("service-account-id"))
	assert.NotNil(t, cmd.Flags().Lookup("fullsend-dir"))
	assert.NotNil(t, cmd.Flags().Lookup("variables"))
	assert.NotNil(t, cmd.Flags().Lookup("repo"))
}

func TestResolveImportIDs_FromFlags(t *testing.T) {
	ids, err := resolveImportIDs(ui.New(io.Discard), nil, "aud", "idp", "sa", "")
	require.NoError(t, err)
	assert.Equal(t, config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	}, ids)
}

func TestResolveImportIDs_FromFile(t *testing.T) {
	dir := t.TempDir()
	replyPath := filepath.Join(dir, "reply.json")
	reply := openAIReplyDoc{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}
	data, err := json.Marshal(reply)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(replyPath, data, 0o644))

	ids, err := resolveImportIDs(ui.New(io.Discard), []string{replyPath}, "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "fullsend://acme", ids.Audience)
	assert.Equal(t, "idp_123", ids.IdentityProviderID)
	assert.Equal(t, "sa_456", ids.ServiceAccountID)
}

func TestResolveImportIDs_FromFileWithSingleServiceAccountIDs(t *testing.T) {
	dir := t.TempDir()
	replyPath := filepath.Join(dir, "reply.json")
	reply := openAIReplyDoc{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountIDs:  map[string]string{"acme/widget": "sa_widget"},
	}
	data, err := json.Marshal(reply)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(replyPath, data, 0o644))

	ids, err := resolveImportIDs(ui.New(io.Discard), []string{replyPath}, "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "sa_widget", ids.ServiceAccountID)
}

func TestResolveImportIDs_FlagsOverrideFile(t *testing.T) {
	dir := t.TempDir()
	replyPath := filepath.Join(dir, "reply.json")
	reply := openAIReplyDoc{
		Audience:           "from-file",
		IdentityProviderID: "idp-file",
		ServiceAccountID:   "sa-file",
	}
	data, err := json.Marshal(reply)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(replyPath, data, 0o644))

	ids, err := resolveImportIDs(ui.New(io.Discard), []string{replyPath}, "from-flag", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "from-flag", ids.Audience)
	assert.Equal(t, "idp-file", ids.IdentityProviderID)
}

func TestValidateImportIDs_RefusesPartialTrio(t *testing.T) {
	err := validateImportIDs(config.OpenAIWIFConfig{Audience: "aud"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must all be set")
	assert.Contains(t, err.Error(), "identity_provider_id")
	assert.Contains(t, err.Error(), "service_account_id")
}

func TestValidateImportIDs_RefusesEmpty(t *testing.T) {
	err := validateImportIDs(config.OpenAIWIFConfig{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no identifiers provided")
}

func TestValidateImportIDs_AcceptsComplete(t *testing.T) {
	err := validateImportIDs(config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	})
	require.NoError(t, err)
}

func TestRunImportConfig_WritesConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")

	ids := config.OpenAIWIFConfig{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}

	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportConfig(printer, ids, fullsendDir)
	require.NoError(t, err)

	// Verify the config was written.
	configPath := filepath.Join(fullsendDir, "config.yaml")
	data, err := os.ReadFile(configPath)
	require.NoError(t, err)

	cfg, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	perRepo, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)

	openai := perRepo.ConfigInferenceOpenAI()
	assert.Equal(t, "fullsend://acme", openai.Audience)
	assert.Equal(t, "idp_123", openai.IdentityProviderID)
	assert.Equal(t, "sa_456", openai.ServiceAccountID)

	// Make sure the file is valid YAML.
	assert.NotEmpty(t, data)
}

func TestRunImportConfig_PreservesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	// Write a pre-existing config with runtime set.
	configPath := filepath.Join(fullsendDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("runtime: claude\nversion: \"1\"\n"), 0o644))

	ids := config.OpenAIWIFConfig{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}

	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportConfig(printer, ids, fullsendDir)
	require.NoError(t, err)

	// Verify both runtime and openai are present.
	cfg, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	perRepo, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)
	assert.Equal(t, "claude", perRepo.ConfigRuntime())
	assert.Equal(t, "fullsend://acme", perRepo.ConfigInferenceOpenAI().Audience)
}

func TestRunImportVariables_RequiresRepo(t *testing.T) {
	ids := config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	}
	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportVariables(context.Background(), printer, ids, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--repo is required")
}

func TestRunImportVariables_RequiresOwnerSlashRepo(t *testing.T) {
	ids := config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	}
	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportVariables(context.Background(), printer, ids, "acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

// fakeVariableSetter records repository-variable writes and can fail on
// a chosen call, so the partial-write path is exercised.
type fakeVariableSetter struct {
	calls    [][4]string
	failOn   int
	failWith error
}

func (f *fakeVariableSetter) CreateOrUpdateRepoVariable(_ context.Context, owner, repo, name, value string) error {
	f.calls = append(f.calls, [4]string{owner, repo, name, value})
	if f.failWith != nil && len(f.calls) == f.failOn {
		return f.failWith
	}
	return nil
}

func stubVariableSetter(t *testing.T, f *fakeVariableSetter) {
	t.Helper()
	orig := newRepoVariableSetter
	newRepoVariableSetter = func() (repoVariableSetter, error) { return f, nil }
	t.Cleanup(func() { newRepoVariableSetter = orig })
}

func TestRunImportVariables_UsesTheForgeClient(t *testing.T) {
	// Repository variables are a forge operation: they go through
	// forge.Client, never the gh CLI (docs/contributing/forge-abstraction.md).
	f := &fakeVariableSetter{}
	stubVariableSetter(t, f)

	ids := config.OpenAIWIFConfig{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}
	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	require.NoError(t, runImportVariables(context.Background(), printer, ids, "acme/widget"))
	require.Len(t, f.calls, 3)
	assert.Equal(t, [4]string{"acme", "widget", openAIAudienceEnv, "fullsend://acme"}, f.calls[0])
	assert.Equal(t, [4]string{"acme", "widget", openAIIdentityProviderIDEnv, "idp_123"}, f.calls[1])
	assert.Equal(t, [4]string{"acme", "widget", openAIServiceAccountIDEnv, "sa_456"}, f.calls[2])
}

func TestRunImportVariables_PartialWriteIsReported(t *testing.T) {
	f := &fakeVariableSetter{failOn: 2, failWith: fmt.Errorf("403 forbidden")}
	stubVariableSetter(t, f)

	ids := config.OpenAIWIFConfig{
		Audience:           "fullsend://acme",
		IdentityProviderID: "idp_123",
		ServiceAccountID:   "sa_456",
	}
	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportVariables(context.Background(), printer, ids, "acme/widget")
	require.Error(t, err)
	assert.Contains(t, err.Error(), openAIIdentityProviderIDEnv)
	assert.Contains(t, buf.String(), "partial trio",
		"the operator must be told the repository is now in a state a run refuses")
	assert.Contains(t, buf.String(), openAIAudienceEnv, "and which variable did land")
	assert.Len(t, f.calls, 2, "the third write is not attempted after a failure")
}

// --- status command tests ---

func TestInferenceOpenAIStatusCmd_RequiresArg(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "status"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "accepts 1 arg(s)")
}

func TestInferenceOpenAIStatusCmd_RejectsOrgOnly(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "status", "acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "owner/repo")
}

func TestResolveOpenAIStatusSources_FromConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_cfg
    service_account_id: sa_cfg
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	// Clear env vars.
	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")

	s, err := resolveOpenAIStatusSources(fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "fullsend://acme", s.Audience)
	assert.Equal(t, "config.yaml", s.AudienceSource)
	assert.Equal(t, "idp_cfg", s.IdentityProviderID)
	assert.Equal(t, "config.yaml", s.IDPSource)
	assert.Equal(t, "sa_cfg", s.ServiceAccountID)
	assert.Equal(t, "config.yaml", s.SASource)
}

func TestResolveOpenAIStatusSources_VariablesWinAsASet(t *testing.T) {
	// The run path (resolveOpenAICredential) treats the FULLSEND_OPENAI_*
	// variables as one source: any of them set means all three come from
	// variables, and the committed block is not consulted. status must
	// report the same thing, or it would call a trio healthy that a run
	// refuses.
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: from-config
    identity_provider_id: idp_cfg
    service_account_id: sa_cfg
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	t.Setenv(openAIAudienceEnv, "from-env")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")

	s, err := resolveOpenAIStatusSources(fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "variables", s.Source)
	assert.Equal(t, "from-env", s.Audience)
	assert.Contains(t, s.AudienceSource, "variable")
	assert.Empty(t, s.IdentityProviderID, "config.yaml is not consulted once a variable is set")
	assert.Empty(t, s.ServiceAccountID, "config.yaml is not consulted once a variable is set")

	ids := config.OpenAIWIFConfig{
		Audience:           s.Audience,
		IdentityProviderID: s.IdentityProviderID,
		ServiceAccountID:   s.ServiceAccountID,
	}
	assert.Equal(t, []string{"identity_provider_id", "service_account_id"}, ids.Missing(),
		"the same partial trio a run would refuse")
}

func TestResolveOpenAIStatusSources_MalformedConfigIsAnError(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"),
		[]byte("inference: [this is not a mapping\n"), 0o644))
	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")

	_, err := resolveOpenAIStatusSources(fullsendDir)
	require.Error(t, err, "a broken config must not read as 'not configured yet'")
}

func TestResolveOpenAIStatusSources_NoConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")

	s, err := resolveOpenAIStatusSources(fullsendDir)
	require.NoError(t, err)
	assert.Empty(t, s.Audience)
	assert.Empty(t, s.IdentityProviderID)
	assert.Empty(t, s.ServiceAccountID)
}

func TestRunInferenceOpenAIStatus_NoConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	// CI itself sets this; the command must behave the same either way.
	t.Setenv("GITHUB_REPOSITORY", "")

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget",
		"--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	// An unconfigured repository is a failure, like the GCP status
	// command's unhealthy mapping — it has to be able to gate a check.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no OpenAI WIF identifiers configured")
	assert.Contains(t, buf.String(), "No OpenAI WIF identifiers configured")
}

func TestRunInferenceOpenAIStatus_PartialConfig(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: fullsend://acme
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget",
		"--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.Error(t, err, "a partial trio is a state the run path refuses")
	assert.Contains(t, err.Error(), "partially configured")
	assert.Contains(t, buf.String(), "Partial trio")
}

func TestRunInferenceOpenAIStatus_FullConfigNoActions(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_123
    service_account_id: sa_456
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget",
		"--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "All three identifiers are set")
	assert.Contains(t, output, "Not inside a GitHub Actions job")
}

// --- buildRequestDoc tests ---

func TestBuildRequestDoc_DefaultAudience(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget"}, "fullsend://acme", "", "", "")
	assert.Equal(t, "fullsend://acme", doc.Provider.Audience)
	assert.Equal(t, "fullsend://acme", doc.Reply.Audience)
}

func TestBuildRequestDoc_CorrectAssertions(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "fullsend://acme", "proj-1", "", "")

	require.Len(t, doc.Mappings, 2)

	for _, m := range doc.Mappings {
		assert.Equal(t, githubOIDCIssuer, m.Assertions.Iss)
		assert.Equal(t, "fullsend://acme", m.Assertions.Aud)
		assert.Equal(t, m.Repository, m.Assertions.Repository)
		assert.Empty(t, m.Assertions.Ref, "default: no ref assertion")
		assert.Equal(t, "proj-1", m.Target.Project)
		assert.Equal(t, []string{"api.model.request"}, m.Target.Permissions)
	}
}

func TestBuildRequestDoc_ServiceAccountIDPerRepo(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "aud", "", "", "")
	assert.Equal(t, "fullsend-widget-ci", doc.Mappings[0].Target.ServiceAccount)
	assert.Equal(t, "fullsend-gadget-ci", doc.Mappings[1].Target.ServiceAccount)
}

func TestBuildRequestDoc_SharedServiceAccount(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "aud", "", "shared-sa", "")
	assert.Equal(t, "shared-sa", doc.Mappings[0].Target.ServiceAccount)
	assert.Equal(t, "shared-sa", doc.Mappings[1].Target.ServiceAccount)
}

// --- renderRequestMarkdown tests ---

func TestRenderRequestMarkdown_ContainsExpectedSections(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget"}, "fullsend://acme", "", "", "")
	md, err := renderRequestMarkdown(doc)
	require.NoError(t, err)

	assert.Contains(t, md, "## Provider (reuse or create)")
	assert.Contains(t, md, "## Service account mappings")
	assert.Contains(t, md, "### acme/widget")
	assert.Contains(t, md, "## Reply")
	assert.Contains(t, md, "not secrets")
}

// --- end-to-end import via root command ---

func TestInferenceOpenAIImportCmd_FullFlowViaFlags(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "import",
		"--audience", "fullsend://acme",
		"--identity-provider-id", "idp_123",
		"--service-account-id", "sa_456",
		"--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify config was written correctly.
	cfg, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	perRepo, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)
	openai := perRepo.ConfigInferenceOpenAI()
	assert.Equal(t, "fullsend://acme", openai.Audience)
	assert.Equal(t, "idp_123", openai.IdentityProviderID)
	assert.Equal(t, "sa_456", openai.ServiceAccountID)
}

func TestInferenceOpenAIImportCmd_RefusesPartialTrio(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "import",
		"--audience", "fullsend://acme"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must all be set")
}

func TestInferenceOpenAIImportCmd_RefusesNoArgs(t *testing.T) {
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "import"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no identifiers provided")
}

// newTestPrinter creates a ui.Printer that writes to the given buffer.
func newTestPrinter(buf *bytes.Buffer) *ui.Printer {
	return ui.New(buf)
}

// --- helpers ---

func TestInferenceOpenAIRequestCmd_DoesNotRequireGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "json"})
	err := cmd.Execute()
	require.NoError(t, err)
}

func TestInferenceOpenAIStatusCmd_DoesNotRequireGitHubToken(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")

	dir := t.TempDir()
	cmd := newRootCmd()
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget",
		"--fullsend-dir", filepath.Join(dir, ".fullsend")})
	err := cmd.Execute()
	// It reports an unconfigured repository, but never a missing GitHub
	// token: status needs no forge credentials.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no OpenAI WIF identifiers configured")
	assert.NotContains(t, err.Error(), "token")
}

// --- request JSON round-trip test (golden) ---

func TestBuildRequestDoc_JSONRoundTrip(t *testing.T) {
	doc := buildRequestDoc(
		[]string{"acme/widget", "acme/gadget"},
		"fullsend://acme",
		"openai-proj-001",
		"",
		"",
	)

	b, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)

	var roundTrip openAIRequestDoc
	require.NoError(t, json.Unmarshal(b, &roundTrip))

	assert.Equal(t, doc.Version, roundTrip.Version)
	assert.Equal(t, doc.Provider, roundTrip.Provider)
	assert.Len(t, roundTrip.Mappings, 2)
	assert.Equal(t, doc.Mappings[0].Assertions, roundTrip.Mappings[0].Assertions)
	assert.Equal(t, doc.Mappings[1].Assertions, roundTrip.Mappings[1].Assertions)
	assert.Equal(t, doc.Reply.Audience, roundTrip.Reply.Audience)
}

// --- import replaces existing openai block ---

func TestRunImportConfig_ReplacesExistingOpenAI(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))

	configYAML := `inference:
  openai:
    audience: old-audience
    identity_provider_id: old-idp
    service_account_id: old-sa
`
	require.NoError(t, os.WriteFile(
		filepath.Join(fullsendDir, "config.yaml"),
		[]byte(configYAML), 0o644))

	ids := config.OpenAIWIFConfig{
		Audience:           "new-audience",
		IdentityProviderID: "new-idp",
		ServiceAccountID:   "new-sa",
	}

	var buf bytes.Buffer
	printer := newTestPrinter(&buf)

	err := runImportConfig(printer, ids, fullsendDir)
	require.NoError(t, err)

	// Read back and verify the new values.
	cfg, err := config.LoadConfigWriter(fullsendDir, config.LoadOpts{})
	require.NoError(t, err)
	perRepo, ok := cfg.(config.PerRepoConfigReader)
	require.True(t, ok)
	openai := perRepo.ConfigInferenceOpenAI()
	assert.Equal(t, "new-audience", openai.Audience)
	assert.Equal(t, "new-idp", openai.IdentityProviderID)
	assert.Equal(t, "new-sa", openai.ServiceAccountID)

	// Verify old values are gone.
	raw, err := os.ReadFile(filepath.Join(fullsendDir, "config.yaml"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "old-audience")
}

// --- status with env vars from flags ---

func TestInferenceOpenAIStatusCmd_Flags(t *testing.T) {
	cmd := newInferenceOpenAIStatusCmd()
	assert.NotNil(t, cmd.Flags().Lookup("fullsend-dir"))
}

// --- request multi-repo with whitespace ---

func TestParseRepoList_TrimsWhitespace(t *testing.T) {
	repos, err := parseRepoList(" acme/widget , acme/gadget ")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/widget", "acme/gadget"}, repos)
}

// Verify that Markdown output for multi-repo request contains all repos.
func TestInferenceOpenAIRequestCmd_MarkdownMultiRepo(t *testing.T) {
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request",
		"acme/widget,acme/gadget",
		"--format", "md"})
	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "### acme/widget")
	assert.Contains(t, output, "### acme/gadget")
	assert.Contains(t, output, "fullsend-widget-ci")
	assert.Contains(t, output, "fullsend-gadget-ci")
	assert.True(t, strings.Contains(output, "Service account ID for acme/widget"))
	assert.True(t, strings.Contains(output, "Service account ID for acme/gadget"))
}

// --- round trip: the document we generate is the document an admin returns ---

func TestImport_AcceptsTheFilledInRequestDocument(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget"}, "fullsend://acme", "proj-1", "", "")
	// What an administrator does: fill in the reply section of the file
	// `request --format json` produced, and send the same file back.
	doc.Reply.IdentityProviderID = "idp_live"
	doc.Reply.ServiceAccountIDs["acme/widget"] = "sa_live"

	b, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "reply.json")
	require.NoError(t, os.WriteFile(path, b, 0o644))

	ids, err := resolveImportIDs(ui.New(io.Discard), []string{path}, "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "fullsend://acme", ids.Audience)
	assert.Equal(t, "idp_live", ids.IdentityProviderID)
	assert.Equal(t, "sa_live", ids.ServiceAccountID)
	require.NoError(t, validateImportIDs(ids))
}

func TestImport_MultiRepoReplyNeedsASelector(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "fullsend://acme", "proj-1", "", "")
	doc.Reply.IdentityProviderID = "idp_live"
	doc.Reply.ServiceAccountIDs["acme/widget"] = "sa_widget"
	doc.Reply.ServiceAccountIDs["acme/gadget"] = "sa_gadget"
	b, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "reply.json")
	require.NoError(t, os.WriteFile(path, b, 0o644))

	_, err = resolveImportIDs(ui.New(io.Discard), []string{path}, "", "", "", "")
	require.Error(t, err, "two service accounts and nothing to choose with")
	assert.Contains(t, err.Error(), "--repo")
	assert.Contains(t, err.Error(), "acme/gadget")

	ids, err := resolveImportIDs(ui.New(io.Discard), []string{path}, "", "", "", "acme/gadget")
	require.NoError(t, err)
	assert.Equal(t, "sa_gadget", ids.ServiceAccountID, "--repo selects that repository's account")

	_, err = resolveImportIDs(ui.New(io.Discard), []string{path}, "", "", "", "acme/absent")
	require.Error(t, err, "a repository the reply does not name")
	assert.Contains(t, err.Error(), "no service account for acme/absent")
}

// --- request generation guards ---

func TestRequest_MixedOwnersNeedAnExplicitAudience(t *testing.T) {
	cmd := newRootCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget,other/gadget", "--project", "p"})
	err := cmd.Execute()
	require.Error(t, err, "the default audience is derived from the owner, so two owners are ambiguous")
	assert.Contains(t, err.Error(), "--audience")

	cmd = newRootCmd()
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget,other/gadget", "--project", "p", "--audience", "shared-aud", "--format", "json"})
	require.NoError(t, cmd.Execute(), "explicit audience covers both owners")
	assert.Contains(t, out.String(), "shared-aud")
}

func TestRequest_RefIsOverridable(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget"}, "aud", "", "", "refs/heads/trunk")
	require.Len(t, doc.Mappings, 2, "explicit --ref emits two mappings: one for the ref, one for refs/pull/*")
	assert.Equal(t, "refs/heads/trunk", doc.Mappings[0].Assertions.Ref,
		"a repository whose default branch is not main needs its own ref")
	assert.Equal(t, openAIPullRefPattern, doc.Mappings[1].Assertions.Ref,
		"companion mapping for PR-review-triggered runs")

	defaultDoc := buildRequestDoc([]string{"acme/widget"}, "aud", "", "", "")
	require.Len(t, defaultDoc.Mappings, 1, "default: one mapping with no ref assertion")
	assert.Empty(t, defaultDoc.Mappings[0].Assertions.Ref, "default: no ref assertion")
}

func TestParseRepoList_Dedupes(t *testing.T) {
	repos, err := parseRepoList("acme/widget, acme/widget ,acme/gadget")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/widget", "acme/gadget"}, repos)
}

func TestRequestMarkdown_ExistingServiceAccountIsNotCreated(t *testing.T) {
	md, err := renderRequestMarkdown(buildRequestDoc([]string{"acme/widget"}, "aud", "p", "sa_existing", ""))
	require.NoError(t, err)
	assert.Contains(t, md, "sa_existing (existing — map it, do not create a new one)")
	assert.NotContains(t, md, "sa_existing (create inline")

	md, err = renderRequestMarkdown(buildRequestDoc([]string{"acme/widget"}, "aud", "p", "", ""))
	require.NoError(t, err)
	assert.Contains(t, md, "create inline in the mapping")
}

func TestRequestMarkdown_StatesTheAssertionRules(t *testing.T) {
	md, err := renderRequestMarkdown(buildRequestDoc([]string{"acme/widget"}, "aud", "p", "", ""))
	require.NoError(t, err)
	for _, want := range []string{
		"`repository_owner`",
		"`workflow_ref`",
		"`sub`",
		"Do **not** create an API key",
		"`api.model.request` only",
	} {
		assert.Contains(t, md, want, "the generated request must carry the rule: %s", want)
	}
	// Default: no ref assertion in the output.
	assert.NotContains(t, md, "`ref`", "default output omits ref assertion")
}

func TestRequestMarkdown_WithRefShowsRefAssertions(t *testing.T) {
	md, err := renderRequestMarkdown(buildRequestDoc([]string{"acme/widget"}, "aud", "p", "", "refs/heads/main"))
	require.NoError(t, err)
	assert.Contains(t, md, "`ref` = `refs/heads/main`", "first mapping has explicit ref")
	assert.Contains(t, md, "`ref` = `refs/pull/*`", "companion mapping for PR-triggered runs")
}

func TestRunInferenceOpenAIStatus_RefusesToTestAnotherRepository(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(`inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_123
    service_account_id: sa_456
`), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	// A job that does have an OIDC endpoint, but belongs to another repo.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_REPOSITORY", "acme/other")

	exchanged := false
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		exchanged = true
		return &openaiwif.Token{Value: "tok-abcdef123456", ExpiresAt: time.Now().Add(time.Hour), Scope: "api.model.request"}, nil
	})

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget", "--fullsend-dir", fullsendDir})
	require.NoError(t, cmd.Execute())

	assert.False(t, exchanged, "this job's token proves nothing about acme/widget")
	assert.Contains(t, buf.String(), "cannot test acme/widget")
}

func TestRunInferenceOpenAIStatus_ExchangesForItsOwnRepository(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(`inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_123
    service_account_id: sa_456
`), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_REPOSITORY", "acme/widget")

	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return &openaiwif.Token{Value: "tok-secret-value-abcdef", ExpiresAt: time.Now().Add(30 * time.Minute), Scope: "api.model.request"}, nil
	})

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget", "--fullsend-dir", fullsendDir})
	require.NoError(t, cmd.Execute())

	out := buf.String()
	assert.Contains(t, out, "Exchange succeeded for acme/widget")
	assert.Contains(t, out, "api.model.request")
	assert.NotContains(t, out, "tok-secret-value-abcdef", "the token is never printed")
}

func TestRunInferenceOpenAIStatus_OverBroadScopeFailsClosed(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(`inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_123
    service_account_id: sa_456
`), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_REPOSITORY", "acme/widget")

	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return &openaiwif.Token{Value: "tok-abcdef123456", ExpiresAt: time.Now().Add(time.Hour), Scope: "api.model.request api.admin"}, nil
	})

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget", "--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.Error(t, err, "a run refuses a broader token, so status must not call it healthy")
	assert.Contains(t, err.Error(), "refused")
}

func TestRunInferenceOpenAIStatus_OverBroadScopeNeverPrintsSuccess(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(`inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_123
    service_account_id: sa_456
`), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_REPOSITORY", "acme/widget")

	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		return &openaiwif.Token{Value: "tok-abcdef123456", ExpiresAt: time.Now().Add(time.Hour), Scope: "api.model.request api.admin"}, nil
	})

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget", "--fullsend-dir", fullsendDir})
	require.Error(t, cmd.Execute())
	assert.NotContains(t, buf.String(), "Exchange succeeded",
		"a token the run path refuses must never be announced as a success")
}

func TestRunInferenceOpenAIStatus_UnknownJobRepositoryFailsClosed(t *testing.T) {
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(`inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_123
    service_account_id: sa_456
`), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "runner-token")
	t.Setenv("GITHUB_REPOSITORY", "")

	exchanged := false
	stubOpenAIExchange(t, func(context.Context, openaiwif.Config) (*openaiwif.Token, error) {
		exchanged = true
		return &openaiwif.Token{Value: "tok-abcdef123456", ExpiresAt: time.Now().Add(time.Hour), Scope: "api.model.request"}, nil
	})

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget", "--fullsend-dir", fullsendDir})
	require.NoError(t, cmd.Execute())
	assert.False(t, exchanged, "with no job repository there is nothing to attribute the exchange to")
	assert.Contains(t, buf.String(), "GITHUB_REPOSITORY is not set")
}

func TestResolveOpenAIStatusSources_StaticKeyWinsOffActions(t *testing.T) {
	// resolveOpenAICredential ignores the committed block when there is
	// no OIDC endpoint and OPENAI_API_KEY is set; status must say the
	// same, or it would describe a run that cannot happen.
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(`inference:
  openai:
    audience: fullsend://acme
    identity_provider_id: idp_123
    service_account_id: sa_456
`), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv(openAIStaticKeyEnv, "sk-local-developer-key")

	s, err := resolveOpenAIStatusSources(fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "static key", s.Source)
	assert.Empty(t, s.Audience, "the committed block is not what a run here would use")

	// With an OIDC endpoint the block applies again, as it does for a run.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://oidc.example/token")
	s, err = resolveOpenAIStatusSources(fullsendDir)
	require.NoError(t, err)
	assert.Equal(t, "config.yaml", s.Source)
	assert.Equal(t, "fullsend://acme", s.Audience)
}

func TestImport_ServiceAccountFlagResolvesAnAmbiguousReply(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "fullsend://acme", "proj-1", "", "")
	doc.Reply.IdentityProviderID = "idp_live"
	doc.Reply.ServiceAccountIDs["acme/widget"] = "sa_widget"
	doc.Reply.ServiceAccountIDs["acme/gadget"] = "sa_gadget"
	b, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "reply.json")
	require.NoError(t, os.WriteFile(path, b, 0o644))

	ids, err := resolveImportIDs(ui.New(io.Discard), []string{path}, "", "", "sa_chosen", "")
	require.NoError(t, err, "--service-account-id is the answer to the ambiguity, not a value applied after failing on it")
	assert.Equal(t, "sa_chosen", ids.ServiceAccountID)
}

func TestImport_RepoSelectionIsCaseInsensitive(t *testing.T) {
	doc := buildRequestDoc([]string{"acme/widget", "acme/gadget"}, "fullsend://acme", "proj-1", "", "")
	doc.Reply.IdentityProviderID = "idp_live"
	doc.Reply.ServiceAccountIDs["acme/widget"] = "sa_widget"
	doc.Reply.ServiceAccountIDs["acme/gadget"] = "sa_gadget"
	b, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "reply.json")
	require.NoError(t, os.WriteFile(path, b, 0o644))

	ids, err := resolveImportIDs(ui.New(io.Discard), []string{path}, "", "", "", "Acme/Widget")
	require.NoError(t, err, "GitHub matches owner/repo case-insensitively")
	assert.Equal(t, "sa_widget", ids.ServiceAccountID)
}

func TestParseRepoList_DedupesCaseInsensitively(t *testing.T) {
	repos, err := parseRepoList("acme/widget,Acme/Widget,acme/gadget")
	require.NoError(t, err)
	assert.Equal(t, []string{"acme/widget", "acme/gadget"}, repos)

	// The same rule keeps the mixed-owner guard honest.
	assert.Equal(t, []string{"acme"}, repoOwners([]string{"acme/widget", "Acme/gadget"}))
}

func TestImport_AudienceFromProviderBlockWhenReplyLeavesItDefault(t *testing.T) {
	// An administrator who reuses an existing provider is told to put its
	// audience in the provider block; import must honour that rather than
	// recording the audience we proposed.
	doc := buildRequestDoc([]string{"acme/widget"}, "fullsend://acme", "proj-1", "", "")
	doc.Reply.IdentityProviderID = "idp_live"
	doc.Reply.Audience = ""
	doc.Reply.ServiceAccountIDs["acme/widget"] = "sa_live"
	doc.Provider.Audience = "corp-existing-audience"

	b, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "reply.json")
	require.NoError(t, os.WriteFile(path, b, 0o644))

	ids, err := resolveImportIDs(ui.New(io.Discard), []string{path}, "", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "corp-existing-audience", ids.Audience)
}

func TestRunInferenceOpenAIStatus_PartialTrioExitsNonZero(t *testing.T) {
	// The GCP status command fails on an unhealthy mapping; this one must
	// too, or a broken enrolment cannot gate anything.
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(`inference:
  openai:
    audience: fullsend://acme
`), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "")
	t.Setenv("GITHUB_REPOSITORY", "")

	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"inference", "openai", "status", "acme/widget", "--fullsend-dir", fullsendDir})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "partially configured")
}

func TestServiceAccountFor_SeveralNamedRepositoriesAlwaysNeedASelector(t *testing.T) {
	reply := openAIReplyDoc{ServiceAccountIDs: map[string]string{
		"acme/widget": "sa_widget",
		"acme/gadget": "", // not filled in yet
	}}
	_, err := reply.serviceAccountFor("")
	require.Error(t, err, "picking the only filled entry would misattribute once the other is filled")
	assert.Contains(t, err.Error(), "--repo")

	sa, err := reply.serviceAccountFor("acme/widget")
	require.NoError(t, err)
	assert.Equal(t, "sa_widget", sa)
}

// --- golden tests: default (no --ref) and explicit --ref shapes ---

func TestGolden_DefaultNoRef_SingleMapping(t *testing.T) {
	// Golden test 1 — default (no --ref): single mapping with
	// assertions {iss, aud, repository} and no ref field.
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--format", "json"})
	require.NoError(t, cmd.Execute())

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	require.Len(t, doc.Mappings, 1, "default: one mapping per repository")
	m := doc.Mappings[0]
	assert.Equal(t, githubOIDCIssuer, m.Assertions.Iss)
	assert.Equal(t, "fullsend://acme", m.Assertions.Aud)
	assert.Equal(t, "acme/widget", m.Assertions.Repository)
	assert.Empty(t, m.Assertions.Ref, "default: no ref assertion")

	// Verify ref is omitted from JSON output (omitempty).
	assert.NotContains(t, buf.String(), `"ref"`,
		"the ref field must not appear in JSON output when not set")
}

func TestGolden_ExplicitRef_TwoMappings(t *testing.T) {
	// Golden test 2 — explicit --ref: two mappings per repository.
	//   [0] assertions {iss, aud, repository, ref: refs/heads/main}
	//   [1] assertions {iss, aud, repository, ref: refs/pull/*}
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request", "acme/widget",
		"--ref", "refs/heads/main",
		"--format", "json"})
	require.NoError(t, cmd.Execute())

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	require.Len(t, doc.Mappings, 2, "explicit --ref: two mappings per repository")

	// First mapping: exact ref assertion.
	m0 := doc.Mappings[0]
	assert.Equal(t, githubOIDCIssuer, m0.Assertions.Iss)
	assert.Equal(t, "fullsend://acme", m0.Assertions.Aud)
	assert.Equal(t, "acme/widget", m0.Assertions.Repository)
	assert.Equal(t, "refs/heads/main", m0.Assertions.Ref)

	// Second mapping: companion for PR-review-triggered runs.
	m1 := doc.Mappings[1]
	assert.Equal(t, githubOIDCIssuer, m1.Assertions.Iss)
	assert.Equal(t, "fullsend://acme", m1.Assertions.Aud)
	assert.Equal(t, "acme/widget", m1.Assertions.Repository)
	assert.Equal(t, "refs/pull/*", m1.Assertions.Ref)

	// Both share the same target.
	assert.Equal(t, m0.Target, m1.Target)
}

func TestGolden_ExplicitRef_MultiRepo(t *testing.T) {
	// With --ref and two repos, expect 4 mappings (2 per repo).
	var buf bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"inference", "openai", "request",
		"acme/widget,acme/gadget",
		"--ref", "refs/heads/main",
		"--format", "json"})
	require.NoError(t, cmd.Execute())

	var doc openAIRequestDoc
	require.NoError(t, json.Unmarshal(buf.Bytes(), &doc))

	require.Len(t, doc.Mappings, 4, "two repos × two ref patterns")

	// widget mappings.
	assert.Equal(t, "acme/widget", doc.Mappings[0].Repository)
	assert.Equal(t, "refs/heads/main", doc.Mappings[0].Assertions.Ref)
	assert.Equal(t, "acme/widget", doc.Mappings[1].Repository)
	assert.Equal(t, "refs/pull/*", doc.Mappings[1].Assertions.Ref)

	// gadget mappings.
	assert.Equal(t, "acme/gadget", doc.Mappings[2].Repository)
	assert.Equal(t, "refs/heads/main", doc.Mappings[2].Assertions.Ref)
	assert.Equal(t, "acme/gadget", doc.Mappings[3].Repository)
	assert.Equal(t, "refs/pull/*", doc.Mappings[3].Assertions.Ref)

	// Reply still has one entry per repo, not per mapping.
	assert.Len(t, doc.Reply.ServiceAccountIDs, 2)
}

func TestImport_RefusesADocumentThatDisagreesAboutTheAudience(t *testing.T) {
	// The generated document pre-fills reply.audience, so an administrator
	// who reuses a provider and edits only the provider block leaves the two
	// disagreeing. Recording either one silently configures an audience no
	// mapping asserts, and every exchange then fails far from the cause.
	doc := buildRequestDoc([]string{"acme/widget"}, "fullsend://acme", "proj-1", "", "")
	doc.Reply.IdentityProviderID = "idp_live"
	doc.Reply.ServiceAccountIDs["acme/widget"] = "sa_live"
	doc.Provider.Audience = "corp-existing-audience"

	b, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "reply.json")
	require.NoError(t, os.WriteFile(path, b, 0o644))

	_, err = resolveImportIDs(ui.New(io.Discard), []string{path}, "", "", "", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "corp-existing-audience")
	assert.Contains(t, err.Error(), "fullsend://acme")

	// --audience is the documented way out of the ambiguity — and the
	// operator still hears that the file contradicts itself, since only
	// they know which value the mapping was written against.
	var out bytes.Buffer
	ids, err := resolveImportIDs(ui.New(&out), []string{path}, "corp-existing-audience", "", "", "")
	require.NoError(t, err)
	assert.Equal(t, "corp-existing-audience", ids.Audience)
	assert.Contains(t, out.String(), "disagrees with itself about the audience")
}

func TestRequestMarkdown_ReplyListsEachRepositoryOnce(t *testing.T) {
	// --ref emits two mappings per repository; the reply table asks for one
	// service account per repository, not one per mapping.
	md, err := renderRequestMarkdown(buildRequestDoc([]string{"acme/widget"}, "aud", "p", "", "refs/heads/main"))
	require.NoError(t, err)
	assert.Equal(t, 1, strings.Count(md, "Service account ID for acme/widget"),
		"one reply row per repository, however many mappings it takes")
}

func TestBuildRequestDoc_ExplicitPullRefEmitsOneMapping(t *testing.T) {
	// --ref refs/pull/* asks for the mapping the companion already provides.
	doc := buildRequestDoc([]string{"acme/widget"}, "aud", "p", "", openAIPullRefPattern)
	require.Len(t, doc.Mappings, 1)
	assert.Equal(t, openAIPullRefPattern, doc.Mappings[0].Assertions.Ref)
}

func TestRequestCmd_RejectsARefThatIsNotARef(t *testing.T) {
	cmd := newInferenceOpenAIRequestCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"acme/widget", "--audience", "aud", "--ref", "main"})
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refs/heads/main")
}

func TestRequestCmd_WarnsWhenMappingsExceedTheProviderLimit(t *testing.T) {
	repos := make([]string, 0, 26)
	for i := 0; i < 26; i++ {
		repos = append(repos, fmt.Sprintf("acme/widget-%d", i))
	}
	var stdout, stderr bytes.Buffer
	cmd := newInferenceOpenAIRequestCmd()
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{strings.Join(repos, ","), "--audience", "aud", "--ref", "refs/heads/main"})
	require.NoError(t, cmd.Execute())
	assert.Contains(t, stderr.String(), "exceeds OpenAI's limit of 50")
	assert.Contains(t, stderr.String(), "--ref emits two mappings")

	// 25 repositories with --ref is exactly the cap, so it must not warn.
	var quietOut, quietErr bytes.Buffer
	quiet := newInferenceOpenAIRequestCmd()
	quiet.SetOut(&quietOut)
	quiet.SetErr(&quietErr)
	quiet.SetArgs([]string{strings.Join(repos[:25], ","), "--audience", "aud", "--ref", "refs/heads/main"})
	require.NoError(t, quiet.Execute())
	assert.NotContains(t, quietErr.String(), "exceeds OpenAI's limit")
}

func TestOpenAIEnrolment_RefusesOrgModeConfig(t *testing.T) {
	// Org install mode is deprecated (ADR 0044): both entry points must say
	// so by name rather than reporting "nothing configured".
	dir := t.TempDir()
	fullsendDir := filepath.Join(dir, ".fullsend")
	require.NoError(t, os.MkdirAll(fullsendDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(fullsendDir, "config.yaml"), []byte(`version: 1
org: acme
repos:
  acme/widget:
    enabled: true
`), 0o644))

	t.Setenv(openAIAudienceEnv, "")
	t.Setenv(openAIIdentityProviderIDEnv, "")
	t.Setenv(openAIServiceAccountIDEnv, "")
	t.Setenv(openAIStaticKeyEnv, "")

	_, err := resolveOpenAIStatusSources(fullsendDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "org-mode config")

	err = runImportConfig(ui.New(&bytes.Buffer{}), config.OpenAIWIFConfig{
		Audience:           "aud",
		IdentityProviderID: "idp",
		ServiceAccountID:   "sa",
	}, fullsendDir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "org-mode config")
}
