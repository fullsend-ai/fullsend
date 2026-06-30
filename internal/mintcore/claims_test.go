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

func TestValidateWorkflowRef(t *testing.T) {
	perRepo := map[string]bool{"myorg/my-repo": true}
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
			"per-repo workflow matching token repo",
			"myorg/my-repo/.github/workflows/triage.yml@refs/heads/main",
			"myorg/my-repo",
			"",
		},
		{
			"per-repo workflow from different repo",
			"myorg/my-repo/.github/workflows/triage.yml@refs/heads/main",
			"myorg/other-repo",
			"does not reference",
		},
		{
			"unregistered repo",
			"myorg/other-repo/.github/workflows/dispatch.yml@refs/heads/main",
			"myorg/other-repo",
			"does not reference",
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
			err := ValidateWorkflowRef(tt.ref, tt.repository, perRepo, allowedFiles)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateWorkflowRef_Wildcard(t *testing.T) {
	err := ValidateWorkflowRef(
		"myorg/.fullsend/.github/workflows/anything.yml@refs/heads/main",
		"myorg/.fullsend",
		nil, []string{"*"},
	)
	assert.NoError(t, err)
}
