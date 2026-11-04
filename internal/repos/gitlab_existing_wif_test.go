package repos

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// Tests for the GitLab vertex-wif options that point inference at an
// existing WIF provider the installer does not own (#8810).

const (
	testExistingWIF      = "projects/123456789/locations/global/workloadIdentityPools/shared-pool/providers/gitlab-instance"
	testExistingAudience = "//iam.googleapis.com/" + testExistingWIF
	testServiceAccount   = "agent-sa@example-project.iam.gserviceaccount.com"
)

func gitlabExistingWIFManifest(t *testing.T, managed string) *Manifest {
	t.Helper()
	return &Manifest{
		Version:  1,
		Defaults: testInferenceDefaults(),
		GitLab: &PlatformConfig{
			URL:         "https://gitlab.example.com",
			FullsendRef: "v2.5.0",
			Config:      mustManagedConfig(t, managed),
			Repos:       []RepoEntry{{Name: "group/project"}},
		},
	}
}

func TestValidateWIFAudience(t *testing.T) {
	for _, ok := range []string{testExistingAudience, "https:" + testExistingAudience, "fullsend", "custom-audience"} {
		assert.NoError(t, validateWIFAudience(ok), ok)
	}
	for bad, want := range map[string]string{
		"":                           "non-empty",
		" padded":                    "surrounding spaces",
		"line\nbreak":                "single line",
		"tab\there":                  "single line",
		"café":                       "printable ASCII",
		"//iam.googleapis.com/$POOL": "must not contain",
	} {
		err := validateWIFAudience(bad)
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), want, "%q", bad)
	}
}

func TestValidateGCPServiceAccountEmail(t *testing.T) {
	for _, ok := range []string{testServiceAccount, "my-agent@my-project-123.iam.gserviceaccount.com", "agent-sa@project.example.com.iam.gserviceaccount.com"} {
		assert.NoError(t, validateGCPServiceAccountEmail(ok), ok)
	}
	for _, bad := range []string{
		"agent-sa",
		"agent-sa@example-project",
		"agent-sa@example-project.gserviceaccount.com",
		"Agent-SA@example-project.iam.gserviceaccount.com",
		"ab@example-project.iam.gserviceaccount.com",
		`agent-sa@example-project.iam.gserviceaccount.com"`,
		"agent-sa@example-project.iam.gserviceaccount.com\n",
	} {
		err := validateGCPServiceAccountEmail(bad)
		require.Error(t, err, "%q", bad)
		assert.Contains(t, err.Error(), "full service account email")
	}
}

func TestApplyEffectiveInfrastructure_GitLabExistingWIFOptions(t *testing.T) {
	m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+
		"\n  wif_audience: \""+testExistingAudience+"\"\n  service_account: "+testServiceAccount+"\n")
	rc, err := resolvedFor(t, m, "group", "project", "")
	require.NoError(t, err)
	assert.Equal(t, testExistingAudience, rc.DeclaredWIFAudience)
	assert.Equal(t, testServiceAccount, rc.DeclaredServiceAccount)

	project, wif, err := vertexCredentialInputs(ConvergeConfig{}, rc)
	require.NoError(t, err)
	assert.Equal(t, "example-project", project)
	assert.Equal(t, testExistingWIF, wif)
}

func TestApplyEffectiveInfrastructure_ExistingWIFOptionsFromPreset(t *testing.T) {
	m := gitlabExistingWIFManifest(t, "inference:\n  region: us-east5\n")
	preset := "version: \"1\"\ninference:\n  project: example-project\n  wif_provider: " + testExistingWIF +
		"\n  wif_audience: custom-audience\n  service_account: " + testServiceAccount + "\n"
	rc, err := resolvedFor(t, m, "group", "project", preset)
	require.NoError(t, err)
	assert.Equal(t, "custom-audience", rc.DeclaredWIFAudience)
	assert.Equal(t, testServiceAccount, rc.DeclaredServiceAccount)

	// A preset value bypasses managed-layer validation, so the format is
	// checked during resolution too.
	bad := strings.Replace(preset, testServiceAccount, "agent-sa@example-project", 1)
	_, err = resolvedFor(t, m, "group", "project", bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "full service account email")
}

func TestApplyEffectiveInfrastructure_ExistingWIFOptionsInertOutsideGitLabVertexWIF(t *testing.T) {
	const options = "inference:\n  wif_audience: custom-audience\n  service_account: " + testServiceAccount + "\n"
	t.Run("GitHub inherits from defaults.config", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, options)
		require.NoError(t, m.Validate())
		rc, err := resolvedFor(t, m, "acme", "api", "")
		require.NoError(t, err)
		assert.Empty(t, rc.DeclaredWIFAudience)
		assert.Empty(t, rc.DeclaredServiceAccount)
		_, _, err = vertexCredentialInputs(ConvergeConfig{}, rc)
		assert.NoError(t, err, "no explicit-provider requirement where the options are inert")
	})
	for _, auth := range []string{InferenceAuthOpenAIAPIKey, InferenceAuthOpenAIWIF} {
		t.Run(auth, func(t *testing.T) {
			m := gitlabExistingWIFManifest(t, options)
			m.Defaults.Inference.Auth = auth
			rc, err := resolvedFor(t, m, "group", "project", "")
			require.NoError(t, err)
			assert.Empty(t, rc.DeclaredWIFAudience)
			assert.Empty(t, rc.DeclaredServiceAccount)
		})
	}
	t.Run("format is still checked", func(t *testing.T) {
		m := newConvergeManifest("acme/api")
		m.Defaults.Config = mustManagedConfig(t, "inference:\n  service_account: agent-sa\n")
		require.ErrorContains(t, m.Validate(), "full service account email")
		_, err := resolvedFor(t, m, "acme", "api", "")
		require.ErrorContains(t, err, "full service account email")
	})
}

func TestManifestValidate_ExistingWIFOptionsRejectedInGitHubLayers(t *testing.T) {
	for _, key := range []string{"wif_audience: custom-audience", "service_account: " + testServiceAccount} {
		raw := "inference:\n  " + key + "\n"
		name := strings.SplitN(key, ":", 2)[0]

		m := newConvergeManifest("acme/api")
		m.GitHub.Config = mustManagedConfig(t, raw)
		err := m.Validate()
		require.Error(t, err, key)
		assert.Contains(t, err.Error(), "github.config.inference."+name+" applies only to GitLab repositories")
		assert.Contains(t, err.Error(), "gitlab.config")

		m = newConvergeManifest("acme/api")
		m.GitHub.Repos[0].Config = mustManagedConfig(t, raw)
		err = m.Validate()
		require.Error(t, err, key)
		assert.Contains(t, err.Error(), "github.repos[acme/api].config.inference."+name)
	}

	// The same keys are accepted on the GitLab side.
	m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+
		"\n  wif_audience: custom-audience\n  service_account: "+testServiceAccount+"\n")
	m.GitLab.Repos[0].Config = mustManagedConfig(t, "inference:\n  service_account: "+testServiceAccount+"\n")
	require.NoError(t, m.Validate())
}

// A GitLab repository whose inference.auth is not vertex-wif installs
// without the variable or a rendered audience even when the options are
// inherited.
func TestConverge_GitLabExistingWIFOptionsInertForOpenAI(t *testing.T) {
	fc := newFakeClientForBatch("group/project")
	m := gitlabExistingWIFManifest(t, "inference:\n  wif_audience: custom-audience\n  service_account: "+testServiceAccount+"\n")
	m.Defaults.Inference.Auth = InferenceAuthOpenAIAPIKey
	spy := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true, OpenAIAPIKey: "sk-test-key-0123456789"},
		newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	_, written := fc.VariableValues["group/project/"+forge.VarGCPServiceAccount]
	assert.False(t, written)
	assert.Contains(t, gitlabAgentTemplate(t, spy.files), "\n      aud: \"fullsend\"\n")
}

func TestVertexCredentialInputs_ExistingWIFOptionsRequireExplicitProvider(t *testing.T) {
	base := ResolvedConfig{
		Owner: "group", Repo: "project", Forge: ForgeGitLab, InferenceAuth: InferenceAuthVertexWIF,
		DeclaredInferenceProject: "example-project",
	}
	for name, rc := range map[string]ResolvedConfig{
		"audience":        func() ResolvedConfig { r := base; r.DeclaredWIFAudience = testExistingAudience; return r }(),
		"service account": func() ResolvedConfig { r := base; r.DeclaredServiceAccount = testServiceAccount; return r }(),
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := vertexCredentialInputs(ConvergeConfig{}, rc)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "require an explicitly supplied WIF provider")
			assert.Contains(t, err.Error(), "gitlab-oidc")

			// --vertex-wif-provider satisfies the requirement.
			_, wif, err := vertexCredentialInputs(ConvergeConfig{WIFProvider: testExistingWIF}, rc)
			require.NoError(t, err)
			assert.Equal(t, testExistingWIF, wif)
		})
	}

	// Without the options the derived provider stays allowed.
	_, wif, err := vertexCredentialInputs(ConvergeConfig{}, base)
	require.NoError(t, err)
	assert.Empty(t, wif)
}

func TestConverge_GitLabExistingWIFOptionsWithDerivedProviderFails(t *testing.T) {
	fc := newFakeClientForBatch("group/project")
	m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  service_account: "+testServiceAccount+"\n")
	cfg := ConvergeConfig{
		Manifest:             m,
		MaxConcurrency:       4,
		Direct:               true,
		ResolveProjectNumber: func(context.Context, string) (string, error) { return "777", nil },
	}
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Len(t, result.Failed(), 1)
	assert.Contains(t, result.Failed()[0].Error.Error(), "derived per-project gitlab-oidc provider")
	assert.Empty(t, fc.CreatedSecrets, "nothing is written")
	assert.Empty(t, fc.Variables, "nothing is written")
}

func TestValidateManagedMintAndWIF_ExistingWIFOptionFormats(t *testing.T) {
	for raw, want := range map[string]string{
		"inference:\n  service_account: agent-sa\n":         "full service account email",
		"inference:\n  wif_audience: \"aud-$CI_PROJECT\"\n": "must not contain",
	} {
		err := validateManagedBlock("gitlab.config", mustManagedConfig(t, raw))
		require.Error(t, err, raw)
		assert.Contains(t, err.Error(), want)
	}
	require.NoError(t, validateManagedBlock("gitlab.config", mustManagedConfig(t,
		"inference:\n  wif_audience: custom-audience\n  service_account: "+testServiceAccount+"\n")))
}

func gitlabAgentTemplate(t *testing.T, files []forge.TreeFile) string {
	t.Helper()
	for _, f := range files {
		if f.Path == fullsendAgentTemplatePath {
			return string(f.Content)
		}
	}
	t.Fatalf("%s not among the files", fullsendAgentTemplatePath)
	return ""
}

func TestBuildScaffoldFiles_GitLabAgentAudience(t *testing.T) {
	cfg := InstallConfig{Owner: "group", Repo: "project", Forge: ForgeGitLab, Roles: []string{"triage"}}
	defaultFiles, err := BuildScaffoldFiles(cfg)
	require.NoError(t, err)
	shipped, err := scaffold.GitLabPerRepoFile(fullsendAgentTemplatePath)
	require.NoError(t, err)
	wantDefault := strings.ReplaceAll(string(shipped), "__AGENT_RUNNER_TAGS__", scaffold.FormatRunnerTags(nil))
	wantDefault = scaffold.RenderRunnerImageTag(wantDefault, scaffold.ResolveRunnerImageTag("", ""))
	assert.Equal(t, wantDefault, gitlabAgentTemplate(t, defaultFiles), "an unset audience keeps the shipped template byte for byte")

	cfg.InferenceWIFAudience = testExistingAudience
	customFiles, err := BuildScaffoldFiles(cfg)
	require.NoError(t, err)
	custom := gitlabAgentTemplate(t, customFiles)
	assert.Contains(t, custom, "aud: \""+testExistingAudience+"\"")
	assert.NotContains(t, custom, `aud: "fullsend"`)

	// Pinned (prebuilt) templates are rendered too.
	cfg.PrebuiltScaffoldFiles = scaffold.InstallFiles{{Path: fullsendAgentTemplatePath, Content: []byte(wantDefault), Mode: "100644"}}
	prebuilt, err := BuildScaffoldFiles(cfg)
	require.NoError(t, err)
	assert.Equal(t, custom, gitlabAgentTemplate(t, prebuilt))

	// A full set without the agent template cannot carry the audience.
	cfg.PrebuiltScaffoldFiles = scaffold.InstallFiles{{Path: gitlabAgentJobScriptPath, Content: []byte("#!/bin/sh\n"), Mode: "100755"}}
	_, err = BuildScaffoldFiles(cfg)
	require.ErrorContains(t, err, "has no "+fullsendAgentTemplatePath+" to render it into")

	// A template without the shipped audience line cannot be rendered.
	cfg.PrebuiltScaffoldFiles = scaffold.InstallFiles{{Path: fullsendAgentTemplatePath, Content: []byte("job: {}\n"), Mode: "100644"}}
	_, err = BuildScaffoldFiles(cfg)
	require.ErrorContains(t, err, "applying GitLab existing WIF provider options")
}

func TestManagedVarsForForge_GitLabServiceAccount(t *testing.T) {
	vars, err := managedVarsForForge(InstallConfig{Forge: ForgeGitLab, InferenceServiceAccount: testServiceAccount}, "")
	require.NoError(t, err)
	assert.Contains(t, vars, ManagedVar{Name: forge.VarGCPServiceAccount, Value: testServiceAccount})

	vars, err = managedVarsForForge(InstallConfig{Forge: ForgeGitLab}, "")
	require.NoError(t, err)
	for _, v := range vars {
		assert.NotEqual(t, forge.VarGCPServiceAccount, v.Name, "no variable without a configured service account")
	}
}

func TestInstall_RejectsInvalidExistingWIFOptions(t *testing.T) {
	_, err := Install(context.Background(), InstallConfig{
		Owner: "group", Repo: "project", Forge: ForgeGitLab, InferenceServiceAccount: "agent-sa",
	}, forge.NewFakeClient(), (&fakeScaffoldCommit{}).fn(), nil)
	require.ErrorContains(t, err, "full service account email")
}

func TestConverge_GitLabExistingWIFProviderInstall(t *testing.T) {
	fc := newFakeClientForBatch("group/project")
	m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+
		"\n  wif_audience: \""+testExistingAudience+"\"\n  service_account: "+testServiceAccount+"\n")
	cfg := ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true}
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())

	wif, ok := createdSecretValue(fc, "group/project", forge.SecretGCPWIFProvider)
	require.True(t, ok)
	assert.Equal(t, testExistingWIF, wif, "the explicit provider is used verbatim")
	assert.Equal(t, testServiceAccount, fc.VariableValues["group/project/"+forge.VarGCPServiceAccount])
	agent := gitlabAgentTemplate(t, spy.files)
	assert.Contains(t, agent, "aud: \""+testExistingAudience+"\"")
	assert.NotContains(t, agent, `aud: "fullsend"`)
}

func TestConverge_GitLabDefaultInstallKeepsDefaultAudience(t *testing.T) {
	fc := newFakeClientForBatch("group/project")
	m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+"\n")
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
		newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Contains(t, gitlabAgentTemplate(t, spy.files), "\n      aud: \"fullsend\"\n")
	_, written := fc.VariableValues["group/project/"+forge.VarGCPServiceAccount]
	assert.False(t, written, "no service account variable without the option")
}

func TestProbeComponents_GitLabServiceAccountDrift(t *testing.T) {
	expected := map[string]string{forge.VarGCPServiceAccount: testServiceAccount}
	component := func(fc *forge.FakeClient) ComponentStatus {
		t.Helper()
		components, err := ProbeComponents(context.Background(), fc, "group", "project", ForgeGitLab, GitLabForgeConfig(), expected)
		require.NoError(t, err)
		for _, c := range components {
			if c.Name == "var:"+forge.VarGCPServiceAccount {
				return c
			}
		}
		t.Fatalf("no %s component", forge.VarGCPServiceAccount)
		return ComponentStatus{}
	}

	missing := component(forge.NewFakeClient())
	assert.False(t, missing.Present)
	assert.False(t, missing.Match, "a configured but missing variable is drift")
	assert.Equal(t, testServiceAccount, missing.Expected)

	fc := forge.NewFakeClient()
	fc.VariableValues["group/project/"+forge.VarGCPServiceAccount] = "other-sa@example-project.iam.gserviceaccount.com"
	drifted := component(fc)
	assert.True(t, drifted.Present)
	assert.False(t, drifted.Match)

	fc.VariableValues["group/project/"+forge.VarGCPServiceAccount] = testServiceAccount
	assert.True(t, component(fc).Match)
}

func TestCheckOrphanVars_GitLabServiceAccountNotFlagged(t *testing.T) {
	fc := forge.NewFakeClient()
	fc.VariableValues["group/project/"+forge.VarGCPServiceAccount] = testServiceAccount
	orphans, err := CheckOrphanVars(context.Background(), fc, "group", "project", InstallConfig{Forge: ForgeGitLab}, "")
	require.NoError(t, err)
	for _, o := range orphans {
		assert.NotEqual(t, forge.VarGCPServiceAccount, o.Name)
	}
}

func TestUninstall_GitLabRemovesServiceAccountVariable(t *testing.T) {
	assert.True(t, slices.Contains(gitlabUninstallVars, forge.VarGCPServiceAccount))
	client := newInstalledFakeGitLabClient("group/project")
	results, err := Uninstall(context.Background(), UninstallConfig{
		Manifest:       testGitLabManifest("group/project"),
		Repos:          []string{"group/project"},
		Direct:         true,
		MaxConcurrency: 4,
	}, newTestClientFactory(client), uninstallCommitFn(client), nil)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.True(t, results[0].Success, "error: %v", results[0].Error)
	deleted := false
	for _, v := range client.DeletedVariables {
		if v.Name == forge.VarGCPServiceAccount {
			deleted = true
		}
	}
	assert.True(t, deleted, "uninstall deletes %s", forge.VarGCPServiceAccount)
}

func TestCheckGitLabTriggerDispatchGate_AcceptsRenderedAgentAudience(t *testing.T) {
	e := readyTriggerDispatchEnv(t)
	key := trigOwner + "/" + trigRepo + "/" + fullsendAgentTemplatePath
	shipped := e.state.FileContents[key]

	rendered, err := scaffold.RenderGitLabAgentAudience(shipped, testExistingAudience)
	require.NoError(t, err)
	e.state.FileContents[key] = rendered
	gate := e.dispatchGate()
	assert.True(t, gate.Ready, "deferred: %v", gate.Deferred)

	// An audience that would fail inference.wif_audience validation, such as
	// a GitLab variable expansion, is not accepted.
	e.state.FileContents[key] = []byte(strings.Replace(string(shipped), `aud: "fullsend"`, `aud: "$CI_PROJECT_URL"`, 1))
	gate = e.dispatchGate()
	assert.False(t, gate.Ready)
}

func TestGitLabCommittedAgentAudience(t *testing.T) {
	shipped, err := scaffold.GitLabPerRepoFile(fullsendAgentTemplatePath)
	require.NoError(t, err)
	aud, ok := gitlabCommittedAgentAudience(shipped, fullsendAgentJobName)
	require.True(t, ok)
	assert.Equal(t, "fullsend", aud)

	_, ok = gitlabCommittedAgentAudience([]byte("---\nother: {}\n"), fullsendAgentJobName)
	assert.False(t, ok, "missing job")
	_, ok = gitlabCommittedAgentAudience([]byte("---\n\"fullsend $[[ inputs.stage | expand_vars ]] agent\":\n  script: [true]\n"), fullsendAgentJobName)
	assert.False(t, ok, "missing id_tokens")
}

// installedGitLabExistingWIF is an installed GitLab repository whose
// committed scaffold matches the default install, with its Vertex secrets
// present, under a manifest that declares the existing-provider options.
func installedGitLabExistingWIF(t *testing.T) (*forge.FakeClient, *Manifest) {
	t.Helper()
	fc := newFakeClientForBatch("group/project")
	populateGitLabInstalled(fc, "group", "project")
	populateGitLabTypedWrapper(t, fc, "group", "project")
	fc.Secrets["group/project/"+forge.SecretGCPProjectID] = true
	fc.Secrets["group/project/"+forge.SecretGCPWIFProvider] = true
	m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+
		"\n  wif_audience: \""+testExistingAudience+"\"\n  service_account: "+testServiceAccount+"\n")
	m.GitLab.Repos[0].Name = "group/project"
	return fc, m
}

func TestStatus_GitLabExistingWIFDrift(t *testing.T) {
	fc, m := installedGitLabExistingWIF(t)

	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	require.Empty(t, status.Repos[0].Error)
	fields := driftFields(status.Repos[0].Drifts)
	assert.Contains(t, fields, forge.VarGCPServiceAccount, "a configured but missing variable is drift")
	assert.Contains(t, fields, fullsendAgentTemplatePath, "an agent job still requesting aud fullsend is drift")

	// A different service account is value drift.
	fc.VariableValues["group/project/"+forge.VarGCPServiceAccount] = "other-sa@example-project.iam.gserviceaccount.com"
	status, err = Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	assert.Contains(t, driftFields(status.Repos[0].Drifts), forge.VarGCPServiceAccount)

	// The configured variable and rendered audience clear both drifts.
	fc.VariableValues["group/project/"+forge.VarGCPServiceAccount] = testServiceAccount
	key := "group/project/" + fullsendAgentTemplatePath
	rendered, err := scaffold.RenderGitLabAgentAudience(fc.FileContents[key], testExistingAudience)
	require.NoError(t, err)
	fc.FileContents[key] = rendered
	status, err = Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	fields = driftFields(status.Repos[0].Drifts)
	assert.NotContains(t, fields, forge.VarGCPServiceAccount)
	assert.NotContains(t, fields, fullsendAgentTemplatePath)
}

// An already-installed repository converges to the configured options: the
// service account variable is added and the agent template is rendered.
func TestConverge_GitLabExistingWIFOptionsOnInstalledRepo(t *testing.T) {
	fc, m := installedGitLabExistingWIF(t)
	spy := &spyScaffoldCommit{}

	result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
		newTestClientFactory(fc), spy.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	assert.Equal(t, testServiceAccount, fc.VariableValues["group/project/"+forge.VarGCPServiceAccount])
	assert.Contains(t, gitlabAgentTemplate(t, spy.files), "aud: \""+testExistingAudience+"\"")
}

// installedGitLabWithLeftoverServiceAccount is an installed GitLab
// repository that still carries FULLSEND_GCP_SERVICE_ACCOUNT under a
// manifest that no longer declares inference.service_account.
func installedGitLabWithLeftoverServiceAccount(t *testing.T) (*forge.FakeClient, *Manifest) {
	t.Helper()
	fc, _ := installedGitLabExistingWIF(t)
	fc.VariableValues["group/project/"+forge.VarGCPServiceAccount] = testServiceAccount
	m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+"\n")
	m.GitLab.Repos[0].Name = "group/project"
	return fc, m
}

func TestObsoleteInferenceVariables_ServiceAccount(t *testing.T) {
	names := func(cfg ResolvedConfig) map[string]string {
		out := map[string]string{}
		for _, v := range obsoleteInferenceVariables(cfg) {
			out[v.name] = v.reason
		}
		return out
	}
	gitlab := ResolvedConfig{Forge: ForgeGitLab, InferenceAuth: InferenceAuthVertexWIF}
	assert.Equal(t, map[string]string{forge.VarGCPServiceAccount: "inference.service_account is not declared"}, names(gitlab))

	gitlab.DeclaredServiceAccount = testServiceAccount
	assert.Empty(t, names(gitlab), "a declared service account keeps the variable")

	openai := ResolvedConfig{Forge: ForgeGitLab, InferenceAuth: InferenceAuthOpenAIAPIKey}
	assert.Equal(t, map[string]string{
		forge.VarGCPRegion:         "inference.auth is openai-api-key",
		forge.VarGCPServiceAccount: "inference.auth is openai-api-key",
	}, names(openai))

	assert.Empty(t, names(ResolvedConfig{Forge: ForgeGitHub, InferenceAuth: InferenceAuthVertexWIF}),
		"GitHub repositories never get the variable, so it is not Fullsend-owned there")
}

func TestStatus_GitLabLeftoverServiceAccountIsObsolete(t *testing.T) {
	fc, m := installedGitLabWithLeftoverServiceAccount(t)
	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	require.Empty(t, status.Repos[0].Error)
	var found *Drift
	for i, d := range status.Repos[0].Drifts {
		if d.Field == forge.VarGCPServiceAccount {
			found = &status.Repos[0].Drifts[i]
		}
	}
	require.NotNil(t, found, "drifts: %+v", status.Repos[0].Drifts)
	assert.Equal(t, "absent", found.Expected)
	assert.Equal(t, "obsolete variable (inference.service_account is not declared)", found.Actual)

	delete(fc.VariableValues, "group/project/"+forge.VarGCPServiceAccount)
	status, err = Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	assert.NotContains(t, driftFields(status.Repos[0].Drifts), forge.VarGCPServiceAccount)
}

func TestConverge_GitLabDeletesLeftoverServiceAccount(t *testing.T) {
	fc, m := installedGitLabWithLeftoverServiceAccount(t)

	dry, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true, DryRun: true},
		newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, dry.Failed(), "results: %+v", dry.Results)
	_, kept := fc.VariableValues["group/project/"+forge.VarGCPServiceAccount]
	assert.True(t, kept, "a dry run deletes nothing")

	// Cleanup waits until this run's scaffold changes are on the default
	// branch, as for the other obsolete inference credentials.
	result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
		newTestClientFactory(fc), landingScaffoldCommit(fc, "group", "project"), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "results: %+v", result.Results)
	_, kept = fc.VariableValues["group/project/"+forge.VarGCPServiceAccount]
	assert.False(t, kept, "install deletes the undeclared variable")
	var detail string
	for _, a := range result.Results[0].Actions {
		if a.Component == "var:"+forge.VarGCPServiceAccount {
			detail = a.Detail
		}
	}
	assert.Equal(t, "deleted obsolete "+forge.VarGCPServiceAccount+" (inference.service_account is not declared)", detail)
}

// legacyAgentJobScript is a run-agent-job.sh from before service-account
// impersonation: it never reads FULLSEND_GCP_SERVICE_ACCOUNT.
const legacyAgentJobScript = "#!/usr/bin/env bash\n# pre-impersonation job script\nexport GOOGLE_APPLICATION_CREDENTIALS=/tmp/creds.json\n"

func TestRequireServiceAccountScriptSupport(t *testing.T) {
	current, err := scaffold.GitLabPerRepoFile(gitlabAgentJobScriptPath)
	require.NoError(t, err)
	supported := []forge.TreeFile{{Path: gitlabAgentJobScriptPath, Content: current}}
	legacy := []forge.TreeFile{{Path: gitlabAgentJobScriptPath, Content: []byte(legacyAgentJobScript)}}

	require.NoError(t, requireServiceAccountScriptSupport(supported, testServiceAccount))
	require.NoError(t, requireServiceAccountScriptSupport(legacy, ""), "nothing to check without a service account")
	require.NoError(t, requireServiceAccountScriptSupport(nil, testServiceAccount), "a set without the script passes")
	err = requireServiceAccountScriptSupport(legacy, testServiceAccount)
	require.Error(t, err)
	assert.Equal(t, "inference.service_account needs a gitlab.fullsend_ref that includes service-account impersonation support (the pinned run-agent-job.sh does not read FULLSEND_GCP_SERVICE_ACCOUNT)", err.Error())
}

func TestBuildScaffoldFiles_GitLabServiceAccountNeedsScriptSupport(t *testing.T) {
	cfg := InstallConfig{
		Owner: "group", Repo: "project", Forge: ForgeGitLab, Roles: []string{"triage"},
		InferenceServiceAccount: testServiceAccount,
	}
	_, err := BuildScaffoldFiles(cfg)
	require.NoError(t, err, "the embedded script reads the variable")

	cfg.PrebuiltScaffoldFiles = scaffold.InstallFiles{{Path: gitlabAgentJobScriptPath, Content: []byte(legacyAgentJobScript), Mode: "100755"}}
	_, err = BuildScaffoldFiles(cfg)
	require.ErrorContains(t, err, "does not read FULLSEND_GCP_SERVICE_ACCOUNT")
}

// pinGitLabLegacyAgentScript serves a pre-impersonation run-agent-job.sh at
// the manifest's pinned gitlab.fullsend_ref.
func pinGitLabLegacyAgentScript(fc *forge.FakeClient, m *Manifest) {
	fc.FileContentsRef[shimOwner+"/"+shimRepo+"/"+gitlabAgentJobScriptUpstreamPath+"@"+m.GitLab.FullsendRef] = []byte(legacyAgentJobScript)
}

func TestConverge_GitLabServiceAccountRejectsLegacyPinnedScript(t *testing.T) {
	t.Run("fresh install", func(t *testing.T) {
		fc := newFakeClientForBatch("group/project")
		m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+
			"\n  service_account: "+testServiceAccount+"\n")
		pinGitLabLegacyAgentScript(fc, m)
		result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
			newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "needs a gitlab.fullsend_ref that includes service-account impersonation support")
		assert.Empty(t, fc.CreatedSecrets, "nothing is written")
		assert.Empty(t, fc.Variables, "nothing is written")
	})

	t.Run("established install", func(t *testing.T) {
		fc, m := installedGitLabExistingWIF(t)
		pinGitLabLegacyAgentScript(fc, m)
		result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
			newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		assert.Contains(t, result.Failed()[0].Error.Error(), "does not read FULLSEND_GCP_SERVICE_ACCOUNT")
		_, written := fc.VariableValues["group/project/"+forge.VarGCPServiceAccount]
		assert.False(t, written, "the variable is not written")
		assert.Empty(t, fc.CreatedSecrets)
	})

	t.Run("without a service account the legacy script is fine", func(t *testing.T) {
		fc := newFakeClientForBatch("group/project")
		m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+"\n")
		pinGitLabLegacyAgentScript(fc, m)
		require.NoError(t, checkPinnedServiceAccountSupport(context.Background(), ResolvedConfig{
			Owner: "group", Repo: "project", Forge: ForgeGitLab, FullsendRef: m.GitLab.FullsendRef,
		}, "", "", NewRefResolver(fc)))
	})
}

func TestStatus_GitLabServiceAccountLegacyPinnedScript(t *testing.T) {
	fc, m := installedGitLabExistingWIF(t)
	pinGitLabLegacyAgentScript(fc, m)
	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	assert.Contains(t, status.Repos[0].Error, "group/project: inference.service_account needs a gitlab.fullsend_ref")
}

func TestCheckPinnedServiceAccountSupport_SkipsReleaseMatchAndFetchErrors(t *testing.T) {
	fc := forge.NewFakeClient()
	rc := ResolvedConfig{
		Owner: "group", Repo: "project", Forge: ForgeGitLab, FullsendRef: "v9.9.9",
		DeclaredServiceAccount: testServiceAccount,
	}
	require.NoError(t, checkPinnedServiceAccountSupport(context.Background(), rc, "abc123", "v9.9.9", NewRefResolver(fc)),
		"a release-matching ref renders the embedded script")
	require.NoError(t, checkPinnedServiceAccountSupport(context.Background(), rc, "", "", nil), "no upstream client")

	err := checkPinnedServiceAccountSupport(context.Background(), rc, "", "", NewRefResolver(fc))
	require.ErrorContains(t, err, "to verify inference.service_account support")
}

func TestRenderGitLabAgentAudience_MissingTemplate(t *testing.T) {
	files := []forge.TreeFile{{Path: gitlabAgentJobScriptPath, Content: []byte("#!/bin/sh\n")}}
	got, err := renderGitLabAgentAudience(files, "")
	require.NoError(t, err, "nothing to render without an audience")
	assert.Equal(t, files, got)

	_, err = renderGitLabAgentAudience(files, testExistingAudience)
	require.ErrorContains(t, err, "inference.wif_audience is declared")
	assert.Equal(t, scaffold.GitLabAgentTemplatePath, fullsendAgentTemplatePath)
}

// unpinnedInstalledGitLabExistingWIF is installedGitLabExistingWIF with no
// gitlab.fullsend_ref, so under a build with no upstream ref nothing will
// refresh the live scaffold.
func unpinnedInstalledGitLabExistingWIF(t *testing.T) (*forge.FakeClient, *Manifest) {
	t.Helper()
	fc, m := installedGitLabExistingWIF(t)
	m.GitLab.FullsendRef = ""
	return fc, m
}

func TestConverge_GitLabServiceAccountUnpinnedEstablishedInstall(t *testing.T) {
	t.Run("legacy live script fails before writes", func(t *testing.T) {
		fc, m := unpinnedInstalledGitLabExistingWIF(t)
		fc.FileContents["group/project/"+gitlabAgentJobScriptPath] = []byte(legacyAgentJobScript)
		spy := &spyScaffoldCommit{}
		result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
			newTestClientFactory(fc), spy.fn(), noopProgress)
		require.NoError(t, err)
		require.Len(t, result.Failed(), 1)
		msg := result.Failed()[0].Error.Error()
		assert.Contains(t, msg, "does not read FULLSEND_GCP_SERVICE_ACCOUNT")
		assert.Contains(t, msg, "set gitlab.fullsend_ref")
		_, written := fc.VariableValues["group/project/"+forge.VarGCPServiceAccount]
		assert.False(t, written, "the variable is not written")
		assert.Empty(t, fc.CreatedSecrets)
		assert.Empty(t, spy.files, "no scaffold commit")
	})

	t.Run("current live script passes", func(t *testing.T) {
		fc, m := unpinnedInstalledGitLabExistingWIF(t)
		result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
			newTestClientFactory(fc), (&spyScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed(), "results: %+v", result.Results)
		assert.Equal(t, testServiceAccount, fc.VariableValues["group/project/"+forge.VarGCPServiceAccount])
	})
}

func TestStatus_GitLabServiceAccountUnpinnedLegacyLiveScript(t *testing.T) {
	fc, m := unpinnedInstalledGitLabExistingWIF(t)
	fc.FileContents["group/project/"+gitlabAgentJobScriptPath] = []byte(legacyAgentJobScript)
	status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	require.Len(t, status.Repos, 1)
	assert.Contains(t, status.Repos[0].Error,
		"group/project: inference.service_account is not supported by the installed GitLab scaffold")

	// The current live script clears the error.
	current, err := scaffold.GitLabPerRepoFile(gitlabAgentJobScriptPath)
	require.NoError(t, err)
	fc.FileContents["group/project/"+gitlabAgentJobScriptPath] = current
	status, err = Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	assert.NotContains(t, status.Repos[0].Error, "inference.service_account")
}

func TestCheckEstablishedServiceAccountSupport(t *testing.T) {
	ctx := context.Background()
	fc := forge.NewFakeClient()
	rc := ResolvedConfig{
		Owner: "group", Repo: "project", Forge: ForgeGitLab,
		DeclaredServiceAccount: testServiceAccount, ForgeConfig: ForgeConfig{Client: fc},
	}
	stamped := []ComponentStatus{
		{Name: "workflow", Present: true, Actual: "v2.5.0", Match: true},
		{Name: "scaffold:" + gitlabAgentJobScriptPath, Present: true, Match: true},
	}
	fc.FileContents["group/project/"+gitlabAgentJobScriptPath] = []byte(legacyAgentJobScript)
	require.ErrorContains(t, checkEstablishedServiceAccountSupport(ctx, rc, "", stamped), "does not read",
		"nothing refreshes a stamped install's legacy script")
	require.NoError(t, checkEstablishedServiceAccountSupport(ctx, rc, "v9.9.9", stamped),
		"a build-time ref refreshes the scaffold")
	pinned := rc
	pinned.FullsendRef = "v9.9.9"
	require.NoError(t, checkEstablishedServiceAccountSupport(ctx, pinned, "", stamped),
		"a pin is checked by checkPinnedServiceAccountSupport")
	none := rc
	none.DeclaredServiceAccount = ""
	require.NoError(t, checkEstablishedServiceAccountSupport(ctx, none, "", stamped))

	unstamped := []ComponentStatus{stamped[0], stamped[1]}
	unstamped[0].Actual = ""
	require.NoError(t, checkEstablishedServiceAccountSupport(ctx, rc, "", unstamped),
		"content drift re-renders an unstamped install from the embedded templates")
	missing := []ComponentStatus{stamped[0], {Name: "scaffold:" + gitlabAgentJobScriptPath}}
	require.NoError(t, checkEstablishedServiceAccountSupport(ctx, rc, "", missing),
		"missing-component repair restores the script from the embedded templates")

	fc.Errors["GetFileContent"] = assert.AnError
	require.ErrorContains(t, checkEstablishedServiceAccountSupport(ctx, rc, "", stamped),
		"to verify inference.service_account support")
}

// unstampedInstalledGitLabExistingWIF is an unpinned GitLab install whose
// wrapper carries no ref marker (written by a build without a release ref)
// and whose agent job script predates service-account impersonation.
func unstampedInstalledGitLabExistingWIF(t *testing.T) (*forge.FakeClient, *Manifest) {
	t.Helper()
	fc := newFakeClientForBatch("group/project")
	populateGitLabInstalled(fc, "group", "project")
	populateGitLabScaffoldContent(t, fc, "group", "project", "")
	populateGitLabTypedRoot(t, fc, "group", "project", fc.FileContents["group/project/"+fullsendPipelineInclude])
	fc.FileContents["group/project/"+gitlabAgentJobScriptPath] = []byte(legacyAgentJobScript)
	fc.Secrets["group/project/"+forge.SecretGCPProjectID] = true
	fc.Secrets["group/project/"+forge.SecretGCPWIFProvider] = true
	m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+
		"\n  service_account: "+testServiceAccount+"\n")
	m.GitLab.Repos[0].Name = "group/project"
	m.GitLab.FullsendRef = ""
	return fc, m
}

// committedFile returns the content spy committed for path, if any.
func committedFile(spy *spyScaffoldCommit, path string) ([]byte, bool) {
	spy.mu.Lock()
	defer spy.mu.Unlock()
	for _, f := range spy.files {
		if f.Path == path {
			return f.Content, true
		}
	}
	return nil, false
}

func TestConverge_GitLabServiceAccountUnpinnedRefreshedScript(t *testing.T) {
	t.Run("unstamped legacy install is re-rendered", func(t *testing.T) {
		fc, m := unstampedInstalledGitLabExistingWIF(t)
		require.Empty(t, unstampedProbeRef(t, fc), "the wrapper carries no ref marker")
		spy := &spyScaffoldCommit{}
		result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
			newTestClientFactory(fc), spy.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed(), "results: %+v", result.Results)
		script, ok := committedFile(spy, gitlabAgentJobScriptPath)
		require.True(t, ok, "content drift refreshes run-agent-job.sh")
		assert.Contains(t, string(script), forge.VarGCPServiceAccount)
		assert.Equal(t, testServiceAccount, fc.VariableValues["group/project/"+forge.VarGCPServiceAccount])
	})

	t.Run("missing script is restored", func(t *testing.T) {
		fc, m := unpinnedInstalledGitLabExistingWIF(t)
		delete(fc.FileContents, "group/project/"+gitlabAgentJobScriptPath)
		spy := &spyScaffoldCommit{}
		result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
			newTestClientFactory(fc), spy.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed(), "results: %+v", result.Results)
		script, ok := committedFile(spy, gitlabAgentJobScriptPath)
		require.True(t, ok, "missing-component repair restores run-agent-job.sh")
		assert.Contains(t, string(script), forge.VarGCPServiceAccount)
		assert.Equal(t, testServiceAccount, fc.VariableValues["group/project/"+forge.VarGCPServiceAccount])
	})

	t.Run("fresh install renders the embedded script", func(t *testing.T) {
		fc := newFakeClientForBatch("group/project")
		m := gitlabExistingWIFManifest(t, "inference:\n  project: example-project\n  wif_provider: "+testExistingWIF+
			"\n  service_account: "+testServiceAccount+"\n")
		m.GitLab.FullsendRef = ""
		result, err := Converge(context.Background(), ConvergeConfig{Manifest: m, MaxConcurrency: 4, Direct: true},
			newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed(), "results: %+v", result.Results)
	})
}

// unstampedProbeRef returns the probed workflow ref of group/project.
func unstampedProbeRef(t *testing.T, fc *forge.FakeClient) string {
	t.Helper()
	components, err := ProbeComponents(context.Background(), fc, "group", "project", ForgeGitLab, GitLabForgeConfig(), nil)
	require.NoError(t, err)
	for _, c := range components {
		if c.Name == "workflow" {
			require.True(t, c.Present)
			return c.Actual
		}
	}
	t.Fatal("no workflow component")
	return ""
}

func TestStatus_GitLabServiceAccountUnpinnedRefreshedScript(t *testing.T) {
	t.Run("unstamped legacy install", func(t *testing.T) {
		fc, m := unstampedInstalledGitLabExistingWIF(t)
		status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
		require.NoError(t, err)
		require.Len(t, status.Repos, 1)
		assert.NotContains(t, status.Repos[0].Error, "inference.service_account")
		assert.Contains(t, driftFields(status.Repos[0].Drifts), gitlabAgentJobScriptPath,
			"the legacy script is reported as content drift")
	})

	t.Run("missing script", func(t *testing.T) {
		fc, m := unpinnedInstalledGitLabExistingWIF(t)
		delete(fc.FileContents, "group/project/"+gitlabAgentJobScriptPath)
		status, err := Status(context.Background(), m, newTestClientFactory(fc), 4, nil)
		require.NoError(t, err)
		require.Len(t, status.Repos, 1)
		assert.NotContains(t, status.Repos[0].Error, "inference.service_account")
		assert.Contains(t, driftFields(status.Repos[0].Drifts), gitlabAgentJobScriptPath,
			"the missing script is reported as drift")
	})
}
