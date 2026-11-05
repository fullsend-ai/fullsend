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

func TestAudience_Contains(t *testing.T) {
	a := Audience{"fullsend-mint", "other"}
	assert.True(t, a.Contains("fullsend-mint"))
	assert.True(t, a.Contains("other"))
	assert.False(t, a.Contains("missing"))
}

func TestValidateWorkflowRef(t *testing.T) {
	// Only workflow host repos and upstream are allowed.
	workflowHosts := map[string]bool{"myorg/my-repo": true}
	allowedFiles := []string{"dispatch.yml", "triage.yml"}

	tests := []struct {
		name    string
		ref     string
		wantErr string
	}{
		{"empty ref", "", "missing job_workflow_ref"},
		{
			"upstream workflow",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"",
		},
		{
			"workflow host listed repo",
			"myorg/my-repo/.github/workflows/triage.yml@refs/heads/main",
			"",
		},
		{
			"workflow host listed repo is case-insensitive",
			"MyOrg/My-Repo/.github/workflows/triage.yml@refs/heads/main",
			"",
		},
		{
			"workflow host not listed repo",
			"myorg/other-repo/.github/workflows/dispatch.yml@refs/heads/main",
			"does not reference an allowed workflow host repo",
		},
		{
			"legacy org .fullsend config repo not in host list is denied",
			"myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"does not reference an allowed workflow host repo",
		},
		{
			"not a workflow path",
			"myorg/my-repo/scripts/run.sh@refs/heads/main",
			"does not reference a workflow file",
		},
		{
			"workflow file not in allowed list",
			"myorg/my-repo/.github/workflows/evil.yml@refs/heads/main",
			"not in allowed list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkflowRef(tt.ref, workflowHosts, allowedFiles)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateWorkflowRef_DefaultHost(t *testing.T) {
	// When no workflow host repos are configured, default includes upstream.
	defaultHosts := map[string]bool{"fullsend-ai/fullsend": true}
	err := ValidateWorkflowRef(
		"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
		defaultHosts, []string{"*"},
	)
	assert.NoError(t, err)

	err = ValidateWorkflowRef(
		"myorg/my-repo/.github/workflows/dispatch.yml@refs/heads/main",
		defaultHosts, []string{"*"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not reference an allowed workflow host repo")
}

func TestValidateWorkflowRef_UpstreamAcceptedWithoutHosts(t *testing.T) {
	// Upstream is always accepted, even when the host map is empty.
	err := ValidateWorkflowRef(
		"fullsend-ai/fullsend/.github/workflows/anything.yml@refs/heads/main",
		nil, []string{"*"},
	)
	assert.NoError(t, err)
}

func TestValidateWorkflowRef_LegacyOrgConfigRepoDenied(t *testing.T) {
	// The legacy per-org {org}/.fullsend config repo is no longer
	// hard-wired as a trusted workflow host, even with a wildcard
	// workflow-file allowlist.
	err := ValidateWorkflowRef(
		"myorg/.fullsend/.github/workflows/anything.yml@refs/heads/main",
		nil, []string{"*"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not reference an allowed workflow host repo")
}

func TestAuthorizeToken(t *testing.T) {
	tests := []struct {
		name            string
		claims          *Claims
		perRepoWIFRepos map[string]bool
		wantErr         string
	}{
		{
			"enrolled per-repo caller succeeds",
			&Claims{Repository: "myorg/my-repo", RepositoryOwner: "myorg"},
			map[string]bool{"myorg/my-repo": true},
			"",
		},
		{
			"enrolled per-repo caller is case-insensitive",
			&Claims{Repository: "MyOrg/My-Repo", RepositoryOwner: "MyOrg"},
			map[string]bool{"myorg/my-repo": true},
			"",
		},
		{
			"caller not enrolled per-repo is denied",
			&Claims{Repository: "myorg/other-repo", RepositoryOwner: "myorg"},
			map[string]bool{"myorg/my-repo": true},
			"not enrolled for per-repo mint access",
		},
		{
			"caller with no per-repo enrollment configured is denied",
			&Claims{Repository: "myorg/my-repo", RepositoryOwner: "myorg"},
			nil,
			"not enrolled for per-repo mint access",
		},
		{
			"empty repository_owner fails",
			&Claims{Repository: "myorg/my-repo", RepositoryOwner: ""},
			map[string]bool{"myorg/my-repo": true},
			"missing repository_owner claim",
		},
		{
			"public mint mode (*) accepts any repo",
			&Claims{Repository: "anyorg/any-repo", RepositoryOwner: "anyorg"},
			map[string]bool{"*": true},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := AuthorizeToken(tt.claims, tt.perRepoWIFRepos)
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

func TestValidateWorkflowRef_PublicMode(t *testing.T) {
	// Public mode (PER_REPO_WIF_REPOS=*) is not special-cased:
	// workflow host repos and basename allowlist apply.
	// See ADR 0082 §2 (revised 2026-08-05).
	defaultHosts := map[string]bool{"fullsend-ai/fullsend": true}

	tests := []struct {
		name    string
		ref     string
		wantErr string
	}{
		{
			"upstream workflow with allowed basename",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"",
		},
		{
			"upstream workflow tag",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@refs/tags/v1.0.0",
			"",
		},
		{
			"upstream workflow sha",
			"fullsend-ai/fullsend/.github/workflows/dispatch.yml@abc123def456",
			"",
		},
		{
			"upstream workflow disallowed basename",
			"fullsend-ai/fullsend/.github/workflows/custom.yml@refs/heads/main",
			"not in allowed list",
		},
		{
			"legacy fullsend config repo not in host list",
			"myorg/.fullsend/.github/workflows/dispatch.yml@refs/heads/main",
			"does not reference an allowed workflow host repo",
		},
		{
			"per-repo self workflow not in host list",
			"myorg/my-repo/.github/workflows/dispatch.yml@refs/heads/main",
			"does not reference an allowed workflow host repo",
		},
		{
			"non-workflow path",
			"fullsend-ai/fullsend/scripts/run.sh@refs/heads/main",
			"does not reference a workflow file",
		},
		{
			"empty workflow filename",
			"fullsend-ai/fullsend/.github/workflows/@refs/heads/main",
			"not in allowed list",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWorkflowRef(tt.ref, defaultHosts, []string{"dispatch.yml"})
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}
