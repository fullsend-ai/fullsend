package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Tests for inference.wif_audience and inference.service_account, the
// GitLab vertex-wif options for an existing WIF provider (#8810).

const (
	testWIFAudience    = "//iam.googleapis.com/projects/1/locations/global/workloadIdentityPools/p/providers/x"
	testServiceAccount = "agent-sa@example-project.iam.gserviceaccount.com"
)

func TestPerRepoConfig_InferenceExistingWIFOptions_Layering(t *testing.T) {
	defaults := &perRepoDefaults{}
	assert.Empty(t, defaults.ConfigInferenceWIFAudience(), "no code default: aud stays fullsend")
	assert.Empty(t, defaults.ConfigInferenceServiceAccount(), "no code default: direct federation")

	base := &perRepoConfig{
		Inference: &PerRepoInferenceConfig{WIFAudience: "base-aud", ServiceAccount: testServiceAccount},
		parent:    defaults,
	}
	overlay := &perRepoConfig{parent: base}
	assert.Equal(t, "base-aud", overlay.ConfigInferenceWIFAudience(), "unset overlay inherits")
	assert.Equal(t, testServiceAccount, overlay.ConfigInferenceServiceAccount())

	overlay.SetInferenceWIFAudience(testWIFAudience)
	overlay.SetInferenceServiceAccount("other-sa@example-project.iam.gserviceaccount.com")
	assert.Equal(t, testWIFAudience, overlay.ConfigInferenceWIFAudience(), "overlay wins")
	assert.Equal(t, "other-sa@example-project.iam.gserviceaccount.com", overlay.ConfigInferenceServiceAccount())

	empty := &perRepoConfig{}
	assert.Empty(t, empty.ConfigInferenceWIFAudience())
	assert.Empty(t, empty.ConfigInferenceServiceAccount())
}

func TestMergeManaged_InferenceExistingWIFOptions(t *testing.T) {
	parent := &perRepoConfig{
		Inference: &PerRepoInferenceConfig{WIFAudience: "parent-aud", ServiceAccount: testServiceAccount},
		parent:    &perRepoDefaults{},
	}
	child := &perRepoConfig{
		Inference: &PerRepoInferenceConfig{WIFAudience: testWIFAudience},
		parent:    &perRepoDefaults{},
	}
	merged := MergeManaged(parent, child)
	require.NotNil(t, merged)
	assert.Equal(t, testWIFAudience, merged.ConfigInferenceWIFAudience(), "child wins")
	assert.Equal(t, testServiceAccount, merged.ConfigInferenceServiceAccount(), "unset child keeps the parent value")

	body, err := merged.Marshal()
	require.NoError(t, err)
	assert.Contains(t, string(body), "wif_audience: "+testWIFAudience)
	assert.Contains(t, string(body), "service_account: "+testServiceAccount)
}

func TestDeclaredInfrastructureValues_ExistingWIFOptions(t *testing.T) {
	var overlay ManagedConfig
	require.NoError(t, yaml.Unmarshal([]byte("inference:\n  service_account: "+testServiceAccount+"\n"), &overlay))
	base := []byte("inference:\n  wif_audience: " + testWIFAudience + "\n  service_account: base-sa@example-project.iam.gserviceaccount.com\n")
	got, err := DeclaredInfrastructureValues(overlay.Writer(), base)
	require.NoError(t, err)
	assert.Equal(t, testWIFAudience, got.InferenceWIFAudience, "base value")
	assert.Equal(t, testServiceAccount, got.InferenceServiceAccount, "overlay wins")
}

func TestEffectiveYAML_ExistingWIFOptions(t *testing.T) {
	layered, err := LayerOnBase(&perRepoConfig{}, []byte("inference:\n  wif_audience: "+testWIFAudience+"\n  service_account: "+testServiceAccount+"\n"))
	require.NoError(t, err)
	data, err := EffectiveYAML(layered)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, yaml.Unmarshal(data, &got))
	inference, ok := got["inference"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, testWIFAudience, inference["wif_audience"])
	assert.Equal(t, testServiceAccount, inference["service_account"])
}
