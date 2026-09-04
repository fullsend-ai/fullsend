package mintcore

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAudience_UnmarshalString(t *testing.T) {
	var a Audience
	require.NoError(t, json.Unmarshal([]byte(`"fullsend-mint"`), &a))
	assert.Equal(t, Audience{"fullsend-mint"}, a)
}

func TestAudience_UnmarshalArray(t *testing.T) {
	var a Audience
	require.NoError(t, json.Unmarshal([]byte(`["fullsend-mint", "other"]`), &a))
	assert.Equal(t, Audience{"fullsend-mint", "other"}, a)
}

func TestAudience_UnmarshalEmpty(t *testing.T) {
	var a Audience
	assert.Error(t, json.Unmarshal([]byte(`""`), &a))
	assert.Error(t, json.Unmarshal([]byte(`[]`), &a))
}

func TestAudience_UnmarshalWhitespace(t *testing.T) {
	var a Audience
	assert.Error(t, json.Unmarshal([]byte(`"   "`), &a))
}

func TestAudience_UnmarshalArrayWithEmpty(t *testing.T) {
	var a Audience
	assert.Error(t, json.Unmarshal([]byte(`["valid", ""]`), &a))
	assert.Error(t, json.Unmarshal([]byte(`["valid", "  "]`), &a))
}

func TestValidateOrgAllowed_EmptyList(t *testing.T) {
	assert.Error(t, ValidateOrgAllowed("anyorg", nil))
	assert.Error(t, ValidateOrgAllowed("anyorg", []string{}))
}

func TestAudience_Contains(t *testing.T) {
	a := Audience{"fullsend-mint", "other"}
	assert.True(t, a.Contains("fullsend-mint"))
	assert.True(t, a.Contains("other"))
	assert.False(t, a.Contains("missing"))
}

func TestValidateOrgAllowed(t *testing.T) {
	orgs := []string{"myorg", "OtherOrg"}

	assert.NoError(t, ValidateOrgAllowed("myorg", orgs))
	assert.NoError(t, ValidateOrgAllowed("MYORG", orgs))
	assert.NoError(t, ValidateOrgAllowed("otherorg", orgs))
	assert.Error(t, ValidateOrgAllowed("unknown", orgs))
}

func TestIsPublicMint(t *testing.T) {
	assert.True(t, IsPublicMint([]string{"*"}))
	assert.True(t, IsPublicMint([]string{"org1", "*"}))
	assert.False(t, IsPublicMint([]string{"myorg"}))
	assert.False(t, IsPublicMint(nil))
}

func TestParseAllowedOrgs(t *testing.T) {
	assert.Equal(t, []string{"*"}, ParseAllowedOrgs("*"))
	assert.Equal(t, []string{"org-a", "org-b"}, ParseAllowedOrgs(" org-a , org-b "))
	assert.Nil(t, ParseAllowedOrgs(""))
}

func TestValidateOrgAllowed_PublicMode(t *testing.T) {
	public := []string{"*"}
	assert.NoError(t, ValidateOrgAllowed("anyorg", public))
	assert.NoError(t, ValidateOrgAllowed("AnotherOrg", public))
	assert.Error(t, ValidateOrgAllowed("", public))
}

func TestValidateWorkflowRef_PerOrg(t *testing.T) {
	// Per-org mode: only .fullsend and upstream are allowed (hard-wired).
	allowedFiles := []string{"dispatch.yml", "triage.yml"}

	tests := []struct {
		name       string
		ref        string
		repository string
		wantErr    string
	}{
		{"empty ref", "", "myorg/my-repo", "missing job_workflow_ref"},
		{
			"config repo workflow",
			"myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/.fullsend",
			"",
		},
		{
			"upstream workflow",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/my-repo",
			"",
		},
		{
			"per-repo workflow from own repo denied in per-org mode",
			"myorg/my-repo/.github/workflows/triage.yml@refs/heads/main",
			"myorg/my-repo",
			"does not reference .fullsend or upstream repo",
		},
		{
			"unregistered repo",
			"myorg/other-repo/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/other-repo",
			"does not reference .fullsend or upstream repo",
		},
		{
			"not a workflow path",
			"myorg/.fullsend/scripts/run.sh@refs/heads/main",
			"myorg/.fullsend",
			"does not reference a workflow file",
		},
		{
			"workflow file not in allowed list",
			"myorg/.fullsend/.github/workflows/evil.yml@refs/heads/main",
			"myorg/.fullsend",
			"not in allowed list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkflowRef(tt.ref, tt.repository, false, nil, allowedFiles)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateWorkflowRef_PerRepo(t *testing.T) {
	// Per-repo mode: only workflow host repos and upstream are allowed.
	workflowHosts := map[string]bool{"myorg/my-repo": true}
	allowedFiles := []string{"dispatch.yml", "triage.yml"}

	tests := []struct {
		name       string
		ref        string
		repository string
		wantErr    string
	}{
		{
			"upstream workflow",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/my-repo",
			"",
		},
		{
			"workflow host listed repo",
			"myorg/my-repo/.github/workflows/triage.yml@refs/heads/main",
			"myorg/my-repo",
			"",
		},
		{
			"workflow host not listed repo",
			"myorg/other-repo/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/other-repo",
			"does not reference an allowed workflow host repo",
		},
		{
			".fullsend not accepted in per-repo mode without being in host list",
			"myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/.fullsend",
			"does not reference an allowed workflow host repo",
		},
		{
			"workflow file not in allowed list",
			"myorg/my-repo/.github/workflows/evil.yml@refs/heads/main",
			"myorg/my-repo",
			"not in allowed list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkflowRef(tt.ref, tt.repository, true, workflowHosts, allowedFiles)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateWorkflowRef_PerRepo_DefaultHost(t *testing.T) {
	// When no workflow host repos are configured, default includes upstream.
	defaultHosts := map[string]bool{"fullsend-ai/fullsend": true}
	err := ValidateWorkflowRef(
		"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
		"myorg/my-repo",
		true, defaultHosts, []string{"*"},
	)
	assert.NoError(t, err)

	err = ValidateWorkflowRef(
		"myorg/my-repo/.github/workflows/dispatch.yml@refs/heads/main",
		"myorg/my-repo",
		true, defaultHosts, []string{"*"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not reference an allowed workflow host repo")
}

func TestValidateWorkflowRef_Wildcard(t *testing.T) {
	err := ValidateWorkflowRef(
		"myorg/.fullsend/.github/workflows/anything.yml@refs/heads/main",
		"myorg/.fullsend",
		false, nil, []string{"*"},
	)
	assert.NoError(t, err)
}

func TestAuthorizeToken(t *testing.T) {
	tests := []struct {
		name            string
		claims          *Claims
		allowedOrgs     []string
		perRepoWIFRepos map[string]bool
		wantErr         string
	}{
		{
			"per-repo caller bypasses org check",
			&Claims{Repository: "myorg/my-repo", RepositoryOwner: "myorg"},
			[]string{"other-org"},
			map[string]bool{"myorg/my-repo": true},
			"",
		},
		{
			"per-org caller in ALLOWED_ORGS succeeds",
			&Claims{Repository: "myorg/my-repo", RepositoryOwner: "myorg"},
			[]string{"myorg"},
			nil,
			"",
		},
		{
			"per-org caller not in ALLOWED_ORGS fails",
			&Claims{Repository: "evilorg/repo", RepositoryOwner: "evilorg"},
			[]string{"myorg"},
			nil,
			"not in allowed orgs",
		},
		{
			"empty repository_owner fails",
			&Claims{Repository: "myorg/my-repo", RepositoryOwner: ""},
			[]string{"myorg"},
			map[string]bool{"myorg/my-repo": true},
			"missing repository_owner claim",
		},
		{
			"public mint mode (*) bypasses org check",
			&Claims{Repository: "anyorg/any-repo", RepositoryOwner: "anyorg"},
			nil,
			map[string]bool{"*": true},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AuthorizeToken(tt.claims, tt.allowedOrgs, tt.perRepoWIFRepos)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestIsPerRepoMode(t *testing.T) {
	perRepo := map[string]bool{"myorg/my-repo": true}

	assert.True(t, IsPerRepoMode("myorg/my-repo", perRepo))
	assert.True(t, IsPerRepoMode("MyOrg/My-Repo", perRepo))
	assert.False(t, IsPerRepoMode("myorg/other-repo", perRepo))
	assert.False(t, IsPerRepoMode("myorg/my-repo", nil))

	// Wildcard mode
	wildcard := map[string]bool{"*": true}
	assert.True(t, IsPerRepoMode("any/repo", wildcard))
}

func TestIsPublicMintRepos(t *testing.T) {
	assert.True(t, IsPublicMintRepos(map[string]bool{"*": true}))
	assert.True(t, IsPublicMintRepos(map[string]bool{"*": true, "org/repo": true}))
	assert.False(t, IsPublicMintRepos(map[string]bool{"org/repo": true}))
	assert.False(t, IsPublicMintRepos(nil))
	assert.False(t, IsPublicMintRepos(map[string]bool{}))
}

func TestValidateWorkflowRef_WorkflowHostWildcard(t *testing.T) {
	wildcardHosts := map[string]bool{"*": true}
	allowedFiles := []string{"reusable-dispatch.yml", "dispatch.yml"}

	tests := []struct {
		name       string
		ref        string
		repository string
		wantErr    string
	}{
		{
			"wildcard accepts any repo workflow",
			"myorg/my-repo/.github/workflows/reusable-dispatch.yml@refs/heads/main",
			"myorg/my-repo",
			"",
		},
		{
			"wildcard accepts ephemeral repo workflow",
			"fullsend-ai-test/bt-abc12345/.github/workflows/dispatch.yml@refs/heads/main",
			"fullsend-ai-test/bt-abc12345",
			"",
		},
		{
			"wildcard still enforces basename allowlist",
			"myorg/my-repo/.github/workflows/evil.yml@refs/heads/main",
			"myorg/my-repo",
			"not in allowed list",
		},
		{
			"wildcard rejects non-workflow path",
			"myorg/my-repo/scripts/run.sh@refs/heads/main",
			"myorg/my-repo",
			"does not reference an allowed workflow host repo",
		},
		{
			"wildcard still accepts upstream",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/my-repo",
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkflowRef(tt.ref, tt.repository, true, wildcardHosts, allowedFiles)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateWorkflowRef_PublicMode(t *testing.T) {
	// Public mode (PER_REPO_WIF_REPOS=*) is no longer special-cased.
	// It behaves like per-repo mode: workflow host repos and basename
	// allowlist apply. See ADR 0082 §2 (revised 2026-08-05).
	defaultHosts := map[string]bool{"fullsend-ai/fullsend": true}

	tests := []struct {
		name       string
		ref        string
		repository string
		wantErr    string
	}{
		{
			"upstream workflow with allowed basename",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/my-repo",
			"",
		},
		{
			"upstream workflow tag",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/tags/v1.0.0",
			"myorg/my-repo",
			"",
		},
		{
			"upstream workflow sha",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@abc123def456",
			"myorg/my-repo",
			"",
		},
		{
			"upstream workflow disallowed basename",
			"fullsend-ai/fullsend/.github/workflows/custom.yml@refs/heads/main",
			"myorg/my-repo",
			"not in allowed list",
		},
		{
			"legacy fullsend config repo not in host list",
			"myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/.fullsend",
			"does not reference an allowed workflow host repo",
		},
		{
			"per-repo self workflow not in host list",
			"myorg/my-repo/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/my-repo",
			"does not reference an allowed workflow host repo",
		},
		{
			"non-workflow path",
			"fullsend-ai/fullsend/scripts/run.sh@refs/heads/main",
			"myorg/my-repo",
			"does not reference a workflow file",
		},
		{
			"empty workflow filename",
			"fullsend-ai/fullsend/.github/workflows/@refs/heads/main",
			"myorg/my-repo",
			"not in allowed list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkflowRef(tt.ref, tt.repository, true, defaultHosts, []string{"dispatch.yml"})
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}
