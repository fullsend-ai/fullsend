package repos

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/config"
	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/preset"
)

// partialInheritedClient reports the inherited variables the fake holds
// together with an UnverifiedScopesError, like a GitLab caller that cannot
// read every ancestor group.
type partialInheritedClient struct {
	*forge.FakeClient
	scopes []string
}

func (c partialInheritedClient) ListInheritedRepoVariables(ctx context.Context, owner, repo string) ([]forge.OrgVariable, error) {
	vars, err := c.FakeClient.ListInheritedRepoVariables(ctx, owner, repo)
	if err != nil {
		return nil, err
	}
	return vars, &forge.UnverifiedScopesError{Scopes: c.scopes}
}

func TestOpenAIWIFIdentifiers_UnreadableInheritedVariablesAreUnverified(t *testing.T) {
	ctx := context.Background()
	full := config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"}

	for name, listErr := range map[string]error{
		"github organization forbidden": fmt.Errorf("list inherited repo variables page 1: %w", forge.ErrForbidden),
		"github organization not found": fmt.Errorf("list inherited repo variables page 1: %w", forge.ErrNotFound),
	} {
		t.Run(name+" does not verify config identifiers", func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			fc.Errors["ListInheritedRepoVariables"] = listErr
			fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, full)

			ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
			require.NoError(t, err)
			assert.Equal(t, "config.yaml", ids.source)
			assert.NotEmpty(t, ids.unverified)
			assert.False(t, ids.complete(), "unread inherited variables could override the configuration")
			assert.Contains(t, ids.problem(), "cannot verify")
		})
	}

	t.Run("nothing found with an unread scope is not verified empty", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.Errors["ListInheritedRepoVariables"] = forge.ErrForbidden
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.Empty(t, ids.source)
		assert.NotEmpty(t, ids.unverified)
		assert.Contains(t, ids.problem(), "could not be read")

		scopes, err := unverifiedOpenAIWIFScopes(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.NotEmpty(t, scopes)
	})

	t.Run("partial results keep readable variables and report unread scopes", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", forge.VarOpenAIAudience)
		client := partialInheritedClient{FakeClient: fc, scopes: []string{"group top"}}

		ids, err := liveOpenAIWIFIdentifiers(ctx, client, "acme", "api")
		require.NoError(t, err)
		assert.Equal(t, openAIWIFVariableSource, ids.source)
		assert.Equal(t, []string{"group top"}, ids.unverified)
		assert.False(t, ids.complete())

		scopes, err := unverifiedOpenAIWIFScopes(ctx, client, "acme", "api")
		require.NoError(t, err)
		assert.Empty(t, scopes, "found identifiers are handled by the residual check, not the unverified gate")
	})

	t.Run("a complete variable set stands despite unread scopes", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)
		client := partialInheritedClient{FakeClient: fc, scopes: []string{"group top"}}

		ids, err := liveOpenAIWIFIdentifiers(ctx, client, "acme", "api")
		require.NoError(t, err)
		assert.True(t, ids.complete())
	})

	t.Run("a forge without inheritance is verified empty", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		fc.Errors["ListInheritedRepoVariables"] = forge.ErrNotSupported
		ids, err := liveOpenAIWIFIdentifiers(ctx, fc, "acme", "api")
		require.NoError(t, err)
		assert.Empty(t, ids.unverified)
	})
}

func TestProbeOpenAIWIFIdentifiers_UnreadableInheritedVariables(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	fc.Errors["ListInheritedRepoVariables"] = forge.ErrForbidden
	fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t,
		config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})

	cs, err := probeOpenAIWIFIdentifiers(context.Background(), fc, "acme", "api")
	require.NoError(t, err)
	assert.False(t, cs.Match)
	assert.Contains(t, cs.Actual, "cannot verify")
}

func TestConverge_OpenAIWIFUnreadableInheritedVariablesFailBeforeWrites(t *testing.T) {
	for name, listErr := range map[string]error{
		"organization forbidden": forge.ErrForbidden,
		"organization not found": forge.ErrNotFound,
	} {
		t.Run(name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			fc.Errors["ListInheritedRepoVariables"] = listErr
			fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t,
				config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})

			sc := &fakeScaffoldCommit{}
			result, err := Converge(context.Background(), openAIWIFConvergeCfg("acme/api"), newTestClientFactory(fc), sc.fn(), noopProgress)
			require.NoError(t, err)
			failed := result.Failed()
			require.Len(t, failed, 1)
			assert.Contains(t, failed[0].Error.Error(), "cannot verify")
			assert.False(t, sc.called)
			assert.Empty(t, fc.CreatedSecrets)
		})
	}
}

func TestConverge_SwitchToAPIKeyKeepsCredentialsWhileInheritedVariablesUnverified(t *testing.T) {
	// The readable scopes hold no identifier, but an unreadable one could:
	// the API-key route is unverified, so the obsolete GCP pair stays.
	t.Run("github organization forbidden", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		fc.Errors["ListInheritedRepoVariables"] = forge.ErrForbidden

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed(), "the install itself is not blocked")

		assert.Empty(t, deletedSecretNames(fc, "api"), "obsolete credentials are kept while unverified")
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
		assert.Contains(t, actionDetails(result), "inherited variables could not be read")
	})
	t.Run("gitlab-style partial scopes", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		client := partialInheritedClient{FakeClient: fc, scopes: []string{"group top", "instance"}}

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), cfg, newTestClientFactory(client), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())

		assert.Empty(t, deletedSecretNames(fc, "api"))
		assert.Contains(t, actionDetails(result), "group top, instance")
	})

	t.Run("gitlab retained config cannot verify unread inherited variables", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)
		cfg = gitlabConvergeCfg("acme/api")
		populateGitLabInstalled(fc, "acme", "api")
		populateGitLabScaffoldContent(t, fc, "acme", "api", "v2.5.0")
		populateGitLabTypedRoot(t, fc, "acme", "api", fc.FileContents["acme/api/"+fullsendPipelineInclude])
		fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})
		client := partialInheritedClient{FakeClient: fc, scopes: []string{"group top", "instance"}}
		scopes, err := unverifiedOpenAIWIFScopes(context.Background(), client, "acme", "api")
		require.NoError(t, err)
		assert.Equal(t, []string{"group top", "instance"}, scopes)
		result, err := Converge(context.Background(), cfg, newTestClientFactory(client), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
		assert.Empty(t, deletedSecretNames(fc, "api"))
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPProjectID])
		assert.True(t, fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider])
		assert.Contains(t, actionDetails(result), "group top, instance")
	})
	t.Run("verified inheritance still deletes", func(t *testing.T) {
		fc, cfg := installedOpenAISwitchFixture(t)

		result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
		assert.ElementsMatch(t, []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, deletedSecretNames(fc, "api"))
	})
}

// actionDetails joins the details of every action in result.
func actionDetails(result *ConvergeBatchResult) string {
	var out string
	for _, r := range result.Results {
		for _, a := range r.Actions {
			out += a.Detail + "\n"
		}
	}
	return out
}

func TestConverge_OpenAIWIFVendoredInstallChecksVendorSource(t *testing.T) {
	vendoredCfg := func(read func(string) ([]byte, error)) ConvergeConfig {
		cfg := openAIWIFConvergeCfg("acme/api")
		vendor := true
		cfg.VendorOverride = &vendor
		cfg.ReadVendoredWorkflow = read
		return cfg
	}
	serveVendor := func(forward bool) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return reusableWorkflowFixture(forward, false), nil }
	}

	t.Run("capable vendor source passes despite an incapable remote ref", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		serveOpenAIWIFWorkflows(fc, "v1.0.0", false)

		result, err := Converge(context.Background(), vendoredCfg(serveVendor(true)), newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
	})
	t.Run("incapable vendor source is rejected despite a capable remote ref", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		serveOpenAIWIFWorkflows(fc, "v1.0.0", true)

		sc := &fakeScaffoldCommit{}
		result, err := Converge(context.Background(), vendoredCfg(serveVendor(false)), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "vendored reusable workflows")
		assert.False(t, sc.called)
		assert.Empty(t, fc.CreatedSecrets)
	})
	t.Run("missing workflow in the vendor source is rejected", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)

		missing := func(path string) ([]byte, error) { return nil, fmt.Errorf("%s: %w", path, forge.ErrNotFound) }
		result, err := Converge(context.Background(), vendoredCfg(missing), newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "vendored reusable workflows")
	})
	t.Run("unreadable vendor source fails closed", func(t *testing.T) {
		fc := newFakeClientForBatch("acme/api")
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)

		broken := func(string) ([]byte, error) { return nil, errors.New("vendor source unavailable") }
		result, err := Converge(context.Background(), vendoredCfg(broken), newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "verify openai-wif support")
	})
}

func TestConverge_OpenAIWIFEstablishedVendoredInstallChecksInstalledWorkflowsWithoutCommit(t *testing.T) {
	vendoredCfg := func() ConvergeConfig {
		cfg := openAIWIFConvergeCfg("acme/api")
		vendor := true
		cfg.VendorOverride = &vendor
		cfg.ReadVendoredWorkflow = func(string) ([]byte, error) { return reusableWorkflowFixture(true, false), nil }
		return cfg
	}
	// vendoredInstall is an established vendored install whose shim and thin
	// callers already match the vendored render, so convergence has no
	// scaffold files to commit; the installed reusable workflows are served
	// from forward.
	vendoredInstall := func(t *testing.T, forward bool) *forge.FakeClient {
		fc, _ := installedOpenAISwitchFixture(t)
		files, err := BuildScaffoldFiles(InstallConfig{
			Owner: "acme", Repo: "api", Forge: ForgeGitHub, Roles: []string{"triage"},
			MintURL: "https://mint.example.com", UpstreamRef: "v1.0.0", UpstreamTag: "v1.0.0",
			VendorBinary: true,
		})
		require.NoError(t, err)
		for _, f := range files {
			fc.FileContents["acme/api/"+f.Path] = f.Content
		}
		for _, path := range openAIWIFReusableWorkflows {
			fc.FileContents["acme/api/"+path] = reusableWorkflowFixture(forward, false)
		}
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		return fc
	}

	t.Run("incompatible installed vendored workflow with nothing to commit is rejected", func(t *testing.T) {
		fc := vendoredInstall(t, false)

		sc := &spyScaffoldCommit{}
		result, err := Converge(context.Background(), vendoredCfg(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "installed reusable workflows do not support")
		assert.Empty(t, sc.files)
		assert.Empty(t, fc.DeletedSecrets)
	})
	t.Run("rejection with supplied Vertex inputs happens before any variable or secret write", func(t *testing.T) {
		withVertex := func() ConvergeConfig {
			cfg := vendoredCfg()
			full := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
			cfg.InferenceProject = full.InferenceProject
			cfg.InferenceProjectNumber = full.InferenceProjectNumber
			cfg.InferenceRegion = full.InferenceRegion
			cfg.WIFProvider = full.WIFProvider
			return cfg
		}
		require.NotEmpty(t, withVertex().InferenceProject)

		// Control: with compatible workflows the supplied inputs are written.
		okClient := vendoredInstall(t, true)
		result, err := Converge(context.Background(), withVertex(), newTestClientFactory(okClient), (&spyScaffoldCommit{}).fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
		require.NotEmpty(t, okClient.CreatedSecrets, "control run must write the supplied GCP secrets")

		fc := vendoredInstall(t, false)
		sc := &spyScaffoldCommit{}
		result, err = Converge(context.Background(), withVertex(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		failed := result.Failed()
		require.Len(t, failed, 1)
		assert.Contains(t, failed[0].Error.Error(), "installed reusable workflows do not support")
		assert.Empty(t, fc.CreatedSecrets, "no secret may be written on the rejection path")
		assert.Empty(t, fc.UpdatedVariables, "no variable may be written on the rejection path")
		assert.Empty(t, fc.DeletedSecrets)
		assert.Empty(t, sc.files)
	})
	t.Run("compatible installed vendored workflow succeeds", func(t *testing.T) {
		fc := vendoredInstall(t, true)

		sc := &spyScaffoldCommit{}
		result, err := Converge(context.Background(), vendoredCfg(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
		assert.Empty(t, sc.files, "nothing to commit")
	})
	t.Run("a pending scaffold commit delivers the vendored workflows instead", func(t *testing.T) {
		fc := vendoredInstall(t, false)
		fc.FileContents["acme/api/.github/workflows/fullsend.yaml"] = []byte("stale shim\n")

		sc := &spyScaffoldCommit{}
		result, err := Converge(context.Background(), vendoredCfg(), newTestClientFactory(fc), sc.fn(), noopProgress)
		require.NoError(t, err)
		require.Empty(t, result.Failed())
		assert.NotEmpty(t, sc.files)
	})
}

// An established vendored install whose only pending change is a managed
// configuration update will redeliver the vendored workflows with that commit,
// so a dry run must accept it exactly as the real run does, even though the
// collectors plan the change as an action and return no file in a dry run.
func TestConverge_OpenAIWIFEstablishedVendoredInstallDryRunMatchesLiveWithPendingConfig(t *testing.T) {
	vendoredCfg := func(dryRun bool) ConvergeConfig {
		cfg := managedWIFConvergeCfg(t, "")
		vendor := true
		cfg.VendorOverride = &vendor
		cfg.ReadVendoredWorkflow = func(string) ([]byte, error) { return reusableWorkflowFixture(true, false), nil }
		cfg.DryRun = dryRun
		return cfg
	}
	// An established vendored install whose scaffold matches the vendored
	// render and whose installed reusable workflows predate the identifier
	// forwarding; only the managed configuration is missing.
	install := func(t *testing.T) *forge.FakeClient {
		fc, _ := installedOpenAISwitchFixture(t)
		files, err := BuildScaffoldFiles(InstallConfig{
			Owner: "acme", Repo: "api", Forge: ForgeGitHub, Roles: []string{"triage"},
			MintURL: "https://mint.example.com", UpstreamRef: "v1.0.0", UpstreamTag: "v1.0.0",
			VendorBinary: true,
		})
		require.NoError(t, err)
		for _, f := range files {
			fc.FileContents["acme/api/"+f.Path] = f.Content
		}
		for _, path := range openAIWIFReusableWorkflows {
			fc.FileContents["acme/api/"+path] = reusableWorkflowFixture(false, false)
		}
		delete(fc.FileContents, "acme/api/"+preset.OverlayPath)
		setOpenAIWIFVariables(fc, "acme/api", openAIWIFVariables...)
		return fc
	}

	fc := install(t)
	sc := &spyScaffoldCommit{}
	result, err := Converge(context.Background(), vendoredCfg(false), newTestClientFactory(fc), sc.fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "the pending configuration commit redelivers the vendored workflows")
	var delivered []string
	for _, f := range sc.files {
		delivered = append(delivered, f.Path)
	}
	require.Contains(t, delivered, ".fullsend/config.yaml")

	dry := install(t)
	result, err = Converge(context.Background(), vendoredCfg(true), newTestClientFactory(dry), (&spyScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed(), "a dry run must not reject what the real run accepts")
}

func TestCheckPinnedOpenAIWIFContract_EstablishedWithoutRefSkipsDefaultBranch(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClientForBatch("acme/api")
	fc.Errors["GetFileContentAtRef"] = errors.New("main unreachable")
	resolver := NewRefResolver(fc)
	resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: ForgeGitHub, InferenceAuth: InferenceAuthOpenAIWIF}

	// An established installation with no scaffold refresh ref is covered
	// by checkEstablishedOpenAIWIFContract; the default branch is not read.
	require.NoError(t, checkPinnedOpenAIWIFContract(ctx, resolved, ConvergeConfig{}, resolver, gcpSecretSet{}, true))

	// A fresh install still verifies the default branch and fails closed.
	err := checkPinnedOpenAIWIFContract(ctx, resolved, ConvergeConfig{}, resolver, gcpSecretSet{}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "main unreachable")
}

func TestStatus_OpenAIAPIKeyUnreadInheritedScopeIsNotHealthy(t *testing.T) {
	for name, listErr := range map[string]error{"forbidden": forge.ErrForbidden, "not found": forge.ErrNotFound} {
		t.Run(name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
			fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
			delete(fc.Secrets, "acme/api/"+forge.SecretGCPProjectID)
			delete(fc.Secrets, "acme/api/"+forge.SecretGCPWIFProvider)
			delete(fc.VariableValues, "acme/api/"+forge.VarGCPRegion)
			result, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIAPIKey), newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			require.Empty(t, result.Repos[0].Drifts, "control installation must be healthy")
			fc.Errors["ListInheritedRepoVariables"] = listErr
			result, err = Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIAPIKey), newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			require.Empty(t, result.Repos[0].Error)
			drift, ok := driftsByField(result.Repos[0])["residual-identifiers"]
			require.True(t, ok, "unverified inheritance must be reported as readiness drift")
			assert.Contains(t, drift.Actual, "cannot verify inherited variables")
		})
	}
}

func TestStatus_GitLabAPIKeyInheritedWIFReadiness(t *testing.T) {
	for _, tc := range []struct {
		name        string
		identifiers bool
		unread      bool
		config      bool
		wantDrift   bool
	}{
		{name: "group identifiers", identifiers: true, wantDrift: true},
		{name: "instance identifiers", identifiers: true, wantDrift: true},
		{name: "unread scopes", unread: true, wantDrift: true},
		{name: "config ignored", config: true},
		{name: "config with unread scopes", config: true, unread: true, wantDrift: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClientForBatch("acme/api")
			populateGitLabInstalled(fc, "acme", "api")
			populateGitLabScaffoldContent(t, fc, "acme", "api", "v2.5.0")
			populateGitLabTypedRoot(t, fc, "acme", "api", fc.FileContents["acme/api/"+fullsendPipelineInclude])
			if tc.identifiers {
				setOrgOpenAIWIFVariables(fc, "acme", openAIWIFVariables...)
			}
			if tc.config {
				fc.FileContents["acme/api/"+preset.OverlayPath] = openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})
			}
			var client forge.Client = fc
			if tc.unread {
				client = partialInheritedClient{FakeClient: fc, scopes: []string{"group top", "instance"}}
			}
			manifest := singleRepoGitLabManifest(InferenceAuthOpenAIAPIKey)
			manifest.GitLab.FullsendRef = "v2.5.0"
			result, err := Status(context.Background(), manifest, newTestClientFactory(client), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			require.Empty(t, result.Repos[0].Error)
			drift, ok := driftsByField(result.Repos[0])["residual-identifiers"]
			assert.Equal(t, tc.wantDrift, ok)
			if tc.unread {
				assert.Contains(t, drift.Actual, "cannot verify inherited variables")
			}
		})
	}
}

func TestRemoveObsoleteInferenceSecrets_DryRunHonorsReadiness(t *testing.T) {
	for _, tc := range []struct {
		name            string
		forgeName, auth string
		unread          bool
	}{
		{"GitLab unread inherited scopes", ForgeGitLab, InferenceAuthOpenAIAPIKey, true},
		{"WIF identifiers pending default branch", ForgeGitHub, InferenceAuthOpenAIWIF, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, dryRun := range []bool{true, false} {
				fc, _ := installedOpenAISwitchFixture(t)
				var client forge.Client = fc
				var files []forge.TreeFile
				if tc.unread {
					populateGitLabInstalled(fc, "acme", "api")
					populateGitLabScaffoldContent(t, fc, "acme", "api", "v2.5.0")
					client = partialInheritedClient{FakeClient: fc, scopes: []string{"group top", "instance"}}
				} else {
					fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
					delete(fc.FileContents, "acme/api/"+preset.OverlayPath)
					files = []forge.TreeFile{{Path: preset.OverlayPath, Content: openAIWIFConfigYAML(t, config.OpenAIWIFConfig{Audience: "aud", IdentityProviderID: "idp", ServiceAccountID: "sa"})}}
				}
				resolved := ResolvedConfig{Owner: "acme", Repo: "api", Forge: tc.forgeName, InferenceAuth: tc.auth, ForgeConfig: ForgeConfig{Client: client}}
				actions := removeObsoleteInferenceSecrets(context.Background(), resolved, dryRun, len(files) > 0, files, noopProgress)
				require.NotEmpty(t, actions)
				for _, action := range actions {
					assert.Equal(t, "none", action.Action)
					if dryRun {
						assert.Contains(t, action.Detail, "would keep obsolete")
					} else {
						assert.Contains(t, action.Detail, "kept obsolete")
					}
				}
				assert.Empty(t, fc.DeletedSecrets)
			}
		})
	}
}
