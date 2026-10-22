package repos

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fullsend-ai/fullsend/internal/forge"
	"github.com/fullsend-ai/fullsend/internal/scaffold"
)

// Tests for review feedback on #8012: obsolete inference configuration is
// drift even when the repository is otherwise not installed, and the
// obsolete FULLSEND_GCP_REGION variable is detected and converged away on
// openai-api-key repositories.

func obsoleteStatusManifest(forgeName, auth string) *Manifest {
	if forgeName == ForgeGitLab {
		return singleRepoGitLabManifest(auth)
	}
	return singleRepoGitHubManifest(auth)
}

// Leftover credentials of the unselected method are reported even when no
// component of the selected method exists.
func TestStatus_ObsoleteOnlyReportsDriftWhenNotInstalled(t *testing.T) {
	for _, tc := range []struct {
		name      string
		forgeName string
		auth      string
		secrets   []string
		variables []string
		wantDrift []string
	}{
		{"github openai with old gcp secrets", ForgeGitHub, InferenceAuthOpenAIAPIKey,
			[]string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, nil,
			[]string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}},
		{"github vertex with old openai key", ForgeGitHub, InferenceAuthVertexWIF,
			[]string{forge.SecretOpenAIAPIKey}, nil,
			[]string{forge.SecretOpenAIAPIKey}},
		{"github openai with old region", ForgeGitHub, InferenceAuthOpenAIAPIKey,
			nil, []string{forge.VarGCPRegion},
			[]string{forge.VarGCPRegion}},
		{"gitlab openai with old gcp secrets", ForgeGitLab, InferenceAuthOpenAIAPIKey,
			[]string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, nil,
			[]string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}},
		{"gitlab vertex with old openai key", ForgeGitLab, InferenceAuthVertexWIF,
			[]string{forge.SecretOpenAIAPIKey}, nil,
			[]string{forge.SecretOpenAIAPIKey}},
		{"gitlab openai with old region", ForgeGitLab, InferenceAuthOpenAIAPIKey,
			nil, []string{forge.VarGCPRegion},
			[]string{forge.VarGCPRegion}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := forge.NewFakeClient()
			for _, name := range tc.secrets {
				fc.Secrets["acme/api/"+name] = true
			}
			for _, name := range tc.variables {
				fc.VariableValues["acme/api/"+name] = "us-east1"
			}

			result, err := Status(context.Background(), obsoleteStatusManifest(tc.forgeName, tc.auth), newTestClientFactory(fc), 4, nil)
			require.NoError(t, err)
			require.Len(t, result.Repos, 1)
			rs := result.Repos[0]
			require.Empty(t, rs.Error)
			assert.False(t, rs.Installed, "obsolete configuration alone is not an installation")
			assert.Equal(t, 1, result.Summary.Drifted)

			drifts := driftsByField(rs)
			require.Len(t, drifts, len(tc.wantDrift), "got %v", rs.Drifts)
			for _, name := range tc.wantDrift {
				d, ok := drifts[name]
				require.True(t, ok, "want obsolete drift for %s, got %v", name, rs.Drifts)
				assert.Equal(t, "absent", d.Expected)
				assert.Contains(t, d.Actual, "obsolete")
				assert.Contains(t, d.Actual, "inference.auth is "+tc.auth)
			}
		})
	}
}

// A leftover region on an otherwise healthy openai-api-key repository is
// obsolete drift; the selected Vertex method legitimately owns it.
func TestStatus_ObsoleteRegionVariable(t *testing.T) {
	for _, forgeName := range []string{ForgeGitHub, ForgeGitLab} {
		for _, tc := range []struct {
			auth      string
			wantDrift bool
		}{
			{InferenceAuthOpenAIAPIKey, true},
			{InferenceAuthVertexWIF, false},
		} {
			t.Run(forgeName+"/"+tc.auth, func(t *testing.T) {
				fc := forge.NewFakeClient()
				if forgeName == ForgeGitLab {
					populateInstalledGitLabRepo(fc, "acme/api")
				} else {
					populateInstalledRepo(t, fc, "acme", "api", "v2.3.0", "https://mint.example.com", "us-central1")
					delete(fc.Secrets, "acme/api/"+forge.SecretGCPProjectID)
					delete(fc.Secrets, "acme/api/"+forge.SecretGCPWIFProvider)
				}
				fc.VariableValues["acme/api/"+forge.VarGCPRegion] = "us-central1"
				switch tc.auth {
				case InferenceAuthOpenAIAPIKey:
					fc.Secrets["acme/api/"+forge.SecretOpenAIAPIKey] = true
				default:
					fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
					fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
				}

				result, err := Status(context.Background(), obsoleteStatusManifest(forgeName, tc.auth), newTestClientFactory(fc), 4, nil)
				require.NoError(t, err)
				rs := result.Repos[0]
				require.Empty(t, rs.Error)
				require.True(t, rs.Installed)

				d, ok := driftsByField(rs)[forge.VarGCPRegion]
				if !tc.wantDrift {
					assert.False(t, ok, "unexpected region drift %+v", d)
					return
				}
				require.True(t, ok, "want obsolete region drift, got %v", rs.Drifts)
				assert.Equal(t, "absent", d.Expected)
				assert.Equal(t, "obsolete variable (inference.auth is "+tc.auth+")", d.Actual)
				assert.NotContains(t, d.Actual, "us-central1", "values must not be reported")
				assert.Empty(t, fc.DeletedVariables, "status is read-only")
			})
		}
	}
}

// Convergence clears the obsolete region once the replacement
// configuration is on the default branch, so status drift is clearable.
func TestConverge_AuthSwitchRemovesObsoleteRegionVariable(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	require.Contains(t, fc.VariableValues, "acme/api/"+forge.VarGCPRegion)

	// Dry run only plans the deletion.
	dry := cfg
	dry.DryRun = true
	result, err := Converge(context.Background(), dry, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, fc.DeletedVariables)
	planned := map[string]string{}
	for _, a := range result.Results[0].Actions {
		planned[a.Component] = a.Action
	}
	assert.Equal(t, "delete", planned["var:"+forge.VarGCPRegion])

	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, fc.DeletedVariables, 1)
	assert.Equal(t, forge.VarGCPRegion, fc.DeletedVariables[0].Name)
	assert.NotContains(t, fc.VariableValues, "acme/api/"+forge.VarGCPRegion)

	// Status now reports no obsolete region drift for the converged repo.
	status, err := Status(context.Background(), singleRepoGitHubManifest(InferenceAuthOpenAIAPIKey), newTestClientFactory(fc), 4, nil)
	require.NoError(t, err)
	assert.NotContains(t, driftsByField(status.Repos[0]), forge.VarGCPRegion)
}

// The obsolete region is kept while the replacement scaffold is unmerged,
// the same as the obsolete secrets.
func TestConverge_AuthSwitchKeepsObsoleteRegionUntilScaffoldLands(t *testing.T) {
	fc, cfg := installedOpenAISwitchFixture(t)
	paths := scaffold.PerRepoThinCallerPaths()
	require.NotEmpty(t, paths)
	fc.FileContents["acme/api/"+paths[0]] = []byte("name: stale")

	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, fc.DeletedVariables, "region must be kept while the replacement is unmerged")
	assert.Contains(t, fc.VariableValues, "acme/api/"+forge.VarGCPRegion)

	result, err = Converge(context.Background(), cfg, newTestClientFactory(fc), landingScaffoldCommit(fc, "acme", "api"), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	require.Len(t, fc.DeletedVariables, 1)
	assert.Equal(t, forge.VarGCPRegion, fc.DeletedVariables[0].Name)
}

func TestConverge_GitLabAuthSwitchRemovesObsoleteRegionVariable(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	populateGitLabInstalled(fc, "acme", "api")
	fc.Secrets["acme/api/"+forge.SecretGCPProjectID] = true
	fc.Secrets["acme/api/"+forge.SecretGCPWIFProvider] = true
	fc.VariableValues["acme/api/"+forge.VarGCPRegion] = "us-central1"

	cfg := gitlabConvergeCfg("acme/api")
	cfg.Manifest.GitLab.FullsendRef = ""
	cfg.OpenAIAPIKey = ""
	_, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&spyScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{forge.SecretGCPProjectID, forge.SecretGCPWIFProvider}, deletedSecretNames(fc, "api"))
	require.Len(t, fc.DeletedVariables, 1)
	assert.Equal(t, forge.VarGCPRegion, fc.DeletedVariables[0].Name)
}

// Vertex repositories keep their region.
func TestConverge_VertexKeepsRegionVariable(t *testing.T) {
	fc := newFakeClientForBatch("acme/api")
	markFullyInstalled(fc, "acme", "api")
	populateScaffoldContent(t, fc, "acme", "api", "v1.0.0", "https://mint.example.com")

	cfg := convergeCfgWithDefaults(newConvergeManifest("acme/api"))
	result, err := Converge(context.Background(), cfg, newTestClientFactory(fc), (&fakeScaffoldCommit{}).fn(), noopProgress)
	require.NoError(t, err)
	require.Empty(t, result.Failed())
	assert.Empty(t, fc.DeletedVariables)
	assert.Contains(t, fc.VariableValues, "acme/api/"+forge.VarGCPRegion)
}
