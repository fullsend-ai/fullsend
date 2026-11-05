package steps

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/fullsend-ai/fullsend/internal/agentnew"
	"github.com/fullsend-ai/fullsend/pkg/behaviourtest/world"
)

// assertBasePolicy checks that a committed policy resource is the real base
// policy. OpenShell 0.1 rejects a policy file that declares no fields (a
// comment-only placeholder) at sandbox create.
func assertBasePolicy(t *testing.T, got []byte) {
	t.Helper()
	require.NotNil(t, got, "policy resource should be committed")
	assert.Equal(t, string(agentnew.BasePolicy()), string(got))
	var p struct {
		Version          int            `yaml:"version"`
		FilesystemPolicy map[string]any `yaml:"filesystem_policy"`
	}
	require.NoError(t, yaml.Unmarshal(got, &p))
	assert.Equal(t, 1, p.Version)
	assert.NotEmpty(t, p.FilesystemPolicy)
}

// A URL-sourced base that names a relative policy gets the base policy
// committed next to it in the hosting repo, so a child that names no policy
// inherits a policy OpenShell can activate.
func TestGivenURLSourcedBaseHarness_CommitsBasePolicy(t *testing.T) {
	stubRawHTTPClient(t)
	scm := &fakeURLSCM{files: map[string][]byte{
		"org/repo/.fullsend/config.yaml": []byte("version: \"1\"\nagents: []\n"),
	}}
	w := &world.World{
		Org:                 "org",
		RepoName:            "repo",
		SCM:                 scm,
		URLHarnessRepoOwner: "org",
		URLHarnessRepoName:  "base-host",
	}

	require.NoError(t, givenURLSourcedBaseHarness(w, "remote-base",
		"agent: agents/triage.md\npolicy: policies/base.yaml\nrole: triage\nslug: fullsend-ai-base"))

	assertBasePolicy(t, scm.files["org/base-host/policies/base.yaml"])
}
