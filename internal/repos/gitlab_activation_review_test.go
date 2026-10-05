package repos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

func TestTypedDeliveryRequiresVerifiedRestriction(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, role := range []string{"", forge.PipelineVarOverrideDeveloper, forge.PipelineVarOverrideOwner} {
			t.Run(fmt.Sprintf("direct=%t/role=%s", direct, role), func(t *testing.T) {
				t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
				fc := newFakeClientWithRepo()
				fc.PipelineVarOverrideRoles["acme/widgets"] = role
				fc.PipelineSchedules["acme/widgets"] = []forge.PipelineSchedule{{ID: 1, Description: PipelineScheduleSpecs()[0].Description, Variables: map[string]string{forge.VarPollMode: "slash"}}}
				cfg := baseCfg()
				cfg.Forge, cfg.Direct = ForgeGitLab, direct
				committed := false
				_, err := Install(context.Background(), cfg, fc, func(context.Context, string, string, []forge.TreeFile, bool, bool) error {
					committed = true
					return nil
				}, noopProgress)
				require.ErrorContains(t, err, "refusing to deliver runnable typed GitLab jobs")
				assert.False(t, committed)
				assert.Equal(t, role, fc.PipelineVarOverrideRoles["acme/widgets"])
				assert.Equal(t, "slash", fc.PipelineSchedules["acme/widgets"][0].Variables[forge.VarPollMode])
				assert.Empty(t, fc.Variables)
				assert.Empty(t, fc.CreatedSecrets)
			})
		}
	}
	for _, err := range []error{forge.ErrNotSupported, errors.New("read failed")} {
		fc := newFakeClientWithRepo()
		fc.Errors["GetPipelineVariablesMinimumOverrideRole"] = err
		require.ErrorContains(t, requireGitLabRestrictionBeforeDelivery(context.Background(), fc, "acme", "widgets"), "verifying GitLab restriction")
	}
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforce-typo")
	require.ErrorContains(t, requireGitLabRestrictionBeforeDelivery(context.Background(), newFakeClientWithRepo(), "acme", "widgets"), "not a recognized value")
}

func TestConvergeRefusesTypedDeliveryBeforeRestriction(t *testing.T) {
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	fc := newFakeClientWithRepo()
	fc.PipelineVarOverrideRoles["acme/widgets"] = forge.PipelineVarOverrideDeveloper
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	fc.FileContents["acme/widgets/"+fullsendPipelineInclude] = wrapper
	d := convergeDiscovery{
		repo:       ResolvedRepo{Owner: "acme", Repo: "widgets"},
		resolved:   ResolvedConfig{Owner: "acme", Repo: "widgets", Forge: ForgeGitLab, ForgeConfig: ForgeConfig{Client: fc}},
		components: []ComponentStatus{{Name: "workflow", Present: true}},
	}
	sc := &fakeScaffoldCommit{}
	result := convergeRepo(context.Background(), d, "", ConvergeConfig{}, nil, sc.fn(), noopProgress)
	require.ErrorContains(t, result.Error, "refusing to deliver runnable typed GitLab jobs")
	assert.False(t, sc.called)
}

// TestConvergeDryRunFreshInstallRunsGitLabPreflight guards the preview-
// fidelity bug where a fresh-install dry run reported "Would install
// (new)" before resolving the scaffold or running the typed-contract
// preflight (restriction, root-input conflicts), so a plan the real
// installation would reject was reported as installable.
func TestConvergeDryRunFreshInstallRunsGitLabPreflight(t *testing.T) {
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	fc := newFakeClientWithRepo()
	fc.PipelineVarOverrideRoles["acme/widgets"] = forge.PipelineVarOverrideDeveloper
	d := convergeDiscovery{
		repo:     ResolvedRepo{Owner: "acme", Repo: "widgets"},
		resolved: ResolvedConfig{Owner: "acme", Repo: "widgets", Forge: ForgeGitLab, ForgeConfig: ForgeConfig{Client: fc}},
	}
	sc := &fakeScaffoldCommit{}
	result := convergeRepo(context.Background(), d, "", ConvergeConfig{DryRun: true}, nil, sc.fn(), noopProgress)
	require.ErrorContains(t, result.Error, "refusing to deliver runnable typed GitLab jobs")
	assert.False(t, sc.called)
	for _, a := range result.Actions {
		assert.NotEqual(t, "add", a.Action, "a plan the real install would reject must not report \"Would install\"")
	}
}

func TestUnmergeRetainsStageBridgeForUserVariableConsumers(t *testing.T) {
	wrapper, err := scaffold.GitLabPerRepoFile(fullsendPipelineInclude)
	require.NoError(t, err)
	docs, err := decodeGitLabDocuments(wrapper)
	require.NoError(t, err)
	stage := findMappingValue(findMappingValue(findMappingValue(docs[0].Content[0], "spec"), "inputs"), "stage")
	var definition map[string]any
	require.NoError(t, stage.Decode(&definition))
	for _, reference := range []string{"$STAGE", "${STAGE}", "${STAGE:-fallback}", "$STAGE_SUFFIX"} {
		t.Run(reference, func(t *testing.T) {
			// Construct the user's matching declaration before installation.
			original, err := yaml.Marshal(map[string]any{"spec": map[string]any{
				"inputs": map[string]any{"stage": definition},
			}})
			require.NoError(t, err)
			original = append(original, []byte("---\nvariables:\n  STAGE: '$[[ inputs.stage ]]'\nuser-job:\n  script: 'echo "+reference+"'\n")...)
			merged, err := MergeGitLabCI(original)
			require.NoError(t, err)
			cleaned, err := UnmergeGitLabCI(merged)
			require.NoError(t, err)
			assert.Contains(t, string(cleaned), reference)
			assert.NotContains(t, string(cleaned), fullsendPipelineInclude)
			if reference == "$STAGE_SUFFIX" {
				assert.NotContains(t, string(cleaned), "inputs.stage")
			} else {
				assert.Contains(t, string(cleaned), "$[[ inputs.stage ]]")
				assert.Contains(t, string(cleaned), "stage:")
			}
		})
	}
}

func TestExpectedGitLabScaffoldForRecordedReleaseDefault(t *testing.T) {
	for _, ref := range []string{"release-sha", "v1.0.0"} {
		t.Run(ref, func(t *testing.T) {
			resolved := ResolvedConfig{Owner: "acme", Repo: "widgets", Forge: ForgeGitLab, FullsendRef: ref, ForgeConfig: ForgeConfigFor(ForgeGitLab)}
			dcfg := DriftConfig{UpstreamRef: "release-sha", UpstreamTag: "v1.0.0"}
			files, err := ExpectedScaffoldContent(context.Background(), resolved, dcfg, nil)
			require.NoError(t, err)
			require.NotEmpty(t, files)

			// Seed "installed" content the way a real release-default
			// install actually renders it — ref pinned to the release SHA
			// and tag to its distinct human-readable version (mirroring
			// resolveTargetRef's matching-release normalization) — rather
			// than reusing this test's own ExpectedScaffoldContent output
			// as the installed baseline. Seeding both sides from the same
			// computation previously masked a SHA/tag disagreement: when
			// the manifest recorded the release SHA, the status baseline
			// rendered FULLSEND_VERSION from that SHA while installation/
			// convergence render it from the release tag (#7850 review).
			installed, err := scaffold.CollectGitLabPerRepoInstallFiles(nil, nil, "release-sha", "v1.0.0")
			require.NoError(t, err)
			require.NotEmpty(t, installed)

			status := &RepoStatus{}
			fc := newFakeClientWithRepo()
			for _, f := range installed {
				fc.FileContents["acme/widgets/"+f.Path] = f.Content
			}
			checkScaffoldContentDrift(context.Background(), fc, resolved, dcfg, nil, status)
			assert.Empty(t, status.Error)
			assert.Empty(t, status.Drifts, "installation-rendered content with a SHA ref and a distinct release tag must not report drift: %+v", status.Drifts)
		})
	}
	resolved := ResolvedConfig{Forge: ForgeGitLab, FullsendRef: "v0.1.0"}
	_, err := ExpectedScaffoldContent(context.Background(), resolved, DriftConfig{UpstreamRef: "release-sha", UpstreamTag: "v1.0.0"}, nil)
	require.ErrorContains(t, err, "matching pinned GitLab templates")
}

func TestStatusWithoutGitHubUsesRecordedReleaseDefault(t *testing.T) {
	fc := newFakeClientWithRepo()
	m := &Manifest{Version: 1, Defaults: testInferenceDefaults(), GitLab: &PlatformConfig{FullsendRef: "v1.0.0", Repos: []RepoEntry{{Name: "acme/widgets"}}}}
	factory := &perForgeClientFactory{clients: map[string]forge.Client{ForgeGitLab: fc}}
	// Render the wrapper the way a real release-default install commits
	// it: carrying a "# fullsend-ref: <sha> (<tag>)" version marker that
	// records the running release's own SHA, while the manifest records
	// only the release tag. Without a GitHub resolver, comparing that
	// recorded SHA against the manifest's tag must not report false
	// fullsend_ref drift (#7850 review).
	files, err := scaffold.CollectGitLabPerRepoInstallFiles(nil, nil, "release-sha", "v1.0.0")
	require.NoError(t, err)
	var wrapper []byte
	for _, f := range files {
		if f.Path == fullsendPipelineInclude {
			wrapper = f.Content
		}
	}
	require.NotEmpty(t, wrapper, "rendered scaffold must include the pipeline wrapper")
	fc.FileContents["acme/widgets/"+fullsendPipelineInclude] = wrapper
	fc.Secrets["acme/widgets/"+forge.SecretGitLabPollerToken] = true
	result, err := Status(context.Background(), m, factory, 1, nil, DriftConfig{UpstreamRef: "release-sha", UpstreamTag: "v1.0.0"})
	require.NoError(t, err)
	require.Len(t, result.Repos, 1)
	assert.False(t, strings.Contains(result.Repos[0].Error, "matching pinned GitLab templates"), result.Repos[0].Error)
	assert.Empty(t, result.Repos[0].Error)
	assert.Equal(t, "release-sha", result.Repos[0].CurrentRef)
	for _, d := range result.Repos[0].Drifts {
		assert.NotEqual(t, "fullsend_ref", d.Field, "release-default status must not report fullsend_ref drift: %+v", d)
	}
}

type restrictionReadbackClient struct {
	forge.Client
	reads    int
	readback string
	err      error
}

func (c *restrictionReadbackClient) GetPipelineVariablesMinimumOverrideRole(ctx context.Context, owner, repo string) (string, error) {
	c.reads++
	if c.reads > 1 {
		return c.readback, c.err
	}
	return c.Client.GetPipelineVariablesMinimumOverrideRole(ctx, owner, repo)
}

func TestRestrictionWriteRequiresReadback(t *testing.T) {
	t.Setenv(GitLabPipelineVarRestrictionEnv, "enforced")
	for _, failure := range []bool{false, true} {
		fc := forge.NewFakeClient()
		fc.PipelineVarOverrideRoles["acme/widgets"] = forge.PipelineVarOverrideDeveloper
		client := &restrictionReadbackClient{Client: fc, readback: forge.PipelineVarOverrideDeveloper}
		if failure {
			client.err = errors.New("readback denied")
		}
		_, err := EnsureGitLabPipelineVariableOverrideRole(context.Background(), client, "acme", "widgets", false)
		require.Error(t, err)
		assert.Equal(t, 2, client.reads)
		if failure {
			assert.Contains(t, err.Error(), "verifying pipeline-variable restriction")
		} else {
			assert.Contains(t, err.Error(), "restriction readback")
		}
	}
}

type scaffoldReadFailureClient struct {
	forge.Client
	path string
}

func (c scaffoldReadFailureClient) GetFileContent(ctx context.Context, owner, repo, path string) ([]byte, error) {
	if path == c.path {
		return nil, errors.New("template read denied")
	}
	return c.Client.GetFileContent(ctx, owner, repo, path)
}

func TestActivationFailsClosedOnReadsOrUnsafeRestriction(t *testing.T) {
	for _, failure := range []string{fullsendPipelineInclude, fullsendAgentTemplatePath, fullsendPollTemplatePath, "restriction", "schedule", "missing poll"} {
		t.Run(failure, func(t *testing.T) {
			fc := newFakeClientWithRepo()
			for _, path := range []string{fullsendPipelineInclude, fullsendAgentTemplatePath, fullsendPollTemplatePath} {
				content, err := scaffold.GitLabPerRepoFile(path)
				require.NoError(t, err)
				fc.FileContents["acme/widgets/"+path] = content
			}
			// The root .gitlab-ci.yml must also already declare and forward
			// the typed contract: ActivateGitLabTypedDispatch now defers
			// (rather than proceeding to the checks below) when it doesn't
			// — see gitlabRootDeclaresDispatchInputs.
			mergedRoot, err := MergeGitLabCI(nil)
			require.NoError(t, err)
			fc.FileContents["acme/widgets/.gitlab-ci.yml"] = mergedRoot
			fc.PipelineSchedules["acme/widgets"] = []forge.PipelineSchedule{{ID: 1, Description: PipelineScheduleSpecs()[0].Description, Variables: map[string]string{forge.VarPollMode: "slash"}}}
			var client forge.Client = fc
			switch failure {
			case "restriction":
				fc.PipelineVarOverrideRoles["acme/widgets"] = forge.PipelineVarOverrideDeveloper
			case "schedule":
				fc.Errors["ListPipelineSchedules"] = errors.New("schedules read denied")
			case "missing poll":
				delete(fc.FileContents, "acme/widgets/"+fullsendPollTemplatePath)
			default:
				client = scaffoldReadFailureClient{Client: fc, path: failure}
			}
			result, err := ActivateGitLabTypedDispatch(context.Background(), client, "acme", "widgets", false)
			actions := activateGitLabTypedDispatch(context.Background(), client, "acme", "widgets")
			if failure == "missing poll" {
				require.NoError(t, err)
				assert.Equal(t, "deferred", result.Action)
				assert.Empty(t, actions)
			} else {
				require.Error(t, err)
				require.Len(t, actions, 1)
				assert.Equal(t, "error", actions[0].Action)
				assert.Contains(t, actions[0].Detail, "error activating")
			}
			assert.Equal(t, "slash", fc.PipelineSchedules["acme/widgets"][0].Variables[forge.VarPollMode])
		})
	}
}
